//go:build integration

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

// Integration tests for internal/store. Requires Docker on the host —
// testcontainers-go spins up a real Postgres per test package. Guarded by
// the `integration` build tag so `make test` stays Docker-free (§step-09
// acceptance).
//
// Run with: make test-integration
package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	testcontainers "github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// pgHarness owns the shared Postgres container + pool for the whole
// package. Migrations are applied once; each test uses a fresh partition
// and asserts on its own UIDs so parallel-safe under `go test -p 1`.
type pgHarness struct {
	container *tcpostgres.PostgresContainer
	dsn       string
	pool      *pgxpool.Pool
}

var harness *pgHarness

func TestMain(m *testing.M) {
	ctx := context.Background()
	c, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("kubetest"),
		tcpostgres.WithUsername("kubetest"),
		tcpostgres.WithPassword("kubetest"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start postgres container:", err)
		os.Exit(1)
	}
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintln(os.Stderr, "connection string:", err)
		_ = testcontainers.TerminateContainer(c)
		os.Exit(1)
	}
	if err := ApplyMigrations(ctx, dsn); err != nil {
		fmt.Fprintln(os.Stderr, "migrations:", err)
		_ = testcontainers.TerminateContainer(c)
		os.Exit(1)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pool:", err)
		_ = testcontainers.TerminateContainer(c)
		os.Exit(1)
	}
	harness = &pgHarness{container: c, dsn: dsn, pool: pool}
	code := m.Run()
	pool.Close()
	_ = testcontainers.TerminateContainer(c)
	os.Exit(code)
}

// Helper: fully-populated terminal run at a given finish time. UID is
// per-test-supplied to keep rows isolated across parallel tests.
func newRun(uid string, testRef string, phase testsv1alpha1.Phase, finishedAt time.Time) *testsv1alpha1.TestRun {
	q := metav1.NewTime(finishedAt.Add(-30 * time.Second))
	s := metav1.NewTime(finishedAt.Add(-25 * time.Second))
	f := metav1.NewTime(finishedAt)
	return &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-" + uid[:8],
			Namespace: "int-test",
			UID:       types.UID(uid),
		},
		Spec: testsv1alpha1.TestRunSpec{
			TestRef: testRef,
			Source:  "cli",
			Tags:    map[string]string{"team": "sre"},
		},
		Status: testsv1alpha1.TestRunStatus{
			Phase:      phase,
			QueuedAt:   &q,
			StartedAt:  &s,
			FinishedAt: &f,
			DurationMs: 25_000,
			Metrics: map[string]string{
				"p95_ms": "123.4",
				"bad":    "not-a-number", // exercises skip-with-warn path
			},
			ResolvedSpec: `{"type":"k6"}`,
		},
	}
}

// TestIntegration_MigrationsPartitionsAndSave covers three plan
// requirements at once:
//   - Migrations apply cleanly from zero (already done in TestMain).
//   - Partitioned insert lands in the correct partition (assert via
//     tableoid::regclass — the child table backing the row).
//   - Metric parse failure DOES NOT fail the save (row lands, "bad" is
//     silently dropped from Metrics; parseable "p95_ms" survives).
func TestIntegration_MigrationsPartitionsAndSave(t *testing.T) {
	ctx := t.Context()
	// pre-create the partition holding our fixed test time.
	finished := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	p := NewPostgres(harness.pool)
	require.NoError(t, p.EnsurePartitions(ctx, PartitionsToCreate(finished, 0, 0)))

	run := newRun("aaaaaaaa-0000-0000-0000-000000000001", "sample", testsv1alpha1.PhasePassed, finished)
	require.NoError(t, p.SaveFinished(ctx, run))

	// Assert routing: the row lives in test_runs_2026_08.
	var tableName string
	err := harness.pool.QueryRow(ctx,
		`SELECT tableoid::regclass::text FROM test_runs WHERE uid = $1`,
		string(run.UID)).Scan(&tableName)
	require.NoError(t, err)
	assert.Equal(t, "test_runs_2026_08", tableName)

	got, err := p.Get(ctx, string(run.UID))
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{"p95_ms": 123.4}, got.Metrics,
		"unparseable metric was silently dropped, parseable survived")
	assert.Equal(t, "sample", got.TestRef)
	assert.Equal(t, "passed", got.Phase)
	assert.True(t, got.FinishedAt.Equal(finished))
}

// TestIntegration_IdempotentUpsertByUID covers:
//   - Same UID + same FinishedAt → single row, last-write-wins on message.
//   - Second save is idempotent (no duplicate, no error).
func TestIntegration_IdempotentUpsertByUID(t *testing.T) {
	ctx := t.Context()
	finished := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	p := NewPostgres(harness.pool)
	require.NoError(t, p.EnsurePartitions(ctx, PartitionsToCreate(finished, 0, 0)))

	uid := "aaaaaaaa-0000-0000-0000-000000000002"
	run := newRun(uid, "sample", testsv1alpha1.PhasePassed, finished)
	run.Status.Message = "first save"
	require.NoError(t, p.SaveFinished(ctx, run))

	run.Status.Message = "second save (last-write-wins)"
	require.NoError(t, p.SaveFinished(ctx, run))

	// Count rows for this UID — must be 1.
	var count int
	require.NoError(t, harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM test_runs WHERE uid = $1`, uid).Scan(&count))
	assert.Equal(t, 1, count, "upsert must not create duplicates")

	got, err := p.Get(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, "second save (last-write-wins)", got.Message)
}

// TestIntegration_ListFiltersAndKeysetPagination covers:
//   - Filters by test_ref, namespace, phase, time range.
//   - Keyset pagination is stable — page 2 doesn't repeat page 1 items
//     even when a new row is inserted between the two calls.
func TestIntegration_ListFiltersAndKeysetPagination(t *testing.T) {
	ctx := t.Context()
	base := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	p := NewPostgres(harness.pool)
	require.NoError(t, p.EnsurePartitions(ctx, PartitionsToCreate(base, 0, 0)))

	// Seed 5 runs of same test, alternating pass/fail, 10s apart.
	uids := []string{
		"bbbbbbbb-0000-0000-0000-000000000001",
		"bbbbbbbb-0000-0000-0000-000000000002",
		"bbbbbbbb-0000-0000-0000-000000000003",
		"bbbbbbbb-0000-0000-0000-000000000004",
		"bbbbbbbb-0000-0000-0000-000000000005",
	}
	for i, uid := range uids {
		phase := testsv1alpha1.PhasePassed
		if i%2 == 1 {
			phase = testsv1alpha1.PhaseFailed
		}
		run := newRun(uid, "listed", phase, base.Add(time.Duration(i)*10*time.Second))
		require.NoError(t, p.SaveFinished(ctx, run))
	}

	// Filter by test_ref + phase=failed → expect uids [1,3] (indexes 1 & 3).
	failed, err := p.List(ctx, Filter{TestRef: "listed", Phase: "failed"}, Page{})
	require.NoError(t, err)
	require.Len(t, failed, 2)
	// Newest first → uid index 3 before uid index 1.
	assert.Equal(t, uids[3], failed[0].UID)
	assert.Equal(t, uids[1], failed[1].UID)

	// Time range: [base+20s, base+40s) → uids index 2 & 3.
	sinceInc := base.Add(20 * time.Second)
	untilEx := base.Add(40 * time.Second)
	inRange, err := p.List(ctx, Filter{
		TestRef:        "listed",
		SinceInclusive: &sinceInc,
		UntilExclusive: &untilEx,
	}, Page{})
	require.NoError(t, err)
	require.Len(t, inRange, 2)
	assert.Equal(t, uids[3], inRange[0].UID)
	assert.Equal(t, uids[2], inRange[1].UID)

	// Pagination: page 1 of size 2 → newest 2 (indexes 4, 3).
	page1, err := p.List(ctx, Filter{TestRef: "listed"}, Page{Limit: 2})
	require.NoError(t, err)
	require.Len(t, page1, 2)
	assert.Equal(t, uids[4], page1[0].UID)
	assert.Equal(t, uids[3], page1[1].UID)

	// Insert a NEWER row between the two page fetches — it must NOT appear
	// on page 2 (keyset pagination pins the anchor).
	require.NoError(t, p.SaveFinished(ctx, newRun(
		"bbbbbbbb-0000-0000-0000-00000000000f",
		"listed",
		testsv1alpha1.PhasePassed,
		base.Add(60*time.Second),
	)))

	// Page 2 anchored on last-of-page-1 → expect index 2, 1.
	last := page1[len(page1)-1]
	page2, err := p.List(ctx, Filter{TestRef: "listed"}, Page{
		Limit:           2,
		After:           last.UID,
		AfterFinishedAt: &last.FinishedAt,
	})
	require.NoError(t, err)
	require.Len(t, page2, 2)
	assert.Equal(t, uids[2], page2[0].UID)
	assert.Equal(t, uids[1], page2[1].UID)
}

// TestIntegration_RetentionDrop covers:
//   - EnsurePartitions creates the expected months.
//   - ExistingPartitions round-trips the pg_get_expr parser.
//   - DropPartitions removes rows in dropped partitions.
//   - The safety property: partition holding rows newer than cutoff is
//     NEVER dropped, even when other rows in it are older. Row-level
//     boundary test explicitly asserts run at cutoff-1s survives BECAUSE
//     its partition also holds newer rows.
func TestIntegration_RetentionDrop(t *testing.T) {
	ctx := t.Context()
	p := NewPostgres(harness.pool)

	// Base: 2026-11-15. Retention 30d → cutoff 2026-10-16.
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	retention := 30 * 24 * time.Hour
	require.NoError(t, p.EnsurePartitions(ctx, PartitionsToCreate(now, retention, 0)))

	// Seed rows across THREE months to exercise boundaries:
	// - "old" in 2026-09 (wholly before cutoff) → will drop
	// - "boundary-older" in 2026-10 at cutoff-1s (older than cutoff by 1s,
	//    but in the October partition which straddles cutoff → survives)
	// - "boundary-newer" in 2026-10 at cutoff+1s (safely inside retention)
	// - "recent" in 2026-11 (obviously survives)
	cutoff := now.Add(-retention) // 2026-10-16 12:00:00 UTC
	seed := []struct {
		uid string
		t   time.Time
	}{
		{"cccccccc-0000-0000-0000-000000000001", time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)},
		{"cccccccc-0000-0000-0000-000000000002", cutoff.Add(-time.Second)}, // boundary-older
		{"cccccccc-0000-0000-0000-000000000003", cutoff.Add(time.Second)},  // boundary-newer
		{"cccccccc-0000-0000-0000-000000000004", time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)},
	}
	for _, s := range seed {
		run := newRun(s.uid, "retention", testsv1alpha1.PhasePassed, s.t)
		require.NoError(t, p.SaveFinished(ctx, run))
	}

	// List existing → run drop math → drop.
	existing, err := p.ExistingPartitions(ctx)
	require.NoError(t, err)
	toDrop := PartitionsToDrop(existing, now, retention)
	dropped := names(toDrop)
	// The math contract: 2026_09 MUST be in the drop set (wholly before
	// cutoff). October MUST NOT be (its End=2026-11-01 > cutoff). November
	// MUST NOT be (obviously inside). Prior tests may have left additional
	// partitions in the DB — assert on what MUST/MUST NOT be present rather
	// than exact equality (tests share the container).
	assert.Contains(t, dropped, "test_runs_2026_09")
	assert.NotContains(t, dropped, "test_runs_2026_10")
	assert.NotContains(t, dropped, "test_runs_2026_11")

	require.NoError(t, p.DropPartitions(ctx, toDrop))

	// Assert results.
	//  - "old" row is gone
	//  - boundary-older row SURVIVES (its partition straddles cutoff)
	//  - boundary-newer row SURVIVES
	//  - "recent" row SURVIVES
	_, err = p.Get(ctx, "cccccccc-0000-0000-0000-000000000001")
	assert.ErrorIs(t, err, ErrNotFound, "old row must be dropped with partition")

	older, err := p.Get(ctx, "cccccccc-0000-0000-0000-000000000002")
	require.NoError(t, err, "row at cutoff-1s must survive (partition straddles cutoff)")
	assert.NotNil(t, older)

	newer, err := p.Get(ctx, "cccccccc-0000-0000-0000-000000000003")
	require.NoError(t, err, "row at cutoff+1s must survive (safety property)")
	assert.NotNil(t, newer)

	recent, err := p.Get(ctx, "cccccccc-0000-0000-0000-000000000004")
	require.NoError(t, err)
	assert.NotNil(t, recent)
}
