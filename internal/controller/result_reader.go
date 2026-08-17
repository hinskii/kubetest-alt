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

package controller

import (
	"context"
	"errors"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// RunResult is the reconciler-facing subset of the wrapper's result.json.
// Extended in step 07 to carry the scraper's output (artifacts, JUnit counts,
// tool metrics) that lands on TestRun.Status.
type RunResult struct {
	// Phase is one of the terminal Phase enum values (passed/failed/error/aborted).
	Phase testsv1alpha1.Phase

	// ErrorMessage carries the wrapper's error message when phase is failed
	// or error. Empty for passed.
	ErrorMessage string

	// Metrics is the flat tool-metrics map (p95_ms, rps, checks_*). Nil when
	// the tool didn't emit metrics.
	Metrics map[string]float64

	// TestCounts is the JUnit-aggregated summary the scraper computed. Nil
	// when no JUnit files were uploaded.
	TestCounts *testsv1alpha1.TestCounts

	// Artifacts is the ref list — path + object-store key + size. Populated
	// by the scraper. Empty slice means "scraper ran, nothing matched".
	Artifacts []testsv1alpha1.ArtifactRef

	// ScrapeError carries a scraper failure message (network down, bucket
	// missing). The Phase verdict is still the tool's; UI can surface this
	// as "run passed, artifacts not saved: ..." separately.
	ScrapeError string
}

// ErrResultNotFound indicates the wrapper didn't produce a result.json (crash,
// OOM, SIGKILL). Callers fall back to Pod terminated state per §15.2.
var ErrResultNotFound = errors.New("result: not found")

// ResultReader fetches the wrapper's terminal result for a given TestRun.
// Interface exists so step 07 can drop in a MinIO/S3-backed implementation
// without touching the reconciler.
type ResultReader interface {
	Read(ctx context.Context, runID string) (*RunResult, error)
}

// NoResultReader always returns ErrResultNotFound. It's the default when the
// operator boots before step 07 wires the real reader — every Job completion
// then falls back to the pod-terminated-state analysis, which is the correct
// behavior when we truly have no result store.
type NoResultReader struct{}

// Read implements ResultReader.
func (NoResultReader) Read(context.Context, string) (*RunResult, error) {
	return nil, ErrResultNotFound
}
