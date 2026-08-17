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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeScraper records calls and returns configured results.
type fakeScraper struct {
	scrapeCalls    atomic.Int32
	uploadCalls    atomic.Int32
	returnResult   ScrapeResult
	returnErr      error
	uploadedRunID  string
	uploadedResult []byte
}

func (f *fakeScraper) Scrape(_ context.Context, _ string, spec ScrapeSpec) (ScrapeResult, error) {
	f.scrapeCalls.Add(1)
	// Stamp RunID into a returned artifact so tests can verify wire-through.
	res := f.returnResult
	if len(res.Artifacts) == 0 {
		res.Artifacts = []ArtifactRef{{Path: "example.txt", Key: spec.RunID + "/example.txt"}}
	}
	return res, f.returnErr
}

func (f *fakeScraper) UploadResult(_ context.Context, runID string, payload []byte) error {
	f.uploadCalls.Add(1)
	f.uploadedRunID = runID
	f.uploadedResult = append([]byte(nil), payload...) // copy
	return nil
}

// TestEntry_ScrapeRunsAfterRunner: happy path — Scrape called once with the
// correct RunID + Paths, result folded into ExecutionResult, and UploadResult
// called with the merged bytes.
func TestEntry_ScrapeRunsAfterRunner(t *testing.T) {
	fake := &fakeScraper{
		returnResult: ScrapeResult{
			Artifacts:  []ArtifactRef{{Path: "results/summary.json", Key: "run-1/results/summary.json", SizeBytes: 42}},
			TestCounts: &TestCounts{Total: 5, Passed: 4, Failed: 1},
		},
	}
	req := ExecutionRequest{
		RunID:          "run-1",
		DataDir:        "/tmp/data",
		WorkingDir:     t.TempDir(),
		Artifacts:      ArtifactSpec{Paths: []string{"results/**"}},
		TimeoutSeconds: 30,
	}
	e := &Entry{
		Runner:      &fakeRunner{Result: ExecutionResult{Phase: PhasePassed}},
		Scraper:     fake,
		RequestPath: writeRequest(t, req),
		ResultDir:   t.TempDir(),
		Stderr:      &bytes.Buffer{},
	}
	require.NoError(t, e.Execute(context.Background()))

	assert.Equal(t, int32(1), fake.scrapeCalls.Load(), "scrape ran exactly once")
	assert.Equal(t, int32(1), fake.uploadCalls.Load(), "UploadResult ran exactly once")
	assert.Equal(t, "run-1", fake.uploadedRunID)

	// The uploaded bytes decode as the merged ExecutionResult (Phase + Artifacts + TestCounts).
	var got ExecutionResult
	require.NoError(t, json.Unmarshal(fake.uploadedResult, &got))
	assert.Equal(t, PhasePassed, got.Phase)
	assert.Len(t, got.Artifacts, 1)
	require.NotNil(t, got.TestCounts)
	assert.Equal(t, 5, got.TestCounts.Total)
}

// TestEntry_ScrapeStillRunsOnSignal: SIGTERM mid-run should still trigger
// scrape + upload — plan §15.3: "flush partial result + trigger artifact
// scrape hook before exit".
func TestEntry_ScrapeStillRunsOnSignal(t *testing.T) {
	fake := &fakeScraper{}
	req := ExecutionRequest{RunID: "sig-run", WorkingDir: t.TempDir(), TimeoutSeconds: 60}

	e := &Entry{
		Runner:      &fakeRunner{Delay: 5 * time.Second},
		Scraper:     fake,
		RequestPath: writeRequest(t, req),
		ResultDir:   t.TempDir(),
		Stderr:      &bytes.Buffer{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	require.NoError(t, e.Execute(ctx))

	assert.Equal(t, int32(1), fake.scrapeCalls.Load(), "scrape must fire even after signal")
	assert.Equal(t, int32(1), fake.uploadCalls.Load(), "result.json still uploaded")
	// Phase is aborted (signal-set), not the Runner's default.
	var got ExecutionResult
	require.NoError(t, json.Unmarshal(fake.uploadedResult, &got))
	assert.Equal(t, PhaseAborted, got.Phase)
}

// TestEntry_NoScraperSkipsCleanly: Entry with Scraper=nil must NOT panic
// and must still write result.json locally.
func TestEntry_NoScraperSkipsCleanly(t *testing.T) {
	req := ExecutionRequest{RunID: "no-scraper", TimeoutSeconds: 30}
	e := &Entry{
		Runner:      &fakeRunner{Result: ExecutionResult{Phase: PhasePassed}},
		RequestPath: writeRequest(t, req),
		ResultDir:   t.TempDir(),
		Stderr:      &bytes.Buffer{},
		// Scraper: nil
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhasePassed, got.Phase)
	assert.Nil(t, got.Artifacts, "no scraper → no artifacts recorded")
}
