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

	// Scraper is the optional post-tool artifact scraper (step 07).
	// nil means "no MinIO configured, skip upload" — the wrapper still writes
	// result.json locally, controller falls back to NoResultReader.
	//
	// cmd/entry composes internal/scraper.Scraper + pkg/storage.MinIO when
	// $MINIO_ENDPOINT is set; otherwise leaves this nil.
	Scraper Scraper

	// RequestPath is the path to request.json. Production sets it to the
	// package constant RequestPath; tests inject a temp file.
	RequestPath string

	// ResultDir is where result.json is written. Production reads it from
	// $KUBETEST_RESULTDIR; tests use t.TempDir().
	ResultDir string

	// WorkingDir is where the scraper looks for artifacts.paths matches. Defaults
	// to the request's WorkingDir when empty. Tests can override.
	WorkingDir string

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

	// Scrape happens post-tool AND on the SIGTERM/timeout paths — the plan
	// §15.3 spec says "flush partial result + trigger artifact scrape hook
	// before exit". Even a failed / aborted run may have produced diagnostic
	// files worth uploading. Scrape errors NEVER change Phase; they're
	// recorded in ScrapeError so the UI can surface the partial state.
	//
	// Scraping uses a background ctx so a fired parent (SIGTERM/timeout) doesn't
	// immediately kill the scrape too. The scraper enforces its own internal
	// budget via file counts and per-upload timeouts inside pkg/storage.MinIO.
	e.runScrape(ctx, req, &result)

	if err := WriteResultAtomic(e.ResultDir, result); err != nil {
		_, _ = fmt.Fprintf(e.Stderr, "write result: %v\n", err)
		return err
	}

	// Upload the finalized result.json so the controller's ResultReader can
	// fetch it (step 04 currently uses NoResultReader; step 07 wires the
	// real MinIO reader). Non-fatal — a wrapper without a Scraper still
	// leaves result.json on disk for post-mortem.
	if e.Scraper != nil && req.RunID != "" {
		payload, err := marshalResult(result)
		if err == nil {
			if uerr := e.Scraper.UploadResult(ctx, req.RunID, payload); uerr != nil {
				_, _ = fmt.Fprintf(e.Stderr, "upload result.json: %v\n", uerr)
			}
		}
	}
	return nil
}

// runScrape triggers the scraper if one is configured, and merges its output
// into result. Extracted so tests can exercise the scrape hook independently.
func (e *Entry) runScrape(ctx context.Context, req ExecutionRequest, result *ExecutionResult) {
	if e.Scraper == nil {
		return
	}
	workingDir := e.WorkingDir
	if workingDir == "" {
		workingDir = req.WorkingDir
	}
	if workingDir == "" {
		workingDir = req.DataDir
	}
	if workingDir == "" {
		return
	}

	// If the parent ctx already fired (SIGTERM/timeout mid-run), scrape gets
	// a fresh short-lived ctx so it can flush a partial upload before the
	// pod's second signal / SIGKILL. Bounded to 30s — anything longer risks
	// overrunning the Job's ADS grace.
	scrapeCtx := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		scrapeCtx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
	}

	sr, err := e.Scraper.Scrape(scrapeCtx, workingDir, ScrapeSpec{
		RunID: req.RunID,
		Paths: req.Artifacts.Paths,
	})
	if err != nil {
		// Programmer-error from scraper (nil uploader, empty bucket) — treat
		// as scrape failure, don't propagate to Phase.
		if result.ScrapeError == "" {
			result.ScrapeError = err.Error()
		}
		return
	}
	result.Artifacts = sr.Artifacts
	if sr.TestCounts != nil {
		result.TestCounts = sr.TestCounts
	}
	if sr.ScrapeError != "" {
		result.ScrapeError = sr.ScrapeError
	}
}

// marshalResult serializes ExecutionResult the same way WriteResultAtomic
// does — kept in one place so the on-disk file and the uploaded object have
// identical bytes.
func marshalResult(r ExecutionResult) ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
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
