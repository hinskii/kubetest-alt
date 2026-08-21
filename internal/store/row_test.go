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

package store

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// fixedTime is a small helper — every test needs deterministic wall clock.
func fixedTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339Nano, s)
	require.NoError(t, err)
	return ts.UTC()
}

func mkTerminalRun(t *testing.T) *testsv1alpha1.TestRun {
	t.Helper()
	q := metav1.NewTime(fixedTime(t, "2026-08-18T10:00:00Z"))
	s := metav1.NewTime(fixedTime(t, "2026-08-18T10:00:05Z"))
	f := metav1.NewTime(fixedTime(t, "2026-08-18T10:00:35Z"))
	return &testsv1alpha1.TestRun{
		TypeMeta: metav1.TypeMeta{Kind: "TestRun", APIVersion: "tests.kubetest.io/v1alpha1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sample-run",
			Namespace: "default",
			UID:       types.UID("00000000-0000-0000-0000-0000cafe0001"),
		},
		Spec: testsv1alpha1.TestRunSpec{
			TestRef: "sample-test",
			Source:  "cli",
			Tags:    map[string]string{"team": "sre"},
		},
		Status: testsv1alpha1.TestRunStatus{
			Phase:      testsv1alpha1.PhasePassed,
			QueuedAt:   &q,
			StartedAt:  &s,
			FinishedAt: &f,
			DurationMs: 30_000,
		},
	}
}

// TestRowFromRun_MinimalPassed pins the happy path — a bare-terminal-run
// projects to a Row with matching fields and no phantom sub-objects.
func TestRowFromRun_MinimalPassed(t *testing.T) {
	run := mkTerminalRun(t)
	row, err := RowFromRun(run, nil)
	require.NoError(t, err)

	assert.Equal(t, "00000000-0000-0000-0000-0000cafe0001", row.UID)
	assert.Equal(t, "sample-run", row.Name)
	assert.Equal(t, "default", row.Namespace)
	assert.Equal(t, "sample-test", row.TestRef)
	assert.Equal(t, "passed", row.Phase)
	assert.Equal(t, "cli", row.Source)
	assert.Equal(t, map[string]string{"team": "sre"}, row.Tags)
	assert.Equal(t, int64(30_000), row.DurationMs)
	assert.NotNil(t, row.QueuedAt)
	assert.NotNil(t, row.StartedAt)
	assert.True(t, row.FinishedAt.Equal(fixedTime(t, "2026-08-18T10:00:35Z")))

	// Sub-objects should all be nil (not empty maps) — the DB stores NULL
	// rather than {}. Reduces noise for the API server's JSON responses.
	assert.Nil(t, row.Metrics)
	assert.Nil(t, row.Steps)
	assert.Nil(t, row.TestCounts)
	assert.Nil(t, row.ArtifactRefs)
	assert.Nil(t, row.ResolvedSpec)
}

// TestRowFromRun_MetricsParseSkipInvalid is the step-09 rule for metric
// values that don't parse as float: SKIP the value + emit a warning; NEVER
// fail the save. A single tool emitting a bad metric can't block persisting
// the whole run's history.
func TestRowFromRun_MetricsParseSkipInvalid(t *testing.T) {
	run := mkTerminalRun(t)
	run.Status.Metrics = map[string]string{
		"p95_ms":         "123.4",
		"rps":            "1500",
		"checks_ratio":   "0.98",
		"bad_alpha":      "not-a-number",
		"bad_percentage": "98%", // Postgres numeric wouldn't take it either
		"empty":          "",
	}
	var warnings []MetricParseWarning
	row, err := RowFromRun(run, func(w MetricParseWarning) { warnings = append(warnings, w) })
	require.NoError(t, err)

	// Only the three parseable metrics survive.
	assert.Equal(t, map[string]float64{
		"p95_ms":       123.4,
		"rps":          1500,
		"checks_ratio": 0.98,
	}, row.Metrics)

	// The three invalid ones were warned about — order irrelevant.
	warnedKeys := map[string]bool{}
	for _, w := range warnings {
		warnedKeys[w.Key] = true
		assert.NotNil(t, w.Err)
	}
	assert.Equal(t, map[string]bool{
		"bad_alpha":      true,
		"bad_percentage": true,
		"empty":          true,
	}, warnedKeys)
}

// TestRowFromRun_AllInvalidMetricsYieldsNilMap — an entirely bad metrics map
// should not create an empty JSONB object; NULL is cleaner for the API
// server and Postgres.
func TestRowFromRun_AllInvalidMetricsYieldsNilMap(t *testing.T) {
	run := mkTerminalRun(t)
	run.Status.Metrics = map[string]string{"a": "x", "b": "y"}
	row, err := RowFromRun(run, nil) // nil warn is legal
	require.NoError(t, err)
	assert.Nil(t, row.Metrics, "all-invalid map must yield nil, not empty map")
}

// TestRowFromRun_NilWarnFuncDropsWarnings proves warn=nil is safe.
func TestRowFromRun_NilWarnFuncDropsWarnings(t *testing.T) {
	run := mkTerminalRun(t)
	run.Status.Metrics = map[string]string{"good": "1", "bad": "nope"}
	row, err := RowFromRun(run, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{"good": 1}, row.Metrics)
}

// TestRowFromRun_TestCountsAndArtifactsRoundTrip pins the whole payload
// shape survives the projection.
func TestRowFromRun_TestCountsAndArtifactsRoundTrip(t *testing.T) {
	run := mkTerminalRun(t)
	run.Status.TestCounts = &testsv1alpha1.TestCounts{Total: 10, Passed: 8, Failed: 2}
	run.Status.ArtifactRefs = []testsv1alpha1.ArtifactRef{
		{Path: "results/junit.xml", Key: "run-1/results/junit.xml", SizeBytes: 512, ContentType: "application/xml"},
	}
	run.Status.LogsRef = "kubetest-logs/run-1/"
	run.Status.Message = "verdict: passed"

	row, err := RowFromRun(run, nil)
	require.NoError(t, err)

	require.NotNil(t, row.TestCounts)
	assert.Equal(t, 10, row.TestCounts.Total)
	assert.Equal(t, 8, row.TestCounts.Passed)
	require.Len(t, row.ArtifactRefs, 1)
	assert.Equal(t, "run-1/results/junit.xml", row.ArtifactRefs[0].Key)
	assert.Equal(t, int64(512), row.ArtifactRefs[0].SizeBytes)
	assert.Equal(t, "kubetest-logs/run-1/", row.LogsRef)
	assert.Equal(t, "verdict: passed", row.Message)
}

// TestRowFromRun_StepsRoundTrip verifies the map[string]StepResult → JSONB
// projection. Steps carry timestamps that must land in UTC.
func TestRowFromRun_StepsRoundTrip(t *testing.T) {
	run := mkTerminalRun(t)
	stepStart := metav1.NewTime(fixedTime(t, "2026-08-18T10:00:10Z"))
	stepEnd := metav1.NewTime(fixedTime(t, "2026-08-18T10:00:30Z"))
	run.Status.Steps = map[string]testsv1alpha1.StepResult{
		"setup": {Phase: testsv1alpha1.StepPhasePassed, StartedAt: &stepStart, FinishedAt: &stepEnd},
		"run":   {Phase: testsv1alpha1.StepPhasePassed},
	}
	row, err := RowFromRun(run, nil)
	require.NoError(t, err)

	require.Contains(t, row.Steps, "setup")
	setup := row.Steps["setup"].(map[string]any)
	assert.Equal(t, "passed", setup["phase"])
	// timestamps must be UTC time.Time values so the pgx driver serializes
	// them as timestamptz. Assert IsZero-adjacent — .Equal() on time values.
	ts, ok := setup["startedAt"].(time.Time)
	require.True(t, ok, "startedAt should be time.Time, got %T", setup["startedAt"])
	assert.True(t, ts.Equal(fixedTime(t, "2026-08-18T10:00:10Z")))
}

// TestRowFromRun_ResolvedSpecJSONUnmarshalled — the CRD stores the Test
// spec snapshot as a raw JSON string; the row must land it as a map so it
// goes into jsonb, not text.
func TestRowFromRun_ResolvedSpecJSONUnmarshalled(t *testing.T) {
	run := mkTerminalRun(t)
	run.Status.ResolvedSpec = `{"type":"k6","container":{"image":"grafana/k6"}}`
	row, err := RowFromRun(run, nil)
	require.NoError(t, err)
	assert.Equal(t, "k6", row.ResolvedSpec["type"])
	container := row.ResolvedSpec["container"].(map[string]any)
	assert.Equal(t, "grafana/k6", container["image"])
}

// TestRowFromRun_BadResolvedSpecSilentlyDropped — corrupt snapshot bytes
// shouldn't fail the whole save; the raw string still lives on the CRD.
func TestRowFromRun_BadResolvedSpecSilentlyDropped(t *testing.T) {
	run := mkTerminalRun(t)
	run.Status.ResolvedSpec = `this-is-not-json`
	row, err := RowFromRun(run, nil)
	require.NoError(t, err)
	assert.Nil(t, row.ResolvedSpec)
}

// TestRowFromRun_ValidationErrors — the guards.
func TestRowFromRun_ValidationErrors(t *testing.T) {
	t.Run("nil run", func(t *testing.T) {
		_, err := RowFromRun(nil, nil)
		require.Error(t, err)
	})
	t.Run("empty UID", func(t *testing.T) {
		run := mkTerminalRun(t)
		run.UID = ""
		_, err := RowFromRun(run, nil)
		require.Error(t, err)
	})
	t.Run("missing FinishedAt", func(t *testing.T) {
		run := mkTerminalRun(t)
		run.Status.FinishedAt = nil
		_, err := RowFromRun(run, nil)
		require.Error(t, err)
	})
}

// TestIsTerminal pins the phase set the store accepts. If someone adds a new
// terminal phase to the CRD, they need to update this + the store's SQL
// upsert phase check (postgres.go).
func TestIsTerminal(t *testing.T) {
	terminal := []testsv1alpha1.Phase{
		testsv1alpha1.PhasePassed,
		testsv1alpha1.PhaseFailed,
		testsv1alpha1.PhaseAborted,
		testsv1alpha1.PhaseError,
	}
	nonTerminal := []testsv1alpha1.Phase{
		testsv1alpha1.PhaseQueued,
		testsv1alpha1.PhaseRunning,
		testsv1alpha1.PhasePaused,
	}
	for _, p := range terminal {
		assert.Truef(t, IsTerminal(p), "phase %q must be terminal", p)
	}
	for _, p := range nonTerminal {
		assert.Falsef(t, IsTerminal(p), "phase %q must NOT be terminal", p)
	}
}

// TestMetricParseWarning_Error pins the error-string format the operator
// logs use for grep-ability.
func TestMetricParseWarning_Error(t *testing.T) {
	_, parseErr := strconv.ParseFloat("nope", 64)
	require.Error(t, parseErr)
	w := MetricParseWarning{Key: "p95_ms", RawValue: "nope", Err: parseErr}
	assert.Contains(t, w.Error(), `"p95_ms"`)
	assert.Contains(t, w.Error(), `"nope"`)
}
