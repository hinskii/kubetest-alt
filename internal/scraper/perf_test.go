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

package scraper

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseK6_ReadsSummaryFromWorkingDir: k6 perf parser fetches summary.json
// from the wrapper's working directory. Real fixture from pkg/executor/k6.
func TestParseK6_ReadsSummaryFromWorkingDir(t *testing.T) {
	// Copy a real k6 summary fixture into the working dir.
	dir := t.TempDir()
	// #nosec G304 -- fixed testdata path from the sibling k6 package.
	src, err := os.ReadFile(filepath.Join("..", "..", "pkg", "executor", "k6", "testdata", "summary_passing.json"))
	require.NoError(t, err)
	// #nosec G306,G703 -- test fixture; dir is t.TempDir().
	require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.json"), src, 0o644))

	parser := PerfParserFor("k6")
	require.NotNil(t, parser)
	metrics, err := parser(dir)
	require.NoError(t, err)
	// Fixture asserts these values (verified in k6/summary_test.go).
	assert.InDelta(t, 245.3, metrics["p95_ms"], 0.01)
	assert.InDelta(t, 16.7, metrics["rps"], 0.01)
}

// TestParseK6_MissingSummary: no summary.json → error, no panic.
func TestParseK6_MissingSummary(t *testing.T) {
	parser := PerfParserFor("k6")
	_, err := parser(t.TempDir())
	require.Error(t, err)
}
