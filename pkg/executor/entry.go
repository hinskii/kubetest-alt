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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// Entry is the wrapper's main-loop logic, extracted from cmd/entry so it can
// be unit-tested without spawning a process.
//
// Lifecycle:
//  1. Load request.json (fail early with error result if unreadable/malformed).
//  2. Self-enforce TimeoutSeconds via ctx (must be shorter than the Job's ADS
//     — the operator's compiler guarantees this by construction).
//  3. Validate + Run the Runner. Runner NEVER returns a non-nil error for
//     test outcomes (see runner.go contract) — verdict is in result.Phase.
//  4. Reclassify Phase if ctx died (deadline → error, cancel → aborted).
//  5. Write result.json atomically.
//
// Zero of these steps is concurrent — SIGTERM handling is via the ctx passed
// in by cmd/entry (signal.NotifyContext).
type Entry struct {
	// Runner is the single tool wrapper for this build of /entry. Step 05
	// ships only k6; dispatch (map[type]Runner) lands with step 11.
	Runner Runner

	// RequestPath is the path to request.json. Production sets it to the
	// package constant RequestPath; tests inject a temp file.
	RequestPath string

	// ResultDir is where result.json is written. Production reads it from
	// $KUBETEST_RESULTDIR; tests use t.TempDir().
	ResultDir string

	// Stderr is where load/setup errors are logged. Production is os.Stderr;
	// tests inject a bytes.Buffer to assert on messages.
	Stderr io.Writer
}

// Execute runs the wrapper's one-shot lifecycle. Returns nil on any completed
// run (including phase=failed) — non-nil only for setup errors that prevent
// even writing a result.json (e.g. ResultDir doesn't exist and can't be
// created). Callers should exit non-zero only on those.
func (e *Entry) Execute(ctx context.Context) error {
	req, err := loadRequest(e.RequestPath)
	if err != nil {
		return e.writeErrorResult(fmt.Sprintf("load request: %v", err))
	}

	// Self-enforced timeout. Guaranteed < Job ADS by compiler
	// (ADS = TimeoutSeconds + ADSBufferSeconds).
	if req.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	if e.Runner == nil {
		return e.writeErrorResult("no runner configured")
	}
	if err := e.Runner.Validate(ctx, req); err != nil {
		return e.writeErrorResult(fmt.Sprintf("validate: %v", err))
	}

	result, runErr := e.Runner.Run(ctx, req)
	if runErr != nil {
		// Runner contract: only INFRA errors (tool binary missing etc.).
		result.Phase = PhaseError
		if result.ErrorMessage == "" {
			result.ErrorMessage = runErr.Error()
		}
	}

	// Reclassify Phase based on ctx state — this is what turns a timeout into
	// an error-phase-with-timeout-message, and a SIGTERM into aborted.
	// Runner may have already set Phase; ctx state takes precedence.
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.Phase = PhaseError
		if result.ErrorMessage == "" {
			result.ErrorMessage = fmt.Sprintf("timeout: wrapper exceeded TimeoutSeconds=%d", req.TimeoutSeconds)
		} else {
			result.ErrorMessage = fmt.Sprintf("timeout: %s", result.ErrorMessage)
		}
	case errors.Is(ctx.Err(), context.Canceled):
		result.Phase = PhaseAborted
		if result.ErrorMessage == "" {
			result.ErrorMessage = "aborted by signal"
		}
	}

	if err := WriteResultAtomic(e.ResultDir, result); err != nil {
		_, _ = fmt.Fprintf(e.Stderr, "write result: %v\n", err)
		return err
	}
	return nil
}

// writeErrorResult writes an error-phase result.json and logs to stderr.
// Called on setup failures where no Runner ran. Returns nil so the caller's
// exit code stays zero — the operator reads phase from result.json, not from
// the wrapper's exit code (§15.2 says exit code alone is not the verdict).
// The stderr line is what shows up in kubelet logs for humans.
func (e *Entry) writeErrorResult(msg string) error {
	_, _ = fmt.Fprintln(e.Stderr, msg)
	result := ExecutionResult{
		Phase:        PhaseError,
		ErrorMessage: msg,
	}
	if err := WriteResultAtomic(e.ResultDir, result); err != nil {
		// If we can't even write the error result, propagate — the operator
		// will fall back to the pod-terminated-state path (§15.2).
		_, _ = fmt.Fprintf(e.Stderr, "write error result: %v\n", err)
		return err
	}
	return nil
}

// loadRequest reads and parses ExecutionRequest JSON.
func loadRequest(path string) (ExecutionRequest, error) {
	// #nosec G304 -- path is the operator-controlled constant RequestPath in
	// production; tests inject a temp path they themselves created.
	b, err := os.ReadFile(path)
	if err != nil {
		return ExecutionRequest{}, err
	}
	var req ExecutionRequest
	if err := json.Unmarshal(b, &req); err != nil {
		return ExecutionRequest{}, fmt.Errorf("parse json: %w", err)
	}
	return req, nil
}
