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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// Postgres is a RunStore backed by a pgxpool. Zero value not usable —
// construct with NewPostgres.
type Postgres struct {
	pool *pgxpool.Pool

	// Warn is called for every metric-parse warning during SaveFinished.
	// Nil is legal (warnings dropped). cmd/operator wires it to
	// controller-runtime's logger; tests capture into a slice.
	Warn WarnFunc
}

// NewPostgres wraps an existing pgxpool. Kept plain so callers own the
// pool's lifecycle (Close in cmd/operator on manager exit).
func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

// SaveFinished implements RunStore. Idempotent upsert keyed by uid.
// Not-terminal phases return ErrNotTerminal without touching the DB.
func (p *Postgres) SaveFinished(ctx context.Context, run *testsv1alpha1.TestRun) error {
	if run == nil || !IsTerminal(run.Status.Phase) {
		return ErrNotTerminal
	}
	row, err := RowFromRun(run, p.Warn)
	if err != nil {
		return fmt.Errorf("store: map row: %w", err)
	}

	// Auto-create the partition holding this row's finished_at. Cheap
	// (IF NOT EXISTS), and it avoids a hard startup ordering dependency
	// between the retention job and the first save at month rollover.
	part := PartitionForTime(row.FinishedAt)
	if err := p.ensurePartition(ctx, part); err != nil {
		return fmt.Errorf("store: ensure partition %s: %w", part.Name, err)
	}

	resolvedSpec := jsonbOrNil(row.ResolvedSpec)
	steps := jsonbOrNil(row.Steps)
	metrics := jsonbOrNil(row.Metrics)
	testCounts := jsonbOrNil(row.TestCounts)
	artifacts := jsonbOrNil(row.ArtifactRefs)
	tags := jsonbOrNil(row.Tags)

	// ON CONFLICT (uid, finished_at) is safe because uid is unique per run
	// (Kubernetes UUID) and finished_at is stable once a run is terminal
	// (§15.5). Same uid + same finished_at → same partition → same PK.
	const stmt = `
		INSERT INTO test_runs (
			uid, name, namespace, test_ref, phase, source,
			queued_at, started_at, finished_at, duration_ms,
			resolved_spec, steps, metrics, test_counts, artifact_refs,
			logs_ref, message, tags
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13, $14, $15,
			$16, $17, $18
		)
		ON CONFLICT (uid, finished_at) DO UPDATE SET
			name          = EXCLUDED.name,
			namespace     = EXCLUDED.namespace,
			test_ref      = EXCLUDED.test_ref,
			phase         = EXCLUDED.phase,
			source        = EXCLUDED.source,
			queued_at     = EXCLUDED.queued_at,
			started_at    = EXCLUDED.started_at,
			duration_ms   = EXCLUDED.duration_ms,
			resolved_spec = EXCLUDED.resolved_spec,
			steps         = EXCLUDED.steps,
			metrics       = EXCLUDED.metrics,
			test_counts   = EXCLUDED.test_counts,
			artifact_refs = EXCLUDED.artifact_refs,
			logs_ref      = EXCLUDED.logs_ref,
			message       = EXCLUDED.message,
			tags          = EXCLUDED.tags
	`
	_, err = p.pool.Exec(ctx, stmt,
		row.UID, row.Name, row.Namespace, row.TestRef, row.Phase, nullIfEmpty(row.Source),
		row.QueuedAt, row.StartedAt, row.FinishedAt, nullIfZero(row.DurationMs),
		resolvedSpec, steps, metrics, testCounts, artifacts,
		nullIfEmpty(row.LogsRef), nullIfEmpty(row.Message), tags,
	)
	if err != nil {
		return fmt.Errorf("store: upsert %s: %w", row.UID, err)
	}
	return nil
}

// Get implements RunStore.
func (p *Postgres) Get(ctx context.Context, uid string) (*Row, error) {
	const q = `SELECT ` + selectCols + ` FROM test_runs WHERE uid = $1`
	rows, err := p.pool.Query(ctx, q, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list, err := scanRows(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

// List implements RunStore.
func (p *Postgres) List(ctx context.Context, f Filter, page Page) ([]Row, error) {
	limit := page.Limit
	if limit <= 0 {
		limit = DefaultPageLimit
	}
	if limit > MaxPageLimit {
		limit = MaxPageLimit
	}

	// Keyset pagination: (finished_at DESC, uid DESC) is a unique ordering
	// as long as (uid, finished_at) is the PK (uid alone is unique for
	// TestRuns, but the composite covers the theoretical dupe).
	var (
		clauses []string
		args    []any
	)
	if f.TestRef != "" {
		clauses = append(clauses, fmt.Sprintf(`test_ref = $%d`, len(args)+1))
		args = append(args, f.TestRef)
	}
	if f.Namespace != "" {
		clauses = append(clauses, fmt.Sprintf(`namespace = $%d`, len(args)+1))
		args = append(args, f.Namespace)
	}
	if f.Phase != "" {
		clauses = append(clauses, fmt.Sprintf(`phase = $%d`, len(args)+1))
		args = append(args, f.Phase)
	}
	if f.SinceInclusive != nil {
		clauses = append(clauses, fmt.Sprintf(`finished_at >= $%d`, len(args)+1))
		args = append(args, f.SinceInclusive.UTC())
	}
	if f.UntilExclusive != nil {
		clauses = append(clauses, fmt.Sprintf(`finished_at < $%d`, len(args)+1))
		args = append(args, f.UntilExclusive.UTC())
	}
	if page.AfterFinishedAt != nil && page.After != "" {
		// keyset: strictly older than the last row on the previous page.
		clauses = append(clauses, fmt.Sprintf(
			`(finished_at, uid) < ($%d, $%d)`, len(args)+1, len(args)+2))
		args = append(args, page.AfterFinishedAt.UTC(), page.After)
	}

	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	q := fmt.Sprintf(
		`SELECT %s FROM test_runs %s ORDER BY finished_at DESC, uid DESC LIMIT %d`,
		selectCols, where, limit,
	)
	rows, err := p.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

// EnsurePartitions creates every partition in ps that doesn't exist yet.
// Called by cmd/operator on startup (and by a scheduled retention job) to
// keep the accepting-write window healthy across month rollover.
func (p *Postgres) EnsurePartitions(ctx context.Context, ps []Partition) error {
	for _, part := range ps {
		if err := p.ensurePartition(ctx, part); err != nil {
			return err
		}
	}
	return nil
}

// DropPartitions unattaches + drops each named partition. Idempotent (DROP
// TABLE IF EXISTS). Called by the retention job.
func (p *Postgres) DropPartitions(ctx context.Context, ps []Partition) error {
	for _, part := range ps {
		if err := p.dropPartition(ctx, part); err != nil {
			return err
		}
	}
	return nil
}

// ExistingPartitions returns the test_runs child partitions currently
// attached to the parent, sorted oldest-first. Reads pg_inherits +
// pg_class + pg_get_expr to parse the FOR VALUES clause back into Start/End.
//
// Postgres tip: pg_get_expr(relpartbound, oid) returns e.g.
// "FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00')".
// We regex-parse rather than doing full SQL introspection — cheap and
// stable because we own the partition-DDL emitter.
func (p *Postgres) ExistingPartitions(ctx context.Context) ([]Partition, error) {
	const q = `
		SELECT c.relname,
		       pg_get_expr(c.relpartbound, c.oid)
		FROM pg_inherits i
		JOIN pg_class parent ON parent.oid = i.inhparent
		JOIN pg_class c      ON c.oid      = i.inhrelid
		WHERE parent.relname = 'test_runs'
		ORDER BY c.relname
	`
	rows, err := p.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Partition
	for rows.Next() {
		var name, boundExpr string
		if err := rows.Scan(&name, &boundExpr); err != nil {
			return nil, err
		}
		start, end, ok := parsePartitionBound(boundExpr)
		if !ok {
			// Skip odd bounds (default partition, etc.) — retention only
			// touches ranges we emitted.
			continue
		}
		out = append(out, Partition{Name: name, Start: start, End: end})
	}
	return out, rows.Err()
}

// selectCols centralises the column list so scanRows/Get/List stay in sync.
const selectCols = `
	uid::text, name, namespace, test_ref, phase, source,
	queued_at, started_at, finished_at, duration_ms,
	resolved_spec, steps, metrics, test_counts, artifact_refs,
	logs_ref, message, tags
`

func scanRows(rows pgx.Rows) ([]Row, error) {
	var out []Row
	for rows.Next() {
		var (
			r          Row
			source     *string
			logsRef    *string
			message    *string
			duration   *int64
			specBytes  []byte
			stepsBytes []byte
			metricsB   []byte
			tcBytes    []byte
			arBytes    []byte
			tagsBytes  []byte
		)
		if err := rows.Scan(
			&r.UID, &r.Name, &r.Namespace, &r.TestRef, &r.Phase, &source,
			&r.QueuedAt, &r.StartedAt, &r.FinishedAt, &duration,
			&specBytes, &stepsBytes, &metricsB, &tcBytes, &arBytes,
			&logsRef, &message, &tagsBytes,
		); err != nil {
			return nil, err
		}
		if source != nil {
			r.Source = *source
		}
		if logsRef != nil {
			r.LogsRef = *logsRef
		}
		if message != nil {
			r.Message = *message
		}
		if duration != nil {
			r.DurationMs = *duration
		}
		// pgx returns timestamps in the session's timezone; force UTC so
		// callers can compare against wall clocks without surprise.
		if r.QueuedAt != nil {
			t := r.QueuedAt.UTC()
			r.QueuedAt = &t
		}
		if r.StartedAt != nil {
			t := r.StartedAt.UTC()
			r.StartedAt = &t
		}
		r.FinishedAt = r.FinishedAt.UTC()

		if err := unmarshalJSONBIfPresent(specBytes, &r.ResolvedSpec); err != nil {
			return nil, err
		}
		if err := unmarshalJSONBIfPresent(stepsBytes, &r.Steps); err != nil {
			return nil, err
		}
		if err := unmarshalJSONBIfPresent(metricsB, &r.Metrics); err != nil {
			return nil, err
		}
		if len(tcBytes) > 0 {
			var tc TestCounts
			if err := json.Unmarshal(tcBytes, &tc); err != nil {
				return nil, err
			}
			r.TestCounts = &tc
		}
		if len(arBytes) > 0 {
			if err := json.Unmarshal(arBytes, &r.ArtifactRefs); err != nil {
				return nil, err
			}
		}
		if err := unmarshalJSONBIfPresent(tagsBytes, &r.Tags); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *Postgres) ensurePartition(ctx context.Context, part Partition) error {
	// Format literals directly — Postgres CREATE TABLE ... PARTITION OF
	// doesn't accept parameters for the FROM/TO bound expressions. Values
	// come from our own PartitionForTime output, never user input.
	stmt := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s PARTITION OF test_runs FOR VALUES FROM ('%s') TO ('%s')`,
		part.Name,
		part.Start.Format("2006-01-02 15:04:05Z07:00"),
		part.End.Format("2006-01-02 15:04:05Z07:00"),
	)
	_, err := p.pool.Exec(ctx, stmt)
	return err
}

func (p *Postgres) dropPartition(ctx context.Context, part Partition) error {
	// DROP TABLE also detaches the partition — no separate DETACH needed.
	stmt := fmt.Sprintf(`DROP TABLE IF EXISTS %s`, part.Name)
	_, err := p.pool.Exec(ctx, stmt)
	return err
}

// jsonbOrNil returns nil (Postgres NULL) for empty containers, else the
// JSON-encoded bytes ready for a jsonb column. Keeps NULLs out of the
// db-side data instead of empty {}/[] objects.
func jsonbOrNil(v any) any {
	switch vv := v.(type) {
	case nil:
		return nil
	case map[string]any:
		if len(vv) == 0 {
			return nil
		}
	case map[string]string:
		if len(vv) == 0 {
			return nil
		}
	case map[string]float64:
		if len(vv) == 0 {
			return nil
		}
	case []ArtifactRef:
		if len(vv) == 0 {
			return nil
		}
	case *TestCounts:
		if vv == nil {
			return nil
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func unmarshalJSONBIfPresent[T any](b []byte, out *T) error {
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullIfZero(i int64) any {
	if i == 0 {
		return nil
	}
	return i
}

// parsePartitionBound turns pg_get_expr output back into Start/End times.
// Format we care about (produced by our own ensurePartition):
//
//	FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00')
//
// Anything else returns ok=false — the retention job then skips the
// partition (never accidentally drops an operator-external partition).
func parsePartitionBound(expr string) (time.Time, time.Time, bool) {
	const prefix = "FOR VALUES FROM ("
	if !strings.HasPrefix(expr, prefix) {
		return time.Time{}, time.Time{}, false
	}
	rest := expr[len(prefix):]
	startLitRaw, rest, ok := strings.Cut(rest, ") TO (")
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	startLit := trimLiteral(startLitRaw)
	endLitRaw, _, ok := strings.Cut(rest, ")")
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	endLit := trimLiteral(endLitRaw)

	start, err1 := parsePGTimestamp(startLit)
	end, err2 := parsePGTimestamp(endLit)
	if err1 != nil || err2 != nil {
		return time.Time{}, time.Time{}, false
	}
	return start.UTC(), end.UTC(), true
}

func trimLiteral(s string) string {
	return strings.Trim(strings.TrimSpace(s), "'")
}

func parsePGTimestamp(s string) (time.Time, error) {
	// Postgres emits e.g. "2026-07-01 00:00:00+00". Try the format we know,
	// then fall back through a couple of near-variants.
	layouts := []string{
		"2006-01-02 15:04:05Z07",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05-07",
		"2006-01-02 15:04:05.999999Z07",
	}
	var lastErr error
	for _, l := range layouts {
		t, err := time.Parse(l, s)
		if err == nil {
			return t, nil
		}
		lastErr = err
	}
	return time.Time{}, errors.New("store: parse pg timestamp " + s + ": " + lastErr.Error())
}
