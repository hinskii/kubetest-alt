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

package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fixtures are real tool output (hack/report-fixtures.sh): each tool hit a
// target answering "/" with 200 and "/missing" with 404, so every report
// has ~50% failed requests. Expectations below are the values in those
// files — if a tool bump changes them, regenerate and re-read.
func TestParse_RealToolFixtures(t *testing.T) {
	cases := []struct {
		from, file string
		want       map[string]float64 // exact
		present    []string           // must exist (values float-noisy)
	}{
		{
			from: FromK6Summary, file: "testdata/summary.json",
			want: map[string]float64{
				Requests: 40, Errors: 20, ErrorRate: 0.5,
				ChecksPassed: 20, ChecksFailed: 20, Iterations: 20,
				DataReceivedBytes: 14180, DataSentBytes: 3220,
			},
			present: []string{RPS, LatencyAvgMs, LatencyMinMs, LatencyMedMs, LatencyP90Ms, LatencyP95Ms, LatencyMaxMs},
		},
		{
			from: FromJTL, file: "testdata/jmeter.jtl",
			want:    map[string]float64{Requests: 20, Errors: 10, ErrorRate: 0.5, LatencyMaxMs: 17},
			present: []string{RPS, LatencyAvgMs, LatencyP90Ms, LatencyP95Ms, LatencyP99Ms},
		},
		{
			from: FromLocustCSV, file: "testdata/locust_stats.csv",
			want: map[string]float64{
				Requests: 74, Errors: 35,
				LatencyMedMs: 5, LatencyP90Ms: 6, LatencyP95Ms: 7, LatencyP99Ms: 8,
			},
			present: []string{ErrorRate, RPS, LatencyAvgMs, LatencyMinMs, LatencyMaxMs},
		},
		{
			from: FromGatlingStats, file: "testdata/stats.json",
			want: map[string]float64{
				Requests: 20, Errors: 10, ErrorRate: 0.5, RPS: 20,
				LatencyMinMs: 1, LatencyMedMs: 1, LatencyP95Ms: 4, LatencyP99Ms: 4, LatencyMaxMs: 4,
			},
		},
		{
			from: FromArtilleryJSON, file: "testdata/report.json",
			// 9 × 404 count as errors even though Artillery's errors.* is empty.
			want: map[string]float64{
				Requests: 18, Errors: 9, ErrorRate: 0.5, RPS: 9,
				LatencyP95Ms: 2, LatencyP99Ms: 2, LatencyMaxMs: 4,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.from, func(t *testing.T) {
			f, err := os.Open(c.file)
			require.NoError(t, err)
			defer func() { _ = f.Close() }()
			got, err := Parse(c.from, f)
			require.NoError(t, err)
			for k, v := range c.want {
				assert.InDelta(t, v, got[k], 1e-9, "%s", k)
			}
			for _, k := range c.present {
				assert.Contains(t, got, k)
			}
			for k, v := range got {
				assert.GreaterOrEqual(t, v, 0.0, "%s must not be negative", k)
			}
			if r, ok := got[ErrorRate]; ok {
				assert.LessOrEqual(t, r, 1.0)
			}
		})
	}
}

// A report that's garbage, truncated, or the wrong format is an error —
// never a silently empty or zeroed metric set.
func TestParse_RejectsBadInput(t *testing.T) {
	cases := map[string][]string{
		FromK6Summary:     {"", "{", `{"metrics":{}}`, "not json"},
		FromJTL:           {"", "a,b,c\n1,2,3\n", "timeStamp,elapsed,success\n"},
		FromLocustCSV:     {"", "Type,Name\nGET,/\n", "x,y\n"},
		FromGatlingStats:  {"", "{", `{"stats":{}}`},
		FromArtilleryJSON: {"", "{", `{"intermediate":[]}`},
	}
	for from, inputs := range cases {
		for _, in := range inputs {
			_, err := Parse(from, strings.NewReader(in))
			assert.Error(t, err, "%s should reject %q", from, in)
		}
	}
	_, err := Parse("nope", strings.NewReader("{}"))
	assert.ErrorContains(t, err, "unknown report format")
}

func TestParse_JTLSkipsTruncatedLastRow(t *testing.T) {
	in := "timeStamp,elapsed,label,success\n1000,10,a,true\n1010,30,a,false\n1020"
	got, err := Parse(FromJTL, strings.NewReader(in))
	require.NoError(t, err)
	assert.Equal(t, 2.0, got[Requests])
	assert.Equal(t, 1.0, got[Errors])
	assert.Equal(t, 20.0, got[LatencyAvgMs])
	assert.InDelta(t, 2/0.04, got[RPS], 1e-9, "2 samples over 1000..1040 ms")
}

func TestParse_LocustNAPercentilesOmitted(t *testing.T) {
	in := "Type,Name,Request Count,Failure Count,90%,99%\n,Aggregated,0,0,N/A,N/A\n"
	got, err := Parse(FromLocustCSV, strings.NewReader(in))
	require.NoError(t, err)
	assert.NotContains(t, got, LatencyP90Ms)
	assert.NotContains(t, got, ErrorRate, "no error rate without requests")
}

func TestParseFile_GlobPicksNewestMatch(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "results", "sim-1000", "js")
	newer := filepath.Join(dir, "results", "sim-2000", "js")
	require.NoError(t, os.MkdirAll(old, 0o750))
	require.NoError(t, os.MkdirAll(newer, 0o750))
	stats := func(total string) []byte {
		return []byte(`{"stats":{"numberOfRequests":{"total":` + total + `,"ko":0}}}`)
	}
	require.NoError(t, os.WriteFile(filepath.Join(old, "stats.json"), stats("1"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(newer, "stats.json"), stats("2"), 0o600))

	got, rel, err := ParseFile(FromGatlingStats, dir, "results/**/js/stats.json")
	require.NoError(t, err)
	assert.Equal(t, "results/sim-2000/js/stats.json", rel)
	assert.Equal(t, 2.0, got[Requests])
}

func TestParseFile_Errors(t *testing.T) {
	dir := t.TempDir()
	_, _, err := ParseFile(FromK6Summary, dir, "results/summary.json")
	assert.ErrorIs(t, err, ErrNoReport)
	_, _, err = ParseFile(FromK6Summary, dir, "/etc/passwd")
	assert.ErrorContains(t, err, "invalid report path")
	_, _, err = ParseFile(FromK6Summary, dir, "results/[")
	assert.ErrorContains(t, err, "invalid report path")
}

// os.DirFS must keep a glob from escaping the working directory.
func TestParseFile_CannotEscapeWorkingDir(t *testing.T) {
	parent := t.TempDir()
	secret := []byte(`{"metrics":{"http_reqs":{"count":1}}}`)
	require.NoError(t, os.WriteFile(filepath.Join(parent, "secret.json"), secret, 0o600))
	work := filepath.Join(parent, "work")
	require.NoError(t, os.Mkdir(work, 0o750))
	_, _, err := ParseFile(FromK6Summary, work, "../secret.json")
	assert.Error(t, err)
}
