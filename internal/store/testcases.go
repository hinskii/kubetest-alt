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
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/executor"
)

// CaseRow is a stored test case of a finished run.
type CaseRow struct {
	executor.TestCase
	Key        string
	RunUID     string
	FinishedAt time.Time
}

// CaseStats aggregates one test case over a Test's recent runs.
type CaseStats struct {
	Key, Suite, Class, Name string
	// Runs is how many of the window's runs had this case.
	Runs, Passed, Failed, Skipped int
	AvgMs, MaxMs                  int64
	LastStatus                    string
	LastAt                        time.Time
	// Flips counts status changes between consecutive runs (passed ↔ not
	// passed, skips ignored): a case that keeps flipping is flaky.
	Flips int
}

// Flaky reports a case that both passed and failed within the window.
func (c CaseStats) Flaky() bool { return c.Passed > 0 && c.Failed > 0 }

// CaseRun is one run's result for one test case.
type CaseRun struct {
	RunUID, RunName string
	FinishedAt      time.Time
	Status          string
	DurationMs      int64
	Message         string
}

// MaxCaseWindow caps how many recent runs CaseStats looks at.
const MaxCaseWindow = 200

// SaveTestCases replaces the stored cases of a finished run (idempotent:
// a retried persist writes the same set). The run's partition is created
// by SaveFinished, which must succeed first.
func (p *Postgres) SaveTestCases(ctx context.Context, run *testsv1alpha1.TestRun, cases []executor.TestCase) error {
	if run == nil || run.UID == "" || run.Status.FinishedAt == nil {
		return errors.New("store: SaveTestCases needs a finished run with a UID")
	}
	finished := run.Status.FinishedAt.UTC()
	if err := p.ensurePartition(ctx, PartitionForTime(finished)); err != nil {
		return fmt.Errorf("store: ensure partition: %w", err)
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM test_cases WHERE run_uid = $1`, string(run.UID)); err != nil {
		return fmt.Errorf("store: clear cases %s: %w", run.UID, err)
	}
	rows := make([][]any, 0, len(cases))
	for i, c := range cases {
		rows = append(rows, []any{
			string(run.UID), finished, i, run.Namespace, run.Spec.TestRef, c.CaseKey(),
			nullIfEmpty(c.Suite), nullIfEmpty(c.Class), c.Name, c.Status, c.DurationMs,
			nullIfEmpty(c.Message), nullIfEmpty(c.Details), nullIfEmpty(c.File),
		})
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"test_cases"},
		[]string{"run_uid", "finished_at", "idx", "namespace", "test_ref", "case_key",
			"suite", "class", "name", "status", "duration_ms", "message", "details", "file"},
		pgx.CopyFromRows(rows))
	if err != nil {
		return fmt.Errorf("store: save cases %s: %w", run.UID, err)
	}
	return tx.Commit(ctx)
}

// RunCases returns a finished run's cases in report order; failedOnly
// keeps failed and errored ones.
func (p *Postgres) RunCases(ctx context.Context, runUID string, failedOnly bool) ([]CaseRow, error) {
	if _, err := uuid.Parse(runUID); err != nil {
		return []CaseRow{}, nil
	}
	q := `SELECT run_uid::text, finished_at, case_key, coalesce(suite,''), coalesce(class,''), name, status,
	             coalesce(duration_ms,0), coalesce(message,''), coalesce(details,''), coalesce(file,'')
	      FROM test_cases WHERE run_uid = $1`
	if failedOnly {
		q += ` AND status IN ('failed','error')`
	}
	rows, err := p.pool.Query(ctx, q+` ORDER BY idx`, runUID)
	if err != nil {
		return nil, fmt.Errorf("store: run cases: %w", err)
	}
	defer rows.Close()
	out := []CaseRow{}
	for rows.Next() {
		var c CaseRow
		if err := rows.Scan(&c.RunUID, &c.FinishedAt, &c.Key, &c.Suite, &c.Class, &c.Name, &c.Status,
			&c.DurationMs, &c.Message, &c.Details, &c.File); err != nil {
			return nil, err
		}
		c.FinishedAt = c.FinishedAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// CaseStats aggregates every case of a Test over its last `window` runs
// that reported cases, failing and flaky cases first.
func (p *Postgres) CaseStats(ctx context.Context, namespace, testRef string, window int) ([]CaseStats, error) {
	if window <= 0 || window > MaxCaseWindow {
		window = MaxCaseWindow
	}
	const q = `
WITH recent AS (
    SELECT run_uid, max(finished_at) AS finished_at
    FROM test_cases WHERE namespace = $1 AND test_ref = $2
    GROUP BY run_uid ORDER BY 2 DESC LIMIT $3
), cases AS (
    SELECT c.*, lag(c.status) OVER (PARTITION BY c.case_key ORDER BY c.finished_at) AS prev
    FROM test_cases c JOIN recent r ON r.run_uid = c.run_uid AND r.finished_at = c.finished_at
    WHERE c.namespace = $1 AND c.test_ref = $2
)
SELECT case_key, coalesce(min(suite),''), coalesce(min(class),''), min(name),
       count(*),
       count(*) FILTER (WHERE status = 'passed'),
       count(*) FILTER (WHERE status IN ('failed','error')),
       count(*) FILTER (WHERE status = 'skipped'),
       coalesce(avg(duration_ms) FILTER (WHERE status <> 'skipped'), 0)::bigint,
       coalesce(max(duration_ms), 0),
       (array_agg(status ORDER BY finished_at DESC))[1],
       max(finished_at),
       count(*) FILTER (WHERE prev IS NOT NULL AND prev <> 'skipped' AND status <> 'skipped'
                        AND (prev = 'passed') <> (status = 'passed'))
FROM cases GROUP BY case_key
ORDER BY count(*) FILTER (WHERE status IN ('failed','error')) DESC, case_key`
	rows, err := p.pool.Query(ctx, q, namespace, testRef, window)
	if err != nil {
		return nil, fmt.Errorf("store: case stats: %w", err)
	}
	defer rows.Close()
	out := []CaseStats{}
	for rows.Next() {
		var c CaseStats
		if err := rows.Scan(&c.Key, &c.Suite, &c.Class, &c.Name, &c.Runs, &c.Passed, &c.Failed, &c.Skipped,
			&c.AvgMs, &c.MaxMs, &c.LastStatus, &c.LastAt, &c.Flips); err != nil {
			return nil, err
		}
		c.LastAt = c.LastAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// CaseHistory lists one case's results over a Test's runs, newest first.
func (p *Postgres) CaseHistory(ctx context.Context, namespace, testRef, caseKey string, limit int) ([]CaseRun, error) {
	if limit <= 0 || limit > MaxCaseWindow {
		limit = MaxCaseWindow
	}
	rows, err := p.pool.Query(ctx, `
SELECT c.run_uid::text, coalesce(r.name, ''), c.finished_at, c.status, coalesce(c.duration_ms,0), coalesce(c.message,'')
FROM test_cases c
LEFT JOIN test_runs r ON r.uid = c.run_uid AND r.finished_at = c.finished_at
WHERE c.namespace = $1 AND c.test_ref = $2 AND c.case_key = $3
ORDER BY c.finished_at DESC, c.run_uid LIMIT $4`, namespace, testRef, caseKey, limit)
	if err != nil {
		return nil, fmt.Errorf("store: case history: %w", err)
	}
	defer rows.Close()
	out := []CaseRun{}
	for rows.Next() {
		var c CaseRun
		if err := rows.Scan(&c.RunUID, &c.RunName, &c.FinishedAt, &c.Status, &c.DurationMs, &c.Message); err != nil {
			return nil, err
		}
		c.FinishedAt = c.FinishedAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}
