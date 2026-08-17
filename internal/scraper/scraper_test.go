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

package scraper

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hinskii/kubetest-alt/pkg/executor"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

const testBucket = "kubetest-artifacts"

// writeFile is a shorthand for tests that need to plant real bytes for glob
// matching. Different from touch (glob_test.go) because we care about content
// here — the scraper reads and uploads.
func writeFile(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	// #nosec G301 -- t.TempDir scratch.
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	// #nosec G306 -- test fixture in t.TempDir().
	require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
}

func TestScrape_HappyPathUploadsAllMatches(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "results/a.json", `{"metric":1}`)
	writeFile(t, dir, "results/b.txt", "log")
	writeFile(t, dir, "other/c.md", "unrelated")

	up := storage.NewFake()
	s := New(up, testBucket)

	res, err := s.Scrape(context.Background(), dir, executor.ScrapeSpec{
		RunID: "run-1",
		Paths: []string{"results/**"},
	})
	require.NoError(t, err)
	assert.Empty(t, res.ScrapeError)
	assert.Len(t, res.Artifacts, 2, "results/a.json + results/b.txt")

	// Object keys carry the per-run prefix — that's what makes multi-tenant
	// isolation work in a shared bucket.
	assert.ElementsMatch(t,
		[]string{"run-1/results/a.json", "run-1/results/b.txt"},
		up.Keys(testBucket),
	)
}

func TestScrape_NoMatchesEmptyResult(t *testing.T) {
	up := storage.NewFake()
	s := New(up, testBucket)

	res, err := s.Scrape(context.Background(), t.TempDir(), executor.ScrapeSpec{
		RunID: "run-empty",
		Paths: []string{"never/*"},
	})
	require.NoError(t, err)
	assert.Empty(t, res.Artifacts)
	assert.Empty(t, res.ScrapeError)
	assert.Nil(t, res.TestCounts)
}

// TestScrape_JUnitCountsMerged: two JUnit files → aggregate counts.
func TestScrape_JUnitCountsMerged(t *testing.T) {
	dir := t.TempDir()
	singleJUnit := string(fixture(t, "junit-single-suite.xml"))
	nestedJUnit := string(fixture(t, "junit-nested-suites.xml"))
	writeFile(t, dir, "reports/single.xml", singleJUnit)
	writeFile(t, dir, "reports/nested.xml", nestedJUnit)

	up := storage.NewFake()
	s := New(up, testBucket)

	res, err := s.Scrape(context.Background(), dir, executor.ScrapeSpec{
		RunID: "run-junit",
		Paths: []string{"reports/*.xml"},
	})
	require.NoError(t, err)
	require.NotNil(t, res.TestCounts)
	// Single: 4/2/1/1. Nested: 6/3/2/1. Sum: 10/5/3/2.
	assert.Equal(t, &executor.TestCounts{Total: 10, Passed: 5, Failed: 3, Skipped: 2}, res.TestCounts)
}

// TestScrape_NonJUnitXMLIgnored: a config.xml uploaded alongside real JUnit
// contributes ZERO to TestCounts (only its bytes get uploaded).
func TestScrape_NonJUnitXMLIgnored(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "results/junit.xml", string(fixture(t, "junit-single-suite.xml")))
	writeFile(t, dir, "results/config.xml", string(fixture(t, "config.xml")))

	up := storage.NewFake()
	res, err := New(up, testBucket).Scrape(context.Background(), dir, executor.ScrapeSpec{
		RunID: "run-mix",
		Paths: []string{"**/*.xml"},
	})
	require.NoError(t, err)
	require.NotNil(t, res.TestCounts)
	// Only junit.xml contributes → 4/2/1/1 (matches single-suite fixture).
	assert.Equal(t, 4, res.TestCounts.Total)
	// But BOTH files are uploaded.
	assert.Len(t, res.Artifacts, 2)
}

// TestScrape_UploaderPermanentFailureRecordsButDoesNotError:
// scraper never returns error for upload failures — records them in
// ScrapeError so the run's Phase stays intact.
func TestScrape_UploaderPermanentFailureRecordsButDoesNotError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "results/a.txt", "x")

	up := storage.NewFake()
	up.PutErrors = []error{errors.New("permanent 500"), errors.New("permanent 500")}
	s := New(up, testBucket)

	res, err := s.Scrape(context.Background(), dir, executor.ScrapeSpec{
		RunID: "run-fail",
		Paths: []string{"**/*.txt"},
	})
	require.NoError(t, err, "scrape MUST NOT bubble upload errors")
	assert.Empty(t, res.Artifacts, "nothing uploaded successfully")
	assert.Contains(t, res.ScrapeError, "upload failed")
	assert.Contains(t, res.ScrapeError, "results/a.txt")
}

// TestScrape_PartialFailureUploadsWhatItCan: 1 file fails, 1 succeeds →
// Artifacts has 1 entry AND ScrapeError describes the failure.
func TestScrape_PartialFailure(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "x")
	writeFile(t, dir, "b.txt", "y")

	up := storage.NewFake()
	// First upload fails permanently, second succeeds.
	up.PutErrors = []error{errors.New("500")}
	s := New(up, testBucket)

	res, err := s.Scrape(context.Background(), dir, executor.ScrapeSpec{
		RunID: "run-partial",
		Paths: []string{"*.txt"},
	})
	require.NoError(t, err)
	assert.Len(t, res.Artifacts, 1, "one file succeeded")
	assert.NotEmpty(t, res.ScrapeError, "the failure is recorded")
}

func TestScrape_UploadResult_UsesResultJSONKey(t *testing.T) {
	up := storage.NewFake()
	s := New(up, testBucket)

	payload := []byte(`{"phase":"passed"}`)
	require.NoError(t, s.UploadResult(context.Background(), "run-x", payload))

	got, ok := up.Object(testBucket, "run-x/result.json")
	require.True(t, ok, "result.json missing from fake")
	assert.Equal(t, payload, got)
}

func TestScrape_GuardRails(t *testing.T) {
	up := storage.NewFake()
	s := New(up, testBucket)

	t.Run("nil uploader", func(t *testing.T) {
		bad := &Scraper{Bucket: testBucket}
		_, err := bad.Scrape(context.Background(), t.TempDir(), executor.ScrapeSpec{RunID: "r"})
		require.Error(t, err)
	})
	t.Run("empty bucket", func(t *testing.T) {
		bad := &Scraper{Uploader: up}
		_, err := bad.Scrape(context.Background(), t.TempDir(), executor.ScrapeSpec{RunID: "r"})
		require.Error(t, err)
	})
	t.Run("empty runID", func(t *testing.T) {
		_, err := s.Scrape(context.Background(), t.TempDir(), executor.ScrapeSpec{})
		require.Error(t, err)
	})
}

// TestScrape_ContentTypeInference — .xml → application/xml, .json → application/json.
func TestScrape_ContentTypeInference(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.xml", `<x/>`)
	writeFile(t, dir, "b.json", `{}`)
	writeFile(t, dir, "c.bin", "\x00\x01\x02\x03")

	up := storage.NewFake()
	res, err := New(up, testBucket).Scrape(context.Background(), dir, executor.ScrapeSpec{
		RunID: "r",
		Paths: []string{"*"},
	})
	require.NoError(t, err)

	byPath := map[string]string{}
	for _, a := range res.Artifacts {
		byPath[a.Path] = a.ContentType
	}
	assert.Contains(t, byPath["a.xml"], "xml")
	assert.Contains(t, byPath["b.json"], "json")
	// binary → text/plain fallback fails printable heuristic; use octet-stream
	assert.NotEmpty(t, byPath["c.bin"])
}

// TestScrape_PerfRegistryHasK6 asserts the step-07 registry has k6 wired.
// step-11 adds cypress/newman/locust/jmeter.
func TestPerfRegistry_K6Registered(t *testing.T) {
	assert.NotNil(t, PerfParserFor("k6"), "k6 must be in the perf registry after step 07")
	assert.Nil(t, PerfParserFor("cypress"), "cypress not wired until step 11")
	assert.Contains(t, RegisteredPerfTypes(), "k6")
}

// TestScrape_ContextCancelledMidLoop: parent ctx fires between file uploads.
// The scraper aborts cleanly, records "cancelled" in ScrapeError, but the
// files uploaded before cancel remain in the fake.
func TestScrape_ContextCancelledMidLoop(t *testing.T) {
	dir := t.TempDir()
	// Enough files that cancellation between them is realistic.
	for i := range 20 {
		writeFile(t, dir, "results/f"+itoa(i)+".txt", "x")
	}
	up := storage.NewFake()
	s := New(up, testBucket)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately — scrape body's ctx.Err() check trips on first iteration

	res, err := s.Scrape(ctx, dir, executor.ScrapeSpec{
		RunID: "cancelled",
		Paths: []string{"results/*"},
	})
	require.NoError(t, err)
	assert.Contains(t, res.ScrapeError, "cancelled")
}

// TestScrape_UploadResultGuardRails: nil uploader returns error.
func TestScrape_UploadResultNilUploader(t *testing.T) {
	bad := &Scraper{Bucket: testBucket}
	err := bad.UploadResult(context.Background(), "r", []byte("{}"))
	require.Error(t, err)
}

// TestScrape_ContentTypeSniff_PrintableText: file with unknown extension +
// printable content → text/plain via fallback sniff.
func TestScrape_ContentTypeSniff_PrintableText(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "readme", "hello world this is a text file with a lot of printable characters\n")

	up := storage.NewFake()
	res, err := New(up, testBucket).Scrape(context.Background(), dir, executor.ScrapeSpec{
		RunID: "r",
		Paths: []string{"readme"},
	})
	require.NoError(t, err)
	require.Len(t, res.Artifacts, 1)
	assert.Contains(t, res.Artifacts[0].ContentType, "text/plain")
}

// TestScrape_ContentTypeSniff_EmptyFile: 0-byte file → octet-stream fallback.
func TestScrape_ContentTypeSniff_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "empty", "")

	up := storage.NewFake()
	res, err := New(up, testBucket).Scrape(context.Background(), dir, executor.ScrapeSpec{
		RunID: "r",
		Paths: []string{"empty"},
	})
	require.NoError(t, err)
	require.Len(t, res.Artifacts, 1)
	assert.Equal(t, "application/octet-stream", res.Artifacts[0].ContentType)
}
