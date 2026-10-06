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
	"fmt"
	"os/exec"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shExits returns exit codes from codes, one per run (the last repeats),
// and counts the runs.
func shExits(codes ...int) (func(ctx context.Context, name string, args ...string) *exec.Cmd, func() int) {
	var mu sync.Mutex
	n := 0
	return func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			mu.Lock()
			code := codes[min(n, len(codes)-1)]
			n++
			mu.Unlock()
			// #nosec G204 -- test-only, code is a literal int.
			return exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("exit %d", code))
		}, func() int {
			mu.Lock()
			defer mu.Unlock()
			return n
		}
}

func retryEntry(t *testing.T, retries int, codes ...int) (*Entry, *bytes.Buffer, func() int) {
	t.Helper()
	execFn, runs := shExits(codes...)
	out := &bytes.Buffer{}
	req := ExecutionRequest{Args: []string{"tool"}, TimeoutSeconds: 30, Retry: RetrySpec{Count: retries}}
	e := entryFor(t, req, 0, nil)
	e.Exec, e.Stdout = execFn, out
	return e, out, runs
}

// fixes.md #16: spec.retry was validated and never executed.
func TestRetry_PassesOnALaterTry(t *testing.T) {
	e, out, runs := retryEntry(t, 2, 1, 0)
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)

	assert.Equal(t, PhasePassed, got.Phase)
	assert.Equal(t, 2, runs())
	require.Len(t, got.Attempts, 2)
	assert.Equal(t, PhaseFailed, got.Attempts[0].Phase)
	assert.Equal(t, "exit code 1", got.Attempts[0].ErrorMessage)
	assert.Equal(t, PhasePassed, got.Attempts[1].Phase)
	assert.Contains(t, out.String(), "kubetest: try 1/3 failed (exit code 1), retrying")
}

func TestRetry_GivesUpAfterCount(t *testing.T) {
	e, _, runs := retryEntry(t, 2, 3)
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseFailed, got.Phase)
	assert.Equal(t, 3, runs(), "1 try + 2 retries")
	assert.Len(t, got.Attempts, 3)
}

func TestRetry_NotWhenPassedOrNotConfigured(t *testing.T) {
	e, _, runs := retryEntry(t, 3, 0)
	require.NoError(t, e.Execute(context.Background()))
	assert.Equal(t, 1, runs())
	assert.Empty(t, readResult(t, e.ResultDir).Attempts, "a single try lists no attempts")

	e, _, runs = retryEntry(t, 0, 1)
	require.NoError(t, e.Execute(context.Background()))
	assert.Equal(t, 1, runs())
}

// A tool that can't start fails identically every time: no retry.
func TestRetry_NotWhenTheToolCannotStart(t *testing.T) {
	e := entryFor(t, ExecutionRequest{Args: []string{"/definitely/missing"}, TimeoutSeconds: 30,
		Retry: RetrySpec{Count: 3}}, 0, nil)
	e.Exec = exec.CommandContext
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhaseError, got.Phase)
	assert.Empty(t, got.Attempts)
}

// A verdict processor error (no report) is worth retrying: the tool ran.
func TestRetry_ProcessorErrorIsRetried(t *testing.T) {
	e, _, runs := retryEntry(t, 1, 0)
	calls := 0
	e.JUnitProcessor = func(string, []string) (TestCounts, error) {
		calls++
		if calls == 1 {
			return TestCounts{}, fmt.Errorf("no JUnit report found")
		}
		return TestCounts{Total: 2, Passed: 2}, nil
	}
	writeReq := ExecutionRequest{Args: []string{"tool"}, TimeoutSeconds: 30, Retry: RetrySpec{Count: 1},
		Verdict: VerdictSpec{From: VerdictFromJUnit}}
	e.RequestPath = writeRequest(t, writeReq)
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhasePassed, got.Phase)
	assert.Equal(t, 2, runs())
	assert.Equal(t, PhaseError, got.Attempts[0].Phase)
}
