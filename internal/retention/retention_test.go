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

package retention

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

type fakeStore struct {
	mu        sync.Mutex
	parts     []store.Partition
	runs      map[string][]store.RunRef // partition name → runs
	dropped   []string
	pruneUpTo time.Time
}

func (f *fakeStore) ExistingPartitions(context.Context) ([]store.Partition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.Partition(nil), f.parts...), nil
}

func (f *fakeStore) RunsIn(_ context.Context, p store.Partition) ([]store.RunRef, error) {
	return f.runs[p.Name], nil
}

func (f *fakeStore) DropPartitions(_ context.Context, ps []store.Partition) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range ps {
		f.dropped = append(f.dropped, p.Name)
		f.parts = slices.DeleteFunc(f.parts, func(q store.Partition) bool { return q.Name == p.Name })
	}
	return nil
}

func (f *fakeStore) PruneAudit(_ context.Context, before time.Time) (int64, error) {
	f.pruneUpTo = before
	return 3, nil
}

type fakeObjects struct {
	removed []string
	failOn  string
}

func (f *fakeObjects) RemovePrefix(_ context.Context, _, prefix string) error {
	if f.failOn != "" && strings.Contains(prefix, f.failOn) {
		return errors.New("s3 unavailable")
	}
	f.removed = append(f.removed, prefix)
	return nil
}

var now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// month is the 2026 partition of m.
func month(m time.Month) store.Partition {
	return store.PartitionForTime(time.Date(2026, m, 15, 0, 0, 0, 0, time.UTC))
}

func newFake() *fakeStore {
	return &fakeStore{
		parts: []store.Partition{month(7), month(8), month(9), month(10)},
		runs: map[string][]store.RunRef{
			"test_runs_2026_07": {{Namespace: "a", UID: "u1"}, {Namespace: "b", UID: "u2"}},
			"test_runs_2026_08": {{Namespace: "a", UID: "u3"}},
		},
	}
}

func TestRunOnce_DropsExpiredPartitionsAfterTheirObjects(t *testing.T) {
	st, objs := newFake(), &fakeObjects{}
	j := &Job{Store: st, Objects: objs, Bucket: "kubetest", Retention: 30 * 24 * time.Hour,
		Now: func() time.Time { return now }, Log: logr.Discard()}

	res, err := j.RunOnce(t.Context())
	require.NoError(t, err)
	// Cutoff 2026-09-06: July and August end before it; September doesn't.
	assert.Equal(t, []string{"test_runs_2026_07", "test_runs_2026_08"}, res.DroppedPartitions)
	assert.Equal(t, []string{"test_runs_2026_07", "test_runs_2026_08"}, st.dropped)
	assert.Equal(t, []string{
		storage.ForRun("a", "u1").Prefix(), storage.ForRun("b", "u2").Prefix(), storage.ForRun("a", "u3").Prefix(),
	}, objs.removed)
	assert.Equal(t, 3, res.RemovedRuns)
	assert.Equal(t, now.Add(-30*24*time.Hour), st.pruneUpTo)
	assert.Equal(t, int64(3), res.PrunedAudit)
}

// Objects that can't be removed keep their partition: dropping the rows
// would orphan the files with nothing left pointing at them.
func TestRunOnce_ObjectFailureKeepsPartition(t *testing.T) {
	st, objs := newFake(), &fakeObjects{failOn: "u2"}
	j := &Job{Store: st, Objects: objs, Bucket: "kubetest", Retention: 30 * 24 * time.Hour,
		Now: func() time.Time { return now }, Log: logr.Discard()}

	res, err := j.RunOnce(t.Context())
	require.ErrorContains(t, err, "partition kept for the next pass")
	assert.Equal(t, []string{"test_runs_2026_08"}, res.DroppedPartitions, "the other expired month still goes")
	assert.NotContains(t, st.dropped, "test_runs_2026_07")

	objs.failOn = ""
	res, err = j.RunOnce(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"test_runs_2026_07"}, res.DroppedPartitions, "retried on the next pass")
}

func TestRunOnce_WithoutObjectStorageDropsRows(t *testing.T) {
	st := newFake()
	j := &Job{Store: st, Retention: 30 * 24 * time.Hour, Now: func() time.Time { return now }, Log: logr.Discard()}
	res, err := j.RunOnce(t.Context())
	require.NoError(t, err)
	assert.Len(t, res.DroppedPartitions, 2)
}

func TestRunOnce_RejectsZeroRetention(t *testing.T) {
	_, err := (&Job{Store: newFake()}).RunOnce(t.Context())
	require.Error(t, err)
}

func TestStart_RunsAPassThenStopsWithContext(t *testing.T) {
	st := newFake()
	j := &Job{Store: st, Retention: 30 * 24 * time.Hour, Interval: time.Hour,
		Now: func() time.Time { return now }, Log: logr.Discard()}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error)
	go func() { done <- j.Start(ctx) }()
	require.Eventually(t, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return len(st.dropped) == 2
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
	assert.True(t, j.NeedLeaderElection())
}
