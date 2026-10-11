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
	"cmp"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/hinskii/kubetest-alt/pkg/apiclient"
	"github.com/hinskii/kubetest-alt/pkg/executor"
)

// Test cases (step 18-2f): a Test's JUnit cases with flakiness, one
// case's history, and the case-level diff of two runs.

// Windows are the run windows the case list and analytics offer.
var Windows = []int{10, 30, 100}

// defaultWindow is the window when none (or an unknown one) is asked for.
const defaultWindow = 30

func window(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("runs"))
	if !slices.Contains(Windows, n) {
		return defaultWindow
	}
	return n
}

func failing(status string) bool {
	return status == executor.CaseFailed || status == executor.CaseError
}

// CaseList is a Test's cases over its last Window runs.
type CaseList struct {
	Window  int   `json:"window" required:"true"`
	Windows []int `json:"windows" required:"true" nullable:"false"`
	// Cases come most failures first.
	Cases []CaseStat `json:"cases" required:"true" nullable:"false"`
	// Flaky: passed and failed within the window. FailingNow: failed in
	// the latest run.
	Flaky      int `json:"flaky" required:"true"`
	FailingNow int `json:"failingNow" required:"true"`
}

// CaseStat is one case over a window of runs.
type CaseStat struct {
	Key   string `json:"key" required:"true"`
	Name  string `json:"name" required:"true"`
	Suite string `json:"suite,omitempty"`
	Class string `json:"class,omitempty"`
	// Runs is how many of the window's runs had the case.
	Runs       int       `json:"runs" required:"true"`
	Passed     int       `json:"passed" required:"true"`
	Failed     int       `json:"failed" required:"true"`
	Skipped    int       `json:"skipped" required:"true"`
	AvgMs      int64     `json:"avgMs" required:"true"`
	MaxMs      int64     `json:"maxMs" required:"true"`
	LastStatus string    `json:"lastStatus" required:"true"`
	LastAt     time.Time `json:"lastAt" required:"true"`
	// Flips counts passed ↔ not-passed changes between consecutive runs.
	Flips int  `json:"flips" required:"true"`
	Flaky bool `json:"flaky" required:"true"`
}

// CaseHistory is one case's result in each of the Test's runs, newest first.
type CaseHistory struct {
	Key     string       `json:"key" required:"true"`
	Runs    []CaseResult `json:"runs" required:"true" nullable:"false"`
	Passed  int          `json:"passed" required:"true"`
	Failed  int          `json:"failed" required:"true"`
	Skipped int          `json:"skipped" required:"true"`
}

// CaseResult is one run's result for a case.
type CaseResult struct {
	// RunID finds the run (run history: its UID).
	RunID      string    `json:"runId" required:"true"`
	RunName    string    `json:"runName,omitempty"`
	FinishedAt time.Time `json:"finishedAt" required:"true"`
	Status     string    `json:"status" required:"true"`
	DurationMs int64     `json:"durationMs,omitempty"`
	Message    string    `json:"message,omitempty"`
}

// RunCaseDiff is a run's cases against an earlier run's (?with=).
type RunCaseDiff struct {
	Run RunRow `json:"run" required:"true"`
	// TestRef is the run's Test.
	TestRef string `json:"testRef" required:"true"`
	// Candidates are the Test's other finished runs to compare with.
	Candidates []RunRow `json:"candidates" required:"true" nullable:"false"`
	// Base is the run compared with; absent until one is picked.
	Base *RunRow `json:"base,omitempty"`
	// CasesError: the cases couldn't be read (no run history).
	CasesError     string     `json:"casesError,omitempty"`
	RunCases       int        `json:"runCases" required:"true"`
	BaseCases      int        `json:"baseCases" required:"true"`
	StartedFailing []CaseDiff `json:"startedFailing" required:"true" nullable:"false"`
	StillFailing   []CaseDiff `json:"stillFailing" required:"true" nullable:"false"`
	Fixed          []CaseDiff `json:"fixed" required:"true" nullable:"false"`
	Added          []CaseDiff `json:"added" required:"true" nullable:"false"`
	Removed        []CaseDiff `json:"removed" required:"true" nullable:"false"`
	Unchanged      int        `json:"unchanged" required:"true"`
}

// CaseDiff is one case compared between a run and a base run.
type CaseDiff struct {
	Key string `json:"key" required:"true"`
	// Before and Now are the statuses; absent when the case wasn't there.
	Before string `json:"before,omitempty"`
	Now    string `json:"now,omitempty"`
	// DurationDiffMs is now minus before, when the case ran in both.
	DurationDiffMs int64  `json:"durationDiffMs,omitempty"`
	Message        string `json:"message,omitempty"`
}

type casesParams struct {
	Cluster   string `path:"cluster"`
	Namespace string `path:"ns"`
	Name      string `path:"name"`
	Runs      int    `query:"runs" enum:"10,30,100" default:"30"`
}

type caseHistoryParams struct {
	Cluster   string `path:"cluster"`
	Namespace string `path:"ns"`
	Name      string `path:"name"`
	Case      string `query:"case" required:"true"`
}

type compareParams struct {
	Cluster   string `path:"cluster"`
	Namespace string `path:"ns"`
	ID        string `path:"id"`
	// With is the run to compare with.
	With string `query:"with"`
}

func (s *Server) listCases(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	n := window(r)
	stats, err := client.TestCaseStats(r.Context(), r.PathValue("ns"), r.PathValue("name"), n)
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	out := CaseList{Window: n, Windows: Windows, Cases: make([]CaseStat, 0, len(stats))}
	for _, st := range stats {
		out.Cases = append(out.Cases, CaseStat{Key: st.Key, Name: st.Name, Suite: st.Suite, Class: st.Class,
			Runs: st.Runs, Passed: st.Passed, Failed: st.Failed, Skipped: st.Skipped, AvgMs: st.AvgMs, MaxMs: st.MaxMs,
			LastStatus: st.LastStatus, LastAt: st.LastAt.UTC(), Flips: st.Flips, Flaky: st.Flaky})
		if st.Flaky {
			out.Flaky++
		}
		if failing(st.LastStatus) {
			out.FailingNow++
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// caseHistoryRuns is how many runs a case's history shows.
const caseHistoryRuns = 100

func (s *Server) caseHistory(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	key := r.URL.Query().Get("case")
	if key == "" {
		writeError(w, http.StatusBadRequest, "Which test case? (?case= is missing)")
		return
	}
	runs, err := client.TestCaseHistory(r.Context(), r.PathValue("ns"), r.PathValue("name"), key, caseHistoryRuns)
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	out := CaseHistory{Key: key, Runs: make([]CaseResult, 0, len(runs))}
	for _, run := range runs {
		out.Runs = append(out.Runs, CaseResult{RunID: run.RunUID, RunName: run.RunName, FinishedAt: run.FinishedAt.UTC(),
			Status: run.Status, DurationMs: run.DurationMs, Message: run.Message})
		switch run.Status {
		case executor.CasePassed:
			out.Passed++
		case executor.CaseSkipped:
			out.Skipped++
		default:
			out.Failed++
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// compareCandidates is how many recent runs are offered to compare with.
const compareCandidates = 30

// compareRun diffs a run's test cases against ?with= (another run of the
// same Test); without it, it lists the runs to pick from.
func (s *Server) compareRun(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	ns, id := r.PathValue("ns"), r.PathValue("id")
	run, err := client.GetRun(r.Context(), ns, id)
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	out := RunCaseDiff{Run: runRow(*run), TestRef: run.TestRef, Candidates: []RunRow{}, StartedFailing: []CaseDiff{},
		StillFailing: []CaseDiff{}, Fixed: []CaseDiff{}, Added: []CaseDiff{}, Removed: []CaseDiff{}}
	if recent, err := client.ListRuns(r.Context(), apiclient.ListRunsOptions{Namespace: ns, Test: run.TestRef,
		Limit: compareCandidates}); err == nil {
		for _, cand := range recent.Runs {
			if cand.UID != run.UID && Finished(cand.Phase) {
				out.Candidates = append(out.Candidates, runRow(cand))
			}
		}
	}
	with := r.URL.Query().Get("with")
	if with == "" {
		writeJSON(w, http.StatusOK, out)
		return
	}
	base, err := client.GetRun(r.Context(), ns, with)
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	b := runRow(*base)
	out.Base = &b
	nowCases, err1 := client.RunTestCases(r.Context(), ns, id, false)
	baseCases, err2 := client.RunTestCases(r.Context(), ns, with, false)
	if err := cmp.Or(err1, err2); err != nil {
		out.CasesError = messageOf(err)
		writeJSON(w, http.StatusOK, out)
		return
	}
	diffCases(&out, nowCases, baseCases)
	out.RunCases, out.BaseCases = len(nowCases), len(baseCases)
	writeJSON(w, http.StatusOK, out)
}

// diffCases classifies every case of run against base.
func diffCases(d *RunCaseDiff, run, base []apiclient.TestCase) {
	before := make(map[string]apiclient.TestCase, len(base))
	for _, c := range base {
		before[c.Key] = c
	}
	seen := map[string]bool{}
	for _, now := range run {
		seen[now.Key] = true
		old, existed := before[now.Key]
		row := CaseDiff{Key: now.Key, Before: old.Status, Now: now.Status, Message: now.Message}
		if existed {
			row.DurationDiffMs = now.DurationMs - old.DurationMs
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
			d.Removed = append(d.Removed, CaseDiff{Key: old.Key, Before: old.Status})
			seen[old.Key] = true
		}
	}
}
