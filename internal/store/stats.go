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
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// MaxRecentRuns caps how many newest runs TestStats returns per Test.
const MaxRecentRuns = 30

// StatsFilter picks the runs TestStats looks at.
type StatsFilter struct {
	// Namespace limits to one namespace; "" is all.
	Namespace string
	// Since starts the window the counts and percentiles are over.
	Since time.Time
	// RecentSince bounds how far back the newest runs are looked for (it
	// keeps the scan to recent partitions); zero is Since.
	RecentSince time.Time
	// Recent is how many of a Test's newest runs to return (1..MaxRecentRuns).
	Recent int
}

// TestStats sums one Test's finished runs over a window, with its newest
// runs. Counted like Control Center's summaries: the pass rate is passed
// of the runs with a verdict (passed, failed, error); durations are of
// passed and failed runs only (an error or abort says nothing about the
// system under test).
type TestStats struct {
	Namespace, TestRef                      string
	Total, Passed, Failed, Errored, Aborted int
	P50Ms, P95Ms                            int64
	// Recent are the newest runs, newest first (also outside the window,
	// back to RecentSince).
	Recent []RecentRun
}

// RecentRun is one of a Test's newest runs.
type RecentRun struct {
	UID        string    `json:"uid"`
	Name       string    `json:"name"`
	Phase      string    `json:"phase"`
	DurationMs int64     `json:"durationMs"`
	FinishedAt time.Time `json:"finishedAt"`
}

// TestStats returns per-Test stats (one query): counts and p50/p95 over
// the runs finished since f.Since, and each Test's f.Recent newest runs.
// Tests without a run in the window but with recent runs are included
// with zero counts.
func (p *Postgres) TestStats(ctx context.Context, f StatsFilter) ([]TestStats, error) {
	if f.Recent <= 0 || f.Recent > MaxRecentRuns {
		f.Recent = MaxRecentRuns
	}
	if f.RecentSince.IsZero() || f.RecentSince.After(f.Since) {
		f.RecentSince = f.Since
	}
	const q = `
WITH w AS (
    SELECT uid, name, namespace, test_ref, phase, coalesce(duration_ms, 0) AS ms, finished_at,
           finished_at >= $1 AS in_window,
           row_number() OVER (PARTITION BY namespace, test_ref ORDER BY finished_at DESC, uid) AS rn
    FROM test_runs
    WHERE finished_at >= $2 AND ($3 = '' OR namespace = $3)
)
SELECT namespace, test_ref,
       count(*) FILTER (WHERE in_window),
       count(*) FILTER (WHERE in_window AND phase = 'passed'),
       count(*) FILTER (WHERE in_window AND phase = 'failed'),
       count(*) FILTER (WHERE in_window AND phase = 'error'),
       count(*) FILTER (WHERE in_window AND phase = 'aborted'),
       coalesce(percentile_disc(0.5) WITHIN GROUP (ORDER BY ms)
                FILTER (WHERE in_window AND phase IN ('passed','failed') AND ms > 0), 0),
       coalesce(percentile_disc(0.95) WITHIN GROUP (ORDER BY ms)
                FILTER (WHERE in_window AND phase IN ('passed','failed') AND ms > 0), 0),
       coalesce(jsonb_agg(jsonb_build_object('uid', uid::text, 'name', name, 'phase', phase,
                                             'durationMs', ms, 'finishedAt', finished_at)
                          ORDER BY finished_at DESC, uid) FILTER (WHERE rn <= $4), '[]')
FROM w
GROUP BY namespace, test_ref
ORDER BY namespace, test_ref`
	rows, err := p.pool.Query(ctx, q, f.Since.UTC(), f.RecentSince.UTC(), f.Namespace, f.Recent)
	if err != nil {
		return nil, fmt.Errorf("store: test stats: %w", err)
	}
	defer rows.Close()
	out := []TestStats{}
	for rows.Next() {
		var s TestStats
		var recent []byte
		if err := rows.Scan(&s.Namespace, &s.TestRef, &s.Total, &s.Passed, &s.Failed, &s.Errored, &s.Aborted,
			&s.P50Ms, &s.P95Ms, &recent); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(recent, &s.Recent); err != nil {
			return nil, fmt.Errorf("store: test stats recent runs: %w", err)
		}
		for i := range s.Recent {
			s.Recent[i].FinishedAt = s.Recent[i].FinishedAt.UTC()
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
