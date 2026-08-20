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

// Package junit scans a working directory for JUnit XML reports and
// aggregates their pass/fail counts. Used by /entry's verdict processor
// (step 11) when Test.spec.verdict.from == "junit"; parses via the
// existing scraper JUnit reader so tools that emit JUnit only need one
// XML dialect to be understood.
package junit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/hinskii/kubetest-alt/internal/scraper"
)

// TestCounts mirrors the scraper's aggregate counts shape without pulling
// the executor package into the import graph. Downstream (Entry) copies
// into its own executor.TestCounts before writing result.json.
type TestCounts struct {
	Total   int
	Passed  int
	Failed  int
	Skipped int
}

// ErrNoReportFound is returned by Scan when no file matching any of the
// search globs parses as JUnit. Semantically distinct from "found junit,
// everything passed" and from "found junit, some failed" — the wrapper
// surfaces this as phase=error (never as silently passed).
var ErrNoReportFound = errors.New("junit: no parseable report found")

// ErrEmptyReport is returned by Scan when parseable JUnit files ARE present
// but their aggregate test count is zero (empty testsuites, spec pattern
// matched nothing, tool exited before collecting anything). Semantically
// distinct from ErrNoReportFound (which means "no XML at all") because the
// tool clearly ran and wrote a report — it just didn't run any tests.
//
// Message text is load-bearing: the /entry wrapper surfaces this verbatim
// in ExecutionResult.ErrorMessage. Session A had a bug where an empty
// Cypress report (typo in --spec pattern) reported passed; the fix must
// surface loudly so a typo doesn't ship as eternally-green CI.
var ErrEmptyReport = errors.New("junit: no tests found in JUnit report")

// DefaultGlobs lists the standard JUnit reporter output locations across
// the tools we care about. The wrapper scans workingDir with these unless
// the caller passes explicit patterns. Doublestar syntax matches what the
// scraper uses so operators only learn one glob dialect.
var DefaultGlobs = []string{
	"**/junit*.xml",
	"**/*junit*.xml",
	"**/results.xml",           // cypress default
	"**/newman-*.xml",          // newman -r junit
	"**/TEST-*.xml",            // gradle/surefire
	"**/test-results/**/*.xml", // gradle + generic
}

// Scan walks workingDir under the given globs (or DefaultGlobs when empty)
// and returns the AGGREGATE TestCounts across every parseable JUnit file
// it finds. Non-JUnit XML is silently ignored (the scraper makes the same
// call — one report shape, many files).
//
// Returns ErrNoReportFound when nothing parseable turns up. That's the
// verdict-processor semantic: "you asked for junit but there's nothing
// to look at" is an operator problem (missing report path, wrong glob)
// and must be loud, never silently classified as passed.
func Scan(workingDir string, globs []string) (TestCounts, error) {
	if workingDir == "" {
		return TestCounts{}, errors.New("junit: Scan requires workingDir")
	}
	if len(globs) == 0 {
		globs = DefaultGlobs
	}

	var (
		agg    TestCounts
		found  bool
		lastEr error
	)
	seen := map[string]bool{}
	for _, glob := range globs {
		matches, err := doublestar.FilepathGlob(filepath.Join(workingDir, glob))
		if err != nil {
			// Bad glob pattern — bubble up so the caller sees the mistake
			// at operator time (misconfigured Test) instead of silently
			// producing "no reports".
			return TestCounts{}, fmt.Errorf("junit: bad glob %q: %w", glob, err)
		}
		for _, path := range matches {
			if seen[path] {
				continue // same file matched two overlapping globs
			}
			seen[path] = true
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			counts, perr := scraper.ParseJUnitFile(path)
			if perr != nil {
				lastEr = perr
				continue // not JUnit / malformed — keep scanning
			}
			agg.Total += counts.Total
			agg.Passed += counts.Passed
			agg.Failed += counts.Failed
			agg.Skipped += counts.Skipped
			found = true
		}
	}
	if !found {
		if lastEr != nil {
			return TestCounts{}, fmt.Errorf("%w (last parse error: %v)", ErrNoReportFound, lastEr)
		}
		return TestCounts{}, ErrNoReportFound
	}
	// Files parsed but aggregate is zero — same "wrong-green would ship"
	// hazard as no-report-found, distinct sentinel so the operator can tell
	// the two apart in error messages. Session A regression: a typo in
	// Cypress's --spec pattern produced an empty report AND phase=passed.
	if agg.Total == 0 {
		return agg, ErrEmptyReport
	}
	return agg, nil
}
