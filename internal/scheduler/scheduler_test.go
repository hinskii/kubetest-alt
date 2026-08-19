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

package scheduler

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// buildScheme returns a scheme with the CRD types registered so the fake
// client can decode Test / TestRun objects.
func buildScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	require.NoError(t, testsv1alpha1.AddToScheme(s))
	return s
}

// t0 is the fixed epoch we anchor cron scenarios to. UTC so cron parsing has
// no timezone gotchas across CI hosts.
var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// newTest returns a Test with a given schedule and CreationTimestamp,
// preloaded into the fake client. Container fields satisfy the (webhook-
// enforced-in-prod) invariants so the fixture is realistic even though the
// fake client doesn't run webhooks.
func newTest(name, schedule string, createdAt time.Time) *testsv1alpha1.Test {
	return &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(createdAt),
		},
		Spec: testsv1alpha1.TestSpec{
			Schedule: schedule,
			Container: testsv1alpha1.ContainerConfig{
				Image: "grafana/k6:2.2.0",
				Args:  []string{"run", "s.js"},
			},
		},
	}
}

// listRuns fetches TestRuns and returns their Names.
func listRuns(t *testing.T, c client.Client) []string {
	t.Helper()
	var list testsv1alpha1.TestRunList
	require.NoError(t, c.List(context.Background(), &list))
	out := make([]string, 0, len(list.Items))
	for _, r := range list.Items {
		out = append(out, r.Name)
	}
	return out
}

// getRun fetches one TestRun by name.
func getRun(t *testing.T, c client.Client, name string) *testsv1alpha1.TestRun {
	t.Helper()
	var run testsv1alpha1.TestRun
	require.NoError(t, c.Get(context.Background(),
		client.ObjectKey{Namespace: "default", Name: name}, &run))
	return &run
}

// TestScheduler_FiresAtBoundary: at the exact cron boundary → one TestRun.
// Deterministic name = {test}-{unix scheduled time}.
func TestScheduler_FiresAtBoundary(t *testing.T) {
	scheme := buildScheme(t)
	test := newTest("qa", "*/5 * * * *", t0)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(test).Build()
	clk := NewFakeClock(t0.Add(5 * time.Minute))
	s := &Scheduler{Client: c, Clock: clk}

	require.NoError(t, s.Tick(context.Background(), clk.Now()))

	runs := listRuns(t, c)
	require.Len(t, runs, 1)
	// Unix time at t0+5m = deterministic — encoded in fixture, so a rename
	// or accidental time-zone bug in scheduledRun trips the assertion.
	want := fmt.Sprintf("qa-%d", t0.Add(5*time.Minute).Unix())
	assert.Equal(t, want, runs[0])
	// Provenance stamped.
	run := getRun(t, c, want)
	assert.Equal(t, SourceCron, run.Spec.Source)
	assert.Equal(t, "qa", run.Spec.TestRef)
	assert.Equal(t, "qa", run.Labels[LabelScheduledForTest])
	assert.NotEmpty(t, run.Annotations[AnnotationScheduledAt])
}

// TestScheduler_DoubleFireIdempotent proves §15.6: two Scheduler instances
// firing on the exact same tick produce EXACTLY ONE TestRun (the second's
// Create returns AlreadyExists and is swallowed).
func TestScheduler_DoubleFireIdempotent(t *testing.T) {
	scheme := buildScheme(t)
	test := newTest("qa", "*/5 * * * *", t0)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(test).Build()
	// Both instances share the same underlying client (== single apiserver
	// in production) — the deterministic name is what makes them converge.
	clk := NewFakeClock(t0.Add(5 * time.Minute))
	s1 := &Scheduler{Client: c, Clock: clk}
	s2 := &Scheduler{Client: c, Clock: clk}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = s1.Tick(context.Background(), clk.Now()) }()
	go func() { defer wg.Done(); _ = s2.Tick(context.Background(), clk.Now()) }()
	wg.Wait()

	runs := listRuns(t, c)
	assert.Len(t, runs, 1, "two schedulers on same tick must converge to one TestRun")
}

// TestScheduler_SameTickNoRefire: a single scheduler calling Tick TWICE on
// the same clock value produces one TestRun (idempotency of the same
// instance, not just cross-instance).
func TestScheduler_SameTickNoRefire(t *testing.T) {
	scheme := buildScheme(t)
	test := newTest("qa", "*/5 * * * *", t0)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(test).Build()
	clk := NewFakeClock(t0.Add(5 * time.Minute))
	s := &Scheduler{Client: c, Clock: clk}

	require.NoError(t, s.Tick(context.Background(), clk.Now()))
	require.NoError(t, s.Tick(context.Background(), clk.Now()))

	assert.Len(t, listRuns(t, c), 1)
}

// TestScheduler_AdvanceToNextBoundary: two boundaries → two TestRuns with
// distinct deterministic names.
func TestScheduler_AdvanceToNextBoundary(t *testing.T) {
	scheme := buildScheme(t)
	test := newTest("qa", "*/5 * * * *", t0)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(test).Build()
	clk := NewFakeClock(t0.Add(5 * time.Minute))
	s := &Scheduler{Client: c, Clock: clk}

	require.NoError(t, s.Tick(context.Background(), clk.Now()))
	clk.Set(t0.Add(10 * time.Minute))
	require.NoError(t, s.Tick(context.Background(), clk.Now()))

	runs := listRuns(t, c)
	require.Len(t, runs, 2)
	// Sorted alphabetically by fake client: "qa-300" < "qa-600" if t0=epoch,
	// but for arbitrary t0 both names differ. Assert distinctness only.
	assert.NotEqual(t, runs[0], runs[1])
}

// TestScheduler_ScheduleEditPickedUp: change spec.schedule → the next Tick
// uses the fresh value (each Tick lists Tests, no cached schedules).
//
// NOTE: this does NOT assert that a "boundary-only-in-old-schedule" is
// prevented from firing under the new schedule — the missed-fire window may
// still cover past instants under the new cron and fire them once. The
// invariant here is "scheduler reads current spec, not a stale copy at
// registration time".
func TestScheduler_ScheduleEditPickedUp(t *testing.T) {
	scheme := buildScheme(t)
	test := newTest("qa", "*/5 * * * *", t0)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(test).Build()
	clk := NewFakeClock(t0.Add(5 * time.Minute))
	s := &Scheduler{Client: c, Clock: clk}

	require.NoError(t, s.Tick(context.Background(), clk.Now()))
	require.Len(t, listRuns(t, c), 1)

	// Edit schedule to hourly at :00 — a completely different cadence.
	require.NoError(t, c.Get(context.Background(),
		client.ObjectKey{Namespace: "default", Name: "qa"}, test))
	test.Spec.Schedule = "0 * * * *"
	require.NoError(t, c.Update(context.Background(), test))

	// Advance to t0+1h; the hourly boundary lands here.
	clk.Set(t0.Add(1 * time.Hour))
	require.NoError(t, s.Tick(context.Background(), clk.Now()))

	// Expect the hourly-boundary run present alongside the original */5 one.
	// (Prior missed-fire behavior may have populated additional instants
	// during the transition — assert the KEY new instant exists rather than
	// pinning exact count.)
	want := fmt.Sprintf("qa-%d", t0.Add(1*time.Hour).Unix())
	runs := listRuns(t, c)
	assert.True(t, slices.Contains(runs, want),
		"hourly boundary at t0+1h fires after edit; runs=%v", runs)
}

// TestScheduler_TestDeleteUnschedules: delete the Test → no further TestRuns.
func TestScheduler_TestDeleteUnschedules(t *testing.T) {
	scheme := buildScheme(t)
	test := newTest("qa", "*/5 * * * *", t0)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(test).Build()
	clk := NewFakeClock(t0.Add(5 * time.Minute))
	s := &Scheduler{Client: c, Clock: clk}

	require.NoError(t, s.Tick(context.Background(), clk.Now()))
	require.Len(t, listRuns(t, c), 1)
	before := listRuns(t, c)

	require.NoError(t, c.Delete(context.Background(), test))
	clk.Set(t0.Add(10 * time.Minute))
	require.NoError(t, s.Tick(context.Background(), clk.Now()))

	// TestRun list unchanged — no new run from deleted Test.
	assert.Equal(t, before, listRuns(t, c))
}

// TestScheduler_ScheduleRemovalStops: emptying spec.schedule unschedules the
// Test (deletion isn't the only way to opt out).
func TestScheduler_ScheduleRemovalStops(t *testing.T) {
	scheme := buildScheme(t)
	test := newTest("qa", "*/5 * * * *", t0)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(test).Build()
	clk := NewFakeClock(t0.Add(5 * time.Minute))
	s := &Scheduler{Client: c, Clock: clk}

	require.NoError(t, s.Tick(context.Background(), clk.Now()))
	require.NoError(t, c.Get(context.Background(),
		client.ObjectKey{Namespace: "default", Name: "qa"}, test))
	test.Spec.Schedule = ""
	require.NoError(t, c.Update(context.Background(), test))

	clk.Set(t0.Add(10 * time.Minute))
	require.NoError(t, s.Tick(context.Background(), clk.Now()))
	assert.Len(t, listRuns(t, c), 1)
}

// TestScheduler_MissedFirePolicyTable: window boundary table (CLAUDE.md
// §15.6 idempotency + fire-once-if-late policy).
//
// The window per Test is max(2*interval, MinMissedFireWindow). For a 5-min
// cron: window = 10min. The table covers exactly the interesting rows:
//   - inside window   → fire prev
//   - outside window  → skip (do not backfire an ancient schedule)
func TestScheduler_MissedFirePolicyTable(t *testing.T) {
	// The Test is created way in the past so first-fire gate isn't the thing
	// being tested — the missed-fire window is.
	testCreated := t0

	cases := []struct {
		name     string
		schedule string
		tickAt   time.Time
		want     int // 0 or 1 TestRuns
	}{
		{
			name:     "5min cron, tick 30s after boundary, inside 10m window → fires",
			schedule: "*/5 * * * *",
			tickAt:   t0.Add(5*time.Minute + 30*time.Second),
			want:     1,
		},
		{
			name:     "5min cron, tick 9min after boundary, inside 10m window → fires",
			schedule: "*/5 * * * *",
			tickAt:   t0.Add(5*time.Minute + 9*time.Minute),
			// prev at t0+14m = t0+10m (nearest ≤). 14 - 10 = 4m < 10m window → fires.
			want: 1,
		},
		{
			name:     "5min cron, tick 15min after boundary → prev is more-recent boundary within window → fires",
			schedule: "*/5 * * * *",
			tickAt:   t0.Add(5*time.Minute + 15*time.Minute),
			// prev at t0+20m = t0+20m. 0 delta → fires.
			want: 1,
		},
		{
			name:     "daily cron, tick 3h late → within 2×day window → fires",
			schedule: "0 0 * * *",
			tickAt:   t0.Add(24*time.Hour + 3*time.Hour),
			want:     1,
		},
		{
			name:     "daily cron, tick 3 days late — no earlier boundary in lookback → prev is today, fires",
			schedule: "0 0 * * *",
			tickAt:   t0.Add(3 * 24 * time.Hour).Add(0),
			// prev = t0+3d (fires exactly at midnight on that day). window=2d. delta=0 → fires.
			// This case documents that being "days late" does NOT mean skipping the CURRENT
			// scheduled instant — it means skipping HISTORICAL missed instants (only prev is
			// ever attempted, never a backlog).
			want: 1,
		},
		{
			name:     "per-minute cron 20min after boundary → prev=most recent boundary within 2min window → fires",
			schedule: "* * * * *",
			tickAt:   t0.Add(20 * time.Minute),
			// prev = t0+20m (nearest boundary ≤ now). 0 delta → fires (window=max(2m,1m)=2m).
			want: 1,
		},
		{
			name:     "t0 IS a */5 boundary — fires at exact instant",
			schedule: "*/5 * * * *",
			tickAt:   t0,
			// prev = t0 (t0 IS a */5 boundary); delta = 0 < window → fires.
			want: 1,
		},
		{
			name:     "daily cron 2 days late — prev is yesterday, within 48h window ⇒ fires",
			schedule: "0 5 * * *", // 05:00 daily
			tickAt:   t0.Add(2 * 24 * time.Hour),
			// prev = 2026-01-02T05:00Z (~43h ago). window = 2*24h = 48h ⇒ within.
			want: 1,
		},
		{
			name:     "yearly cron 5 months after last fire — prev outside 48h lookback ⇒ skip",
			schedule: "0 0 1 1 *", // Jan 1 midnight every year
			tickAt:   t0.Add(150 * 24 * time.Hour),
			// prevSchedule walks a 48h window forward; the next yearly fire
			// after (now-48h) is next year — after now — so prev returns zero.
			want: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := buildScheme(t)
			test := newTest("qa", tc.schedule, testCreated)
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(test).Build()
			clk := NewFakeClock(tc.tickAt)
			s := &Scheduler{Client: c, Clock: clk}

			require.NoError(t, s.Tick(context.Background(), clk.Now()))
			assert.Len(t, listRuns(t, c), tc.want)
		})
	}
}

// TestScheduler_CreatedMidWindow_DoesNotBackfire: a Test born after the last
// cron boundary should NOT retroactively fire that boundary.
func TestScheduler_CreatedMidWindow_DoesNotBackfire(t *testing.T) {
	scheme := buildScheme(t)
	// Test created 30s AFTER t0+5m boundary (boundary of */5).
	created := t0.Add(5*time.Minute + 30*time.Second)
	test := newTest("qa", "*/5 * * * *", created)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(test).Build()
	// Tick 1 min later — prev=t0+5m, but created was at t0+5m30s → skip.
	clk := NewFakeClock(created.Add(1 * time.Minute))
	s := &Scheduler{Client: c, Clock: clk}

	require.NoError(t, s.Tick(context.Background(), clk.Now()))
	assert.Empty(t, listRuns(t, c),
		"Test created after prev boundary must not retroactively fire that boundary")

	// Advance past the next boundary → fires.
	clk.Set(t0.Add(10*time.Minute + 1*time.Second))
	require.NoError(t, s.Tick(context.Background(), clk.Now()))
	assert.Len(t, listRuns(t, c), 1)
}

// TestScheduler_MultipleTestsIndependent verifies one bad schedule doesn't
// starve others.
func TestScheduler_MultipleTestsIndependent(t *testing.T) {
	scheme := buildScheme(t)
	good := newTest("qa-good", "*/5 * * * *", t0)
	bad := newTest("qa-bad", "not-a-cron-expression", t0)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(good, bad).Build()
	clk := NewFakeClock(t0.Add(5 * time.Minute))
	s := &Scheduler{Client: c, Clock: clk}

	require.NoError(t, s.Tick(context.Background(), clk.Now()))
	runs := listRuns(t, c)
	assert.Len(t, runs, 1)
	assert.True(t, strings.HasPrefix(runs[0], "qa-good-"), "only good test fired: %v", runs)
}

// TestPrevSchedule_LookbackCapped documents that prevScheduleLookback bounds
// the walk — a yearly cron tick 5 months late returns zero.
func TestPrevSchedule_LookbackCapped(t *testing.T) {
	// Yearly cron: Jan 1 00:00.
	sched, err := stdCronParser.Parse("0 0 1 1 *")
	require.NoError(t, err)

	now := t0.Add(150 * 24 * time.Hour) // May 31, 2026
	prev := prevSchedule(sched, now)
	assert.True(t, prev.IsZero(),
		"prevSchedule beyond lookback (%s) must return zero", prevScheduleLookback)
}

// TestPrevSchedule_ExactBoundary: now equals a cron boundary → prev == now.
func TestPrevSchedule_ExactBoundary(t *testing.T) {
	sched, err := stdCronParser.Parse("*/5 * * * *")
	require.NoError(t, err)

	now := t0.Add(5 * time.Minute)
	prev := prevSchedule(sched, now)
	assert.True(t, prev.Equal(now), "prev at boundary equals now, got %s vs %s", prev, now)
}

// TestScheduler_ParserOverride: injecting a 6-field parser lets tests run
// per-second schedules for fast iteration. Regression guard: don't break the
// parser injection point.
func TestScheduler_ParserOverride(t *testing.T) {
	scheme := buildScheme(t)
	test := newTest("qa", "*/5 * * * * *", t0) // 6 fields (with seconds)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(test).Build()
	clk := NewFakeClock(t0.Add(5 * time.Second))
	s := &Scheduler{
		Client: c,
		Clock:  clk,
		Parser: cron.NewParser(cron.Second | cron.Minute | cron.Hour |
			cron.Dom | cron.Month | cron.Dow),
	}

	require.NoError(t, s.Tick(context.Background(), clk.Now()))
	assert.Len(t, listRuns(t, c), 1)
}

// TestScheduler_NeedLeaderElection is a pin so a future refactor cannot
// accidentally flip this to false and start racing on non-leaders.
func TestScheduler_NeedLeaderElection(t *testing.T) {
	assert.True(t, (&Scheduler{}).NeedLeaderElection())
}

// TestScheduler_InvalidScheduleIsDefensiveSkip: the webhook rejects bad
// schedules, but if one slipped through (older CR from a webhook-off env)
// the scheduler must NOT crash — it should skip silently.
func TestScheduler_InvalidScheduleIsDefensiveSkip(t *testing.T) {
	scheme := buildScheme(t)
	test := newTest("qa", "wat", t0)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(test).Build()
	clk := NewFakeClock(t0.Add(5 * time.Minute))
	s := &Scheduler{Client: c, Clock: clk}

	require.NoError(t, s.Tick(context.Background(), clk.Now()))
	assert.Empty(t, listRuns(t, c))
}
