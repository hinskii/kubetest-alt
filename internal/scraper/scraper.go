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

// Package scraper is the wrapper's post-tool step: glob artifacts.paths,
// upload matches to object storage, auto-scan JUnit XMLs into aggregated
// counts. Ships behind the executor.Scraper interface so the wrapper
// (pkg/executor) doesn't depend on internal/scraper directly.
package scraper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hinskii/kubetest-alt/pkg/executor"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

// Retry defaults for uploadOne. Transient uploader errors (network flakes,
// 5xx from MinIO) get retried; a run whose bucket is genuinely gone still
// hits the wall after MaxUploadAttempts × BackoffBase in the worst case.
//
// Kept as package vars so tests can shorten BackoffBase to zero and avoid
// wall-clock latency in the retry-on-transient-error scenario.
var (
	MaxUploadAttempts       = 3
	UploadBackoffBase       = 100 * time.Millisecond
	UploadBackoffMultiplier = 2.0
)

// Scraper is the concrete internal/scraper implementation of the
// pkg/executor.Scraper interface. Injected into the wrapper via cmd/entry.
type Scraper struct {
	// Uploader is where scraped files go. Required.
	Uploader storage.Uploader

	// Bucket is the object-store bucket. Callers set from operator config
	// (--minio-bucket flag → env → wrapper).
	Bucket string
}

// New builds a Scraper.
func New(u storage.Uploader, bucket string) *Scraper {
	return &Scraper{Uploader: u, Bucket: bucket}
}

// Scrape implements pkg/executor.Scraper. See that interface's contract; the
// short version:
//   - Uploads every file matching spec.Paths (relative to workingDir).
//   - JUnit XMLs contribute to TestCounts; other files upload unchanged.
//   - A permanent Uploader failure is captured in ScrapeError and returned
//     as part of the ScrapeResult — the caller's tool-run verdict stays valid.
//
// Bounded by ctx: caller sets a fetch-like timeout; scrape aborts cleanly if it fires.
func (s *Scraper) Scrape(ctx context.Context, workingDir string, spec executor.ScrapeSpec) (executor.ScrapeResult, error) {
	if s.Uploader == nil {
		return executor.ScrapeResult{}, errors.New("scraper: nil Uploader")
	}
	if s.Bucket == "" {
		return executor.ScrapeResult{}, errors.New("scraper: empty Bucket")
	}
	if spec.RunID == "" {
		return executor.ScrapeResult{}, errors.New("scraper: empty RunID")
	}

	result := executor.ScrapeResult{}
	matches, err := ExpandGlobs(workingDir, spec.Paths)
	// ErrTooManyMatches is non-fatal — we process the first MaxMatchedFiles.
	// Any other error means the whole glob step failed.
	switch {
	case errors.Is(err, ErrTooManyMatches):
		result.ScrapeError = fmt.Sprintf("scraper: %d files exceeded cap; uploading first %d", len(matches), MaxMatchedFiles)
	case err != nil:
		return executor.ScrapeResult{ScrapeError: err.Error()}, nil
	}

	// Aggregate JUnit counts as we go; nil unless at least one JUnit file
	// contributed (a k6-only run with no XML → nil TestCounts, GUI shows
	// "no test counts reported" rather than "0/0/0/0").
	var counts *executor.TestCounts
	var uploadErrs []string

	for _, m := range matches {
		if ctx.Err() != nil {
			result.ScrapeError = appendMsg(result.ScrapeError, fmt.Sprintf("scrape cancelled: %v", ctx.Err()))
			break
		}
		ref, err := s.uploadOne(ctx, spec.RunID, m)
		if err != nil {
			uploadErrs = append(uploadErrs, fmt.Sprintf("%s: %v", m.RelPath, err))
			continue
		}
		result.Artifacts = append(result.Artifacts, ref)

		if strings.EqualFold(filepath.Ext(m.RelPath), ".xml") {
			counts = mergeCounts(counts, m.AbsPath)
		}
	}

	if len(uploadErrs) > 0 {
		result.ScrapeError = appendMsg(result.ScrapeError,
			fmt.Sprintf("upload failed for %d files: %s", len(uploadErrs), strings.Join(uploadErrs, "; ")))
	}
	result.TestCounts = counts
	return result, nil
}

// uploadOne opens the file, sniffs content-type, and puts it — with retry
// on transient failures. Returns the ArtifactRef entry that gets appended
// to ExecutionResult.Artifacts.
//
// Retry policy: MaxUploadAttempts with exponential backoff starting at
// UploadBackoffBase. This handles both network flakes and 5xx from MinIO
// where the next request is likely to succeed. A truly gone bucket / hard
// permission error just hits the wall N times — caller records failure
// in ScrapeError without changing the run's Phase.
//
// ctx cancellation short-circuits the retry loop (SIGTERM should exit fast).
func (s *Scraper) uploadOne(ctx context.Context, runID string, m GlobMatch) (executor.ArtifactRef, error) {
	// #nosec G304 -- m.AbsPath came from ExpandGlobs which pinned matches under workingDir.
	f, err := os.Open(m.AbsPath)
	if err != nil {
		return executor.ArtifactRef{}, err
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		return executor.ArtifactRef{}, err
	}
	size := stat.Size()

	// Sniff content-type once before any upload attempt (idempotent).
	ct := detectContentType(m.RelPath, f)

	key := runID + "/" + m.RelPath
	var lastErr error
	backoff := UploadBackoffBase
	for attempt := 1; attempt <= MaxUploadAttempts; attempt++ {
		// Rewind for each attempt — the previous attempt may have consumed bytes.
		if _, seekErr := f.Seek(0, io.SeekStart); seekErr != nil {
			return executor.ArtifactRef{}, seekErr
		}
		lastErr = s.Uploader.Put(ctx, s.Bucket, key, f, size, ct)
		if lastErr == nil {
			return executor.ArtifactRef{
				Path:        m.RelPath,
				Key:         key,
				SizeBytes:   size,
				ContentType: ct,
			}, nil
		}
		if attempt == MaxUploadAttempts || ctx.Err() != nil {
			break
		}
		// Sleep is interruptible by ctx so SIGTERM aborts the whole retry cycle.
		select {
		case <-time.After(backoff):
			backoff = time.Duration(float64(backoff) * UploadBackoffMultiplier)
		case <-ctx.Done():
		}
	}
	return executor.ArtifactRef{}, fmt.Errorf("after %d attempts: %w", MaxUploadAttempts, lastErr)
}

// UploadResult writes the wrapper's ExecutionResult as JSON to
// <bucket>/<runID>/result.json. Called by the wrapper AFTER Scrape merges
// counts and refs into the result, so the object stored is what the operator
// will read back.
func (s *Scraper) UploadResult(ctx context.Context, runID string, payload []byte) error {
	if s.Uploader == nil {
		return errors.New("scraper: nil Uploader")
	}
	key := runID + "/" + executor.ResultFileName
	return s.Uploader.Put(ctx, s.Bucket, key, bytes.NewReader(payload), int64(len(payload)), "application/json")
}

// detectContentType prefers extension-based inference (stable, cheap) and
// falls back to http.DetectContentType-like sniffing via mime pkg. Nothing
// exotic — we just want ".xml" → "application/xml", ".json" → "application/json"
// etc. so the GUI can pick reasonable renderers.
func detectContentType(path string, r io.ReadSeeker) string {
	if ct := mime.TypeByExtension(filepath.Ext(path)); ct != "" {
		return ct
	}
	// Fallback: sniff first 512 bytes.
	buf := make([]byte, 512)
	n, _ := r.Read(buf)
	if n == 0 {
		return "application/octet-stream"
	}
	// http.DetectContentType is stdlib but pulls net/http; avoid for one call.
	// Simple heuristic: printable ASCII → text/plain, else octet-stream.
	printable := 0
	for i := range n {
		c := buf[i]
		if c == '\n' || c == '\r' || c == '\t' || (c >= 0x20 && c < 0x7f) {
			printable++
		}
	}
	if printable > n*9/10 {
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}

// mergeCounts parses one XML file and folds its counts into acc. Silently
// returns acc unchanged on parse error or non-JUnit content — the file is
// still uploaded via the outer loop; we just don't count it.
func mergeCounts(acc *executor.TestCounts, path string) *executor.TestCounts {
	c, err := ParseJUnitFile(path)
	if err != nil {
		// Not JUnit (unrelated XML) or malformed — either way, don't count.
		return acc
	}
	if acc == nil {
		return &c
	}
	acc.Total += c.Total
	acc.Passed += c.Passed
	acc.Failed += c.Failed
	acc.Skipped += c.Skipped
	return acc
}

func appendMsg(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}
