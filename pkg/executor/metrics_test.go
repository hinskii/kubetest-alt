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
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hinskii/kubetest-alt/pkg/report"
)

func runWithMetrics(t *testing.T, exit int, wd string, spec MetricsSpec) ExecutionResult {
	t.Helper()
	e := &Entry{
		Exec:        shExit(exit, nil),
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		WorkingDir:  wd,
		RequestPath: writeRequest(t, ExecutionRequest{Args: []string{"/bin/true"}, TimeoutSeconds: 30, Metrics: spec}),
		ResultDir:   t.TempDir(),
		Loader:      &bytes.Buffer{},
	}
	require.NoError(t, e.Execute(context.Background()))
	return readResult(t, e.ResultDir)
}

// The fix for "status.metrics is always empty": spec.metrics now reaches
// result.json — on failed runs too, since that's when numbers matter most.
func TestMetrics_ParsedIntoResult(t *testing.T) {
	wd := t.TempDir()
	results := filepath.Join(wd, "repo", "results")
	require.NoError(t, os.MkdirAll(results, 0o750))
	fixture, err := os.ReadFile("../report/testdata/summary.json")
	require.NoError(t, err)
	// #nosec G703 -- t.TempDir() + constant names.
	require.NoError(t, os.WriteFile(filepath.Join(results, "summary.json"), fixture, 0o600))

	for _, exit := range []int{0, 99} {
		got := runWithMetrics(t, exit, wd, MetricsSpec{From: report.FromK6Summary, Path: "repo/results/summary.json"})
		assert.Equal(t, 40.0, got.Metrics[report.Requests], "exit %d", exit)
		assert.Equal(t, 0.5, got.Metrics[report.ErrorRate], "exit %d", exit)
		assert.Empty(t, got.MetricsError)
	}
}

// A missing report is recorded, never turns a passing run into anything else.
func TestMetrics_MissingReportKeepsVerdict(t *testing.T) {
	got := runWithMetrics(t, 0, t.TempDir(), MetricsSpec{From: report.FromK6Summary, Path: "repo/results/summary.json"})
	assert.Equal(t, PhasePassed, got.Phase)
	assert.Empty(t, got.ErrorMessage, "passed runs carry no error message")
	assert.Contains(t, got.MetricsError, "report file not found")
	assert.Nil(t, got.Metrics)
}

func TestMetrics_NoSpecNoWork(t *testing.T) {
	got := runWithMetrics(t, 0, t.TempDir(), MetricsSpec{})
	assert.Nil(t, got.Metrics)
	assert.Empty(t, got.MetricsError)
}

func TestStaticDir(t *testing.T) {
	cases := map[string]string{
		"results/**/*.json":        "results",
		"results/summary.json":     "results",
		"repo/results/*.jtl":       filepath.Join("repo", "results"),
		"**/build/**/*.xml":        "",
		"summary.json":             "",
		"/abs/x.json":              "",
		"../escape/x.json":         "",
		"results/../../x/*.json":   "",
		"results/../ok/report.xml": "ok",
	}
	for in, want := range cases {
		assert.Equal(t, want, staticDir(in), in)
	}
}

// k6 silently skips --summary-export and artillery exits 1 when the output
// directory is missing (verified on grafana/k6:1.4.0 and
// artilleryio/artillery:2.0.34): the wrapper pre-creates declared dirs.
func TestPrepareOutputDirs_CreatesDeclaredDirsBeforeTool(t *testing.T) {
	wd := t.TempDir()
	var sawDirs bool
	e := &Entry{
		Exec: shExit(0, func() {
			_, err1 := os.Stat(filepath.Join(wd, "results"))
			_, err2 := os.Stat(filepath.Join(wd, "out", "k6"))
			sawDirs = err1 == nil && err2 == nil
		}),
		Stdout:     io.Discard,
		Stderr:     io.Discard,
		WorkingDir: wd,
		RequestPath: writeRequest(t, ExecutionRequest{
			Args:           []string{"/bin/true"},
			TimeoutSeconds: 30,
			Artifacts:      ArtifactSpec{Paths: []string{"results/**/*.json"}},
			Metrics:        MetricsSpec{From: report.FromK6Summary, Path: "out/k6/summary.json"},
		}),
		ResultDir: t.TempDir(),
		Loader:    &bytes.Buffer{},
	}
	require.NoError(t, e.Execute(context.Background()))
	assert.True(t, sawDirs, "dirs must exist when the tool starts")
}

func TestJUnitGlobs_FromXMLArtifacts(t *testing.T) {
	req := ExecutionRequest{Artifacts: ArtifactSpec{Paths: []string{
		"repo/results/*.xml", "repo/**/cypress/screenshots/**/*", "repo/results/**/*.XML",
	}}}
	assert.Equal(t, []string{"repo/results/*.xml", "repo/results/**/*.XML"}, junitGlobs(req))
	assert.Nil(t, junitGlobs(ExecutionRequest{}), "no artifacts → processor defaults")
}

// The verdict processor must be handed the Test's xml globs, so a report
// with a non-default name (cypress-<hash>.xml) is judged, not "missing".
func TestJUnitVerdict_GetsArtifactGlobs(t *testing.T) {
	var got []string
	e := &Entry{
		Exec:   shExit(0, nil),
		Stdout: io.Discard,
		Stderr: io.Discard,
		JUnitProcessor: func(_ string, globs []string) (TestCounts, error) {
			got = globs
			return TestCounts{Total: 2, Passed: 1, Failed: 1}, nil
		},
		WorkingDir: t.TempDir(),
		RequestPath: writeRequest(t, ExecutionRequest{
			Args: []string{"/bin/true"}, TimeoutSeconds: 30,
			Verdict:   VerdictSpec{From: VerdictFromJUnit},
			Artifacts: ArtifactSpec{Paths: []string{"repo/results/*.xml", "repo/shots/**/*"}},
		}),
		ResultDir: t.TempDir(),
		Loader:    &bytes.Buffer{},
	}
	require.NoError(t, e.Execute(context.Background()))
	assert.Equal(t, []string{"repo/results/*.xml"}, got)
	assert.Equal(t, PhaseFailed, readResult(t, e.ResultDir).Phase)
}
