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
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hinskii/kubetest-alt/pkg/executor"
)

// shExitCommand builds a Runner-compatible ExecCommand factory that ignores
// (name, args...) and instead runs `sh -c "exit <code>"`. Perfect for the
// exit-code table: no real k6 needed, no goroutines, no time.Sleep.
//
// If sideEffect is non-nil, it's executed before the shell script — useful
// for tests that need to plant a summary.json before k6 "exits".
func shExitCommand(code int, sideEffect func()) ExecCommand {
	return func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		if sideEffect != nil {
			sideEffect()
		}
		// #nosec G204 -- test-controlled command line, no user input.
		return exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("exit %d", code))
	}
}

// shSleepCommand runs `sh -c "sleep N"` so ctx cancellation actually needs to
// kill the process. Used for timeout/signal tests.
func shSleepCommand(seconds int) ExecCommand {
	return func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		// #nosec G204 -- test-controlled command line.
		return exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("sleep %d", seconds))
	}
}

// newTestRunner returns a Runner with stdout/stderr sunk to discard, a test
// working dir (so summary.json paths resolve to the temp dir), and the given
// exec factory.
func newTestRunner(t *testing.T, execCmd ExecCommand) (*Runner, string) {
	t.Helper()
	dir := t.TempDir()
	return &Runner{
		Exec:     execCmd,
		Stdout:   io.Discard,
		Stderr:   io.Discard,
		K6Binary: "k6",
	}, dir
}

// writeSummary drops a fixture file at $dir/summary.json for the Runner to
// pick up after "k6 exits".
func writeSummary(t *testing.T, dir, name string) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", name)) // #nosec G304 -- test path
	require.NoError(t, err)
	// #nosec G306,G703 -- test fixture; dir is t.TempDir().
	require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.json"), src, 0o644))
}

// TestRunner_ExitCodeTable is the §15.2 fundamental: k6 lies with exit codes,
// wrapper normalizes. Each row is one deterministic exit → expected phase,
// metrics presence, and message.
//
// Invariant asserted across the table: ErrorMessage is empty when Phase == passed
// (metrics go in Metrics, not smuggled into an error string).
func TestRunner_ExitCodeTable(t *testing.T) {
	cases := []struct {
		name             string
		exitCode         int
		fixture          string // summary fixture to plant; "" = no summary
		wantPhase        string
		wantMsgSubstr    string
		wantMsgNotSubstr string
		wantMetricKeys   []string // subset check — nil = no metrics expected
	}{
		{
			name:      "0 → passed, no summary, no metrics, empty message",
			exitCode:  0,
			wantPhase: executor.PhasePassed,
		},
		{
			name:           "0 → passed with summary → metrics populated, message empty",
			exitCode:       0,
			fixture:        "summary_passing.json",
			wantPhase:      executor.PhasePassed,
			wantMetricKeys: []string{"p95_ms", "rps", "checks_passed", "checks_total"},
		},
		{
			name:           "99 → failed, metrics AND threshold message",
			exitCode:       99,
			fixture:        "summary_failed_thresholds.json",
			wantPhase:      executor.PhaseFailed,
			wantMsgSubstr:  "thresholds not met",
			wantMetricKeys: []string{"p95_ms", "checks_passed", "checks_total"},
		},
		{
			name:             "99 without summary → still failed, generic message",
			exitCode:         99,
			wantPhase:        executor.PhaseFailed,
			wantMsgSubstr:    "thresholds not met",
			wantMsgNotSubstr: ":", // no "metric: threshold" detail available
		},
		{
			name:          "107 → error (script/panic per §15.2), no metrics",
			exitCode:      107,
			wantPhase:     executor.PhaseError,
			wantMsgSubstr: "exited with code 107",
		},
		{
			name:          "1 → error (generic non-zero), no metrics",
			exitCode:      1,
			wantPhase:     executor.PhaseError,
			wantMsgSubstr: "exited with code 1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var summarySrc func()
			r, dir := newTestRunner(t, nil)
			if tc.fixture != "" {
				summarySrc = func() { writeSummary(t, dir, tc.fixture) }
			}
			r.Exec = shExitCommand(tc.exitCode, summarySrc)

			result, err := r.Run(context.Background(), executor.ExecutionRequest{
				WorkingDir: dir,
				Args:       []string{"run", "script.js"},
			})
			require.NoError(t, err, "Runner must never return non-nil error for exit codes")
			assert.Equal(t, tc.wantPhase, result.Phase)

			// Invariant: no ErrorMessage on passed. Metrics land in .Metrics.
			if tc.wantPhase == executor.PhasePassed {
				assert.Empty(t, result.ErrorMessage,
					"passed runs MUST NOT carry ErrorMessage (downstream treats it as failure signal)")
			}
			if tc.wantMsgSubstr != "" {
				assert.Contains(t, result.ErrorMessage, tc.wantMsgSubstr)
			}
			if tc.wantMsgNotSubstr != "" {
				assert.NotContains(t, result.ErrorMessage, tc.wantMsgNotSubstr)
			}
			for _, k := range tc.wantMetricKeys {
				assert.Contains(t, result.Metrics, k, "metric %q missing from result", k)
			}
			// If no metric keys expected, the map should be nil (not empty map)
			// to keep result.json compact.
			if tc.wantMetricKeys == nil {
				assert.Nil(t, result.Metrics, "Metrics must be nil when no data available")
			}
		})
	}
}

// TestRunner_ContextCancel_ProcessKilled: SIGKILL from ctx cancellation gives
// ExitCode == -1, which classify() routes to phase=error (per plan spec).
// This is the "SIGKILL/-1 → error" row from step-05.
//
// Wall time budget: 500ms. sh sleeps for 5s but ctx cancels at 100ms.
// exec.CommandContext SIGKILL propagates within milliseconds.
func TestRunner_ContextCancel_ProcessKilled(t *testing.T) {
	r, _ := newTestRunner(t, shSleepCommand(5))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	result, err := r.Run(ctx, executor.ExecutionRequest{
		WorkingDir: t.TempDir(),
		Args:       []string{"run", "script.js"},
	})
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, executor.PhaseError, result.Phase, "signal-killed process → error")
	assert.Contains(t, result.ErrorMessage, "exited with code -1")
	assert.Less(t, elapsed, 500*time.Millisecond, "process must die within a few ms of ctx cancel")
}

// TestRunner_MissingBinary asserts the "non-exit error" branch of classify —
// when the binary itself doesn't exist, exec returns a *fs.PathError wrapped,
// not *exec.ExitError. Runner classifies as error with the wrapped message.
func TestRunner_MissingBinary(t *testing.T) {
	r := &Runner{
		Exec:     exec.CommandContext,
		Stdout:   io.Discard,
		Stderr:   io.Discard,
		K6Binary: "/definitely/does/not/exist/k6",
	}
	result, err := r.Run(context.Background(), executor.ExecutionRequest{
		Args:       []string{"run"},
		WorkingDir: t.TempDir(),
	})
	require.NoError(t, err)
	assert.Equal(t, executor.PhaseError, result.Phase)
	// Message should include something about the missing binary.
	assert.NotEmpty(t, result.ErrorMessage)
}

func TestRunner_ValidateRejectsEmptyArgs(t *testing.T) {
	r := &Runner{}
	err := r.Validate(context.Background(), executor.ExecutionRequest{Args: nil})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty args")
}

// TestRunner_ValidateRejectsWrongType is the forward-compat guard for step-11
// dispatch: a cypress-typed request hitting a k6 wrapper must fail here with
// a clear message rather than silently trying to run k6 on cypress args.
func TestRunner_ValidateRejectsWrongType(t *testing.T) {
	r := &Runner{}
	err := r.Validate(context.Background(), executor.ExecutionRequest{
		Type: "cypress",
		Args: []string{"cypress", "run"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cypress")
	assert.Contains(t, err.Error(), "k6")
}

func TestRunner_ValidateAcceptsMatchingType(t *testing.T) {
	r := &Runner{}
	err := r.Validate(context.Background(), executor.ExecutionRequest{
		Type: "k6",
		Args: []string{"run", "script.js"},
	})
	assert.NoError(t, err)
}

// Empty Type is accepted for backward compat during rollout — older compilers
// may not set the field yet.
func TestRunner_ValidateAcceptsEmptyType(t *testing.T) {
	r := &Runner{}
	err := r.Validate(context.Background(), executor.ExecutionRequest{
		Type: "",
		Args: []string{"run", "script.js"},
	})
	assert.NoError(t, err)
}

func TestRunner_ValidateAcceptsNonEmptyArgs(t *testing.T) {
	r := &Runner{}
	err := r.Validate(context.Background(), executor.ExecutionRequest{Args: []string{"run", "script.js"}})
	assert.NoError(t, err)
}

func TestRunner_Type(t *testing.T) {
	assert.Equal(t, "k6", NewRunner().Type())
}

func TestEnsureSummaryExport(t *testing.T) {
	// Injects flag when missing.
	got := ensureSummaryExport([]string{"run", "script.js"})
	assert.Contains(t, got, "--summary-export=summary.json")

	// Idempotent when already present.
	base := []string{"run", "--summary-export=custom.json", "script.js"}
	got = ensureSummaryExport(base)
	assert.Equal(t, base, got, "must not append when --summary-export already set")

	// Does not mutate caller's slice.
	orig := []string{"run", "script.js"}
	origCopy := append([]string(nil), orig...)
	_ = ensureSummaryExport(orig)
	assert.Equal(t, origCopy, orig, "caller's slice must be untouched")
}

func TestWorkingDir_Precedence(t *testing.T) {
	assert.Equal(t, "/work", workingDir(executor.ExecutionRequest{WorkingDir: "/work"}))
	assert.Equal(t, "/data", workingDir(executor.ExecutionRequest{DataDir: "/data"}))
	assert.Equal(t, ".", workingDir(executor.ExecutionRequest{}))
}

func TestEnvList_MergesReqEnvOnTop(t *testing.T) {
	// A synthetic base environment for determinism.
	req := executor.ExecutionRequest{Env: map[string]string{"KUBETEST_TEST_KEY": "override"}}
	out := envList(req.Env)
	found := false
	for _, e := range out {
		if e == "KUBETEST_TEST_KEY=override" {
			found = true
		}
	}
	assert.True(t, found, "request env must appear in the merged env list")
}

// TestRunner_StdoutStreamed: fake tool writes "hello" to stdout; Runner
// captures via its Stdout writer. Line-buffered semantics is a runtime
// concern (`bufio.Scanner` or unbuffered io.Copy in real exec) — we assert
// the payload survives round-trip, not the flushing cadence.
func TestRunner_StdoutStreamed(t *testing.T) {
	var stdout bytes.Buffer
	r := &Runner{
		Exec: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			// #nosec G204 -- test-controlled.
			return exec.CommandContext(ctx, "sh", "-c", "printf 'hello\\n'")
		},
		Stdout:   &stdout,
		Stderr:   io.Discard,
		K6Binary: "k6",
	}
	_, err := r.Run(context.Background(), executor.ExecutionRequest{
		Args:       []string{"run"},
		WorkingDir: t.TempDir(),
	})
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "hello")
}
