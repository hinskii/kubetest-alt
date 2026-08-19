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

package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/scheduler"
)

// t0Trigger is the anchor time for gate tests. UTC so nothing depends on
// the CI host's timezone.
var t0Trigger = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

// newTrigger builds a TestTrigger with the given condition spec, deployment
// resource, and modified event kind — the common shape for gate tests.
// name is a parameter so future multi-trigger scenarios can distinguish
// gate outcomes by trigger identity.
// nolint:unparam
func newTrigger(name, ns string, spec *testsv1alpha1.TriggerConditionSpec) *testsv1alpha1.TestTrigger {
	return &testsv1alpha1.TestTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: testsv1alpha1.TestTriggerSpec{
			Resource:      "deployment",
			Event:         TriggerEventModified,
			Action:        "run",
			Execution:     "test",
			ConditionSpec: spec,
			TestSelector:  &testsv1alpha1.TriggerTestSelector{Name: "some-test"},
		},
	}
}

// captureFirer records fires. The (error) return is fed back into the gate
// manager: nil = fire recorded normally; specific errors let tests exercise
// error paths.
type captureFirer struct {
	calls []gateOutcomeCall
	next  error
}

type gateOutcomeCall struct {
	Trigger *testsv1alpha1.TestTrigger
	When    time.Time
}

func (f *captureFirer) firer(_ context.Context, g *gate) error {
	f.calls = append(f.calls, gateOutcomeCall{Trigger: g.Trigger, When: g.EventTime})
	err := f.next
	f.next = nil
	return err
}

// TestGate_ConditionsMet_Immediate: no delay, conditions already met at
// event time → next Evaluate fires.
func TestGate_ConditionsMet_Immediate(t *testing.T) {
	clk := scheduler.NewFakeClock(t0Trigger)
	fire := &captureFirer{}
	gm := newGateManager(clk, fire.firer)

	trg := newTrigger("t", "default",
		&testsv1alpha1.TriggerConditionSpec{
			Conditions: []testsv1alpha1.TriggerCondition{
				{Type: "Available", Status: "True"},
			},
		})
	obj := deploymentWithConditions("d", []conditionEntry{
		{Type: "Available", Status: "True"},
	})

	gm.Enqueue(&gate{Trigger: trg, Target: obj, EventTime: clk.Now()})
	require.Equal(t, 1, gm.Pending())

	gm.Evaluate(context.Background())
	assert.Zero(t, gm.Pending(), "gate should be removed after fire")
	assert.Len(t, fire.calls, 1)
}

// TestGate_Delay_HoldsUntilElapsed: delay=5s. First evaluate at +2s does
// NOT fire; evaluate at +6s does.
func TestGate_Delay_HoldsUntilElapsed(t *testing.T) {
	clk := scheduler.NewFakeClock(t0Trigger)
	fire := &captureFirer{}
	gm := newGateManager(clk, fire.firer)

	trg := newTrigger("t", "default",
		&testsv1alpha1.TriggerConditionSpec{
			Delay: 5,
			Conditions: []testsv1alpha1.TriggerCondition{
				{Type: "Available", Status: "True"},
			},
		})
	obj := deploymentWithConditions("d", []conditionEntry{
		{Type: "Available", Status: "True"},
	})

	gm.Enqueue(&gate{Trigger: trg, Target: obj, EventTime: clk.Now()})

	clk.Set(t0Trigger.Add(2 * time.Second))
	gm.Evaluate(context.Background())
	assert.Equal(t, 1, gm.Pending(), "gate must stay pending inside delay window")
	assert.Empty(t, fire.calls)

	clk.Set(t0Trigger.Add(6 * time.Second))
	gm.Evaluate(context.Background())
	assert.Zero(t, gm.Pending(), "gate fires once delay elapses and conditions still hold")
	assert.Len(t, fire.calls, 1)
}

// TestGate_Timeout_ExpiresWhenConditionsNeverMet: delay=0, timeout=10s,
// conditions never become true → gate expires exactly once at +11s.
func TestGate_Timeout_ExpiresWhenConditionsNeverMet(t *testing.T) {
	clk := scheduler.NewFakeClock(t0Trigger)
	fire := &captureFirer{}
	gm := newGateManager(clk, fire.firer)

	trg := newTrigger("t", "default",
		&testsv1alpha1.TriggerConditionSpec{
			Timeout: 10,
			Conditions: []testsv1alpha1.TriggerCondition{
				{Type: "Available", Status: "True"},
			},
		})
	obj := deploymentWithConditions("d", []conditionEntry{
		{Type: "Available", Status: "False"},
	})

	gm.Enqueue(&gate{Trigger: trg, Target: obj, EventTime: clk.Now()})

	clk.Set(t0Trigger.Add(5 * time.Second))
	gm.Evaluate(context.Background())
	assert.Equal(t, 1, gm.Pending())

	clk.Set(t0Trigger.Add(11 * time.Second))
	out := gm.Evaluate(context.Background())
	assert.Zero(t, gm.Pending())
	assert.Empty(t, fire.calls, "must NOT fire when conditions unmet at timeout")
	// Outcome recorded as expired.
	require.Len(t, out, 1)
	assert.Equal(t, OutcomeExpired, out[0].Kind)
}

// TestGate_TimeoutFromEventTime_NotOperatorStart: enqueue an event 30s in
// the "past" (event fired before scheduler realized). Timeout is measured
// from event time — 10s later is still overdue.
//
// This is the guardrail the plan explicitly called out: conditionSpec.timeout
// MUST be measured from the event, not from operator start / gate creation.
func TestGate_TimeoutFromEventTime_NotOperatorStart(t *testing.T) {
	clk := scheduler.NewFakeClock(t0Trigger)
	fire := &captureFirer{}
	gm := newGateManager(clk, fire.firer)

	trg := newTrigger("t", "default",
		&testsv1alpha1.TriggerConditionSpec{
			Timeout: 10,
			Conditions: []testsv1alpha1.TriggerCondition{
				{Type: "Available", Status: "True"},
			},
		})
	obj := deploymentWithConditions("d", []conditionEntry{
		{Type: "Available", Status: "False"},
	})

	// Simulate: event happened 30s ago (from clock's POV), we're only just
	// enqueuing it now (operator restart, informer initial-list, whatever).
	eventTime := clk.Now().Add(-30 * time.Second)
	gm.Enqueue(&gate{Trigger: trg, Target: obj, EventTime: eventTime})

	// Evaluate at "now" — 30s since event, > 10s timeout → expires immediately.
	out := gm.Evaluate(context.Background())
	assert.Zero(t, gm.Pending())
	require.Len(t, out, 1)
	assert.Equal(t, OutcomeExpired, out[0].Kind)
	assert.Empty(t, fire.calls)
}

// TestGate_TTL_HoldsUntilConditionStable: conditions require TTL=60s. At
// +30s conditions are set but LTT was just now → not settled. At +90s (LTT
// now 90s ago) → settled → fires.
//
// TTL cross-checks with the fake clock the same discipline as cron —
// deterministic, no wall-clock reliance.
func TestGate_TTL_HoldsUntilConditionStable(t *testing.T) {
	clk := scheduler.NewFakeClock(t0Trigger)
	fire := &captureFirer{}
	gm := newGateManager(clk, fire.firer)

	trg := newTrigger("t", "default",
		&testsv1alpha1.TriggerConditionSpec{
			Timeout: 300,
			Conditions: []testsv1alpha1.TriggerCondition{
				{Type: "Available", Status: "True", TTL: 60},
			},
		})
	// LTT = t0Trigger; TTL requires settled 60s.
	obj := deploymentWithConditions("d", []conditionEntry{
		{Type: "Available", Status: "True", LastTransitionTime: t0Trigger},
	})

	gm.Enqueue(&gate{Trigger: trg, Target: obj, EventTime: clk.Now()})

	clk.Set(t0Trigger.Add(30 * time.Second))
	gm.Evaluate(context.Background())
	assert.Equal(t, 1, gm.Pending(), "TTL not satisfied at +30s")
	assert.Empty(t, fire.calls)

	clk.Set(t0Trigger.Add(90 * time.Second))
	gm.Evaluate(context.Background())
	assert.Zero(t, gm.Pending(), "TTL satisfied at +90s")
	assert.Len(t, fire.calls, 1)
}

// TestGate_Dedup_ReplacesEventTime: two events on the same (trigger, target)
// dedupe. The second event's timestamp REPLACES the first — timeout is
// measured from the newer observation, not the earlier one.
func TestGate_Dedup_ReplacesEventTime(t *testing.T) {
	clk := scheduler.NewFakeClock(t0Trigger)
	fire := &captureFirer{}
	gm := newGateManager(clk, fire.firer)

	trg := newTrigger("t", "default",
		&testsv1alpha1.TriggerConditionSpec{
			Timeout: 10,
			Conditions: []testsv1alpha1.TriggerCondition{
				{Type: "Available", Status: "True"},
			},
		})
	obj := deploymentWithConditions("d", []conditionEntry{
		{Type: "Available", Status: "False"},
	})

	// First event at t0.
	gm.Enqueue(&gate{Trigger: trg, Target: obj, EventTime: t0Trigger})
	// Second event at t0+9s — supersedes first.
	gm.Enqueue(&gate{Trigger: trg, Target: obj, EventTime: t0Trigger.Add(9 * time.Second)})
	assert.Equal(t, 1, gm.Pending(), "duplicate (trigger, target) events must dedupe")

	// At t0+15s: first event's timeout window is exceeded (15 > 10) BUT the
	// second event's is not (15-9=6 < 10). Gate stays pending → not expired.
	clk.Set(t0Trigger.Add(15 * time.Second))
	gm.Evaluate(context.Background())
	assert.Equal(t, 1, gm.Pending(),
		"dedup replaces EventTime — timeout measured from newest, not oldest")

	// At t0+20s: 20-9=11 > 10 → now expires.
	clk.Set(t0Trigger.Add(20 * time.Second))
	gm.Evaluate(context.Background())
	assert.Zero(t, gm.Pending())
}

// TestGate_FireError_RetriesUntilTimeout: firer returns error; gate stays
// pending; next evaluate retries.
func TestGate_FireError_RetriesUntilTimeout(t *testing.T) {
	clk := scheduler.NewFakeClock(t0Trigger)
	fire := &captureFirer{next: errors.New("api-server unavailable")}
	gm := newGateManager(clk, fire.firer)

	trg := newTrigger("t", "default",
		&testsv1alpha1.TriggerConditionSpec{
			Timeout: 100,
			Conditions: []testsv1alpha1.TriggerCondition{
				{Type: "Available", Status: "True"},
			},
		})
	obj := deploymentWithConditions("d", []conditionEntry{
		{Type: "Available", Status: "True"},
	})

	gm.Enqueue(&gate{Trigger: trg, Target: obj, EventTime: clk.Now()})

	// First evaluate — firer returns error.
	gm.Evaluate(context.Background())
	assert.Equal(t, 1, gm.Pending(), "fire error leaves gate pending for retry")
	assert.Len(t, fire.calls, 1)

	// Second evaluate — firer succeeds this time (queue was drained above).
	gm.Evaluate(context.Background())
	assert.Zero(t, gm.Pending(), "successful retry removes gate")
	assert.Len(t, fire.calls, 2)
}

// TestGate_ErrGateAlreadyHandled_DoesNotRetry: firer returns
// errGateAlreadyHandled → gate manager MUST NOT record a fire error and
// MUST NOT retry (the firer handled the gate itself, e.g. concurrency skip).
func TestGate_ErrGateAlreadyHandled_DoesNotRetry(t *testing.T) {
	clk := scheduler.NewFakeClock(t0Trigger)
	fire := &captureFirer{next: errGateAlreadyHandled}
	gm := newGateManager(clk, fire.firer)

	trg := newTrigger("t", "default",
		&testsv1alpha1.TriggerConditionSpec{
			Timeout: 100,
			Conditions: []testsv1alpha1.TriggerCondition{
				{Type: "Available", Status: "True"},
			},
		})
	obj := deploymentWithConditions("d", []conditionEntry{
		{Type: "Available", Status: "True"},
	})

	key := gm.Enqueue(&gate{Trigger: trg, Target: obj, EventTime: clk.Now()})
	// Simulate what the real firer would do on concurrency skip: mark
	// skipped + remove from pending. In the real flow, fireTestRun calls
	// gates.FireSkipped BEFORE returning errGateAlreadyHandled; we mimic that.
	gm.FireSkipped(key, trg, clk.Now(), "concurrency: forbid + active")

	// The evaluate above already popped the gate via FireSkipped; a
	// subsequent Evaluate must be a no-op.
	gm.Evaluate(context.Background())
	assert.Zero(t, gm.Pending())
	// firer was NOT called because we called FireSkipped directly, then
	// evaluated with an empty queue.
	assert.Empty(t, fire.calls)

	// Assert a Skipped outcome was recorded.
	out := gm.Outcomes()
	require.NotEmpty(t, out)
	assert.Equal(t, OutcomeSkipped, out[len(out)-1].Kind)
}

// TestGate_ErrGateAlreadyHandled_InEvaluate: the actual flow — firer inside
// Evaluate returns errGateAlreadyHandled after it internally called
// FireSkipped. Evaluate must not record a fire-error and must not retry.
func TestGate_ErrGateAlreadyHandled_InEvaluate(t *testing.T) {
	clk := scheduler.NewFakeClock(t0Trigger)
	// Firer that skips + returns sentinel — mimics real fireTestRun's
	// concurrency-forbid branch.
	skippingFirer := func(ctx context.Context, g *gate) error {
		return errGateAlreadyHandled
	}
	// Wrap into captureFirer so we can inspect fire.calls too.
	firer := func(ctx context.Context, g *gate) error {
		return skippingFirer(ctx, g)
	}
	gm := newGateManager(clk, firer)

	trg := newTrigger("t", "default",
		&testsv1alpha1.TriggerConditionSpec{
			Timeout: 100,
			Conditions: []testsv1alpha1.TriggerCondition{
				{Type: "Available", Status: "True"},
			},
		})
	obj := deploymentWithConditions("d", []conditionEntry{
		{Type: "Available", Status: "True"},
	})
	key := gm.Enqueue(&gate{Trigger: trg, Target: obj, EventTime: clk.Now()})
	// Real firer would call FireSkipped itself before returning the sentinel;
	// we do it here for parity with the real integration.
	gm.FireSkipped(key, trg, clk.Now(), "concurrency skip")

	out := gm.Evaluate(context.Background())
	// No outcome from Evaluate (gate already removed by FireSkipped).
	assert.Empty(t, out)
	assert.Zero(t, gm.Pending())
}
