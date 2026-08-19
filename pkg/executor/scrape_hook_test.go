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
	"io"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeScraper records calls + returns configured results.
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
	res := f.returnResult
	if len(res.Artifacts) == 0 {
		res.Artifacts = []ArtifactRef{{Path: "example.txt", Key: spec.RunID + "/example.txt"}}
	}
	return res, f.returnErr
}

func (f *fakeScraper) UploadResult(_ context.Context, runID string, payload []byte) error {
	f.uploadCalls.Add(1)
	f.uploadedRunID = runID
	f.uploadedResult = append([]byte(nil), payload...)
	return nil
}

// TestScrape_RunsAfterToolExit_HappyPath: scrape fires once with the
// correct RunID + Paths after the tool exits. Uploaded result carries
// scrape output.
func TestScrape_RunsAfterToolExit_HappyPath(t *testing.T) {
	fake := &fakeScraper{
		returnResult: ScrapeResult{
			Artifacts:  []ArtifactRef{{Path: "results/summary.json", Key: "run-1/results/summary.json", SizeBytes: 42}},
			TestCounts: &TestCounts{Total: 5, Passed: 4, Failed: 1},
		},
	}
	req := ExecutionRequest{
		RunID:          "run-1",
		WorkingDir:     t.TempDir(),
		Args:           []string{"/bin/true"},
		Artifacts:      ArtifactSpec{Paths: []string{"results/**"}},
		TimeoutSeconds: 30,
	}
	e := &Entry{
		Exec:        shExit(0, nil),
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Scraper:     fake,
		RequestPath: writeRequest(t, req),
		ResultDir:   t.TempDir(),
		Loader:      &bytes.Buffer{},
	}
	require.NoError(t, e.Execute(context.Background()))

	assert.Equal(t, int32(1), fake.scrapeCalls.Load(), "scrape ran exactly once")
	assert.Equal(t, int32(1), fake.uploadCalls.Load(), "UploadResult ran exactly once")
	assert.Equal(t, "run-1", fake.uploadedRunID)

	var got ExecutionResult
	require.NoError(t, json.Unmarshal(fake.uploadedResult, &got))
	assert.Equal(t, PhasePassed, got.Phase)
	require.Len(t, got.Artifacts, 1)
	require.NotNil(t, got.TestCounts)
	assert.Equal(t, 5, got.TestCounts.Total)
}

// TestScrape_StillRunsOnSignal: SIGTERM mid-run → scrape + upload still
// fire (§15.3 "flush partial result + trigger artifact scrape hook").
func TestScrape_StillRunsOnSignal(t *testing.T) {
	fake := &fakeScraper{}
	req := ExecutionRequest{
		RunID:          "sig-run",
		WorkingDir:     t.TempDir(),
		Args:           []string{"/bin/true"},
		TimeoutSeconds: 60,
	}
	e := &Entry{
		Exec: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", "sleep 5")
		},
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Scraper:     fake,
		RequestPath: writeRequest(t, req),
		ResultDir:   t.TempDir(),
		Loader:      &bytes.Buffer{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	require.NoError(t, e.Execute(ctx))

	assert.Equal(t, int32(1), fake.scrapeCalls.Load(), "scrape must fire even after signal")
	var got ExecutionResult
	require.NoError(t, json.Unmarshal(fake.uploadedResult, &got))
	assert.Equal(t, PhaseAborted, got.Phase)
}

// TestScrape_JUnitVerdictCountsWinOverScraperCounts: when the verdict
// processor set TestCounts (junit path), the scraper's TestCounts don't
// clobber them — verdict counts are authoritative.
func TestScrape_JUnitVerdictCountsWinOverScraperCounts(t *testing.T) {
	fake := &fakeScraper{
		returnResult: ScrapeResult{
			TestCounts: &TestCounts{Total: 99, Failed: 42}, // wrong on purpose
		},
	}
	req := ExecutionRequest{
		RunID:          "junit-run",
		WorkingDir:     t.TempDir(),
		Args:           []string{"/bin/true"},
		Verdict:        VerdictSpec{From: VerdictFromJUnit},
		TimeoutSeconds: 30,
	}
	e := &Entry{
		Exec:        shExit(0, nil),
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Scraper:     fake,
		RequestPath: writeRequest(t, req),
		ResultDir:   t.TempDir(),
		Loader:      &bytes.Buffer{},
	}
	e.WorkingDir = req.WorkingDir
	e.JUnitProcessor = func(_ string) (TestCounts, error) {
		return TestCounts{Total: 10, Passed: 10}, nil
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	require.NotNil(t, got.TestCounts)
	assert.Equal(t, 10, got.TestCounts.Total, "verdict counts win, not scraper's 99")
	assert.Equal(t, 0, got.TestCounts.Failed)
}

// TestScrape_NilSkipsCleanly: Entry with Scraper=nil must NOT panic
// and must still write result.json locally.
func TestScrape_NilSkipsCleanly(t *testing.T) {
	req := ExecutionRequest{
		RunID:          "no-scraper",
		Args:           []string{"/bin/true"},
		TimeoutSeconds: 30,
	}
	e := &Entry{
		Exec:        shExit(0, nil),
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		RequestPath: writeRequest(t, req),
		ResultDir:   t.TempDir(),
		Loader:      &bytes.Buffer{},
	}
	require.NoError(t, e.Execute(context.Background()))
	got := readResult(t, e.ResultDir)
	assert.Equal(t, PhasePassed, got.Phase)
	assert.Nil(t, got.Artifacts)
}
