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

package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readTermination(t *testing.T, path string) ExecutionResult {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- t.TempDir() path
	require.NoError(t, err)
	got, ok := ParseTerminationSummary(string(b))
	require.True(t, ok, "termination message must parse: %s", b)
	return got
}

func TestEntry_WritesVerdictToTerminationMessage(t *testing.T) {
	req := ExecutionRequest{Args: []string{"/bin/false"}, TimeoutSeconds: 30}
	e := entryFor(t, req, 3, nil)
	e.TerminationMessagePath = filepath.Join(t.TempDir(), "termination-log")
	require.NoError(t, e.Execute(context.Background()))

	got := readTermination(t, e.TerminationMessagePath)
	assert.Equal(t, PhaseFailed, got.Phase)
	assert.Contains(t, got.ErrorMessage, "exit code 3")
}

func TestEntry_SetupErrorAlsoReachesTerminationMessage(t *testing.T) {
	e := entryFor(t, ExecutionRequest{TimeoutSeconds: 30}, 0, nil) // no command, no args
	e.TerminationMessagePath = filepath.Join(t.TempDir(), "termination-log")
	require.NoError(t, e.Execute(context.Background()))
	assert.Equal(t, PhaseError, readTermination(t, e.TerminationMessagePath).Phase)
}

func TestTerminationSummary_FitsKubeletLimit(t *testing.T) {
	metrics := map[string]float64{}
	for i := range 300 {
		metrics[fmt.Sprintf("metric_with_a_long_name_%03d", i)] = float64(i) + 0.123456
	}
	r := ExecutionResult{
		Phase:        PhaseFailed,
		ErrorMessage: strings.Repeat("ż", 5000),
		TestCounts:   &TestCounts{Total: 10, Failed: 2, Passed: 8},
		Metrics:      metrics,
		Artifacts:    []ArtifactRef{{Path: "a.txt"}},
	}
	b := TerminationSummary(r)
	assert.LessOrEqual(t, len(b), maxTerminationMessageBytes)

	got, ok := ParseTerminationSummary(string(b))
	require.True(t, ok)
	assert.Equal(t, PhaseFailed, got.Phase)
	assert.Equal(t, r.TestCounts, got.TestCounts, "counts always survive")
	assert.Nil(t, got.Metrics, "metrics are dropped when they don't fit")
	assert.Nil(t, got.Artifacts, "artifacts stay in result.json")
	assert.True(t, utf8.ValidString(got.ErrorMessage), "truncation keeps UTF-8 valid")
	assert.LessOrEqual(t, len(got.ErrorMessage), maxSummaryErrorBytes)

	small := TerminationSummary(ExecutionResult{Phase: PhasePassed, Metrics: map[string]float64{"rps": 12}})
	got, ok = ParseTerminationSummary(string(small))
	require.True(t, ok)
	assert.Equal(t, map[string]float64{"rps": 12}, got.Metrics, "metrics kept when they fit")
}

func TestParseTerminationSummary_RejectsOtherMessages(t *testing.T) {
	for _, msg := range []string{"", "panic: boom", `{"phase":"running"}`, `{"phase":`, "FETCH_ERROR: x"} {
		_, ok := ParseTerminationSummary(msg)
		assert.False(t, ok, msg)
	}
}

func TestEntry_ReportsCheckedOutCommit(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, ContentInfoFile),
		[]byte(`{"gitRevision":"main","gitCommit":"0123456789abcdef0123456789abcdef01234567"}`), 0o600))
	e := entryFor(t, ExecutionRequest{Args: []string{"tool"}, TimeoutSeconds: 30, DataDir: dataDir}, 0, nil)
	e.TerminationMessagePath = filepath.Join(t.TempDir(), "termination-log")
	require.NoError(t, e.Execute(context.Background()))

	want := &ContentInfo{GitRevision: "main", GitCommit: "0123456789abcdef0123456789abcdef01234567"}
	assert.Equal(t, want, readResult(t, e.ResultDir).Content)
	assert.Equal(t, want, readTermination(t, e.TerminationMessagePath).Content, "also without object storage")

	e = entryFor(t, ExecutionRequest{Args: []string{"tool"}, TimeoutSeconds: 30, DataDir: t.TempDir()}, 0, nil)
	require.NoError(t, e.Execute(context.Background()))
	assert.Nil(t, readResult(t, e.ResultDir).Content, "no git source, no content info")
}
