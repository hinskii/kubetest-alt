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

import "context"

// Scraper is the wrapper's post-tool step: glob artifacts.paths from the
// working directory, upload matches to object storage, aggregate JUnit
// counts. cmd/entry composes a concrete implementation (internal/scraper)
// with an Uploader (pkg/storage.MinIO) — the wrapper package (pkg/executor)
// stays free of both dependencies.
//
// Contract:
//   - Scrape MUST NOT return a non-nil error for scrape failures. Failure
//     goes in ScrapeResult.ScrapeError. The run's verdict is not affected —
//     a passing tool run whose artifacts failed to upload is still
//     Phase=passed, with ScrapeError set so the UI can surface the
//     partial-artifact state.
//   - Return non-nil error ONLY for programmer errors (nil uploader, empty
//     bucket) that the caller should treat as a bug, not a runtime hiccup.
//   - Honors ctx cancellation for signal-time scrape (§15.3).
type Scraper interface {
	Scrape(ctx context.Context, workingDir string, spec ScrapeSpec) (ScrapeResult, error)

	// UploadResult persists the FINAL result.json to object storage under
	// <runID>/result.json so the controller's ResultReader can fetch it.
	// Called by the wrapper's Entry AFTER Scrape returns and the result is
	// finalized (Phase reclassified for signal/timeout, Scrape output merged).
	UploadResult(ctx context.Context, runID string, payload []byte) error
}

// ScrapeSpec is the input to Scraper.Scrape. Small struct so extending
// (e.g. per-run compress option) doesn't break every implementation.
type ScrapeSpec struct {
	// RunID becomes the object-store key prefix — <bucket>/<runID>/<file>.
	RunID string

	// Paths are the doublestar glob patterns from Test.spec.artifacts.paths.
	// Empty = nothing to scrape (Scraper returns empty ScrapeResult, no error).
	Paths []string
}

// ScrapeResult is what Scraper.Scrape returns. Fields merge into the
// wrapper's final ExecutionResult before result.json is written.
type ScrapeResult struct {
	Artifacts   []ArtifactRef
	TestCounts  *TestCounts
	ScrapeError string
}

// (No Runner interface anymore — step 11 refactor collapsed the
// Runner-per-tool model into a single generic /entry that runs
// req.Command/Args verbatim. Verdict comes from exit code + optional
// verdictFrom processors. See entry.go.)
