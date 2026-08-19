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
	"os/exec"
	"time"
)

// Entry is the wrapper's main-loop logic, extracted from cmd/entry so it can
// be unit-tested without spawning a process. Workflows model (step 11):
//
//  1. Load request.json (fail early with error result if unreadable).
//  2. Self-enforce TimeoutSeconds via ctx.
//  3. Exec req.Command/Args verbatim — the wrapper does NOT know or care
//     what tool this is.
//  4. Base verdict from exit code (0 → passed, non-zero → failed).
//  5. If req.Verdict.From ∈ {junit,jtl}, run the corresponding processor
//     against the working directory. Processor OVERRIDES the base verdict
//     in BOTH directions:
//     - jmeter exit 0 + failing JTL → failed (JTL wins)
//     - flaky non-zero + clean JUnit → passed (JUnit wins)
//     Rationale: many tools (JMeter, some CI runners) lie in one direction;
//     others lie in the other (retry-on-flaky runners exit 1 while all
//     tests actually pass). One rule handles both without a per-tool
//     dispatch.
//  6. Reclassify Phase if ctx died (timeout → error, cancel → aborted).
//  7. Post-tool scrape (§15.3) → merge counts + artifacts.
//  8. Write result.json + upload to object storage.
type Entry struct {
	// Exec is the process factory. exec.CommandContext in production;
	// test-hook so unit tests don't spawn real subprocesses.
	Exec func(ctx context.Context, name string, args ...string) *exec.Cmd

	// Stdout / Stderr are the tool's stdio. Production wires os.Stdout /
	// os.Stderr so the operator's log tail sees output live.
	Stdout io.Writer
	Stderr io.Writer

	// JUnitProcessor is called when req.Verdict.From == "junit". Signature:
	// (workingDir) → (aggregated counts, error). Injected so tests can
	// swap a synthetic processor without seeding real XML files.
	JUnitProcessor func(workingDir string) (TestCounts, error)

	// JTLProcessor is called when req.Verdict.From == "jtl". Signature:
	// (workingDir, errorRateMax) → (result-ready message, error). Injected
	// for the same reason as JUnitProcessor.
	JTLProcessor func(workingDir string, errorRateMax float64) (JTLProcessorResult, error)

	// Scraper is the optional post-tool artifact scraper (step 07). nil
	// disables scrape; result.json still lands on the local emptyDir.
	Scraper Scraper

	// RequestPath is the path to request.json. Production sets it to the
	// package constant RequestPath; tests inject a temp file.
	RequestPath string

	// ResultDir is where result.json is written. Production reads it from
	// $KUBETEST_RESULTDIR; tests use t.TempDir().
	ResultDir string

	// WorkingDir is where the scraper + verdict processors look for
	// artifacts.paths matches and reports. Defaults to the request's
	// WorkingDir when empty. Tests can override.
	WorkingDir string

	// Stderr is where load/setup errors are logged. Production is os.Stderr;
	// tests inject a bytes.Buffer to assert on messages.
	Loader io.Writer
}

// JTLProcessorResult is the return type from the JTL processor. Distinct
// from TestCounts because JTL's "verdict" is a threshold comparison, not
// a pass/fail count.
type JTLProcessorResult struct {
	// SamplesTotal / SamplesFailed feed Metrics.
	SamplesTotal  int64
	SamplesFailed int64
	// ErrorRate is Failed/Total, precomputed for the error message.
	ErrorRate float64
	// Threshold is the caller-supplied errorRateMax the processor
	// compared against.
	Threshold float64
	// Passed is true when ErrorRate <= Threshold.
	Passed bool
}

// Execute runs the wrapper's one-shot lifecycle. Returns nil on any
// completed run (including phase=failed) — non-nil only for setup errors
// that prevent even writing a result.json.
func (e *Entry) Execute(ctx context.Context) error {
	if e.Loader == nil {
		e.Loader = io.Discard
	}
	req, err := loadRequest(e.RequestPath)
	if err != nil {
		return e.writeErrorResult(fmt.Sprintf("load request: %v", err))
	}

	// Self-enforced timeout. Guaranteed < Job ADS by compiler.
	if req.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	if len(req.Command) == 0 && len(req.Args) == 0 {
		return e.writeErrorResult("request has no command and no args — nothing to run")
	}

	// Exec the tool verbatim.
	result := e.runTool(ctx, req)

	// Reclassify by ctx state — timeout/signal win over any verdict.
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

	// Scrape (§15.3) — runs even on failure/abort paths so partial output
	// survives. Never changes Phase; puts errors in ScrapeError.
	e.runScrape(ctx, req, &result)

	if err := WriteResultAtomic(e.ResultDir, result); err != nil {
		_, _ = fmt.Fprintf(e.Loader, "write result: %v\n", err)
		return err
	}

	// Upload final result.json so the controller's ResultReader can fetch it.
	if e.Scraper != nil && req.RunID != "" {
		payload, merr := marshalResult(result)
		if merr == nil {
			if uerr := e.Scraper.UploadResult(ctx, req.RunID, payload); uerr != nil {
				_, _ = fmt.Fprintf(e.Loader, "upload result.json: %v\n", uerr)
			}
		}
	}
	return nil
}

// runTool execs the request's Command/Args and returns a preliminary
// ExecutionResult driven by exit code + optional verdict processor.
// Extracted so table tests can substitute Exec + processors without
// touching the whole Execute lifecycle.
func (e *Entry) runTool(ctx context.Context, req ExecutionRequest) ExecutionResult {
	binary, argv := splitInvocation(req)
	execFn := e.Exec
	if execFn == nil {
		execFn = exec.CommandContext
	}
	// #nosec G204 -- binary + argv come from operator-controlled request.json.
	cmd := execFn(ctx, binary, argv...)
	cmd.Dir = workingDir(req)
	cmd.Env = envList(req.Env)
	cmd.Stdout = e.Stdout
	cmd.Stderr = e.Stderr

	runErr := cmd.Run()
	exitCode := extractExitCode(runErr)

	// Base verdict from exit code.
	base := baseVerdict(exitCode, runErr)

	// Apply verdictFrom processor when requested. Processor's verdict
	// OVERRIDES the exit-code verdict in BOTH directions.
	if from := req.Verdict.From; from == VerdictFromJUnit || from == VerdictFromJTL {
		wd := e.WorkingDir
		if wd == "" {
			wd = workingDir(req)
		}
		if from == VerdictFromJUnit {
			return e.applyJUnitVerdict(wd, base)
		}
		return e.applyJTLVerdict(wd, req.Verdict.ErrorRateMax, base)
	}
	return base
}

// baseVerdict classifies exit code + exec error into an ExecutionResult.
// Rules:
//   - exit 0 → passed, no message
//   - non-zero exit → failed with "exit code N"
//   - non-exit error (binary missing, permission denied) → error with the
//     raw error text — the wrapper couldn't even run the tool.
//
// Always populates ToolExitCode when the process actually ran (i.e. the
// exit code is well-defined: 0 on success, N on ExitError). Leaves it nil
// on exec-failure — there's no meaningful "tool exit code" when the tool
// never started.
func baseVerdict(exitCode int, runErr error) ExecutionResult {
	if runErr == nil {
		code := 0
		return ExecutionResult{Phase: PhasePassed, ToolExitCode: &code}
	}
	if !isExitError(runErr) {
		return ExecutionResult{
			Phase:        PhaseError,
			ErrorMessage: fmt.Sprintf("exec failed: %v", runErr),
		}
	}
	code := exitCode
	return ExecutionResult{
		Phase:        PhaseFailed,
		ErrorMessage: fmt.Sprintf("exit code %d", exitCode),
		ToolExitCode: &code,
	}
}

// applyJUnitVerdict runs the JUnit processor and OVERRIDES the base
// exit-code verdict. Rules:
//   - No processor configured → operator bug, return error phase.
//   - Processor error (ErrNoJUnitFound / malformed XML) → error phase
//     (never silently passed per plan).
//   - counts.Failed > 0 → failed with counts in ErrorMessage.
//   - counts.Failed == 0 → PASSED, regardless of base exit code (this is
//     the flaky-non-zero-but-tests-actually-passed override case).
//
// The base run's ToolExitCode is PRESERVED on every path — even when the
// verdict flips passed. Losing it would hide the "tests passed but
// teardown crashed" class of near-passing failures.
func (e *Entry) applyJUnitVerdict(workingDir string, base ExecutionResult) ExecutionResult {
	if e.JUnitProcessor == nil {
		return ExecutionResult{
			Phase:        PhaseError,
			ErrorMessage: "verdictFrom=junit but no JUnit processor wired (wrapper misconfig)",
			ToolExitCode: base.ToolExitCode,
		}
	}
	counts, err := e.JUnitProcessor(workingDir)
	if err != nil {
		return ExecutionResult{
			Phase:        PhaseError,
			ErrorMessage: fmt.Sprintf("verdictFrom=junit: %v", err),
			ToolExitCode: base.ToolExitCode,
		}
	}
	tc := counts
	metrics := map[string]float64{
		"tests_total":   float64(tc.Total),
		"tests_passed":  float64(tc.Total - tc.Failed - tc.Skipped),
		"tests_failed":  float64(tc.Failed),
		"tests_skipped": float64(tc.Skipped),
	}
	if tc.Failed > 0 {
		return ExecutionResult{
			Phase:        PhaseFailed,
			TestCounts:   &tc,
			Metrics:      metrics,
			ErrorMessage: fmt.Sprintf("verdictFrom=junit: %d of %d test(s) failed", tc.Failed, tc.Total),
			ToolExitCode: base.ToolExitCode,
		}
	}
	// JUnit says all pass — OVERRIDES base exit-code verdict.
	// Test name pinned: TestEntry_Verdict_JUnitOverridesNonZeroExit.
	return ExecutionResult{
		Phase:        PhasePassed,
		TestCounts:   &tc,
		Metrics:      metrics,
		ToolExitCode: base.ToolExitCode,
	}
}

// applyJTLVerdict runs the JTL processor and OVERRIDES the base verdict.
// Rules:
//   - No processor configured → operator bug, error phase.
//   - Processor error (missing/malformed) → error phase.
//   - ErrorRate > threshold → failed with error-rate in message.
//   - ErrorRate ≤ threshold → passed (this OVERRIDES the base verdict —
//     jmeter exit 0 stays passed, jmeter exit N with clean JTL becomes
//     passed too).
func (e *Entry) applyJTLVerdict(workingDir, errorRateMaxStr string, base ExecutionResult) ExecutionResult {
	if e.JTLProcessor == nil {
		return ExecutionResult{
			Phase:        PhaseError,
			ErrorMessage: "verdictFrom=jtl but no JTL processor wired (wrapper misconfig)",
			ToolExitCode: base.ToolExitCode,
		}
	}
	max, perr := parseErrorRateMax(errorRateMaxStr)
	if perr != nil {
		return ExecutionResult{
			Phase:        PhaseError,
			ErrorMessage: fmt.Sprintf("verdictFrom=jtl: %v", perr),
			ToolExitCode: base.ToolExitCode,
		}
	}
	res, err := e.JTLProcessor(workingDir, max)
	if err != nil {
		return ExecutionResult{
			Phase:        PhaseError,
			ErrorMessage: fmt.Sprintf("verdictFrom=jtl: %v", err),
			ToolExitCode: base.ToolExitCode,
		}
	}
	metrics := map[string]float64{
		"samples_total":  float64(res.SamplesTotal),
		"samples_failed": float64(res.SamplesFailed),
		"error_rate":     res.ErrorRate,
	}
	if !res.Passed {
		return ExecutionResult{
			Phase:   PhaseFailed,
			Metrics: metrics,
			ErrorMessage: fmt.Sprintf(
				"verdictFrom=jtl: error rate %.4f exceeds threshold %.4f (%d/%d samples failed)",
				res.ErrorRate, res.Threshold, res.SamplesFailed, res.SamplesTotal),
			ToolExitCode: base.ToolExitCode,
		}
	}
	// JTL override to passed — preserve the raw exit code as a trace.
	return ExecutionResult{
		Phase:        PhasePassed,
		Metrics:      metrics,
		ToolExitCode: base.ToolExitCode,
	}
}

// splitInvocation returns (binary, args) for exec. Precedence:
//   - Command non-empty → command[0] is the binary, command[1:]+args are args.
//   - Command empty, Args non-empty → args[0] is the binary, args[1:] are args.
//   - Neither → caller already refused at the guard above.
//
// This matches the Kubernetes convention where container.command overrides
// the image ENTRYPOINT and container.args goes AFTER whatever is used as
// the entrypoint.
func splitInvocation(req ExecutionRequest) (string, []string) {
	switch {
	case len(req.Command) > 0:
		binary := req.Command[0]
		argv := append([]string{}, req.Command[1:]...)
		argv = append(argv, req.Args...)
		return binary, argv
	default:
		return req.Args[0], append([]string{}, req.Args[1:]...)
	}
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
	scrapeCtx := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		scrapeCtx, cancel = context.WithTimeout(context.Background(),
			time.Duration(ScrapeGracePeriodSeconds)*time.Second)
		defer cancel()
	}
	sr, err := e.Scraper.Scrape(scrapeCtx, workingDir, ScrapeSpec{
		RunID: req.RunID,
		Paths: req.Artifacts.Paths,
	})
	if err != nil {
		if result.ScrapeError == "" {
			result.ScrapeError = err.Error()
		}
		return
	}
	result.Artifacts = sr.Artifacts
	if sr.TestCounts != nil && result.TestCounts == nil {
		// Verdict processor may have already set TestCounts (junit path).
		// Don't clobber those with scraper counts.
		result.TestCounts = sr.TestCounts
	}
	if sr.ScrapeError != "" {
		result.ScrapeError = sr.ScrapeError
	}
}

// writeErrorResult writes an error-phase result.json and logs to stderr.
func (e *Entry) writeErrorResult(msg string) error {
	_, _ = fmt.Fprintln(e.Loader, msg)
	result := ExecutionResult{Phase: PhaseError, ErrorMessage: msg}
	if err := WriteResultAtomic(e.ResultDir, result); err != nil {
		_, _ = fmt.Fprintf(e.Loader, "write error result: %v\n", err)
		return err
	}
	return nil
}

func loadRequest(path string) (ExecutionRequest, error) {
	// #nosec G304 -- path is operator-controlled in prod; tests inject a temp path.
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

func marshalResult(r ExecutionResult) ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

func extractExitCode(runErr error) int {
	if runErr == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func isExitError(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

func workingDir(req ExecutionRequest) string {
	if req.WorkingDir != "" {
		return req.WorkingDir
	}
	if req.DataDir != "" {
		return req.DataDir
	}
	return "."
}

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

// parseErrorRateMax parses the request's threshold string. Empty → 0
// (strictest possible: any failure fails the run).
func parseErrorRateMax(s string) (float64, error) {
	if s == "" {
		return 0, nil
	}
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	if err != nil {
		return 0, fmt.Errorf("errorRateMax %q not a valid float", s)
	}
	if f < 0 || f > 1 {
		return 0, fmt.Errorf("errorRateMax %f out of range [0,1]", f)
	}
	return f, nil
}
