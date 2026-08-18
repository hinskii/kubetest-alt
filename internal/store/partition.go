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
	"fmt"
	"time"
)

// Partition names a monthly test_runs partition. All times UTC — DST doesn't
// exist here, and Postgres partitions are compared byte-wise on timestamptz
// (which is stored UTC internally regardless of client session tz).
//
// A partition covers [Start, End) — i.e. Start ≤ finished_at < End. That
// half-open convention matches Postgres RANGE partitions ("FOR VALUES FROM
// (a) TO (b)" = [a, b)) and makes month boundaries unambiguous.
type Partition struct {
	// Name is the child table name, e.g. "test_runs_2026_08". Stable across
	// runs so subsequent CreateIfNotExists calls are idempotent.
	Name string

	// Start is the inclusive lower bound (00:00:00 UTC on the 1st of the
	// month).
	Start time.Time

	// End is the exclusive upper bound (00:00:00 UTC on the 1st of the
	// following month — handles Dec → Jan year rollover correctly).
	End time.Time
}

// PartitionForTime returns the Partition that would hold a row with the
// given finished_at timestamp. Pure function — no DB call.
func PartitionForTime(t time.Time) Partition {
	t = t.UTC()
	start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	return Partition{
		Name:  partitionName(start),
		Start: start,
		End:   end,
	}
}

// partitionName encodes month as "test_runs_YYYY_MM" — zero-padded month so
// lex order matches chronological order.
func partitionName(monthStart time.Time) string {
	return fmt.Sprintf("test_runs_%04d_%02d", monthStart.Year(), int(monthStart.Month()))
}

// PartitionsToCreate returns the partitions that MUST exist to accept writes
// for the window [now - retention, now + lookahead]. Lookahead ≥ 1 month
// covers month rollover; a controller booting on the 31st needs the next
// month's partition before midnight so writes at 00:00:01 don't error.
//
// Callers issue CREATE TABLE IF NOT EXISTS <Name> PARTITION OF test_runs
// FOR VALUES FROM (Start) TO (End) — idempotent, safe to run at every
// migration cycle.
//
// The returned slice is sorted oldest→newest, deterministic across calls.
func PartitionsToCreate(now time.Time, retention time.Duration, lookahead time.Duration) []Partition {
	now = now.UTC()
	if retention < 0 {
		retention = 0
	}
	if lookahead < 0 {
		lookahead = 0
	}

	firstMonth := PartitionForTime(now.Add(-retention)).Start
	lastMonth := PartitionForTime(now.Add(lookahead)).Start

	var out []Partition
	for month := firstMonth; !month.After(lastMonth); month = month.AddDate(0, 1, 0) {
		out = append(out, Partition{
			Name:  partitionName(month),
			Start: month,
			End:   month.AddDate(0, 1, 0),
		})
	}
	return out
}

// PartitionsToDrop returns partitions strictly older than the retention cutoff
// — i.e. partitions whose ENTIRE range lies before (now - retention). A
// partition with even one row newer than cutoff is preserved: the "safety"
// property mentioned in §9.
//
// Concretely: drop iff End ≤ cutoff. This is why the boundary tests focus
// on End vs cutoff: a partition ending exactly AT cutoff can be dropped
// (its rows all have finished_at < cutoff), one ending 1ns after cutoff
// still holds a keeper.
//
// Callers list existing partitions from pg_inherits + the child schema,
// then intersect with this result — this function only computes the math,
// it doesn't touch the DB.
func PartitionsToDrop(existing []Partition, now time.Time, retention time.Duration) []Partition {
	cutoff := now.UTC().Add(-retention)
	var out []Partition
	for _, p := range existing {
		if !p.End.After(cutoff) {
			out = append(out, p)
		}
	}
	return out
}
