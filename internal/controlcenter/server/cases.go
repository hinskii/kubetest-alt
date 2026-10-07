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
	"cmp"
	"net/http"
	"slices"
	"strconv"

	"github.com/hinskii/kubetest-alt/internal/controlcenter/views"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
	"github.com/hinskii/kubetest-alt/pkg/executor"
)

// Test-case pages (step 18-2f): a Test's cases with flakiness, one case's
// history, and the case-level diff of two runs.

// caseWindows are the run windows offered on the cases page.
var caseWindows = []int{10, 30, 100}

// sortByName orders the test cases page by case name (?sort=name).
const sortByName = "name"

func casesPath(c, ns, test string) string { return testPath(c, ns, test) + "/cases" }

type casesData struct {
	Cluster, Namespace, Test string
	Cases                    []apiclient.CaseStats
	Runs                     int
	Windows                  []int
	Sort                     string
	FlakyCount, FailingNow   int
}

// testCasesPage lists every case of a Test over its last ?runs runs.
// ?sort: failures (default), flaky, slowest, name.
func (s *Server) testCasesPage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	runs, _ := strconv.Atoi(r.URL.Query().Get("runs"))
	if !slices.Contains(caseWindows, runs) {
		runs = 30
	}
	stats, err := api(r, c).TestCaseStats(r.Context(), ns, name, runs)
	if err != nil {
		s.apiError(w, r, c, err)
		return
	}
	sortBy := r.URL.Query().Get("sort")
	switch sortBy {
	case "flaky":
		slices.SortStableFunc(stats, func(a, b apiclient.CaseStats) int { return cmp.Compare(b.Flips, a.Flips) })
	case "slowest":
		slices.SortStableFunc(stats, func(a, b apiclient.CaseStats) int { return cmp.Compare(b.AvgMs, a.AvgMs) })
	case sortByName:
		slices.SortStableFunc(stats, func(a, b apiclient.CaseStats) int { return cmp.Compare(a.Key, b.Key) })
	default:
		sortBy = "failures" // the API's order
	}
	data := casesData{Cluster: c.Name, Namespace: ns, Test: name, Cases: stats, Runs: runs, Windows: caseWindows, Sort: sortBy}
	for _, st := range stats {
		if st.Flaky {
			data.FlakyCount++
		}
		if failing(st.LastStatus) {
			data.FailingNow++
		}
	}
	crumbs := append(clusterCrumbs(c), views.Crumb{Label: name, Href: testPath(c.Name, ns, name)})
	s.page(w, r, "cases", name+" · test cases", crumbs, data)
}

type caseHistoryData struct {
	Cluster, Namespace, Test, Key string
	Runs                          []apiclient.CaseRun
	Passed, Failed, Skipped       int
}

// caseHistoryPage shows one case's result in each of the Test's runs.
func (s *Server) caseHistoryPage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	ns, name, key := r.PathValue("ns"), r.PathValue("name"), r.URL.Query().Get("case")
	if key == "" {
		s.renderError(w, r, http.StatusBadRequest, "Which test case? (?case= is missing)")
		return
	}
	runs, err := api(r, c).TestCaseHistory(r.Context(), ns, name, key, 100)
	if err != nil {
		s.apiError(w, r, c, err)
		return
	}
	data := caseHistoryData{Cluster: c.Name, Namespace: ns, Test: name, Key: key, Runs: runs}
	for _, run := range runs {
		switch run.Status {
		case executor.CasePassed:
			data.Passed++
		case executor.CaseSkipped:
			data.Skipped++
		default:
			data.Failed++
		}
	}
	crumbs := append(clusterCrumbs(c),
		views.Crumb{Label: name, Href: testPath(c.Name, ns, name)},
		views.Crumb{Label: "Test cases", Href: casesPath(c.Name, ns, name)})
	s.page(w, r, "case", key, crumbs, data)
}

// caseDiff is one case compared between a run and a base run.
type caseDiff struct {
	Key          string
	Before, Now  string // status, "" when absent
	NowCase      apiclient.TestCase
	DurationDiff int64
}

type compareData struct {
	Cluster, Namespace     string
	Run, Base              *apiclient.Run
	StartedFailing, Fixed  []caseDiff
	StillFailing, Added    []caseDiff
	Removed                []caseDiff
	Unchanged              int
	RunCases, BaseCases    int
	Candidates             []apiclient.Run
	CasesUnavailableReason string
}

func failing(status string) bool {
	return status == executor.CaseFailed || status == executor.CaseError
}

// diffCases classifies every case of run against base.
func diffCases(run, base []apiclient.TestCase) (d compareData) {
	before := make(map[string]apiclient.TestCase, len(base))
	for _, c := range base {
		before[c.Key] = c
	}
	seen := map[string]bool{}
	for _, now := range run {
		seen[now.Key] = true
		old, existed := before[now.Key]
		row := caseDiff{Key: now.Key, Before: old.Status, Now: now.Status, NowCase: now}
		if existed {
			row.DurationDiff = now.DurationMs - old.DurationMs
		}
		switch {
		case !existed:
			d.Added = append(d.Added, row)
		case failing(now.Status) && !failing(old.Status):
			d.StartedFailing = append(d.StartedFailing, row)
		case failing(now.Status):
			d.StillFailing = append(d.StillFailing, row)
		case failing(old.Status):
			d.Fixed = append(d.Fixed, row)
		default:
			d.Unchanged++
		}
	}
	for _, old := range base {
		if !seen[old.Key] {
			d.Removed = append(d.Removed, caseDiff{Key: old.Key, Before: old.Status})
			seen[old.Key] = true
		}
	}
	return d
}

// compareRuns diffs a run's test cases against ?with=<run> (another run
// of the same Test); without it, offers the recent runs to pick from.
func (s *Server) compareRuns(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	ns, id := r.PathValue("ns"), r.PathValue("id")
	client := api(r, c)
	run, err := client.GetRun(r.Context(), ns, id)
	if err != nil {
		s.apiError(w, r, c, err)
		return
	}
	data := compareData{Cluster: c.Name, Namespace: ns, Run: run}
	recent, err := client.ListRuns(r.Context(), apiclient.ListRunsOptions{Namespace: ns, Test: run.TestRef, Limit: 30})
	if err == nil {
		for _, cand := range recent.Runs {
			if cand.UID != run.UID && finished(cand.Phase) {
				data.Candidates = append(data.Candidates, cand)
			}
		}
	}
	crumbs := append(clusterCrumbs(c),
		views.Crumb{Label: run.TestRef, Href: testPath(c.Name, ns, run.TestRef)},
		views.Crumb{Label: run.Name, Href: runPath(c.Name, ns, id)})
	with := r.URL.Query().Get("with")
	if with == "" {
		s.page(w, r, "compare", run.Name+" · compare", crumbs, data)
		return
	}
	base, err := client.GetRun(r.Context(), ns, with)
	if err != nil {
		s.apiError(w, r, c, err)
		return
	}
	nowCases, err1 := client.RunTestCases(r.Context(), ns, id, false)
	baseCases, err2 := client.RunTestCases(r.Context(), ns, with, false)
	if err := cmp.Or(err1, err2); err != nil {
		data.Base = base
		data.CasesUnavailableReason = messageOf(err)
		s.page(w, r, "compare", run.Name+" · compare", crumbs, data)
		return
	}
	diff := diffCases(nowCases, baseCases)
	diff.Cluster, diff.Namespace, diff.Run, diff.Base, diff.Candidates = c.Name, ns, run, base, data.Candidates
	diff.RunCases, diff.BaseCases = len(nowCases), len(baseCases)
	s.page(w, r, "compare", run.Name+" · compare", crumbs, diff)
}
