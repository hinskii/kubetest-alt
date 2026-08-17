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

// Package k6 implements the executor.Runner interface for grafana/k6.
package k6

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hinskii/kubetest-alt/pkg/executor"
)

// executorType is the string reported by Runner.Type. Matches the enum value
// on Test.spec.type validated by the CRD webhook.
const executorType = "k6"

// SummaryFileName is where we tell k6 to export its post-run JSON summary.
// Relative to the tool's working directory.
const SummaryFileName = "summary.json"

// K6 exit codes per §15.2. Not exhaustive — the Runner only special-cases
// what changes verdict; anything else falls to "error".
const (
	exitPassed          = 0
	exitThresholdFailed = 99
	// 107, 108, 1, ... — script/panic/user errors, all classified "error".
)

// ExecCommand is the process factory. Overridable for tests so we don't have
// to spawn real k6 binaries in unit tests. Default is exec.CommandContext.
type ExecCommand func(ctx context.Context, name string, args ...string) *exec.Cmd

// Runner is the k6 executor. Zero value is NOT usable — use NewRunner or
// populate all fields explicitly.
type Runner struct {
	// Exec is the process factory. exec.CommandContext in production.
	Exec ExecCommand

	// Stdout / Stderr receive k6's output line-buffered. The operator's log
	// tail reads pod stdout so we point Stdout at os.Stdout in production.
	Stdout io.Writer
	Stderr io.Writer

	// K6Binary is the executable name on PATH. Defaults to "k6". Overridable
	// for tests without touching the Exec factory.
	K6Binary string
}

// NewRunner builds a Runner wired to real exec + os.Stdout/os.Stderr.
func NewRunner() *Runner {
	return &Runner{
		Exec:     exec.CommandContext,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		K6Binary: "k6",
	}
}

// Type implements executor.Runner.
func (r *Runner) Type() string { return executorType }

// Validate implements executor.Runner. Checks:
//   - req.Type, if set, must be "k6" (guards against misrouted request.json
//     once step 11 introduces multi-executor dispatch — a cypress request
//     hitting a k6 wrapper image fails here with a clear message);
//   - req.Args must be non-empty (nothing meaningful to run otherwise).
func (r *Runner) Validate(_ context.Context, req executor.ExecutionRequest) error {
	if req.Type != "" && req.Type != executorType {
		return fmt.Errorf("k6: request Type=%q, this wrapper only serves %q", req.Type, executorType)
	}
	if len(req.Args) == 0 {
		return errors.New("k6: empty args (need at least a subcommand like 'run <script>')")
	}
	return nil
}

// Run implements executor.Runner. Contract:
//   - Never returns non-nil error for test outcomes; the verdict is in
//     result.Phase.
//   - Reads summary.json from working dir after k6 exits (best-effort;
//     missing/malformed summary reduces to a plain message).
//   - Respects ctx cancellation via exec.CommandContext.
func (r *Runner) Run(ctx context.Context, req executor.ExecutionRequest) (executor.ExecutionResult, error) {
	args := ensureSummaryExport(req.Args)
	binary := r.K6Binary
	if binary == "" {
		binary = "k6"
	}

	// #nosec G204 -- binary+args come from operator-controlled request.json,
	// itself derived from the validated Test CRD spec.
	cmd := r.Exec(ctx, binary, args...)
	cmd.Dir = workingDir(req)
	cmd.Env = envList(req.Env)
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr

	runErr := cmd.Run()
	exitCode := extractExitCode(runErr)

	// Best-effort summary parse — never gates verdict.
	summary, _ := LoadSummary(filepath.Join(cmd.Dir, SummaryFileName))

	return classify(exitCode, runErr, summary), nil
}

// classify maps exit code + summary into an ExecutionResult. Extracted so it
// can be table-tested without spawning any real processes.
//
// §15.2 rules:
//   - 0 → passed, Metrics populated from summary (if any), ErrorMessage empty
//   - 99 → failed, Metrics populated, ErrorMessage names failed thresholds
//   - anything else (1, 107, 108, -1 for signal-killed) → error, no Metrics
//
// Invariant: ErrorMessage is empty when Phase == passed. Downstream (scraper,
// GUI) treats non-empty ErrorMessage as a failure signal.
func classify(exitCode int, runErr error, summary *Summary) executor.ExecutionResult {
	switch exitCode {
	case exitPassed:
		return executor.ExecutionResult{
			Phase:   executor.PhasePassed,
			Metrics: ExtractMetrics(summary),
		}

	case exitThresholdFailed:
		res := executor.ExecutionResult{
			Phase:   executor.PhaseFailed,
			Metrics: ExtractMetrics(summary),
		}
		if failed := FailedThresholds(summary); len(failed) > 0 {
			res.ErrorMessage = "k6 thresholds not met: " + strings.Join(failed, ", ")
		} else {
			res.ErrorMessage = "k6 thresholds not met"
		}
		return res

	default:
		msg := fmt.Sprintf("k6 exited with code %d", exitCode)
		if runErr != nil && !isExitError(runErr) {
			// Non-exit errors (e.g. binary not found) — include the raw error.
			msg += ": " + runErr.Error()
		}
		return executor.ExecutionResult{Phase: executor.PhaseError, ErrorMessage: msg}
	}
}

// extractExitCode returns k6's process exit code. Signal-killed processes
// report -1 via *exec.ExitError.ExitCode(); non-exit errors (binary missing,
// permission denied) report -1 too, but with a non-ExitError type.
func extractExitCode(runErr error) int {
	if runErr == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return exitErr.ExitCode()
	}
	// Non-exit errors (binary missing etc.) — treat as error phase, -1
	// gives classify the "default" branch.
	return -1
}

func isExitError(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

// ensureSummaryExport appends --summary-export=summary.json if the user
// didn't set it explicitly. Idempotent — safe to call on args already
// containing the flag.
func ensureSummaryExport(args []string) []string {
	for _, a := range args {
		if strings.HasPrefix(a, "--summary-export") {
			return args
		}
	}
	// Copy to avoid mutating the caller's slice.
	out := make([]string, 0, len(args)+1)
	out = append(out, args...)
	out = append(out, "--summary-export="+SummaryFileName)
	return out
}

// workingDir picks the tool's cwd. Prefer explicit WorkingDir; fall back to
// DataDir (where the content-fetcher put files); last resort "." so exec
// doesn't complain.
func workingDir(req executor.ExecutionRequest) string {
	if req.WorkingDir != "" {
		return req.WorkingDir
	}
	if req.DataDir != "" {
		return req.DataDir
	}
	return "."
}

// envList merges request.Env on top of the current process environment.
// Request-supplied vars win on key conflict — expected, since the operator
// intentionally set them.
func envList(reqEnv map[string]string) []string {
	base := os.Environ()
	if len(reqEnv) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(reqEnv))
	out = append(out, base...)
	for k, v := range reqEnv {
		out = append(out, k+"="+v)
	}
	return out
}
