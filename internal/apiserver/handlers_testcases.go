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

package apiserver

import (
	"context"
	"net/http"
	"strconv"

	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
	"github.com/hinskii/kubetest-alt/pkg/executor"
)

// CaseReader is the run store's test-case read surface (step 18-2f).
type CaseReader interface {
	RunCases(ctx context.Context, runUID string, failedOnly bool) ([]store.CaseRow, error)
	CaseStats(ctx context.Context, namespace, testRef string, window int) ([]store.CaseStats, error)
	CaseHistory(ctx context.Context, namespace, testRef, caseKey string, limit int) ([]store.CaseRun, error)
}

// defaultCaseWindow is how many recent runs GET /tests/{name}/testcases
// aggregates by default.
const defaultCaseWindow = 30

func (s *Server) casesUnavailable(w http.ResponseWriter) bool {
	if s.Cases == nil {
		writeError(w, http.StatusServiceUnavailable, ReasonServiceUnavail, "run history store is not configured")
		return true
	}
	return false
}

// listRunCases returns a finished run's JUnit test cases in report order;
// ?status=failed keeps failed and errored ones. A run that hasn't reached
// run history yet (or reported no JUnit) has none.
func (s *Server) listRunCases(w http.ResponseWriter, r *http.Request) {
	if s.casesUnavailable(w) {
		return
	}
	ns, err := s.targetNamespace(r, "")
	if err != nil {
		writeLookupError(w, err)
		return
	}
	ref, err := s.findRun(r.Context(), ns, r.PathValue("id"))
	if err != nil {
		writeLookupError(w, err)
		return
	}
	rows, err := s.Cases.RunCases(r.Context(), ref.UID, r.URL.Query().Get("status") == executor.CaseFailed)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	out := make([]apiclient.TestCase, 0, len(rows))
	for _, c := range rows {
		out = append(out, apiclient.TestCase{
			Key: c.Key, Suite: c.Suite, Class: c.Class, Name: c.Name, Status: c.Status,
			DurationMs: c.DurationMs, Message: c.Message, Details: c.Details, File: c.File,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// listTestCaseStats aggregates every test case of a Test over its last
// ?runs=N (default 30, max 200) runs that reported cases — failing first.
func (s *Server) listTestCaseStats(w http.ResponseWriter, r *http.Request) {
	if s.casesUnavailable(w) {
		return
	}
	ns, err := s.targetNamespace(r, "")
	if err != nil {
		writeLookupError(w, err)
		return
	}
	window := defaultCaseWindow
	if v := r.URL.Query().Get("runs"); v != "" {
		if window, err = strconv.Atoi(v); err != nil || window < 1 || window > store.MaxCaseWindow {
			writeError(w, http.StatusBadRequest, ReasonBadRequest, "runs: want 1-200")
			return
		}
	}
	stats, err := s.Cases.CaseStats(r.Context(), ns, r.PathValue("name"), window)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	out := make([]apiclient.CaseStats, 0, len(stats))
	for _, c := range stats {
		out = append(out, apiclient.CaseStats{
			Key: c.Key, Suite: c.Suite, Class: c.Class, Name: c.Name,
			Runs: c.Runs, Passed: c.Passed, Failed: c.Failed, Skipped: c.Skipped,
			AvgMs: c.AvgMs, MaxMs: c.MaxMs, LastStatus: c.LastStatus, LastAt: c.LastAt,
			Flips: c.Flips, Flaky: c.Flaky(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// listTestCaseHistory lists one case's (?case=<key>) results over the
// Test's runs, newest first (?limit, default and max 200).
func (s *Server) listTestCaseHistory(w http.ResponseWriter, r *http.Request) {
	if s.casesUnavailable(w) {
		return
	}
	ns, err := s.targetNamespace(r, "")
	if err != nil {
		writeLookupError(w, err)
		return
	}
	key := r.URL.Query().Get("case")
	if key == "" {
		writeError(w, http.StatusBadRequest, ReasonBadRequest, "case is required (a test case key)")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.Cases.CaseHistory(r.Context(), ns, r.PathValue("name"), key, limit)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	out := make([]apiclient.CaseRun, 0, len(runs))
	for _, c := range runs {
		out = append(out, apiclient.CaseRun{
			RunUID: c.RunUID, RunName: c.RunName, FinishedAt: c.FinishedAt,
			Status: c.Status, DurationMs: c.DurationMs, Message: c.Message,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
