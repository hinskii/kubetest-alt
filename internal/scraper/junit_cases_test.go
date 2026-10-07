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
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hinskii/kubetest-alt/pkg/executor"
)

// junit-pytest.xml is real pytest 9.1.1 output (the catalog's pytest case
// plus a skipped test and a fixture error).
func TestParseJUnitReport_PytestCases(t *testing.T) {
	counts, cases, err := ParseJUnitReportFile("testdata/junit-pytest.xml")
	require.NoError(t, err)
	assert.Equal(t, executor.TestCounts{Total: 4, Passed: 1, Failed: 2, Skipped: 1}, counts)
	require.Len(t, cases, 4)

	byName := map[string]executor.TestCase{}
	for _, c := range cases {
		assert.Equal(t, "pytest", c.Suite)
		assert.Equal(t, "test_sample", c.Class)
		byName[c.Name] = c
	}
	assert.Equal(t, executor.CasePassed, byName["test_passes"].Status)
	assert.Empty(t, byName["test_passes"].Message)

	failed := byName["test_fails_on_purpose"]
	assert.Equal(t, executor.CaseFailed, failed.Status)
	assert.Contains(t, failed.Message, "AssertionError: deliberate failure")
	assert.Contains(t, failed.Details, "test_sample.py:6: AssertionError", "the stack excerpt")
	assert.Equal(t, "test_sample › test_fails_on_purpose", failed.CaseKey())

	assert.Equal(t, executor.CaseSkipped, byName["test_skipped_on_purpose"].Status)
	assert.Equal(t, "not on this platform", byName["test_skipped_on_purpose"].Message)
	assert.Equal(t, executor.CaseError, byName["test_errors_in_fixture"].Status)
	assert.Contains(t, byName["test_errors_in_fixture"].Message, "fixture 'missing_fixture' not found")
}

func TestParseJUnitReport_NestedSuitesAndDurations(t *testing.T) {
	_, cases, err := ParseJUnitReportFile("testdata/junit-nested-suites.xml")
	require.NoError(t, err)
	require.Len(t, cases, 6)
	assert.Equal(t, "Suite1", cases[2].Suite)
	assert.Equal(t, "boom", cases[2].Message)
	assert.Equal(t, "Suite2 › b2", cases[4].CaseKey(), "no classname: the suite groups it")
	assert.Equal(t, executor.CaseError, cases[4].Status)

	_, cases, err = ParseJUnitReportFile("testdata/junit-single-suite.xml")
	require.NoError(t, err)
	assert.Equal(t, int64(500), cases[3].DurationMs)
	assert.Equal(t, "stack trace here", cases[3].Details)
}

func TestParseJUnitReport_TrimsHugeFailureText(t *testing.T) {
	xml := `<testsuite name="s"><testcase name="t" time="1,234.5"><failure message="` +
		strings.Repeat("ż", 2000) + `">` + strings.Repeat("x", 10000) + `</failure></testcase></testsuite>`
	_, cases, err := ParseJUnitReport(strings.NewReader(xml))
	require.NoError(t, err)
	require.Len(t, cases, 1)
	assert.LessOrEqual(t, len(cases[0].Message), executor.MaxCaseMessageLen)
	assert.True(t, utf8.ValidString(cases[0].Message))
	assert.LessOrEqual(t, len(cases[0].Details), executor.MaxCaseDetailsLen)
	assert.Equal(t, int64(1234500), cases[0].DurationMs, "thousands separators are tolerated")
	assert.Equal(t, "s › t", cases[0].CaseKey())
	assert.Equal(t, "t", executor.TestCase{Name: "t"}.CaseKey(), "neither class nor suite: the name alone")
}
