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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRunner is a scriptable Runner for Entry-level tests. It respects ctx
// cancellation the same way exec.CommandContext does: waits until either the
// configured Delay elapses or ctx fires.
type fakeRunner struct {
	Delay       time.Duration
	Result      ExecutionResult
	ReturnErr   error
	ValidateErr error
	WentToEnd   bool // set to true if Delay elapsed without cancellation
}

func (f *fakeRunner) Type() string { return "fake" }
func (f *fakeRunner) Validate(_ context.Context, _ ExecutionRequest) error {
	return f.ValidateErr
}

func (f *fakeRunner) Run(ctx context.Context, _ ExecutionRequest) (ExecutionResult, error) {
	select {
	case <-time.After(f.Delay):
		f.WentToEnd = true
		return f.Result, f.ReturnErr
	case <-ctx.Done():
		// Simulate exec.CommandContext behavior: process killed, return
		// a stub result. Runner MUST NOT return an error for outcomes.
		return ExecutionResult{Phase: PhaseError, ErrorMessage: "killed"}, nil
	}
}

// writeRequest writes an ExecutionRequest to a temp file and returns the path.
func writeRequest(t *testing.T, req ExecutionRequest) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "request.json")
	b, err := json.Marshal(req)
	require.NoError(t, err)
	// #nosec G306 -- test fixture, world-readable is fine.
	require.NoError(t, os.WriteFile(path, b, 0o644))
	return path
}

func readResult(t *testing.T, resultDir string) ExecutionResult {
	t.Helper()
	// #nosec G304 -- test path.
	b, err := os.ReadFile(filepath.Join(resultDir, ResultFileName))
	require.NoError(t, err)
	var got ExecutionResult
	require.NoError(t, json.Unmarshal(b, &got), "result.json must be valid schema")
	return got
}

func TestEntry_HappyPath(t *testing.T) {
	req := ExecutionRequest{RunID: "r1", TestRef: "t1", DataDir: "/data", TimeoutSeconds: 60}
	e := &Entry{
		Runner: &fakeRunner{
			Delay:  0,
			Result: ExecutionResult{Phase: PhasePassed, ErrorMessage: "p95=100"},
		},
		RequestPath: writeRequest(t, req),
		ResultDir:   t.TempDir(),
		Stderr:      &bytes.Buffer{},
	}
	require.NoError(t, e.Execute(context.Background()))

	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhasePassed, got.Phase)
	assert.Equal(t, "p95=100", got.ErrorMessage)
}

// TestEntry_TimeoutViaRequestField: 100ms request timeout with a Runner that
// wants 500ms — deterministic without real sleeps beyond the 100ms budget.
// Asserts: (a) result.json phase=error with "timeout" in message, (b) valid
// JSON schema, (c) wall time < TimeoutSeconds+2s (plan spec).
func TestEntry_TimeoutViaRequestField(t *testing.T) {
	req := ExecutionRequest{RunID: "r1", TimeoutSeconds: 1}
	// Runner would take 5s if allowed — ctx timeout fires at 1s.
	e := &Entry{
		Runner:      &fakeRunner{Delay: 5 * time.Second},
		RequestPath: writeRequest(t, req),
		ResultDir:   t.TempDir(),
		Stderr:      &bytes.Buffer{},
	}
	start := time.Now()
	require.NoError(t, e.Execute(context.Background()))
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 3*time.Second, "must complete well before TimeoutSeconds+2s")
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseError, got.Phase)
	assert.Contains(t, got.ErrorMessage, "timeout")
}

// TestEntry_SignalCancelBecomesAborted: SIGTERM is modeled as ctx cancel from
// the parent (signal.NotifyContext does exactly this). Runner returns quickly,
// Entry reclassifies phase to aborted. result.json must be valid JSON.
func TestEntry_SignalCancelBecomesAborted(t *testing.T) {
	req := ExecutionRequest{RunID: "r1", TimeoutSeconds: 60}
	e := &Entry{
		Runner:      &fakeRunner{Delay: 5 * time.Second},
		RequestPath: writeRequest(t, req),
		ResultDir:   t.TempDir(),
		Stderr:      &bytes.Buffer{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel almost immediately — mimics SIGTERM arriving mid-run.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	require.NoError(t, e.Execute(ctx))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseAborted, got.Phase)
	assert.NotEmpty(t, got.ErrorMessage)
}

// TestEntry_PartialResultIsValidJSONAfterSignal is the plan's key assertion:
// "partial result.json exists and is valid JSON (schema check)". Unmarshal
// into ExecutionResult is the schema check — struct fields survive round-trip.
func TestEntry_PartialResultIsValidJSONAfterSignal(t *testing.T) {
	req := ExecutionRequest{TimeoutSeconds: 60}
	e := &Entry{
		Runner:      &fakeRunner{Delay: time.Second},
		RequestPath: writeRequest(t, req),
		ResultDir:   t.TempDir(),
		Stderr:      &bytes.Buffer{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	require.NoError(t, e.Execute(ctx))

	// Schema check: must decode without error.
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseAborted, got.Phase, "signal → aborted")
	// Round-trip: re-marshal to confirm no info loss.
	b, err := json.Marshal(got)
	require.NoError(t, err)
	var again ExecutionResult
	require.NoError(t, json.Unmarshal(b, &again))
	assert.Equal(t, got, again)
}

func TestEntry_MissingRequestJSON(t *testing.T) {
	stderr := &bytes.Buffer{}
	e := &Entry{
		Runner:      &fakeRunner{},
		RequestPath: filepath.Join(t.TempDir(), "does-not-exist.json"),
		ResultDir:   t.TempDir(),
		Stderr:      stderr,
	}
	require.NoError(t, e.Execute(context.Background()))
	// Stderr surfaced the problem.
	assert.Contains(t, stderr.String(), "load request")
	// result.json still written with phase=error.
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseError, got.Phase)
	assert.Contains(t, got.ErrorMessage, "load request")
}

func TestEntry_MalformedRequestJSON(t *testing.T) {
	dir := t.TempDir()
	reqPath := filepath.Join(dir, "request.json")
	// #nosec G306 -- test fixture.
	require.NoError(t, os.WriteFile(reqPath, []byte("{not json"), 0o644))
	e := &Entry{
		Runner:      &fakeRunner{},
		RequestPath: reqPath,
		ResultDir:   t.TempDir(),
		Stderr:      &bytes.Buffer{},
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseError, got.Phase)
	assert.Contains(t, got.ErrorMessage, "parse json")
}

func TestEntry_ValidateFailurePropagates(t *testing.T) {
	e := &Entry{
		Runner:      &fakeRunner{ValidateErr: assertError("bad request")},
		RequestPath: writeRequest(t, ExecutionRequest{TimeoutSeconds: 60}),
		ResultDir:   t.TempDir(),
		Stderr:      &bytes.Buffer{},
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseError, got.Phase)
	assert.Contains(t, got.ErrorMessage, "bad request")
}

func TestEntry_NilRunner(t *testing.T) {
	e := &Entry{
		RequestPath: writeRequest(t, ExecutionRequest{}),
		ResultDir:   t.TempDir(),
		Stderr:      &bytes.Buffer{},
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseError, got.Phase)
	assert.Contains(t, got.ErrorMessage, "no runner")
}

// TestEntry_RunnerReturnsErrorBecomesErrorPhase covers the contract corner:
// Runner returned a non-nil error (infra-level failure). Entry classifies as
// error with the runner's error message.
func TestEntry_RunnerReturnsErrorBecomesErrorPhase(t *testing.T) {
	e := &Entry{
		Runner: &fakeRunner{
			ReturnErr: assertError("k6 binary not found"),
			Result:    ExecutionResult{},
		},
		RequestPath: writeRequest(t, ExecutionRequest{TimeoutSeconds: 60}),
		ResultDir:   t.TempDir(),
		Stderr:      &bytes.Buffer{},
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseError, got.Phase)
	assert.Contains(t, got.ErrorMessage, "k6 binary not found")
}

// TestEntry_NoTimeoutInRequestUsesParentCtx: TimeoutSeconds=0 means "no
// wrapper timeout" — rely on parent ctx (which in production is signal-only).
// Runner completes normally.
func TestEntry_NoTimeoutInRequestUsesParentCtx(t *testing.T) {
	e := &Entry{
		Runner:      &fakeRunner{Delay: 10 * time.Millisecond, Result: ExecutionResult{Phase: PhasePassed}},
		RequestPath: writeRequest(t, ExecutionRequest{TimeoutSeconds: 0}),
		ResultDir:   t.TempDir(),
		Stderr:      &bytes.Buffer{},
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhasePassed, got.Phase)
}

// assertError is a tiny helper that returns a real error implementing error
// so we don't repeat errors.New everywhere.
func assertError(msg string) error {
	return &basicError{msg: msg}
}

type basicError struct{ msg string }

func (e *basicError) Error() string { return e.msg }
