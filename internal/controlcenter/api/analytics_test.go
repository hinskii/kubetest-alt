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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

func at(min int) *time.Time {
	t := time.Date(2026, 10, 7, 12, min, 0, 0, time.UTC)
	return &t
}

func TestSummarize(t *testing.T) {
	s := summarize([]apiclient.Run{
		{Name: "a", Phase: "passed", DurationMs: 1000},
		{Name: "b", Phase: "failed", DurationMs: 3000},
		{Name: "c", Phase: "error", DurationMs: 60000},
		{Name: "d", Phase: "aborted", DurationMs: 5},
		{Name: "e", Phase: "passed", DurationMs: 2000},
	})
	assert.Equal(t, 5, s.Finished)
	assert.Equal(t, "50%", s.PassRate, "2 of 4: an error run did not pass; aborted runs have no verdict")
	assert.Equal(t, int64(2000), s.AvgMs, "error and aborted durations don't count")
	assert.Equal(t, int64(3000), s.P95Ms)
	require.NotNil(t, s.LastFailure)
	assert.Equal(t, "c", s.LastFailure.Name)

	assert.Empty(t, summarize([]apiclient.Run{{Phase: "aborted"}}).PassRate)
}

func TestTrends_RowsAreWhatTheRunsCarry(t *testing.T) {
	rows := trends([]apiclient.Run{
		{Phase: "passed", DurationMs: 1000, Metrics: map[string]float64{"p95_ms": 100, "rps": 50}},
		{Phase: "error", DurationMs: 9000, Metrics: map[string]float64{"p95_ms": 9999}},
		{Phase: "failed", DurationMs: 2000, Metrics: map[string]float64{"p95_ms": 300, "alerts_high": 2}},
	})
	labels := make([]string, 0, len(rows))
	for _, r := range rows {
		labels = append(labels, r.Label)
	}
	assert.Equal(t, []string{"Duration", "alerts_high", "p95_ms", "rps"}, labels, "no JUnit rows without JUnit runs")
	assert.Equal(t, "ms", rows[0].Unit)
	p95 := rows[2]
	assert.Equal(t, []float64{100, 300}, p95.Values, "error runs' values are left out")
	assert.Equal(t, "+200%", p95.Delta, "latest vs the average before it")
	assert.Equal(t, Trend{Label: "p95_ms", Values: []float64{100, 300}, Latest: 300, Min: 100, Avg: 200, Max: 300,
		Delta: "+200%"}, p95)
	assert.Empty(t, rows[3].Delta, "one value: nothing to compare with")

	junit := trends([]apiclient.Run{{Phase: "failed", TestCounts: &apiclient.TestCounts{Total: 9, Failed: 2}}})
	require.Len(t, junit, 2)
	assert.Equal(t, "Tests failed", junit[0].Label)
	assert.Equal(t, 2.0, junit[0].Latest)
	assert.Equal(t, []Trend{}, trends(nil))
}

func TestRelChange(t *testing.T) {
	assert.Equal(t, "+50%", relChange(150, 100))
	assert.Equal(t, "-2.5%", relChange(97.5, 100))
	assert.Equal(t, "+50%", relChange(-50, -100), "against the magnitude of a negative base")
	assert.Empty(t, relChange(5, 0), "no base")
	assert.Empty(t, relChange(100, 100))
	assert.Empty(t, relChange(100.0001, 100), "below the shown precision")
}

func TestComparisonMarkdown(t *testing.T) {
	c := &Comparison{Missing: []string{}}
	fillComparison(c, []apiclient.Run{
		{Name: "load-1", Phase: "passed", StartedAt: at(0), DurationMs: 61000,
			Metrics: map[string]float64{"p95_ms": 120, "rps": 10.12345},
			Config:  map[string]string{"vus": "10", "note": "a|b"}},
		{Name: "load-2", Phase: "failed", StartedAt: at(5), DurationMs: 62000,
			Metrics: map[string]float64{"p95_ms": 180},
			Config:  map[string]string{"vus": "20"},
			Git:     &apiclient.GitCheckout{Commit: "0123456789abcdef", Revision: "main"}},
	})
	md := comparisonMarkdown("Deploy (dev)", "team-a", "load", c, time.Date(2026, 10, 7, 13, 0, 0, 0, time.UTC))
	for _, want := range []string{
		"## Test comparison: load\n",
		"- Cluster: `Deploy (dev)`, namespace `team-a`\n",
		"| Run | load-1 | load-2 |\n| --- | --- | --- |\n",
		"| **Run** |  |  |\n",
		"| Status | passed | failed |\n",
		"| Duration | 1m 1s | 1m 2s |\n",
		"| Commit | — | 0123456 (main) |\n",
		"| p95_ms | 120 | 180 (+50%) |\n",
		"| rps | 10.123 | — |\n",
		"| note | a\\|b | — |\n",
		"| vus | 10 | 20 |\n",
		"Changes in brackets are against the first run.",
	} {
		assert.Contains(t, md, want)
	}
	assert.NotContains(t, md, "| Tests |", "rows nobody has are left out")
	assert.Contains(t, comparisonMarkdown("c", "n", "t", &Comparison{}, time.Now()), "No runs to compare.")
}

func TestSelectedRuns_DistinctAndBounded(t *testing.T) {
	var q strings.Builder
	q.WriteString("?run=a&run=a&run=%20&run=b")
	for i := range 20 {
		q.WriteString("&run=x" + string(rune('a'+i)))
	}
	got := selectedRuns(httptest.NewRequest(http.MethodGet, "/"+q.String(), nil))
	assert.Len(t, got, MaxCompared)
	assert.Equal(t, []string{"a", "b"}, got[:2])
}

// finishedAt is a finished run of smoke with metrics and parameters.
func finishedAt(name, uid string, phase testsv1alpha1.Phase, min int, metrics, cfg map[string]string) *testsv1alpha1.TestRun {
	started := metav1.NewTime(*at(min))
	done := metav1.NewTime(at(min).Add(30 * time.Second))
	return &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team-a", UID: types.UID(uid)},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "smoke", Source: "ui", Config: cfg},
		Status: testsv1alpha1.TestRunStatus{Phase: phase, StartedAt: &started, FinishedAt: &done,
			DurationMs: 30000, Metrics: metrics},
	}
}

func TestAnalytics_TrendsAndComparison(t *testing.T) {
	other := finishedAt("other-1", "uid-f1", testsv1alpha1.PhasePassed, 9, nil, nil)
	other.Spec.TestRef = "other"
	live := runOf("smoke-live", time.Now())
	live.Status.Phase, live.Status.FinishedAt = testsv1alpha1.PhaseRunning, nil
	w := newWorld(t, smokeTest(), other, live,
		finishedAt("smoke-1", "uid-a1", testsv1alpha1.PhasePassed, 0,
			map[string]string{"p95_ms": "100", "rps": "40"}, map[string]string{"vus": "10"}),
		finishedAt("smoke-2", "uid-a2", testsv1alpha1.PhaseFailed, 10,
			map[string]string{"p95_ms": "150"}, map[string]string{"vus": "20"}),
		finishedAt("smoke-3", "uid-a3", testsv1alpha1.PhaseError, 20, nil, nil))
	base := smokePath + "/analytics"

	rec := w.do(t, http.MethodGet, base, viewer, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	a := decodeAs[Analytics](t, rec)
	assert.Equal(t, 30, a.Window)
	require.Len(t, a.Runs, 3, "the running run isn't part of the trends")
	assert.Equal(t, "smoke-3", a.Runs[0].Name, "newest first")
	assert.Equal(t, 3, a.Summary.Finished)
	assert.Equal(t, "33%", a.Summary.PassRate, "1 passed of 3 verdicts")
	require.NotNil(t, a.Summary.LastFailure)
	assert.Equal(t, "smoke-3", a.Summary.LastFailure.Name)
	labels := make([]string, 0, len(a.Trends))
	for _, tr := range a.Trends {
		labels = append(labels, tr.Label)
	}
	assert.Equal(t, []string{"Duration", "p95_ms", "rps"}, labels)
	assert.Equal(t, "+50%", a.Trends[1].Delta)
	assert.Nil(t, a.Cases, "no JUnit, no cases summary")

	rec = w.do(t, http.MethodGet, base+"/compare?run=smoke-2&run=smoke-1&run=other-1", viewer, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	c := decodeAs[Comparison](t, rec)
	require.Len(t, c.Runs, 2)
	assert.Equal(t, "smoke-1", c.Runs[0].Name, "columns oldest first")
	assert.Equal(t, []string{"other-1"}, c.Missing, "not a run of this Test")
	assert.Equal(t, CompareRow{Label: "p95_ms", Cells: []CompareCell{{Value: "100"}, {Value: "150", Delta: "+50%"}}},
		c.Metrics[0])
	assert.Contains(t, c.Markdown, "| p95_ms | 100 | 150 (+50%) |")
	assert.Contains(t, c.Markdown, "| vus | 10 | 20 |")

	none := w.do(t, http.MethodGet, base+"/compare", viewer, nil)
	assert.Equal(t, http.StatusBadRequest, none.Code)
	assert.Equal(t, "Pick the runs to compare (?run= is missing).", errorOf(t, none))
}

func TestAnalytics_CasesAndEmpty(t *testing.T) {
	w := newRunWorld(t, nil, smokeTest(), casesRun())
	a := decodeAs[Analytics](t, w.do(t, http.MethodGet, smokePath+"/analytics?runs=10", viewer, nil))
	assert.Equal(t, 10, a.Window)
	require.NotNil(t, a.Cases, "the runs reported JUnit")
	assert.Equal(t, &CasesSummary{Flaky: 1, FailingNow: 2}, a.Cases)

	empty := decodeAs[Analytics](t, newWorld(t, smokeTest()).do(t, http.MethodGet, smokePath+"/analytics?runs=999", viewer, nil))
	assert.Equal(t, 30, empty.Window, "unknown window falls back to 30")
	assert.Empty(t, empty.Runs)
	assert.Equal(t, []Trend{}, empty.Trends)
}
