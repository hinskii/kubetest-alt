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

// Package scheduler runs the cron scheduler that lives inside the operator
// process (CLAUDE.md §4 — "built-in RPC-based cron scheduler … eliminates the
// need to create separate CronJob pods"). Behind manager leader election, so
// only one replica creates TestRuns; deterministic run names + AlreadyExists
// swallow make double-fire on failover a no-op (§15.6).
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// SourceCron is TestRun.spec.source on runs the scheduler creates.
// Matches the CRD enum in testrun_types.go — kept as a const here so callers
// (including tests) never spell the literal directly.
const SourceCron = "cron"

// MinMissedFireWindow is the floor for the "missed-fire" window per Test:
//
//	window = max(2 * cronInterval, MinMissedFireWindow)
//
// If the most-recent scheduled instant is OLDER than window relative to the
// current tick, it is SKIPPED (do not backfire ancient schedules — after a
// long outage that would DOS the cluster). If it is within the window, we
// fire ONCE for that instant. Historical instants further in the past are
// never re-attempted; we only ever fire prevSchedule(now), never a sequence.
//
// Concretely:
//   - per-minute cron, tick skew of 30s → fires the most-recent minute
//   - per-5min cron, restart 6 min into a window → fires that window once
//   - daily cron, restart 3h after midnight → fires today's midnight once
//   - daily cron, restart 3 days late → skips (3d > 2*day window)
//   - per-minute cron, outage of 1h → fires only the most recent minute,
//     skips the 59 in between
//
// Mirrors k8s CronJob's startingDeadlineSeconds intent: too old = skip.
const MinMissedFireWindow = 60 * time.Second

// prevScheduleLookback caps how far back prevSchedule walks the cron
// expression when searching for the most-recent fire ≤ now. 48h covers every
// cron that fires at least once per day. Fires less-frequent than daily
// (weekly/monthly) will NOT catch up missed instants beyond 48h — acceptable
// trade-off vs. keeping the walk O(iterations-per-2-days) rather than
// O(iterations-since-epoch).
const prevScheduleLookback = 48 * time.Hour

// stdCronParser is the same parser the Test webhook uses: 5 fields, no
// seconds, mirrors k8s CronJob semantics.
var stdCronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// Scheduler creates TestRuns for Tests with spec.schedule. Runs inside the
// operator manager as a leader-elected Runnable.
//
// Design notes:
//   - No in-memory "lastFired" state. Idempotency comes from deterministic
//     TestRun names ({test}-{unixScheduledTime}) + AlreadyExists on Create.
//     A restart / leader failover / two concurrent instances all converge
//     on exactly one TestRun per scheduled instant.
//   - No per-Test event subscription. Each Tick lists Tests and evaluates
//     each; schedule edits and deletions take effect on the next Tick
//     naturally.
//   - Test.CreationTimestamp gates the FIRST fire: prev < createdAt is
//     skipped so a Test created mid-window doesn't retroactively fire the
//     window it wasn't around for.
type Scheduler struct {
	Client client.Client

	// Clock is injectable so tests never touch the wall clock. Defaults to
	// RealClock when the manager Start path initializes it.
	Clock Clock

	// TickInterval is the production tick cadence when Start owns the loop.
	// Tests bypass Start and call Tick directly. Default 30s.
	TickInterval time.Duration

	// Parser overrides the cron parser (5-field, no seconds by default —
	// matches the Test webhook). Tests can inject a 6-field parser to make
	// second-granularity scenarios readable.
	Parser cron.Parser
}

// NeedLeaderElection makes the manager schedule this Runnable behind the
// leader lease so exactly one replica creates TestRuns.
func (s *Scheduler) NeedLeaderElection() bool { return true }

// Start runs the tick loop until ctx is canceled. Manager.Add wires this in.
//
// Tests should NOT call Start — they call Tick directly with a chosen `now`.
// This method exists only for the production goroutine.
func (s *Scheduler) Start(ctx context.Context) error {
	s.applyDefaults()
	logger := log.FromContext(ctx).WithName("scheduler")
	logger.Info("scheduler starting", "tickInterval", s.TickInterval)
	// Initial tick right away so a leader-elect handover doesn't wait a full
	// TickInterval before firing the next boundary.
	if err := s.Tick(ctx, s.Clock.Now()); err != nil {
		logger.Error(err, "initial tick")
	}
	// #nosec G115 -- TickInterval bounded above by operator flag; not attacker-controlled.
	ticker := time.NewTicker(s.TickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("scheduler stopping")
			return nil
		case <-ticker.C:
			if err := s.Tick(ctx, s.Clock.Now()); err != nil {
				logger.Error(err, "tick")
			}
		}
	}
}

func (s *Scheduler) applyDefaults() {
	if s.Clock == nil {
		s.Clock = RealClock{}
	}
	if s.TickInterval == 0 {
		s.TickInterval = 30 * time.Second
	}
}

func (s *Scheduler) parser() cron.Parser {
	// cron.Parser is a value type — zero-value has zero options and rejects
	// every expression. Compare by parsing a known-good expression against
	// the zero value; simpler: track "explicitly set" via non-zero Parser
	// after applyDefaults? cron.Parser has no exported fields — treat unset
	// as stdCronParser.
	if s.Parser == (cron.Parser{}) {
		return stdCronParser
	}
	return s.Parser
}

// Tick evaluates every Test at the given wall-clock instant. Public so tests
// drive the scheduler synchronously without any sleeps — the fake-clock
// discipline required by step 12.
func (s *Scheduler) Tick(ctx context.Context, now time.Time) error {
	s.applyDefaults()
	var list testsv1alpha1.TestList
	if err := s.Client.List(ctx, &list); err != nil {
		return fmt.Errorf("list Tests: %w", err)
	}
	var errs []error
	for i := range list.Items {
		if err := s.evaluate(ctx, &list.Items[i], now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// evaluate handles one Test on one tick. Returns nil for "nothing to do" and
// for AlreadyExists (idempotent double-fire suppression).
func (s *Scheduler) evaluate(ctx context.Context, t *testsv1alpha1.Test, now time.Time) error {
	if t.Spec.Schedule == "" {
		return nil
	}
	sched, err := s.parser().Parse(t.Spec.Schedule)
	if err != nil {
		// Webhook rejects invalid schedules; defensive skip if one slipped
		// through (older CRs from a webhook-off env, for instance).
		return nil
	}
	prev := prevSchedule(sched, now)
	if prev.IsZero() {
		return nil
	}
	// First-fire gate: never fire a scheduled instant older than the Test
	// itself. A Test created mid-window shouldn't retroactively fire.
	if !t.CreationTimestamp.IsZero() && prev.Before(t.CreationTimestamp.Time) {
		return nil
	}
	// Missed-fire window: max(2*interval, MinMissedFireWindow). Compute
	// interval from prev → sched.Next(prev). "Never fire if prev is older
	// than window."
	interval := sched.Next(prev).Sub(prev)
	window := max(2*interval, MinMissedFireWindow)
	if now.Sub(prev) > window {
		return nil
	}

	run := scheduledRun(t, prev)
	if err := s.Client.Create(ctx, run); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Idempotency contract: another replica or a previous tick
			// already created THIS scheduled instant's TestRun. Success.
			return nil
		}
		return fmt.Errorf("create TestRun %q: %w", run.Name, err)
	}
	log.FromContext(ctx).V(1).Info("scheduler created TestRun",
		"test", t.Name, "namespace", t.Namespace,
		"run", run.Name, "scheduledAt", prev.UTC().Format(time.RFC3339))
	return nil
}

// scheduledRun builds the TestRun object for a scheduled instant. Name is
// deterministic ({test}-{unixSeconds}) — this is the entire idempotency
// mechanism for the scheduler (§15.6). Two schedulers on the same tick both
// try to Create the same object; the loser gets AlreadyExists and returns
// success.
func scheduledRun(t *testsv1alpha1.Test, prev time.Time) *testsv1alpha1.TestRun {
	return &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", t.Name, prev.Unix()),
			Namespace: t.Namespace,
			Labels: map[string]string{
				LabelScheduledForTest: t.Name,
			},
			Annotations: map[string]string{
				AnnotationScheduledAt: prev.UTC().Format(time.RFC3339),
			},
		},
		Spec: testsv1alpha1.TestRunSpec{
			TestRef: t.Name,
			Source:  SourceCron,
		},
	}
}

// prevSchedule returns the most recent scheduled instant ≤ now, or the zero
// time if none exists within the lookback window. cron.Schedule only exposes
// Next(); we walk forward from (now - lookback) until we exceed now.
func prevSchedule(sched cron.Schedule, now time.Time) time.Time {
	start := now.Add(-prevScheduleLookback)
	// safetyBound covers a per-second cron across the lookback window with
	// slack — impossible in practice, but a bounded loop is a bounded loop.
	const safetyBound = 4 * 24 * 60 * 60 // 4 days of per-second fires
	var prev time.Time
	t := start
	for range safetyBound {
		next := sched.Next(t)
		if next.After(now) {
			return prev
		}
		prev = next
		t = next
	}
	return prev
}

// Labels + annotations the scheduler stamps on TestRuns so operators can
// distinguish scheduled from api/ui/trigger runs at a glance. The `source`
// field is the semantic source of truth; these are UX sugar for kubectl.
const (
	LabelScheduledForTest = "kubetest.io/scheduled-for-test"
	AnnotationScheduledAt = "kubetest.io/scheduled-at"
)
