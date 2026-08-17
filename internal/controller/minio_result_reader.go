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
	"encoding/json"
	"errors"
	"fmt"
	"io"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/executor"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

// StorageResultReader fetches the wrapper's result.json from object storage
// under the layout the scraper wrote: <bucket>/<runID>/result.json.
//
// Zero value isn't usable — always build via NewStorageResultReader.
type StorageResultReader struct {
	Downloader storage.Downloader
	Bucket     string
}

// NewStorageResultReader wires a Downloader (production: pkg/storage.MinIO)
// with the operator's configured bucket. cmd/operator constructs one when
// --minio-endpoint is set; otherwise the controller stays on NoResultReader.
func NewStorageResultReader(d storage.Downloader, bucket string) *StorageResultReader {
	return &StorageResultReader{Downloader: d, Bucket: bucket}
}

// Read implements controller.ResultReader.
//
// Behavior:
//   - Object present → parse into ExecutionResult, project into RunResult.
//   - Object absent (storage.ErrNotFound) → return ErrResultNotFound so the
//     reconciler falls back to Pod terminated state (§15.2).
//   - Transient errors bubble up unchanged; the reconciler treats them as
//     retryable via FallbackRequeue.
func (r *StorageResultReader) Read(ctx context.Context, runID string) (*RunResult, error) {
	if r.Downloader == nil {
		return nil, errors.New("storage-result-reader: nil Downloader")
	}
	if r.Bucket == "" {
		return nil, errors.New("storage-result-reader: empty Bucket")
	}
	key := runID + "/" + executor.ResultFileName
	rc, err := r.Downloader.Get(ctx, r.Bucket, key)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, ErrResultNotFound
		}
		return nil, fmt.Errorf("get %s/%s: %w", r.Bucket, key, err)
	}
	defer func() { _ = rc.Close() }()

	b, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read %s/%s: %w", r.Bucket, key, err)
	}
	var er executor.ExecutionResult
	if err := json.Unmarshal(b, &er); err != nil {
		return nil, fmt.Errorf("parse %s/%s: %w", r.Bucket, key, err)
	}
	return projectRunResult(&er), nil
}

// projectRunResult narrows ExecutionResult (wire format) into RunResult
// (controller-facing subset). Kept as an explicit function so future
// additions to ExecutionResult (steps 09/10) don't leak into the controller
// by accident — each new field needs a deliberate mapping here.
func projectRunResult(er *executor.ExecutionResult) *RunResult {
	rr := &RunResult{
		Phase:        testsv1alpha1.Phase(er.Phase),
		ErrorMessage: er.ErrorMessage,
		Metrics:      er.Metrics,
		Artifacts:    convertArtifacts(er.Artifacts),
		ScrapeError:  er.ScrapeError,
	}
	if er.TestCounts != nil {
		rr.TestCounts = &testsv1alpha1.TestCounts{
			Total:   er.TestCounts.Total,
			Passed:  er.TestCounts.Passed,
			Failed:  er.TestCounts.Failed,
			Skipped: er.TestCounts.Skipped,
		}
	}
	return rr
}

func convertArtifacts(in []executor.ArtifactRef) []testsv1alpha1.ArtifactRef {
	if len(in) == 0 {
		return nil
	}
	out := make([]testsv1alpha1.ArtifactRef, 0, len(in))
	for _, a := range in {
		out = append(out, testsv1alpha1.ArtifactRef{
			Path:        a.Path,
			Key:         a.Key,
			SizeBytes:   a.SizeBytes,
			ContentType: a.ContentType,
		})
	}
	return out
}
