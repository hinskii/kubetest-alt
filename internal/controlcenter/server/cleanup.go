/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package server

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hinskii/kubetest-alt/internal/controlcenter/views"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// Cleanup (step 18g): delete a Test's finished runs by age or date range.
// Developers preview, admins delete exactly the runs the preview listed —
// each through DELETE /runs/{uid}, which removes the run, its history row,
// test cases, logs and artifacts and is audited by kubetest. Retention
// already expires history after retention.days; this is for doing it
// sooner, per Test.

const (
	// maxCleanup bounds one cleanup: deletes are one API call each and
	// must finish before a proxy in front of Control Center times out.
	maxCleanup = 200
	// cleanupScanPages bounds how much history a preview reads.
	cleanupScanPages = 20
	cleanupPageSize  = 500
	// cleanupParallel deletes at a time.
	cleanupParallel = 4
	// defaultOlderThanDays is the form's default age.
	defaultOlderThanDays = 30
	maxOlderThanDays     = 3650

	modeOlder  = "older"
	modeRange  = "range"
	dateLayout = "2006-01-02"
)

func cleanupPath(c, ns, test string) string { return testPath(c, ns, test) + "/cleanup" }

// cleanupCriteria is the form: runs older than Days, or finished within
// [From, To] (whole days, UTC). Range needs at least one bound — no bound
// would mean "every run of the Test", which is never what someone meant.
type cleanupCriteria struct {
	Mode           string
	Days           int
	RawFrom, RawTo string
	from, to       time.Time // zero = unbounded
}

// errNoBound is the no-bound guard's message.
var errNoBound = errors.New("give a From or To date (or both): a date range without either would match every run of this Test — use “older than” to clear old runs")

func parseCleanup(q func(string) string, now time.Time) (cleanupCriteria, string, error) {
	c := cleanupCriteria{Mode: q("mode"), RawFrom: strings.TrimSpace(q("from")), RawTo: strings.TrimSpace(q("to")), Days: defaultOlderThanDays}
	note := ""
	switch c.Mode {
	case modeRange:
		var err error
		if c.RawFrom != "" {
			if c.from, err = time.Parse(dateLayout, c.RawFrom); err != nil {
				return c, "", fmt.Errorf("the From date %q is not a date (YYYY-MM-DD)", c.RawFrom)
			}
		}
		if c.RawTo != "" {
			if c.to, err = time.Parse(dateLayout, c.RawTo); err != nil {
				return c, "", fmt.Errorf("the To date %q is not a date (YYYY-MM-DD)", c.RawTo)
			}
		}
		if c.from.IsZero() && c.to.IsZero() {
			return c, "", errNoBound
		}
		if !c.from.IsZero() && !c.to.IsZero() && c.from.After(c.to) {
			c.from, c.to = c.to, c.from
			c.RawFrom, c.RawTo = c.RawTo, c.RawFrom
			note = "From was after To — swapped."
		}
		if !c.to.IsZero() {
			c.to = c.to.Add(24*time.Hour - time.Nanosecond) // the whole To day
		}
	default:
		c.Mode = modeOlder
		if raw := strings.TrimSpace(q("days")); raw != "" {
			d, err := strconv.Atoi(raw)
			if err != nil || d < 1 || d > maxOlderThanDays {
				return c, "", fmt.Errorf("days must be a whole number from 1 to %d", maxOlderThanDays)
			}
			c.Days = d
		}
		c.to = now.Add(-time.Duration(c.Days) * 24 * time.Hour)
	}
	return c, note, nil
}

// matches reports whether a finished run falls inside the criteria.
func (c cleanupCriteria) matches(run apiclient.Run) bool {
	if !finished(run.Phase) || run.FinishedAt == nil {
		return false
	}
	t := *run.FinishedAt
	return (c.from.IsZero() || !t.Before(c.from)) && (c.to.IsZero() || !t.After(c.to))
}

type cleanupData struct {
	Cluster, Namespace, Test string
	Criteria                 cleanupCriteria
	Previewed                bool
	Matches                  []apiclient.Run // oldest first, at most maxCleanup
	More                     int             // matches beyond maxCleanup
	Scanned                  int
	ScanCapped               bool
	Error, Note              string
	Max                      int
}

// cleanupPage shows the form and, once submitted (GET, read-only), the
// runs it matches.
func (s *Server) cleanupPage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	q := r.URL.Query()
	data := cleanupData{Cluster: c.Name, Namespace: ns, Test: name, Max: maxCleanup,
		Criteria: cleanupCriteria{Mode: modeOlder, Days: defaultOlderThanDays}}
	crumbs := append(clusterCrumbs(c), views.Crumb{Label: name, Href: testPath(c.Name, ns, name)})
	if !q.Has("mode") {
		s.page(w, r, "cleanup", name+" · clean up runs", crumbs, data)
		return
	}
	crit, note, err := parseCleanup(q.Get, time.Now())
	data.Criteria, data.Note = crit, note
	if err != nil {
		data.Error = err.Error()
		s.page(w, r, "cleanup", name+" · clean up runs", crumbs, data)
		return
	}
	matches, scanned, capped, err := findCleanup(r, api(r, c), ns, name, crit)
	if err != nil {
		s.apiError(w, r, c, err)
		return
	}
	data.Previewed, data.Scanned, data.ScanCapped = true, scanned, capped
	if len(matches) > maxCleanup {
		data.More = len(matches) - maxCleanup
		matches = matches[:maxCleanup]
	}
	data.Matches = matches
	s.page(w, r, "cleanup", name+" · clean up runs", crumbs, data)
}

// findCleanup pages through the Test's history (newest first) and returns
// the matching runs oldest first — the oldest go first when a cleanup is
// bigger than one batch.
func findCleanup(r *http.Request, client *apiclient.Client, ns, test string, crit cleanupCriteria) (
	matches []apiclient.Run, scanned int, capped bool, err error) {
	after := ""
	for page := 0; ; page++ {
		if page == cleanupScanPages {
			capped = true
			break
		}
		res, err := client.ListRuns(r.Context(), apiclient.ListRunsOptions{Namespace: ns, Test: test, Limit: cleanupPageSize, After: after})
		if err != nil {
			return nil, 0, false, err
		}
		olderThanFrom := false
		for _, run := range res.Runs {
			scanned++
			if crit.matches(run) {
				matches = append(matches, run)
			}
			if !crit.from.IsZero() && run.FinishedAt != nil && run.FinishedAt.Before(crit.from) {
				olderThanFrom = true
			}
		}
		if res.NextCursor == "" || olderThanFrom {
			break
		}
		after = res.NextCursor
	}
	slices.Reverse(matches)
	return matches, scanned, capped, nil
}

// cleanupRuns deletes the runs the preview listed (?run=<name>, repeated):
// each must still be a finished run of this Test.
func (s *Server) cleanupRuns(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	back := cleanupPath(c.Name, ns, name)
	if err := r.ParseForm(); err != nil {
		redirect(w, r, back, "Could not read the form: "+err.Error())
		return
	}
	var names []string
	for _, n := range r.PostForm["run"] {
		if n = strings.TrimSpace(n); n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		redirect(w, r, back, "No runs selected — nothing deleted.")
		return
	}
	if len(names) > maxCleanup {
		redirect(w, r, back, fmt.Sprintf("At most %d runs per cleanup — nothing deleted.", maxCleanup))
		return
	}
	client := api(r, c)
	var (
		mu       sync.Mutex
		deleted  int
		failures []string
		wg       sync.WaitGroup
		slots    = make(chan struct{}, cleanupParallel)
	)
	for _, n := range names {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			err := deleteFinishedRun(r, client, ns, name, n)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, n+": "+messageOf(err))
				return
			}
			deleted++
		}()
	}
	wg.Wait()
	slices.Sort(failures)
	msg := fmt.Sprintf("Deleted %d of %d runs.", deleted, len(names))
	if len(failures) > 0 {
		msg += " Not deleted: " + strings.Join(failures, "; ")
	}
	redirect(w, r, back, msg)
}

// errNotThisTest refuses a posted run that isn't a finished run of the
// Test the cleanup is for.
var errNotThisTest = errors.New("not a finished run of this Test")

func deleteFinishedRun(r *http.Request, client *apiclient.Client, ns, test, runName string) error {
	run, err := client.GetRun(r.Context(), ns, runName)
	if err != nil {
		return err
	}
	if run.TestRef != test || !finished(run.Phase) {
		return errNotThisTest
	}
	return client.DeleteRun(r.Context(), ns, views.RunID(*run))
}
