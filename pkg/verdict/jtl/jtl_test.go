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

// JTL fixtures come from the real alpine/jmeter:5.6.3 run captured
// during the (cancelled) old step-11. The header shape is what JMeter
// actually emits — we lock ourselves to that so a version bump surfaces
// as a broken fixture, not silent misparse.
package jtl

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_PassingFixture(t *testing.T) {
	a, err := Load(filepath.Join("testdata", "passing.jtl"))
	require.NoError(t, err)
	assert.Equal(t, int64(3), a.SamplesTotal)
	assert.Equal(t, int64(0), a.SamplesFailed)
	assert.Equal(t, 0.0, a.ErrorRate())
	assert.Greater(t, a.ElapsedAvgMs, 0.0)
}

func TestLoad_AllFailedFixture(t *testing.T) {
	a, err := Load(filepath.Join("testdata", "all_failed.jtl"))
	require.NoError(t, err)
	assert.Equal(t, int64(3), a.SamplesTotal)
	assert.Equal(t, int64(3), a.SamplesFailed)
	assert.Equal(t, 1.0, a.ErrorRate())
}

func TestLoad_OneOfThree_BoundaryFixture(t *testing.T) {
	a, err := Load(filepath.Join("testdata", "one_of_three.jtl"))
	require.NoError(t, err)
	assert.Equal(t, int64(3), a.SamplesTotal)
	assert.Equal(t, int64(1), a.SamplesFailed)
	assert.InDelta(t, 1.0/3.0, a.ErrorRate(), 1e-9)
}

// Header-driven column resolution: reordering columns MUST NOT change the
// verdict. The plan calls this out specifically.
func TestParse_ReorderedColumns(t *testing.T) {
	// success moved from column 8 to column 1; elapsed moved to column 3.
	input := "success,label,elapsed\ntrue,GET /,50\nfalse,GET /,60\n"
	a, err := Parse(strings.NewReader(input))
	require.NoError(t, err)
	assert.Equal(t, int64(2), a.SamplesTotal)
	assert.Equal(t, int64(1), a.SamplesFailed)
	assert.InDelta(t, 55.0, a.ElapsedAvgMs, 1e-9)
}

func TestParse_NoHeaderRejected(t *testing.T) {
	// A file without a proper CSV header (just data rows) still lets
	// csv.Reader parse the first row AS a header — but the "header"
	// contains no `success` column, so we reject cleanly with the
	// sentinel error.
	input := "1,2,3\n4,5,6\n"
	_, err := Parse(strings.NewReader(input))
	require.ErrorIs(t, err, ErrNoSuccessColumn)
}

func TestParse_EmptyFileRejected(t *testing.T) {
	_, err := Parse(strings.NewReader(""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty file")
}

func TestParse_HeaderOnlyOk_ZeroSamples(t *testing.T) {
	// Header present, no rows — legitimate JMeter output for an empty
	// plan. Not an error; verdict layer treats "0 failed of 0" as passed.
	input := "success,elapsed\n"
	a, err := Parse(strings.NewReader(input))
	require.NoError(t, err)
	assert.Equal(t, int64(0), a.SamplesTotal)
	assert.Equal(t, 0.0, a.ErrorRate())
}

func TestLoad_MissingFile_ReturnsSentinel(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.jtl"))
	require.ErrorIs(t, err, ErrNotFound)
}

func TestParse_RaggedRowsTolerated(t *testing.T) {
	// jmeter can emit partial rows on crash. A row that stops BEFORE the
	// success column is silently skipped rather than parsed as false.
	// Header order below puts success at column 3 (0-indexed); the second
	// data row has only 2 fields so successCol >= len(row) → skip.
	input := "label,elapsed,timeStamp,success\n" +
		"GET /,50,1,true\n" +
		"GET /,60\n" // truncated: no timeStamp, no success
	a, err := Parse(strings.NewReader(input))
	require.NoError(t, err)
	assert.Equal(t, int64(1), a.SamplesTotal, "truncated row skipped, only good one counts")
	assert.Equal(t, int64(0), a.SamplesFailed)
}
