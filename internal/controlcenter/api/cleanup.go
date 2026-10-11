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

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// Cleanup (step 18g): delete a Test's finished runs by age or date range.
// Developers preview, admins delete exactly the runs the preview listed —
// each through DELETE /runs/{uid}, which removes the run, its history row,
// test cases, logs and artifacts and is audited by kubetest. Retention
// already expires history after retention.days; this is for doing it
// sooner, per Test.

const (
	// MaxCleanup bounds one cleanup: deletes are one API call each and
	// must finish before a proxy in front of Control Center times out.
	MaxCleanup = 200
	// cleanupScanPages bounds how much history a preview reads.
	cleanupScanPages = 20
	cleanupPageSize  = 500
	// cleanupParallel deletes at a time.
	cleanupParallel = 4
	// DefaultOlderThanDays is the form's default age.
	DefaultOlderThanDays = 30
	maxOlderThanDays     = 3650

	modeOlder  = "older"
	modeRange  = "range"
	dateLayout = "2006-01-02"
)

// cleanupCriteria: runs older than Days, or finished within [From, To]
// (whole days, UTC). Range needs at least one bound — no bound would mean
// "every run of the Test", which is never what someone meant.
type cleanupCriteria struct {
	Mode           string
	Days           int
	RawFrom, RawTo string
	from, to       time.Time // zero = unbounded
}

// errNoBound is the no-bound guard's message.
var errNoBound = errors.New("give a From or To date (or both): a date range without either would match every run of this Test — use “older than” to clear old runs")

func parseCleanup(q func(string) string, now time.Time) (cleanupCriteria, string, error) {
	c := cleanupCriteria{Mode: q("mode"), RawFrom: strings.TrimSpace(q("from")), RawTo: strings.TrimSpace(q("to")),
		Days: DefaultOlderThanDays}
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
	if !Finished(run.Phase) || run.FinishedAt == nil {
		return false
	}
	t := *run.FinishedAt
	return (c.from.IsZero() || !t.Before(c.from)) && (c.to.IsZero() || !t.After(c.to))
}

// CleanupPreview is the runs a cleanup would delete.
type CleanupPreview struct {
	// Mode, Days, From and To are the criteria as applied (From and To may
	// have been swapped; see Note).
	Mode string `json:"mode" required:"true" enum:"older,range"`
	Days int    `json:"days,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	Note string `json:"note,omitempty"`
	// Matches are the matching finished runs, oldest first, at most Max.
	Matches []RunRow `json:"matches" required:"true" nullable:"false"`
	// More counts the matches past Max: run the cleanup again for them.
	More int `json:"more,omitempty"`
	// Scanned is how many runs were checked; ScanCapped: the history is
	// longer than one preview reads.
	Scanned    int  `json:"scanned" required:"true"`
	ScanCapped bool `json:"scanCapped,omitempty"`
	Max        int  `json:"max" required:"true"`
}

// CleanupRequest names the runs to delete (a preview's matches).
type CleanupRequest struct {
	Runs []string `json:"runs" required:"true" nullable:"false" maxItems:"200"`
}

// CleanupResult is what a cleanup did.
type CleanupResult struct {
	Deleted   int `json:"deleted" required:"true"`
	Requested int `json:"requested" required:"true"`
	// Failures are "run: reason" for each run left in place.
	Failures []string `json:"failures" required:"true" nullable:"false"`
}

type cleanupParams struct {
	Cluster   string `path:"cluster"`
	Namespace string `path:"ns"`
	Name      string `path:"name"`
	Mode      string `query:"mode" enum:"older,range"`
	// Days: older than this many days (mode=older; default 30).
	Days string `query:"days"`
	// From, To: finished between these dates, UTC, inclusive (mode=range).
	From string `query:"from"`
	To   string `query:"to"`
}

func runRow(run apiclient.Run) RunRow {
	row := RunRow{ID: RunID(run), Name: run.Name, Phase: run.Phase, StartedAt: utc(run.StartedAt),
		NotBefore: utc(run.NotBefore), FinishedAt: utc(run.FinishedAt), DurationMs: run.DurationMs,
		Source: run.Source, CreatedBy: run.Tags[TagCreatedBy]}
	if run.Git != nil && run.Git.Commit != "" {
		row.Commit = &Commit{SHA: run.Git.Commit, URL: CommitURL(run.Git.URI, run.Git.Commit)}
	}
	if run.Comment != nil {
		row.Comment = run.Comment.Text
	}
	if run.Phase == phaseQueued {
		row.Waiting = run.Message
	}
	return row
}

func (s *Server) previewCleanup(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	crit, note, err := parseCleanup(r.URL.Query().Get, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	matches, scanned, capped, err := findCleanup(r.Context(), client, r.PathValue("ns"), r.PathValue("name"), crit)
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	out := CleanupPreview{Mode: crit.Mode, Note: note, Matches: []RunRow{}, Scanned: scanned, ScanCapped: capped,
		Max: MaxCleanup}
	if crit.Mode == modeOlder {
		out.Days = crit.Days
	} else {
		out.From, out.To = crit.RawFrom, crit.RawTo
	}
	if len(matches) > MaxCleanup {
		out.More = len(matches) - MaxCleanup
		matches = matches[:MaxCleanup]
	}
	for _, run := range matches {
		out.Matches = append(out.Matches, runRow(run))
	}
	writeJSON(w, http.StatusOK, out)
}

// findCleanup pages through the Test's history (newest first) and returns
// the matching runs oldest first — the oldest go first when a cleanup is
// bigger than one batch.
func findCleanup(ctx context.Context, client *apiclient.Client, ns, test string, crit cleanupCriteria) (
	matches []apiclient.Run, scanned int, capped bool, err error) {
	after := ""
	for page := 0; ; page++ {
		if page == cleanupScanPages {
			capped = true
			break
		}
		res, err := client.ListRuns(ctx, apiclient.ListRunsOptions{Namespace: ns, Test: test, Limit: cleanupPageSize, After: after})
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

// cleanup deletes the runs the preview listed: each must still be a
// finished run of this Test.
func (s *Server) cleanup(w http.ResponseWriter, r *http.Request) {
	_, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	var in CleanupRequest
	if !decode(w, r, &in) {
		return
	}
	var ids []string
	for _, n := range in.Runs {
		if n = strings.TrimSpace(n); n != "" && !slices.Contains(ids, n) {
			ids = append(ids, n)
		}
	}
	if len(ids) == 0 {
		writeError(w, http.StatusBadRequest, "No runs selected — nothing deleted.")
		return
	}
	if len(ids) > MaxCleanup {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("At most %d runs per cleanup — nothing deleted.", MaxCleanup))
		return
	}
	ns, test := r.PathValue("ns"), r.PathValue("name")
	var (
		mu    sync.Mutex
		out   = CleanupResult{Requested: len(ids), Failures: []string{}}
		wg    sync.WaitGroup
		slots = make(chan struct{}, cleanupParallel)
	)
	for _, id := range ids {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			err := deleteFinishedRun(r.Context(), client, ns, test, id)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out.Failures = append(out.Failures, id+": "+messageOf(err))
				return
			}
			out.Deleted++
		}()
	}
	wg.Wait()
	slices.Sort(out.Failures)
	writeJSON(w, http.StatusOK, out)
}

// errNotThisTest refuses a run that isn't a finished run of the Test the
// cleanup is for.
var errNotThisTest = errors.New("not a finished run of this Test")

func deleteFinishedRun(ctx context.Context, client *apiclient.Client, ns, test, id string) error {
	run, err := client.GetRun(ctx, ns, id)
	if err != nil {
		return err
	}
	if run.TestRef != test || !Finished(run.Phase) {
		return errNotThisTest
	}
	return client.DeleteRun(ctx, ns, RunID(*run))
}
