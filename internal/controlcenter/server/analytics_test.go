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
	p95 := rows[2]
	assert.Equal(t, []float64{100, 300}, p95.Values, "error runs' values are left out")
	assert.Equal(t, "+200%", p95.Delta, "latest vs the average before it")
	assert.Equal(t, 200.0, p95.Avg)
	assert.Equal(t, "0.0,26.0 140.0,2.0", p95.Points)

	junit := trends([]apiclient.Run{{Phase: "failed", TestCounts: &apiclient.TestCounts{Total: 9, Failed: 2}}})
	require.Len(t, junit, 2)
	assert.Equal(t, "Tests failed", junit[0].Label)
	assert.Equal(t, 2.0, junit[0].Latest)
	assert.Equal(t, "70.0,14.0", junit[0].Points, "one point sits in the middle")
}

func TestRelChange(t *testing.T) {
	assert.Equal(t, "+50%", relChange(150, 100))
	assert.Equal(t, "-2.5%", relChange(97.5, 100))
	assert.Equal(t, "+50%", relChange(-50, -100), "against the magnitude of a negative base")
	assert.Empty(t, relChange(5, 0), "no base")
	assert.Empty(t, relChange(100, 100))
}

func TestComparisonMarkdown(t *testing.T) {
	c := &comparison{Runs: []apiclient.Run{
		{Name: "load-1", Phase: "passed", StartedAt: at(0), DurationMs: 61000,
			Metrics: map[string]float64{"p95_ms": 120, "rps": 10.12345},
			Config:  map[string]string{"vus": "10", "note": "a|b"}},
		{Name: "load-2", Phase: "failed", StartedAt: at(5), DurationMs: 62000,
			Metrics: map[string]float64{"p95_ms": 180},
			Config:  map[string]string{"vus": "20"},
			Git:     &apiclient.GitCheckout{Commit: "0123456789abcdef", Revision: "main"}},
	}}
	fillComparison(c)
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
	assert.Contains(t, comparisonMarkdown("c", "n", "t", &comparison{}, time.Now()), "No runs to compare.")
}

func TestSelectedRuns_DistinctAndBounded(t *testing.T) {
	var q strings.Builder
	q.WriteString("?run=a&run=a&run=%20&run=b")
	for i := range 20 {
		q.WriteString("&run=x" + string(rune('a'+i)))
	}
	got := selectedRuns(httptest.NewRequest(http.MethodGet, "/"+q.String(), nil))
	assert.Len(t, got, maxCompared)
	assert.Equal(t, []string{"a", "b"}, got[:2])
}

// finishedRun is a finished run of smoke with metrics and parameters.
func finishedRun(name, uid string, phase testsv1alpha1.Phase, min int, metrics map[string]string, cfg map[string]string) *testsv1alpha1.TestRun {
	started := metav1.NewTime(*at(min))
	done := metav1.NewTime(at(min).Add(30 * time.Second))
	return &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team-a", UID: types.UID(uid)},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "smoke", Source: "ui", Config: cfg},
		Status: testsv1alpha1.TestRunStatus{Phase: phase, StartedAt: &started, FinishedAt: &done,
			DurationMs: 30000, Metrics: metrics},
	}
}

func TestAnalyticsPage_TrendsAndComparison(t *testing.T) {
	other := finishedRun("other-1", "00000000-0000-0000-0000-0000000000f1", testsv1alpha1.PhasePassed, 9, nil, nil)
	other.Spec.TestRef = "other"
	w := newWorld(t, smokeTest(), other,
		finishedRun("smoke-1", "00000000-0000-0000-0000-0000000000a1", testsv1alpha1.PhasePassed, 0,
			map[string]string{"p95_ms": "100", "rps": "40"}, map[string]string{"vus": "10"}),
		finishedRun("smoke-2", "00000000-0000-0000-0000-0000000000a2", testsv1alpha1.PhaseFailed, 10,
			map[string]string{"p95_ms": "150"}, map[string]string{"vus": "20"}),
		finishedRun("smoke-3", "00000000-0000-0000-0000-0000000000a3", testsv1alpha1.PhaseError, 20, nil, nil),
		runOf("smoke-live", testsv1alpha1.PhaseRunning))
	base := "/clusters/dev/tests/team-a/smoke/analytics"

	rec := w.get(t, base, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, "3 finished runs", "the running run isn't part of the trends")
	assert.Contains(t, body, "33%", "1 passed of 3 verdicts")
	assert.Less(t, strings.Index(body, `title="smoke-1`), strings.Index(body, `title="smoke-3`), "strip is oldest first")
	assert.Contains(t, body, `>p95_ms<`)
	assert.Contains(t, body, `>rps<`)
	assert.Contains(t, body, "<polyline")
	assert.Contains(t, body, "&#43;50%", "html/template encodes +")
	assert.NotContains(t, body, "Comparison", "nothing selected yet")
	assert.Contains(t, body, `name="run" value="smoke-2"`)

	rec = w.get(t, base+"?run=smoke-2&run=smoke-1&run=other-1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = rec.Body.String()
	assert.Contains(t, body, "Comparison")
	assert.Less(t, strings.Index(body, `>smoke-1</a></th>`), strings.Index(body, `>smoke-2</a></th>`), "columns oldest first")
	assert.Contains(t, body, "Not runs of this Test (skipped): other-1")
	assert.Contains(t, body, `value="smoke-1" aria-label="compare smoke-1" checked`)
	assert.Contains(t, body, "| p95_ms | 100 | 150 (&#43;50%) |", "the Markdown is on the page")
	assert.Contains(t, body, `href="/clusters/dev/tests/team-a/smoke/analytics/compare.md?run=smoke-2&amp;run=smoke-1&amp;run=other-1"`)

	md := w.get(t, base+"/compare.md?run=smoke-1&run=smoke-2", "")
	require.Equal(t, http.StatusOK, md.Code)
	assert.Equal(t, "text/markdown; charset=utf-8", md.Header().Get("Content-Type"))
	assert.Contains(t, md.Header().Get("Content-Disposition"), `filename="smoke-comparison.md"`)
	assert.Contains(t, md.Body.String(), "| vus | 10 | 20 |")

	assert.Equal(t, http.StatusBadRequest, w.get(t, base+"/compare.md", "").Code)
}

func TestAnalyticsPage_Empty(t *testing.T) {
	w := newWorld(t, smokeTest())
	body := w.get(t, "/clusters/dev/tests/team-a/smoke/analytics?runs=999", "").Body.String()
	assert.Contains(t, body, "No finished runs yet.")
	assert.Contains(t, body, `<option value="30" selected>`, "unknown window falls back to 30")
}
