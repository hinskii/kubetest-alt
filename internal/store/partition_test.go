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

package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustUTC(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339Nano, s)
	require.NoError(t, err)
	return ts.UTC()
}

// TestPartitionForTime pins the naming + boundary contract that Postgres DDL
// depends on. Regressions here would silently write to the wrong partition.
func TestPartitionForTime(t *testing.T) {
	cases := []struct {
		name      string
		at        string
		wantName  string
		wantStart string
		wantEnd   string
	}{
		{
			name:      "mid-month",
			at:        "2026-08-18T12:34:56Z",
			wantName:  "test_runs_2026_08",
			wantStart: "2026-08-01T00:00:00Z",
			wantEnd:   "2026-09-01T00:00:00Z",
		},
		{
			name:      "first instant of month",
			at:        "2026-08-01T00:00:00Z",
			wantName:  "test_runs_2026_08",
			wantStart: "2026-08-01T00:00:00Z",
			wantEnd:   "2026-09-01T00:00:00Z",
		},
		{
			name:      "last nanosecond of month → same partition",
			at:        "2026-08-31T23:59:59.999999999Z",
			wantName:  "test_runs_2026_08",
			wantStart: "2026-08-01T00:00:00Z",
			wantEnd:   "2026-09-01T00:00:00Z",
		},
		{
			name:      "december → next January (year rollover)",
			at:        "2026-12-15T10:00:00Z",
			wantName:  "test_runs_2026_12",
			wantStart: "2026-12-01T00:00:00Z",
			wantEnd:   "2027-01-01T00:00:00Z",
		},
		{
			name:      "january boundary respects year",
			at:        "2027-01-01T00:00:00Z",
			wantName:  "test_runs_2027_01",
			wantStart: "2027-01-01T00:00:00Z",
			wantEnd:   "2027-02-01T00:00:00Z",
		},
		{
			name:      "leap february → 29-day month; end still 03-01",
			at:        "2028-02-29T23:00:00Z",
			wantName:  "test_runs_2028_02",
			wantStart: "2028-02-01T00:00:00Z",
			wantEnd:   "2028-03-01T00:00:00Z",
		},
		{
			name:      "input in non-UTC zone is normalized",
			at:        "2026-08-01T01:00:00+02:00", // = 2026-07-31 23:00 UTC
			wantName:  "test_runs_2026_07",
			wantStart: "2026-07-01T00:00:00Z",
			wantEnd:   "2026-08-01T00:00:00Z",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := PartitionForTime(mustUTC(t, c.at))
			assert.Equal(t, c.wantName, got.Name)
			assert.True(t, got.Start.Equal(mustUTC(t, c.wantStart)),
				"start=%s, want=%s", got.Start, c.wantStart)
			assert.True(t, got.End.Equal(mustUTC(t, c.wantEnd)),
				"end=%s, want=%s", got.End, c.wantEnd)
		})
	}
}

// TestPartitionsToCreate covers month-rollover and year-rollover — the
// "operator boots at 23:59 on the last day of the month" case.
func TestPartitionsToCreate(t *testing.T) {
	cases := []struct {
		name      string
		now       string
		retention time.Duration
		lookahead time.Duration
		want      []string
	}{
		{
			name:      "single month when retention 0 and lookahead 0",
			now:       "2026-08-18T12:00:00Z",
			retention: 0,
			lookahead: 0,
			want:      []string{"test_runs_2026_08"},
		},
		{
			name:      "30d retention + 1-month lookahead spans 3 partitions mid-month",
			now:       "2026-08-18T12:00:00Z",
			retention: 30 * 24 * time.Hour,
			lookahead: 30 * 24 * time.Hour,
			// now-30d = 2026-07-19 → July; now+30d = 2026-09-17 → September
			want: []string{"test_runs_2026_07", "test_runs_2026_08", "test_runs_2026_09"},
		},
		{
			name:      "year rollover: dec + lookahead into next year",
			now:       "2026-12-31T23:59:00Z",
			retention: 0,
			lookahead: 24 * time.Hour,
			// now+1d = 2027-01-01 → next partition MUST already exist so a
			// TestRun finishing at 00:00:01 doesn't fail its INSERT
			want: []string{"test_runs_2026_12", "test_runs_2027_01"},
		},
		{
			name:      "negative retention/lookahead clamped to 0",
			now:       "2026-08-18T00:00:00Z",
			retention: -100 * time.Hour,
			lookahead: -1 * time.Hour,
			want:      []string{"test_runs_2026_08"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := PartitionsToCreate(mustUTC(t, c.now), c.retention, c.lookahead)
			var names []string
			for _, p := range got {
				names = append(names, p.Name)
			}
			assert.Equal(t, c.want, names)
		})
	}
}

// TestPartitionsToDrop_Boundaries is the safety test called out in step-09.md:
// a partition holding runs newer than cutoff MUST NEVER be dropped. Two
// scenarios pin the boundary at the row and partition granularities.
func TestPartitionsToDrop_Boundaries(t *testing.T) {
	// Scenario A: cutoff falls in the MIDDLE of July. Both the "row at
	// cutoff-1s" (older) and "row at cutoff+1s" (newer) live in the same
	// July partition, so the WHOLE July partition survives — even though
	// half its rows are technically older than the retention cutoff. That's
	// the safety property.
	t.Run("row-level boundary within same partition preserves partition", func(t *testing.T) {
		now := mustUTC(t, "2026-08-18T12:00:00Z")
		retention := 30 * 24 * time.Hour // cutoff = 2026-07-19 12:00:00 UTC
		existing := []Partition{
			partAt(t, "2026-06-01T00:00:00Z"), // wholly before cutoff → drop
			partAt(t, "2026-07-01T00:00:00Z"), // straddles cutoff → keep
			partAt(t, "2026-08-01T00:00:00Z"), // wholly after cutoff → keep
		}
		got := PartitionsToDrop(existing, now, retention)
		assert.Equal(t, []string{"test_runs_2026_06"}, names(got),
			"only wholly-old partitions drop; straddling one stays")
	})

	// Scenario B: cutoff falls EXACTLY on a partition boundary — the July
	// partition ends AT cutoff, so all its rows are strictly older than
	// cutoff. July drops. August (whose start == cutoff) survives.
	//
	// This is the case the plan describes as "run at cutoff-1s dies, run
	// at cutoff+1s lives" (in the sense: age > retention → die, age <
	// retention → live). A row at 2026-07-31T23:59:59Z (cutoff-1s) is in
	// July → dropped with the partition; a row at 2026-08-01T00:00:01Z
	// (cutoff+1s) is in August → survives.
	t.Run("boundary-aligned cutoff drops older partition, keeps newer", func(t *testing.T) {
		now := mustUTC(t, "2026-08-31T00:00:00Z")
		retention := 30 * 24 * time.Hour // cutoff = 2026-08-01 00:00:00 UTC exactly
		existing := []Partition{
			partAt(t, "2026-07-01T00:00:00Z"), // End=2026-08-01 == cutoff → drop
			partAt(t, "2026-08-01T00:00:00Z"), // Start=2026-08-01 == cutoff → keep
		}
		got := PartitionsToDrop(existing, now, retention)
		assert.Equal(t, []string{"test_runs_2026_07"}, names(got))
	})

	// Scenario C: retention 0 → nothing dropped except partitions that
	// ended before now. Sanity check.
	t.Run("zero retention keeps only future/current partitions", func(t *testing.T) {
		now := mustUTC(t, "2026-08-18T00:00:00Z")
		existing := []Partition{
			partAt(t, "2026-07-01T00:00:00Z"), // End=2026-08-01 < now → drop
			partAt(t, "2026-08-01T00:00:00Z"), // End=2026-09-01 > now → keep
		}
		got := PartitionsToDrop(existing, now, 0)
		assert.Equal(t, []string{"test_runs_2026_07"}, names(got))
	})

	// Scenario D: empty existing → empty output, no error, no panic.
	t.Run("empty existing → empty output", func(t *testing.T) {
		got := PartitionsToDrop(nil, mustUTC(t, "2026-08-18T00:00:00Z"), 24*time.Hour)
		assert.Empty(t, got)
	})
}

// partAt is a test helper — takes a month-start string, returns the full
// Partition covering that month.
func partAt(t *testing.T, monthStart string) Partition {
	t.Helper()
	return PartitionForTime(mustUTC(t, monthStart))
}

func names(ps []Partition) []string {
	if ps == nil {
		return nil
	}
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}
