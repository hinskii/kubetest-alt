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
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hinskii/kubetest-alt/pkg/executor"
)

// MaxJUnitFileBytes bounds how much of one XML file we parse. A malicious
// or huge JUnit report shouldn't OOM the wrapper container. 10MB is the
// same knob the plan §15.4 uses for kubelet log rotation — mirror it.
const MaxJUnitFileBytes = 10 * 1024 * 1024

// junitSuites is the loosest possible JUnit schema — matches both
// <testsuites> (top-level container) and <testsuite> (single-suite files).
// stdlib encoding/xml is deliberate over the joshdk/go-junit lib: we control
// what fields exist and how counts aggregate, and adding a dep for one
// half-page of XML parsing pays no bill.
type junitSuites struct {
	XMLName xml.Name     `xml:"testsuites"`
	Suites  []junitSuite `xml:"testsuite"`
	Cases   []junitCase  `xml:"testcase"` // some tools emit flat cases
}

type junitSuite struct {
	Tests    int          `xml:"tests,attr"`
	Failures int          `xml:"failures,attr"`
	Errors   int          `xml:"errors,attr"`
	Skipped  int          `xml:"skipped,attr"`
	Cases    []junitCase  `xml:"testcase"`
	Nested   []junitSuite `xml:"testsuite"`
}

// junitCase is present so we can count files that emit <testcase> without
// summary attributes on the enclosing <testsuite> — that's newman's default.
type junitCase struct {
	Skipped *struct{} `xml:"skipped"`
	Failure *struct{} `xml:"failure"`
	Errored *struct{} `xml:"error"`
}

// IsProbablyJUnit does a cheap top-level element check so we don't fail
// parsing on arbitrary XML (Cypress config, k8s manifests scraped by mistake).
// Only files that begin with <testsuites> or <testsuite> go into the JUnit
// path — the rest are uploaded verbatim without contributing to TestCounts.
func IsProbablyJUnit(head []byte) bool {
	s := strings.TrimSpace(string(head))
	// Skip XML declaration if present.
	if strings.HasPrefix(s, "<?xml") {
		if end := strings.Index(s, "?>"); end > 0 {
			s = strings.TrimSpace(s[end+2:])
		}
	}
	return strings.HasPrefix(s, "<testsuites") || strings.HasPrefix(s, "<testsuite")
}

// ParseJUnit reads r (bounded to MaxJUnitFileBytes) and returns the aggregated
// counts. Malformed XML returns an error — the scraper logs it and moves on
// rather than aborting the whole run.
func ParseJUnit(r io.Reader) (executor.TestCounts, error) {
	limited := io.LimitReader(r, MaxJUnitFileBytes)
	data, err := io.ReadAll(limited)
	if err != nil {
		return executor.TestCounts{}, fmt.Errorf("read: %w", err)
	}
	if len(data) == 0 {
		return executor.TestCounts{}, errors.New("empty file")
	}
	if !IsProbablyJUnit(data[:min(len(data), 512)]) {
		return executor.TestCounts{}, errNotJUnit
	}

	// Try <testsuites> first (Cypress, Newman, k6 with junit output).
	var top junitSuites
	if err := xml.Unmarshal(data, &top); err == nil && (len(top.Suites) > 0 || len(top.Cases) > 0) {
		return aggregate(top.Suites, top.Cases), nil
	}

	// Fall back to a single <testsuite> root.
	var single junitSuite
	if err := xml.Unmarshal(data, &single); err == nil {
		return aggregate([]junitSuite{single}, nil), nil
	}

	// If both parses failed, surface the LAST error verbatim.
	return executor.TestCounts{}, fmt.Errorf("parse: not a valid JUnit XML document")
}

// errNotJUnit is a sentinel the scraper checks with errors.Is to tell "not
// JUnit at all" apart from "malformed JUnit". The first is normal (unrelated
// XML file uploaded verbatim); the second is worth a warning.
var errNotJUnit = errors.New("scraper: not a JUnit document")

// aggregate walks the parsed tree and sums counts. Two counting modes:
//   - Prefer <testsuite tests="N" failures="N" ...> attributes when set
//     (canonical form, faster).
//   - Fall back to counting <testcase> children when attrs are all zero
//     (some tools like newman only emit case-level markers).
func aggregate(suites []junitSuite, topLevelCases []junitCase) executor.TestCounts {
	var t executor.TestCounts
	countCases(topLevelCases, &t)
	for _, s := range suites {
		countSuite(s, &t)
	}
	return t
}

func countSuite(s junitSuite, out *executor.TestCounts) {
	if s.Tests > 0 || s.Failures > 0 || s.Errors > 0 || s.Skipped > 0 {
		out.Total += s.Tests
		out.Failed += s.Failures + s.Errors
		out.Skipped += s.Skipped
		out.Passed += s.Tests - s.Failures - s.Errors - s.Skipped
	} else {
		countCases(s.Cases, out)
	}
	for _, n := range s.Nested {
		countSuite(n, out)
	}
}

func countCases(cases []junitCase, out *executor.TestCounts) {
	for _, c := range cases {
		out.Total++
		switch {
		case c.Failure != nil, c.Errored != nil:
			out.Failed++
		case c.Skipped != nil:
			out.Skipped++
		default:
			out.Passed++
		}
	}
}

// ParseJUnitFile is a thin wrapper for scraper.Scrape use — opens, defers
// close, delegates to ParseJUnit.
func ParseJUnitFile(path string) (executor.TestCounts, error) {
	// #nosec G304 -- path comes from ExpandGlobs which validated it stays under workingDir.
	f, err := os.Open(path)
	if err != nil {
		return executor.TestCounts{}, err
	}
	defer func() { _ = f.Close() }()
	return ParseJUnit(f)
}
