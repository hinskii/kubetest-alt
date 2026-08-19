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

package junit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScan_FindsAndAggregatesReports drops two JUnit XMLs into a temp
// dir and asserts the aggregate counts match the sum.
func TestScan_FindsAndAggregatesReports(t *testing.T) {
	dir := t.TempDir()
	// One report with 3 pass, 1 fail; another with 2 pass, 0 fail.
	writeJUnit(t, filepath.Join(dir, "sub", "junit-1.xml"),
		junitXML(3, 1, 0))
	writeJUnit(t, filepath.Join(dir, "junit-2.xml"),
		junitXML(2, 0, 0))

	counts, err := Scan(dir, nil)
	require.NoError(t, err)
	// (3 pass + 1 fail) + (2 pass) = 6 total, 1 failed.
	assert.Equal(t, 6, counts.Total)
	assert.Equal(t, 1, counts.Failed)
}

// TestScan_NoReportsSurfacesSentinel is the plan's exact rule: verdictFrom
// =junit with no parseable report → error, never silently passed. The
// wrapper depends on this to differentiate "tool wrote no report" from
// "tool ran and passed cleanly".
func TestScan_NoReportsSurfacesSentinel(t *testing.T) {
	dir := t.TempDir()
	_, err := Scan(dir, nil)
	require.ErrorIs(t, err, ErrNoReportFound)
}

// TestScan_IgnoresNonJUnitXML: an operator's misplaced XML doesn't blow
// up the scan; it's silently skipped and the sentinel fires if nothing
// else parses.
func TestScan_IgnoresNonJUnitXML(t *testing.T) {
	dir := t.TempDir()
	writeJUnit(t, filepath.Join(dir, "not-junit.xml"),
		`<?xml version="1.0"?><random>data</random>`)
	_, err := Scan(dir, nil)
	require.ErrorIs(t, err, ErrNoReportFound)
}

// TestScan_CustomGlobsRespected: the caller's globs override the defaults
// — useful for tools with non-standard report paths.
func TestScan_CustomGlobsRespected(t *testing.T) {
	dir := t.TempDir()
	writeJUnit(t, filepath.Join(dir, "reports", "myreport.xml"),
		junitXML(4, 0, 0))
	// Default globs don't match the "myreport.xml" name.
	_, err := Scan(dir, nil)
	require.ErrorIs(t, err, ErrNoReportFound)
	// Explicit glob finds it.
	counts, err := Scan(dir, []string{"reports/*.xml"})
	require.NoError(t, err)
	assert.Equal(t, 4, counts.Total)
}

// TestScan_EmptyWorkingDirRejected — safety guard.
func TestScan_EmptyWorkingDirRejected(t *testing.T) {
	_, err := Scan("", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires workingDir")
}

// TestScan_BadGlobBubblesUp — malformed pattern → operator sees it.
func TestScan_BadGlobBubblesUp(t *testing.T) {
	_, err := Scan(t.TempDir(), []string{"[unclosed"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad glob")
}

// writeJUnit writes body to path, creating parent dirs as needed. Helper
// for tests that seed multiple report files.
func writeJUnit(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

// junitXML builds a minimal well-formed JUnit XML with the given counts.
// The scraper's aggregate walks testsuite attributes so this is the
// smallest thing that produces correct counts.
func junitXML(pass, fail, skip int) string {
	total := pass + fail + skip
	var cases strings.Builder
	for range pass {
		cases.WriteString(`<testcase classname="c" name="p"/>`)
	}
	for range fail {
		cases.WriteString(`<testcase classname="c" name="f"><failure message="x">stack</failure></testcase>`)
	}
	for range skip {
		cases.WriteString(`<testcase classname="c" name="s"><skipped/></testcase>`)
	}
	return `<?xml version="1.0"?><testsuites tests="` + itoa(total) +
		`" failures="` + itoa(fail) + `" skipped="` + itoa(skip) +
		`"><testsuite tests="` + itoa(total) + `" failures="` + itoa(fail) +
		`" skipped="` + itoa(skip) + `">` + cases.String() + `</testsuite></testsuites>`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return sign + string(b[i:])
}
