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

package k6

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustReadFixture reads a testdata/*.json fixture. Fixture reads are safe
// (fixed relative path from _test.go location).
func mustReadFixture(t *testing.T, name string) []byte {
	t.Helper()
	// #nosec G304 -- path is a test-defined relative fixture name.
	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return b
}

func TestParseSummary_PassingFixture(t *testing.T) {
	s, err := ParseSummary(mustReadFixture(t, "summary_passing.json"))
	require.NoError(t, err)
	require.NotNil(t, s)

	assert.InDelta(t, 245.3, s.Metrics["http_req_duration"].P95, 0.01)
	assert.InDelta(t, 16.7, s.Metrics["http_reqs"].Rate, 0.01)
	assert.Equal(t, float64(500), s.Metrics["checks"].Passes)
	assert.Equal(t, float64(0), s.Metrics["checks"].Fails)

	assert.Empty(t, FailedThresholds(s), "passing fixture should have no failed thresholds")

	// ExtractMetrics maps summary → flat plan-§11 keys the operator UI consumes.
	m := ExtractMetrics(s)
	assert.InDelta(t, 245.3, m["p95_ms"], 0.01)
	assert.InDelta(t, 16.7, m["rps"], 0.01)
	assert.Equal(t, float64(500), m["checks_passed"])
	assert.Equal(t, float64(500), m["checks_total"])
}

func TestParseSummary_FailedThresholdsFixture(t *testing.T) {
	s, err := ParseSummary(mustReadFixture(t, "summary_failed_thresholds.json"))
	require.NoError(t, err)
	require.NotNil(t, s)

	failed := FailedThresholds(s)
	assert.Len(t, failed, 2, "two thresholds failed: p95 and error rate")
	// Deterministic sort — assert the ORDER too, so the message text is stable.
	assert.Equal(t, []string{"http_req_duration: p(95)<500", "http_req_failed: rate<0.01"}, failed)
}

// TestParseSummary_TruncatedReturnsErrorNotPanic guards against k6 crashing
// mid-write of summary.json. Wrapper must classify the run without panicking.
func TestParseSummary_TruncatedReturnsErrorNotPanic(t *testing.T) {
	require.NotPanics(t, func() {
		_, err := ParseSummary(mustReadFixture(t, "summary_truncated.json"))
		assert.Error(t, err)
	})
}

func TestParseSummary_Empty(t *testing.T) {
	_, err := ParseSummary(nil)
	assert.Error(t, err)
	_, err = ParseSummary([]byte(""))
	assert.Error(t, err)
}

func TestExtractMetrics_NilSafe(t *testing.T) {
	assert.Nil(t, ExtractMetrics(nil))
}

func TestExtractMetrics_EmptyMetricsReturnsNil(t *testing.T) {
	// Non-nil Summary but with no interesting metrics → nil map (compact JSON).
	assert.Nil(t, ExtractMetrics(&Summary{Metrics: map[string]Metric{}}))
}

func TestFailedThresholds_NilSafe(t *testing.T) {
	assert.Nil(t, FailedThresholds(nil))
}

func TestLoadSummary_MissingFile(t *testing.T) {
	_, err := LoadSummary(filepath.Join(t.TempDir(), "nope.json"))
	assert.Error(t, err)
}

func TestLoadSummary_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	src := mustReadFixture(t, "summary_passing.json")
	// #nosec G306 -- test fixture.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.json"), src, 0o644))

	s, err := LoadSummary(filepath.Join(dir, "summary.json"))
	require.NoError(t, err)
	assert.InDelta(t, 245.3, s.Metrics["http_req_duration"].P95, 0.01)
}
