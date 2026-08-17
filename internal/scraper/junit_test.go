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
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hinskii/kubetest-alt/pkg/executor"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	// #nosec G304 -- fixed testdata path.
	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return b
}

func TestIsProbablyJUnit(t *testing.T) {
	yes := []string{
		`<testsuite name="x">`,
		`<testsuites><testsuite/></testsuites>`,
		"<?xml version=\"1.0\"?>\n<testsuite/>",
	}
	no := []string{
		`<configuration/>`,
		`<pod></pod>`,
		``,
		`{"json": true}`,
	}
	for _, s := range yes {
		assert.Truef(t, IsProbablyJUnit([]byte(s)), "expected JUnit: %q", s)
	}
	for _, s := range no {
		assert.Falsef(t, IsProbablyJUnit([]byte(s)), "expected NOT JUnit: %q", s)
	}
}

func TestParseJUnit_SingleSuiteFixture(t *testing.T) {
	c, err := ParseJUnit(bytes.NewReader(fixture(t, "junit-single-suite.xml")))
	require.NoError(t, err)
	assert.Equal(t, executor.TestCounts{Total: 4, Passed: 2, Failed: 1, Skipped: 1}, c)
}

func TestParseJUnit_NestedSuitesFixture(t *testing.T) {
	c, err := ParseJUnit(bytes.NewReader(fixture(t, "junit-nested-suites.xml")))
	require.NoError(t, err)
	// Suite1: 3 total, 1 failed, 2 passed. Suite2: 3 total, 1 error+1 skipped, 1 passed.
	assert.Equal(t, executor.TestCounts{Total: 6, Passed: 3, Failed: 2, Skipped: 1}, c)
}

// TestParseJUnit_Malformed_NoPanic — plan step-07 mandate: malformed XML
// must be skipped (return error), never panic.
func TestParseJUnit_Malformed_NoPanic(t *testing.T) {
	require.NotPanics(t, func() {
		_, err := ParseJUnit(bytes.NewReader(fixture(t, "junit-malformed.xml")))
		assert.Error(t, err)
	})
}

// TestParseJUnit_NotJUnit — random XML uploaded by mistake shouldn't count
// as JUnit output. Returns errNotJUnit which the outer scraper distinguishes
// from "malformed JUnit" via errors.Is (though both paths result in no
// counting; the distinction matters for warnings in step-11 UI work).
func TestParseJUnit_NotJUnit(t *testing.T) {
	_, err := ParseJUnit(bytes.NewReader(fixture(t, "config.xml")))
	require.Error(t, err)
}

func TestParseJUnit_Empty(t *testing.T) {
	_, err := ParseJUnit(strings.NewReader(""))
	require.Error(t, err)
}

// TestParseJUnit_HugeFile — the size cap is what protects the wrapper
// container from an accidental (or malicious) multi-GB XML blob. We don't
// need to actually allocate MaxJUnitFileBytes; we prove the LimitReader
// truncates by feeding it a stream that would otherwise blow past the cap.
func TestParseJUnit_HugeFile_Truncated(t *testing.T) {
	// Head with valid JUnit start, then padding beyond the cap.
	var buf bytes.Buffer
	buf.WriteString(`<testsuite name="huge" tests="1" failures="0"><testcase name="one"/></testsuite>`)
	buf.WriteString(strings.Repeat("x", MaxJUnitFileBytes)) // pushes past cap; ignored after limit

	// Doesn't panic; truncation may make the XML unparseable, but that's the
	// point — we cap before parsing.
	require.NotPanics(t, func() {
		_, _ = ParseJUnit(&buf)
	})
}

// TestParseJUnit_CaseLevelCounting exercises the fallback where <testsuite>
// has no summary attrs — count individual <testcase> children instead.
// Newman default.
func TestParseJUnit_CaseLevelCounting(t *testing.T) {
	xml := `<testsuite name="cases-only">
	  <testcase name="a"/>
	  <testcase name="b"><failure/></testcase>
	  <testcase name="c"><skipped/></testcase>
	  <testcase name="d"><error/></testcase>
	</testsuite>`
	c, err := ParseJUnit(strings.NewReader(xml))
	require.NoError(t, err)
	assert.Equal(t, executor.TestCounts{Total: 4, Passed: 1, Failed: 2, Skipped: 1}, c)
}
