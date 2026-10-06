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

// Package retention enforces run-history retention (CLAUDE.md §9;
// fixes.md #4 — the partition math existed but nothing ever called it, the
// failure mode of Testkube issue #6389). A leader-elected runnable in the
// operator, like the cron scheduler: no CronJob.
//
// Each pass:
//  1. for every monthly test_runs partition entirely older than the
//     retention window: remove each stored run's objects (logs, artifacts,
//     result.json), then drop the partition. If removing objects fails the
//     partition stays, so the next pass retries instead of orphaning files;
//  2. delete audit-log entries older than the window.
//
// Granularity is a month: a run is kept between Retention and Retention +
// one month. Runs never written to Postgres (no --postgres-dsn) aren't seen
// here; give the bucket a lifecycle rule in that setup (docs/storage.md).
package retention

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"

	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

// DefaultInterval is how often a pass runs.
const DefaultInterval = time.Hour

// Store is the run-history surface retention needs (store.Postgres).
type Store interface {
	ExistingPartitions(ctx context.Context) ([]store.Partition, error)
	RunsIn(ctx context.Context, p store.Partition) ([]store.RunRef, error)
	DropPartitions(ctx context.Context, ps []store.Partition) error
	PruneAudit(ctx context.Context, before time.Time) (int64, error)
}

// Job is the retention runnable.
type Job struct {
	Store Store
	// Objects removes run objects; nil when no object storage is configured.
	Objects storage.Remover
	Bucket  string
	// Retention is how long runs are kept. Must be > 0.
	Retention time.Duration
	// Interval between passes. Zero = DefaultInterval.
	Interval time.Duration
	Log      logr.Logger
	// Now is the clock (tests). Zero = time.Now.
	Now func() time.Time
}

// Result summarizes one pass.
type Result struct {
	DroppedPartitions []string
	RemovedRuns       int
	PrunedAudit       int64
}

// RunOnce performs one retention pass. It keeps going past a partition it
// can't clean and reports every failure.
func (j *Job) RunOnce(ctx context.Context) (Result, error) {
	var res Result
	if j.Retention <= 0 {
		return res, errors.New("retention: Retention must be positive")
	}
	now := time.Now()
	if j.Now != nil {
		now = j.Now()
	}
	existing, err := j.Store.ExistingPartitions(ctx)
	if err != nil {
		return res, fmt.Errorf("retention: list partitions: %w", err)
	}
	var errs []error
	for _, part := range store.PartitionsToDrop(existing, now, j.Retention) {
		n, err := j.dropPartition(ctx, part)
		res.RemovedRuns += n
		if err != nil {
			errs = append(errs, err)
			continue
		}
		res.DroppedPartitions = append(res.DroppedPartitions, part.Name)
	}
	pruned, err := j.Store.PruneAudit(ctx, now.Add(-j.Retention))
	res.PrunedAudit = pruned
	if err != nil {
		errs = append(errs, err)
	}
	return res, errors.Join(errs...)
}

// dropPartition removes the objects of every run in part, then the
// partition itself. Returns how many runs' objects were removed.
func (j *Job) dropPartition(ctx context.Context, part store.Partition) (int, error) {
	if j.Objects != nil && j.Bucket != "" {
		runs, err := j.Store.RunsIn(ctx, part)
		if err != nil {
			return 0, err
		}
		for i, r := range runs {
			keys := storage.ForRun(r.Namespace, r.UID)
			if !keys.Valid() {
				continue
			}
			if err := j.Objects.RemovePrefix(ctx, j.Bucket, keys.Prefix()); err != nil {
				return i, fmt.Errorf("retention: %s: remove objects of run %s/%s (partition kept for the next pass): %w",
					part.Name, r.Namespace, r.UID, err)
			}
		}
		if err := j.Store.DropPartitions(ctx, []store.Partition{part}); err != nil {
			return len(runs), fmt.Errorf("retention: drop %s: %w", part.Name, err)
		}
		return len(runs), nil
	}
	if err := j.Store.DropPartitions(ctx, []store.Partition{part}); err != nil {
		return 0, fmt.Errorf("retention: drop %s: %w", part.Name, err)
	}
	return 0, nil
}

// Start implements manager.Runnable: a pass at start, then every Interval,
// until ctx ends.
func (j *Job) Start(ctx context.Context) error {
	interval := j.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		res, err := j.RunOnce(ctx)
		if err != nil {
			j.Log.Error(err, "retention pass incomplete")
		}
		if len(res.DroppedPartitions) > 0 || res.PrunedAudit > 0 {
			j.Log.Info("retention pass", "droppedPartitions", res.DroppedPartitions,
				"removedRuns", res.RemovedRuns, "prunedAuditEntries", res.PrunedAudit)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// NeedLeaderElection: one replica prunes.
func (j *Job) NeedLeaderElection() bool { return true }
