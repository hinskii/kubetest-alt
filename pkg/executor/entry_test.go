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

// Tests for the generic /entry pipeline. The verdict matrix (baseline
// exit code × optional verdictFrom processor) is the heart of the step 11
// refactor — cases are named to make the intent grep-obvious.
package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -----------------------------------------------------------------------------
// Scaffolding
// -----------------------------------------------------------------------------

// shExit builds an Exec factory that runs `sh -c "exit N"`, ignoring the
// requested binary. sideEffect (optional) fires BEFORE the shell script —
// tests use it to plant a JUnit report or JTL file before the "tool exits".
func shExit(code int, sideEffect func()) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		if sideEffect != nil {
			sideEffect()
		}
		// #nosec G204 -- test-only, code is a literal int.
		return exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("exit %d", code))
	}
}

func writeRequest(t *testing.T, req ExecutionRequest) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "request.json")
	b, err := json.Marshal(req)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, b, 0o600))
	return path
}

func readResult(t *testing.T, resultDir string) ExecutionResult {
	t.Helper()
	// #nosec G304 -- resultDir is t.TempDir() from the caller.
	b, err := os.ReadFile(filepath.Join(resultDir, ResultFileName))
	require.NoError(t, err)
	var got ExecutionResult
	require.NoError(t, json.Unmarshal(b, &got), "result.json must be valid schema")
	return got
}

// entryFor returns an Entry wired to a shell-exit factory and no verdict
// processors — tests attach processors as they need them. The sideEffect
// parameter is currently always nil at call sites (the verdict-processor
// tests plant their own fake processors instead of file-system fixtures);
// kept as a parameter so a future test needing "plant a file BEFORE the
// tool exits" doesn't have to duplicate the helper.
//
//nolint:unparam // sideEffect is a future-proofing hook — keep the parameter.
func entryFor(t *testing.T, req ExecutionRequest, exitCode int, sideEffect func()) *Entry {
	t.Helper()
	return &Entry{
		Exec:        shExit(exitCode, sideEffect),
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		RequestPath: writeRequest(t, req),
		ResultDir:   t.TempDir(),
		Loader:      &bytes.Buffer{},
	}
}

// -----------------------------------------------------------------------------
// R1: baseline verdict from exit code (no verdictFrom)
// -----------------------------------------------------------------------------

// TestEntry_Verdict_ExitZeroNoProcessorPassed: the default rule.
func TestEntry_Verdict_ExitZeroNoProcessorPassed(t *testing.T) {
	req := ExecutionRequest{Args: []string{"/bin/true"}, TimeoutSeconds: 30}
	e := entryFor(t, req, 0, nil)
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhasePassed, got.Phase)
	assert.Empty(t, got.ErrorMessage)
}

// TestEntry_Verdict_ExitNonZeroNoProcessorFailed: non-zero exit → failed
// with "exit code N" — no attempt to classify further.
func TestEntry_Verdict_ExitNonZeroNoProcessorFailed(t *testing.T) {
	req := ExecutionRequest{Args: []string{"/bin/false"}, TimeoutSeconds: 30}
	e := entryFor(t, req, 3, nil)
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseFailed, got.Phase)
	assert.Contains(t, got.ErrorMessage, "exit code 3")
}

// TestEntry_Verdict_ExecFailureIsError: binary missing / permission
// denied → error phase (distinct from tool-said-fail).
func TestEntry_Verdict_ExecFailureIsError(t *testing.T) {
	req := ExecutionRequest{Args: []string{"/definitely/does/not/exist"}, TimeoutSeconds: 30}
	e := &Entry{
		Exec: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			// #nosec G204 -- test-only real-exec path; name comes from the test literal.
			return exec.CommandContext(ctx, name, args...)
		},
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		RequestPath: writeRequest(t, req),
		ResultDir:   t.TempDir(),
		Loader:      &bytes.Buffer{},
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseError, got.Phase)
	assert.Contains(t, got.ErrorMessage, "exec failed")
}

// -----------------------------------------------------------------------------
// R2: verdictFrom=jtl — JTL processor overrides base exit-code verdict
// -----------------------------------------------------------------------------

// TestEntry_Verdict_ExitZeroButJTL100PctErrors_ShouldFail: THE §15.2
// regression — jmeter exits 0 even at 100% failures. Verdict comes from
// the JTL processor; base "exit 0 = passed" gets OVERRIDDEN.
func TestEntry_Verdict_ExitZeroButJTL100PctErrors_ShouldFail(t *testing.T) {
	req := ExecutionRequest{
		Args:           []string{"/bin/true"},
		TimeoutSeconds: 30,
		Verdict:        VerdictSpec{From: VerdictFromJTL, ErrorRateMax: "0"},
	}
	e := entryFor(t, req, 0, nil)
	e.WorkingDir = t.TempDir()
	e.JTLProcessor = func(_ string, threshold float64) (JTLProcessorResult, error) {
		// Fake: report 100% error rate against threshold 0 → not passed.
		return JTLProcessorResult{
			SamplesTotal:  10,
			SamplesFailed: 10,
			ErrorRate:     1.0,
			Threshold:     threshold,
			Passed:        false,
		}, nil
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseFailed, got.Phase, "JTL override wins over exit code 0")
	assert.Contains(t, got.ErrorMessage, "error rate")
}

// TestEntry_Verdict_JTLBoundary_RatioEqualsThreshold_Passed: ≤ inclusive.
// Ratio EXACTLY at threshold → passed. Pinned test name per plan.
func TestEntry_Verdict_JTLBoundary_RatioEqualsThreshold_Passed(t *testing.T) {
	req := ExecutionRequest{
		Args:           []string{"/bin/true"},
		TimeoutSeconds: 30,
		Verdict:        VerdictSpec{From: VerdictFromJTL, ErrorRateMax: "0.05"},
	}
	e := entryFor(t, req, 0, nil)
	e.WorkingDir = t.TempDir()
	e.JTLProcessor = func(_ string, threshold float64) (JTLProcessorResult, error) {
		return JTLProcessorResult{
			SamplesTotal: 100, SamplesFailed: 5, ErrorRate: 0.05,
			Threshold: threshold, Passed: true, // ratio == threshold → passed
		}, nil
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhasePassed, got.Phase)
	assert.Empty(t, got.ErrorMessage)
}

// TestEntry_Verdict_JTLBoundary_RatioJustAboveThreshold_Failed: opposite
// side of the boundary. Ratio 0.0501 vs threshold 0.05 → failed.
func TestEntry_Verdict_JTLBoundary_RatioJustAboveThreshold_Failed(t *testing.T) {
	req := ExecutionRequest{
		Args:           []string{"/bin/true"},
		TimeoutSeconds: 30,
		Verdict:        VerdictSpec{From: VerdictFromJTL, ErrorRateMax: "0.05"},
	}
	e := entryFor(t, req, 0, nil)
	e.WorkingDir = t.TempDir()
	e.JTLProcessor = func(_ string, threshold float64) (JTLProcessorResult, error) {
		return JTLProcessorResult{
			SamplesTotal: 10000, SamplesFailed: 501, ErrorRate: 0.0501,
			Threshold: threshold, Passed: false,
		}, nil
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseFailed, got.Phase)
}

// TestEntry_Verdict_JTLMalformed_IsErrorNotPanic: processor error →
// phase=error, never panic.
func TestEntry_Verdict_JTLMalformed_IsErrorNotPanic(t *testing.T) {
	req := ExecutionRequest{
		Args:           []string{"/bin/true"},
		TimeoutSeconds: 30,
		Verdict:        VerdictSpec{From: VerdictFromJTL, ErrorRateMax: "0"},
	}
	e := entryFor(t, req, 0, nil)
	e.WorkingDir = t.TempDir()
	e.JTLProcessor = func(_ string, _ float64) (JTLProcessorResult, error) {
		return JTLProcessorResult{}, errors.New("malformed JTL: header missing success")
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseError, got.Phase)
	assert.Contains(t, got.ErrorMessage, "verdictFrom=jtl")
}

// TestEntry_Verdict_JTLBadThresholdString_IsError: user set errorRateMax
// to garbage (should have been caught by webhook, but wrapper defends
// itself too).
func TestEntry_Verdict_JTLBadThresholdString_IsError(t *testing.T) {
	req := ExecutionRequest{
		Args:           []string{"/bin/true"},
		TimeoutSeconds: 30,
		Verdict:        VerdictSpec{From: VerdictFromJTL, ErrorRateMax: "chicken"},
	}
	e := entryFor(t, req, 0, nil)
	e.WorkingDir = t.TempDir()
	e.JTLProcessor = func(_ string, _ float64) (JTLProcessorResult, error) {
		t.Fatal("processor must not be called when threshold parse fails")
		return JTLProcessorResult{}, nil
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseError, got.Phase)
	assert.Contains(t, got.ErrorMessage, "errorRateMax")
}

// -----------------------------------------------------------------------------
// R3: verdictFrom=junit — JUnit processor overrides base exit-code verdict
// -----------------------------------------------------------------------------

// TestEntry_Verdict_JUnitOverridesNonZeroExit: flaky-runner scenario. Base
// verdict from exit 1 → failed; JUnit says all pass → OVERRIDES to passed.
// Referenced by name from the entry.go docstring — DO NOT RENAME lightly.
//
// Critical assertion: the raw tool exit code is PRESERVED in
// ExecutionResult.ToolExitCode even when the override flips phase to
// passed. exit ≠ 0 + clean JUnit is often a diagnostic signal ("tests
// passed but teardown crashed / flaky reporter / connection drop") —
// hiding it would silently swallow a whole class of near-passing bugs.
func TestEntry_Verdict_JUnitOverridesNonZeroExit(t *testing.T) {
	req := ExecutionRequest{
		Args:           []string{"/bin/false"},
		TimeoutSeconds: 30,
		Verdict:        VerdictSpec{From: VerdictFromJUnit},
	}
	e := entryFor(t, req, 1, nil)
	e.WorkingDir = t.TempDir()
	e.JUnitProcessor = func(_ string) (TestCounts, error) {
		return TestCounts{Total: 10, Passed: 10}, nil
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhasePassed, got.Phase, "JUnit override: all-pass beats non-zero exit")
	assert.Empty(t, got.ErrorMessage)
	require.NotNil(t, got.TestCounts)
	assert.Equal(t, 10, got.TestCounts.Total)

	// Trace preservation: the raw exit code survives the override so
	// the UI / operator can surface "phase=passed BUT tool exited 1"
	// as a warning rather than silently smoothing over the signal.
	require.NotNil(t, got.ToolExitCode, "ToolExitCode must survive JUnit override")
	assert.Equal(t, 1, *got.ToolExitCode,
		"raw tool exit code preserved even when JUnit flips phase to passed")
}

// TestEntry_Verdict_ToolExitCode_PreservedOnJTLOverrideToFailed: symmetric
// trace for the other direction — jmeter exit 0 (tool lied), JTL says
// failed. The 0 is the diagnostic signal ("tool lied") — preserved.
func TestEntry_Verdict_ToolExitCode_PreservedOnJTLOverrideToFailed(t *testing.T) {
	req := ExecutionRequest{
		Args:           []string{"/bin/true"},
		TimeoutSeconds: 30,
		Verdict:        VerdictSpec{From: VerdictFromJTL, ErrorRateMax: "0"},
	}
	e := entryFor(t, req, 0, nil)
	e.WorkingDir = t.TempDir()
	e.JTLProcessor = func(_ string, threshold float64) (JTLProcessorResult, error) {
		return JTLProcessorResult{
			SamplesTotal: 100, SamplesFailed: 100, ErrorRate: 1.0,
			Threshold: threshold, Passed: false,
		}, nil
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseFailed, got.Phase)
	require.NotNil(t, got.ToolExitCode, "ToolExitCode must survive JTL override")
	assert.Equal(t, 0, *got.ToolExitCode, "exit 0 preserved even when JTL flips to failed")
}

// TestEntry_Verdict_ToolExitCode_NilOnExecFailure: no meaningful "tool
// exit code" when the tool never ran. Field stays nil (omitempty in
// JSON) rather than a misleading -1.
func TestEntry_Verdict_ToolExitCode_NilOnExecFailure(t *testing.T) {
	req := ExecutionRequest{Args: []string{"/definitely/does/not/exist"}, TimeoutSeconds: 30}
	e := &Entry{
		Exec: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			// #nosec G204 -- test-only.
			return exec.CommandContext(ctx, name, args...)
		},
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		RequestPath: writeRequest(t, req),
		ResultDir:   t.TempDir(),
		Loader:      &bytes.Buffer{},
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseError, got.Phase)
	assert.Nil(t, got.ToolExitCode, "no exit code when the tool never ran")
}

// TestEntry_Verdict_JUnitFailsOverridesExitZero: k6-with-junit-plugin
// scenario. Base exit 0, JUnit reports 3 failures → failed with counts.
func TestEntry_Verdict_JUnitFailsOverridesExitZero(t *testing.T) {
	req := ExecutionRequest{
		Args:           []string{"/bin/true"},
		TimeoutSeconds: 30,
		Verdict:        VerdictSpec{From: VerdictFromJUnit},
	}
	e := entryFor(t, req, 0, nil)
	e.WorkingDir = t.TempDir()
	e.JUnitProcessor = func(_ string) (TestCounts, error) {
		return TestCounts{Total: 10, Passed: 7, Failed: 3}, nil
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseFailed, got.Phase)
	assert.Contains(t, got.ErrorMessage, "3 of 10")
}

// TestEntry_Verdict_JUnitNoReport_IsError: verdictFrom=junit and no
// parseable report → error (NEVER silently passed).
func TestEntry_Verdict_JUnitNoReport_IsError(t *testing.T) {
	req := ExecutionRequest{
		Args:           []string{"/bin/true"},
		TimeoutSeconds: 30,
		Verdict:        VerdictSpec{From: VerdictFromJUnit},
	}
	e := entryFor(t, req, 0, nil)
	e.WorkingDir = t.TempDir()
	e.JUnitProcessor = func(_ string) (TestCounts, error) {
		return TestCounts{}, errors.New("no parseable JUnit report found")
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseError, got.Phase, "no junit → error, NEVER passed")
	assert.Contains(t, got.ErrorMessage, "verdictFrom=junit")
}

// TestEntry_Verdict_JUnitEmptyReport_IsError: end-to-end regression for
// the session-A closing fix. A JUnit report that PARSES but reports 0
// tests (typo in Cypress --spec pattern, empty suite from an early
// bail-out) MUST surface as phase=error with a message that says "no
// tests found in JUnit report". Before the fix this returned tc.Failed==0
// → phase=passed, letting a typo ship as eternally-green CI.
func TestEntry_Verdict_JUnitEmptyReport_IsError(t *testing.T) {
	req := ExecutionRequest{
		Args:           []string{"/bin/true"},
		TimeoutSeconds: 30,
		Verdict:        VerdictSpec{From: VerdictFromJUnit},
	}
	e := entryFor(t, req, 0, nil)
	e.WorkingDir = t.TempDir()
	// Simulate what junit.Scan returns for a tests==0 report: aggregate
	// counts are zero AND the empty-report sentinel is returned.
	e.JUnitProcessor = func(_ string) (TestCounts, error) {
		return TestCounts{}, errors.New("junit: no tests found in JUnit report")
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseError, got.Phase,
		"empty JUnit report MUST NOT ship as passed (session-A closing fix)")
	assert.Contains(t, got.ErrorMessage, "no tests found in JUnit report",
		"error message must name the failure mode so operators fix the pattern")
}

// TestEntry_Verdict_JUnitMalformed_IsErrorNotPanic: parser error must not
// crash the wrapper.
func TestEntry_Verdict_JUnitMalformed_IsErrorNotPanic(t *testing.T) {
	req := ExecutionRequest{
		Args:           []string{"/bin/true"},
		TimeoutSeconds: 30,
		Verdict:        VerdictSpec{From: VerdictFromJUnit},
	}
	e := entryFor(t, req, 0, nil)
	e.WorkingDir = t.TempDir()
	e.JUnitProcessor = func(_ string) (TestCounts, error) {
		return TestCounts{}, errors.New("XML syntax error")
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseError, got.Phase)
}

// -----------------------------------------------------------------------------
// R4: request-loading + timeout + signal reclassification (from step 05)
// -----------------------------------------------------------------------------

func TestEntry_MissingRequestJSON(t *testing.T) {
	stderr := &bytes.Buffer{}
	e := &Entry{
		Exec:        shExit(0, nil),
		RequestPath: filepath.Join(t.TempDir(), "does-not-exist.json"),
		ResultDir:   t.TempDir(),
		Loader:      stderr,
	}
	require.NoError(t, e.Execute(context.Background()))
	assert.Contains(t, stderr.String(), "load request")
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseError, got.Phase)
}

func TestEntry_RequestWithoutCommandOrArgs_IsError(t *testing.T) {
	req := ExecutionRequest{TimeoutSeconds: 30} // both Command AND Args empty
	e := entryFor(t, req, 0, nil)
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseError, got.Phase)
	assert.Contains(t, got.ErrorMessage, "no command and no args")
}

func TestEntry_TimeoutReclassifiesAsError(t *testing.T) {
	req := ExecutionRequest{
		Args:           []string{"/bin/true"},
		TimeoutSeconds: 1,
	}
	e := &Entry{
		Exec: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", "sleep 5")
		},
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		RequestPath: writeRequest(t, req),
		ResultDir:   t.TempDir(),
		Loader:      &bytes.Buffer{},
	}
	start := time.Now()
	require.NoError(t, e.Execute(context.Background()))
	elapsed := time.Since(start)
	// 8s budget (was 3s) — the assertion's intent is "wrapper exits
	// due to timeout, not stuck 15 min later", so any bound well below
	// the sleep 5s child + wrapper shutdown overhead is enough. Slower
	// shared runners (GH Actions ubuntu-latest under load) can spend
	// the full 5s waiting for the sh child's sleep to finish after
	// TerminateProcess arrives — Go's exec.CommandContext SIGKILLs the
	// direct child (sh) but the orphaned sleep runs to completion
	// unless a process group is used. Bumping the budget avoids
	// intermittent CI flakes without changing behavior.
	assert.Less(t, elapsed, 8*time.Second, "must exit around TimeoutSeconds")
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseError, got.Phase)
	assert.Contains(t, got.ErrorMessage, "timeout")
}

func TestEntry_SignalCancelBecomesAborted(t *testing.T) {
	req := ExecutionRequest{
		Args:           []string{"/bin/true"},
		TimeoutSeconds: 60,
	}
	e := &Entry{
		Exec: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", "sleep 5")
		},
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		RequestPath: writeRequest(t, req),
		ResultDir:   t.TempDir(),
		Loader:      &bytes.Buffer{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	require.NoError(t, e.Execute(ctx))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseAborted, got.Phase)
}

// -----------------------------------------------------------------------------
// R5: splitInvocation — command/args precedence
// -----------------------------------------------------------------------------

func TestSplitInvocation_CommandTakesPrecedence(t *testing.T) {
	binary, args := splitInvocation(ExecutionRequest{
		Command: []string{"/usr/bin/tool", "--flag"},
		Args:    []string{"positional"},
	})
	assert.Equal(t, "/usr/bin/tool", binary)
	assert.Equal(t, []string{"--flag", "positional"}, args)
}

func TestSplitInvocation_ArgsOnlyPromotesFirst(t *testing.T) {
	binary, args := splitInvocation(ExecutionRequest{
		Args: []string{"newman", "run", "coll.json"},
	})
	assert.Equal(t, "newman", binary)
	assert.Equal(t, []string{"run", "coll.json"}, args)
}

func TestParseErrorRateMax_TableDrives(t *testing.T) {
	cases := []struct {
		in      string
		want    float64
		wantErr bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"0.05", 0.05, false},
		{"1", 1, false},
		{"1.5", 0, true},
		{"-0.01", 0, true},
		{"chicken", 0, true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := parseErrorRateMax(c.in)
			if c.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.InDelta(t, c.want, got, 1e-9)
			}
		})
	}
}
