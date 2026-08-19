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

// Package executor defines the wire contract shared by the operator's
// compiler (which writes request.json) and the in-container /entry wrapper
// (which reads it). Any change to ExecutionRequest or ExecutionResult is a
// breaking change for older wrapper images running in the cluster — coordinate
// deploys accordingly.
package executor

// ExecutionRequest is the JSON payload the compiler serializes into
// $KUBETEST_REQUEST_DIR/request.json (via ConfigMap projection) and the
// wrapper reads back inside the pod. See CLAUDE.md §11.
//
// Workflows model (step 11): no `type` field — the wrapper is generic and
// runs Command/Args verbatim. Verdict semantics are declared per-Test via
// the Verdict field.
type ExecutionRequest struct {
	RunID          string            `json:"runId"`
	TestRef        string            `json:"testRef"`
	DataDir        string            `json:"dataDir"`
	WorkingDir     string            `json:"workingDir,omitempty"`
	Command        []string          `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Config         map[string]string `json:"config,omitempty"`
	Artifacts      ArtifactSpec      `json:"artifacts,omitzero"`
	TimeoutSeconds int64             `json:"timeoutSeconds,omitempty"`

	// Verdict, if set, overrides the default exit-code verdict rule. Empty
	// From (or omitted Verdict) means "verdict from process exit code".
	// See VerdictSpec + entry.go's post-tool processor pipeline.
	Verdict VerdictSpec `json:"verdict,omitzero"`
}

// VerdictSpec mirrors testsv1alpha1.VerdictSpec on the wire. The wrapper
// doesn't import the CRD types package (keeps the image small and free of
// k8s API dependencies).
type VerdictSpec struct {
	// From is one of "exitCode" (default), "junit", "jtl".
	From string `json:"from,omitempty"`
	// ErrorRateMax is the JTL error-rate threshold (float in [0,1] as
	// a decimal string) — only meaningful when From=jtl.
	ErrorRateMax string `json:"errorRateMax,omitempty"`
}

// VerdictFrom* string values recognized in VerdictSpec.From.
const (
	VerdictFromExitCode = "exitCode"
	VerdictFromJUnit    = "junit"
	VerdictFromJTL      = "jtl"
)

// ArtifactSpec mirrors testsv1alpha1.ArtifactSpec on the wire. Kept here so
// the wire format is self-contained (no cross-package dep from the wrapper).
type ArtifactSpec struct {
	Paths    []string `json:"paths,omitempty"`
	Compress string   `json:"compress,omitempty"`
}

// ExecutionResult is what the wrapper writes to $KUBETEST_RESULTDIR/result.json.
// The operator's controller reads it (step 07 via the ResultReader interface).
type ExecutionResult struct {
	// Phase is one of Phase* constants. The operator's TestRun phase mirrors
	// this value 1:1 unless infra state (OOMKill etc.) overrides.
	Phase string `json:"phase"`

	// ErrorMessage carries human-readable detail on failed/error/aborted runs.
	// MUST be empty when Phase == passed — downstream consumers (scraper, GUI)
	// treat non-empty ErrorMessage as a failure signal. Metrics for passing
	// runs go in Metrics, not here.
	ErrorMessage string `json:"errorMessage,omitempty"`

	// Metrics is a flat name→numeric map of tool-emitted metrics. Common
	// keys across tools (see plan §11):
	//   - p95_ms         request duration 95th percentile
	//   - rps            requests per second
	//   - checks_passed  number of passing assertions
	//   - checks_total   total assertions run
	// Populated on both passed AND failed runs so the operator UI can chart
	// trends over time. Nil on error/aborted (nothing meaningful to report).
	Metrics map[string]float64 `json:"metrics,omitempty"`

	// TestCounts is filled by the scraper (step 07) when JUnit XML files
	// are found and parsed. Nil when the tool doesn't emit JUnit output.
	TestCounts *TestCounts `json:"testCounts,omitempty"`

	// Steps is per-step results for tools with an internal step concept
	// (Cypress specs, Newman requests). k6 leaves it empty.
	Steps []StepResult `json:"steps,omitempty"`

	// Artifacts is the list of artifact references produced by the scraper
	// (step 07). Each entry has the original relative path + the object-store
	// key the operator UI uses to fetch the file.
	//
	// Populated even on failed runs — the scraper runs post-tool regardless
	// of exit code so partial diagnostic output survives.
	Artifacts []ArtifactRef `json:"artifacts,omitempty"`

	// ScrapeError carries a human-readable message if the artifact scraper
	// hit a permanent failure (network down, bucket missing). The run's
	// verdict (Phase) still stands — a scrape failure never turns a passing
	// tool run into a failing status. Callers can surface this separately
	// in the UI ("run passed, artifacts not saved: <reason>").
	ScrapeError string `json:"scrapeError,omitempty"`
}

// TestCounts aggregates JUnit-derived counts across all uploaded XML files.
type TestCounts struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// ArtifactRef is one uploaded artifact — original path + object-store key.
type ArtifactRef struct {
	// Path is the file path relative to the wrapper's working directory
	// (e.g. "results/summary.json") — how the user's spec.artifacts.paths
	// glob matched it.
	Path string `json:"path"`

	// Key is the object-store key the operator/UI uses to fetch the file.
	// Layout: "<runID>/<Path>". Bucket is implicit (operator config).
	Key string `json:"key"`

	// SizeBytes is the uploaded size. Handy for UI listings; not enforced.
	SizeBytes int64 `json:"sizeBytes,omitempty"`

	// ContentType is what the scraper detected/set on upload.
	ContentType string `json:"contentType,omitempty"`
}

// StepResult is the wire shape for one sub-execution. Timestamps are ISO 8601
// strings so tools without a fine-grained clock can leave them empty.
type StepResult struct {
	Name       string `json:"name,omitempty"`
	Phase      string `json:"phase"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

// Phase values written to result.json. Must match testsv1alpha1.Phase strings
// (the operator maps these into TestRun status directly).
const (
	PhasePassed  = "passed"
	PhaseFailed  = "failed"
	PhaseError   = "error"
	PhaseAborted = "aborted"
)
