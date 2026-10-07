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
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
	"github.com/hinskii/kubetest-alt/pkg/executor"
)

const baseUID = "99999999-0000-0000-0000-000000000001"

// cannedCases plays the run store's test-case side for the real API
// server: the current run (runUID) against an earlier one (baseUID).
type cannedCases struct{}

func row(name, status string, ms int64, msg string) store.CaseRow {
	return store.CaseRow{Key: "shop › " + name, TestCase: executor.TestCase{
		Class: "shop", Name: name, Status: status, DurationMs: ms, Message: msg, Details: "at " + name}}
}

func (cannedCases) RunCases(_ context.Context, uid string, failedOnly bool) ([]store.CaseRow, error) {
	var rows []store.CaseRow
	switch uid {
	case runUID:
		rows = []store.CaseRow{row("login", "passed", 100, ""), row("checkout", "failed", 900, "timeout after 5s"),
			row("refund", "failed", 50, "500"), row("search", "passed", 40, ""), row("wishlist", "passed", 10, "")}
	case baseUID:
		rows = []store.CaseRow{row("login", "passed", 120, ""), row("checkout", "passed", 300, ""),
			row("refund", "failed", 50, "500"), row("search", "failed", 40, "no results"), row("legacy", "passed", 5, "")}
	}
	if failedOnly {
		var out []store.CaseRow
		for _, r := range rows {
			if failing(r.Status) {
				out = append(out, r)
			}
		}
		return out, nil
	}
	return rows, nil
}

func (cannedCases) CaseStats(context.Context, string, string, int) ([]store.CaseStats, error) {
	return []store.CaseStats{
		{Key: "shop › refund", Class: "shop", Name: "refund", Runs: 10, Failed: 10, LastStatus: "failed", AvgMs: 50},
		{Key: "shop › checkout", Class: "shop", Name: "checkout", Runs: 10, Passed: 6, Failed: 4, Flips: 7,
			LastStatus: "failed", AvgMs: 600, MaxMs: 900},
		{Key: "shop › login", Class: "shop", Name: "login", Runs: 10, Passed: 10, LastStatus: "passed", AvgMs: 110},
	}, nil
}

func (cannedCases) CaseHistory(_ context.Context, _, _, key string, _ int) ([]store.CaseRun, error) {
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	return []store.CaseRun{
		{RunUID: runUID, RunName: "smoke-abcde", FinishedAt: at, Status: "failed", Message: "timeout after 5s"},
		{RunUID: baseUID, RunName: "smoke-older", FinishedAt: at.Add(-time.Hour), Status: "passed"},
	}, nil
}

func failedRun() *testsv1alpha1.TestRun {
	r := runOf("smoke-abcde", testsv1alpha1.PhaseFailed)
	r.Status.TestCounts = &testsv1alpha1.TestCounts{Total: 5, Passed: 3, Failed: 2}
	return r
}

func olderRun() *testsv1alpha1.TestRun {
	r := runOf("smoke-older", testsv1alpha1.PhaseFailed)
	r.UID = types.UID(baseUID)
	earlier := metav1.NewTime(time.Date(2026, 10, 7, 11, 0, 0, 0, time.UTC))
	r.Status.FinishedAt = &earlier
	return r
}

func TestRunPage_ShowsFailedTests(t *testing.T) {
	w := newWorld(t, smokeTest(), failedRun())
	body := w.get(t, "/clusters/dev/runs/team-a/smoke-abcde", "").Body.String()
	assert.Contains(t, body, "Failed tests")
	assert.Contains(t, body, "timeout after 5s")
	assert.Contains(t, body, "at checkout", "details (stack excerpt)")
	assert.Contains(t, body, `href="/clusters/dev/tests/team-a/smoke/cases/history?case=shop%20%e2%80%ba%20checkout"`,
		"encoded once (html/template encodes query values itself)")
	assert.NotContains(t, body, ">login<", "only failed cases")
	assert.Contains(t, body, `href="/clusters/dev/runs/team-a/smoke-abcde/compare"`)
}

func TestCasesPage_FlakyAndSorting(t *testing.T) {
	w := newWorld(t, smokeTest())
	rec := w.get(t, "/clusters/dev/tests/team-a/smoke/cases", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, "<strong>2</strong> failing in the latest run")
	assert.Contains(t, body, "<strong>1</strong> flaky", "the API marks checkout (passed and failed)")
	assert.Contains(t, body, `href="/clusters/dev/tests/team-a/smoke/cases/history?case=shop%20%e2%80%ba%20refund"`)
	assert.Contains(t, body, ">60%<", "checkout: 6 of 10")
	assert.Less(t, strings.Index(body, ">refund<"), strings.Index(body, ">checkout<"), "most failures first")

	byFlips := w.get(t, "/clusters/dev/tests/team-a/smoke/cases?sort=flaky&runs=10", "").Body.String()
	assert.Less(t, strings.Index(byFlips, ">checkout<"), strings.Index(byFlips, ">refund<"))
	assert.Contains(t, byFlips, `<option value="10" selected>`)
}

func TestCaseHistoryPage(t *testing.T) {
	w := newWorld(t, smokeTest())
	rec := w.get(t, "/clusters/dev/tests/team-a/smoke/cases/history?case="+"shop%20%E2%80%BA%20checkout", "")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "<h1>shop › checkout</h1>")
	assert.Contains(t, body, "2 runs · 1 passed · 1 failed")
	assert.Equal(t, 2, strings.Count(body, `class="dot status-`))
	assert.Equal(t, http.StatusBadRequest, w.get(t, "/clusters/dev/tests/team-a/smoke/cases/history", "").Code)
}

func TestComparePage(t *testing.T) {
	w := newWorld(t, smokeTest(), failedRun(), olderRun())
	pick := w.get(t, "/clusters/dev/runs/team-a/smoke-abcde/compare", "").Body.String()
	assert.Contains(t, pick, `<option value="smoke-older"`)

	rec := w.get(t, "/clusters/dev/runs/team-a/smoke-abcde/compare?with=smoke-older", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	section := func(title string) string {
		i := strings.Index(body, "<h2>"+title)
		require.GreaterOrEqual(t, i, 0, title)
		rest := body[i:]
		if j := strings.Index(rest[4:], "<h2>"); j >= 0 {
			return rest[:j+4]
		}
		return rest
	}
	assert.Contains(t, section("Started failing"), "shop › checkout")
	assert.Contains(t, section("Started failing"), "&#43;600ms")
	assert.Contains(t, section("Still failing"), "shop › refund")
	assert.Contains(t, section("Fixed"), "shop › search")
	assert.Contains(t, section("New tests"), "shop › wishlist")
	assert.Contains(t, section("No longer run"), "shop › legacy")
	assert.Contains(t, body, "Unchanged: 1")
}

func TestDiffCases(t *testing.T) {
	tc := func(key, status string) apiclient.TestCase { return apiclient.TestCase{Key: key, Status: status} }
	d := diffCases(
		[]apiclient.TestCase{tc("a", "failed"), tc("b", "error"), tc("c", "passed"), tc("d", "skipped"), tc("new", "passed")},
		[]apiclient.TestCase{tc("a", "passed"), tc("b", "failed"), tc("c", "error"), tc("d", "skipped"), tc("gone", "failed")},
	)
	keys := func(rows []caseDiff) (out []string) {
		for _, r := range rows {
			out = append(out, r.Key)
		}
		return out
	}
	assert.Equal(t, []string{"a"}, keys(d.StartedFailing))
	assert.Equal(t, []string{"b"}, keys(d.StillFailing))
	assert.Equal(t, []string{"c"}, keys(d.Fixed))
	assert.Equal(t, []string{"new"}, keys(d.Added))
	assert.Equal(t, []string{"gone"}, keys(d.Removed))
	assert.Equal(t, 1, d.Unchanged)
}
