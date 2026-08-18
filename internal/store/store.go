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

// Package store persists finished TestRuns to Postgres for the API server
// (step 10) and retention (§9). CRDs remain the source of truth for
// definitions; this table is append-only run history plus JSONB summaries
// and object-store pointers — never blobs (§9 forbids blobs in the DB).
//
// # Ownership & write timing
//
// The controller calls SaveFinished exactly once per terminal transition
// (§15.5 spec-snapshot semantics). Errors are logged and NOT propagated to
// the reconciler: run correctness > run history. The next reconcile retries
// the write; idempotent upsert-by-UID means duplicates are impossible.
//
// # Partitioning
//
// test_runs is monthly-partitioned by finished_at. Partition management
// lives in partition.go (pure math) + the DB-facing side in postgres.go.
// A retention job (drop old partitions) runs on a schedule from
// cmd/operator; step 09 ships the math + drop function, the scheduler
// itself is a small wrapper.
package store

import (
	"context"
	"errors"
	"time"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// RunStore is the write + read surface used by the controller and (later)
// the API server. Kept small so tests can inject a fake without pulling in
// a Postgres client.
type RunStore interface {
	// SaveFinished persists a terminal-phase TestRun. Idempotent by UID:
	// calling twice for the same TestRun writes at most one row, with
	// last-write-wins semantics on all mutable fields. Returns
	// ErrNotTerminal if the run's phase is not terminal.
	SaveFinished(ctx context.Context, run *testsv1alpha1.TestRun) error

	// Get returns the row for a UID or ErrNotFound.
	Get(ctx context.Context, uid string) (*Row, error)

	// List returns rows matching the filter, sorted newest-first by
	// finished_at (DESC), with stable pagination via keyset semantics
	// (Page.After). Stable across concurrent inserts.
	List(ctx context.Context, f Filter, p Page) ([]Row, error)
}

// Row is the wire shape returned by List/Get. Mirrors the DB row 1:1 so
// the API server can json.Marshal it directly.
type Row struct {
	UID          string             `json:"uid"`
	Name         string             `json:"name"`
	Namespace    string             `json:"namespace"`
	TestRef      string             `json:"testRef"`
	Phase        string             `json:"phase"`
	Source       string             `json:"source,omitempty"`
	QueuedAt     *time.Time         `json:"queuedAt,omitempty"`
	StartedAt    *time.Time         `json:"startedAt,omitempty"`
	FinishedAt   time.Time          `json:"finishedAt"`
	DurationMs   int64              `json:"durationMs,omitempty"`
	ResolvedSpec map[string]any     `json:"resolvedSpec,omitempty"`
	Steps        map[string]any     `json:"steps,omitempty"`
	Metrics      map[string]float64 `json:"metrics,omitempty"`
	TestCounts   *TestCounts        `json:"testCounts,omitempty"`
	ArtifactRefs []ArtifactRef      `json:"artifactRefs,omitempty"`
	LogsRef      string             `json:"logsRef,omitempty"`
	Message      string             `json:"message,omitempty"`
	Tags         map[string]string  `json:"tags,omitempty"`
}

// TestCounts mirrors the CRD/executor shape for JUnit-derived counts.
type TestCounts struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// ArtifactRef is the object-store pointer shape.
type ArtifactRef struct {
	Path        string `json:"path"`
	Key         string `json:"key,omitempty"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
	ContentType string `json:"contentType,omitempty"`
}

// Filter narrows a List call. Zero-valued fields disable that filter.
type Filter struct {
	TestRef   string
	Namespace string
	Phase     string
	// SinceInclusive / UntilExclusive bound finished_at.
	SinceInclusive *time.Time
	UntilExclusive *time.Time
}

// Page controls pagination. Limit caps the page size (default 50, max 500).
// After, if non-empty, is the UID of the last row on the previous page —
// combined with AfterFinishedAt gives keyset pagination stable under
// concurrent inserts.
type Page struct {
	Limit           int
	After           string
	AfterFinishedAt *time.Time
}

// DefaultPageLimit is the default List limit. Chosen to keep responses small
// enough for a snappy UI without needing infinite scroll for common cases.
const DefaultPageLimit = 50

// MaxPageLimit caps Limit — callers asking for more get capped without error.
const MaxPageLimit = 500

// ErrNotTerminal is returned by SaveFinished when the passed TestRun's phase
// is not a terminal one. The controller never calls SaveFinished with a
// live phase, but the guard prevents accidents (e.g. a webhook mid-flight).
var ErrNotTerminal = errors.New("store: TestRun phase is not terminal")

// ErrNotFound is returned by Get when the UID has no row. Matches the
// pkg/storage.ErrNotFound convention so callers can errors.Is-check.
var ErrNotFound = errors.New("store: row not found")

// Clock returns "now" in UTC. Overridable so tests get deterministic
// timestamps (e.g. for retention math). Defaults to time.Now().UTC().
type Clock func() time.Time
