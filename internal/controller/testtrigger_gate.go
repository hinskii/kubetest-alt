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
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/scheduler"
)

// gate is one pending TestTrigger evaluation — an event arrived on the
// watched resource, we've matched it against a trigger's selectors, and now
// we're waiting for ConditionSpec (delay/timeout/conditions) to resolve.
//
// EventTime is the CLOCK'S OBSERVATION OF THE EVENT, not the time we
// entered Evaluate. This is deliberate: conditionSpec.timeout MUST be
// measured from the event, not from operator start / gate creation drift /
// anything else the plan explicitly warned about.
type gate struct {
	Trigger   *testsv1alpha1.TestTrigger
	Target    *unstructured.Unstructured
	EventTime time.Time
}

// gateKey identifies a pending gate. Two events on the same target from the
// same trigger DEDUPE (the latest replaces the earlier) — a rapid burst of
// "modified" events on one deployment must not spawn N concurrent gates.
type gateKey struct {
	TriggerKey types.NamespacedName
	TargetGVK  string
	TargetKey  types.NamespacedName
}

// firerFn is the callback the gate manager invokes when a gate PASSES its
// gate (conditions met AND not concurrency-blocked). Extracted as an
// interface point so tests substitute a recorder without a real API server.
type firerFn func(ctx context.Context, g *gate) error

// gateOutcome is emitted whenever a gate exits the pending set. Kind is one
// of the const strings below. Tests inspect the outcome list to assert
// behavior deterministically.
type gateOutcome struct {
	Kind      string
	Key       gateKey
	Trigger   *testsv1alpha1.TestTrigger
	EventTime time.Time
	Message   string
}

// gateOutcome kinds.
const (
	OutcomeFired      = "fired"
	OutcomeFireError  = "fired-error"
	OutcomeExpired    = "expired"
	OutcomeSkipped    = "skipped-concurrency"
	OutcomeConditions = "conditions-not-met" // used for waiting still — not removed, but logged
)

// gateManager holds pending gates keyed by (trigger, target). Evaluate is
// side-effecting: it fires ready gates, expires overdue ones, and leaves
// still-waiting gates in place for a later Evaluate.
//
// The manager owns no goroutines — production wires a ticker calling
// Evaluate; tests advance the fake clock and call Evaluate synchronously.
// That's the same discipline the scheduler uses.
type gateManager struct {
	mu       sync.Mutex
	gates    map[gateKey]*gate
	clock    scheduler.Clock
	fire     firerFn
	outcomes []gateOutcome
	// outcomeLimit caps in-memory history so a busy cluster doesn't grow
	// this slice unbounded; 512 covers "look at what happened in the last
	// minute or two" for debugging without becoming a memory hazard.
	outcomeLimit int
}

func newGateManager(clock scheduler.Clock, fire firerFn) *gateManager {
	return &gateManager{
		gates:        map[gateKey]*gate{},
		clock:        clock,
		fire:         fire,
		outcomeLimit: 512,
	}
}

// Enqueue registers a fresh gate. If a gate for the same key already exists,
// the new one REPLACES it (event superseded); EventTime is updated so the
// timeout is measured from the latest observation.
func (m *gateManager) Enqueue(g *gate) gateKey {
	key := gateKeyFor(g)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gates[key] = g
	return key
}

// Pending returns the number of gates currently waiting — mostly for
// assertions in tests.
func (m *gateManager) Pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.gates)
}

// Outcomes returns a snapshot of the recorded outcomes.
func (m *gateManager) Outcomes() []gateOutcome {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]gateOutcome, len(m.outcomes))
	copy(out, m.outcomes)
	return out
}

// Reset clears state — used between test cases.
func (m *gateManager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gates = map[gateKey]*gate{}
	m.outcomes = nil
}

// Evaluate walks the pending set once. For each gate:
//   - if now - EventTime < delay              → still waiting, keep
//   - if ConditionsMet                        → fire, remove
//   - else if now - EventTime > delay+timeout → expire, remove
//   - else                                    → still waiting, keep
//
// Firing errors do NOT remove the gate; a subsequent Evaluate retries until
// the timeout elapses. The gate manager itself never emits k8s Events —
// consumers read gateOutcome for that.
func (m *gateManager) Evaluate(ctx context.Context) []gateOutcome {
	m.mu.Lock()
	snapshot := make([]*gate, 0, len(m.gates))
	keys := make([]gateKey, 0, len(m.gates))
	for k, g := range m.gates {
		snapshot = append(snapshot, g)
		keys = append(keys, k)
	}
	m.mu.Unlock()

	now := m.clock.Now()
	var results []gateOutcome
	for i, g := range snapshot {
		delay := time.Duration(gateDelay(g)) * time.Second
		timeout := time.Duration(gateTimeout(g)) * time.Second
		elapsed := now.Sub(g.EventTime)

		// Still in the pre-delay window — waiting.
		if elapsed < delay {
			continue
		}

		if ConditionsMet(g.Trigger.Spec.ConditionSpec, g.Target, now) {
			var oc gateOutcome
			if err := m.fire(ctx, g); err != nil {
				// The firer may have handled the gate itself (concurrency
				// skip, for instance) and returned errGateAlreadyHandled
				// so Evaluate doesn't record a spurious fire-error. Any
				// other error is a genuine failure — leave the gate in
				// place so a subsequent Evaluate retries until timeout.
				if errors.Is(err, errGateAlreadyHandled) {
					continue
				}
				oc = gateOutcome{Kind: OutcomeFireError, Key: keys[i], Trigger: g.Trigger,
					EventTime: g.EventTime, Message: err.Error()}
				m.mu.Lock()
				m.appendOutcomeLocked(oc)
				m.mu.Unlock()
				results = append(results, oc)
				continue
			}
			oc = gateOutcome{Kind: OutcomeFired, Key: keys[i], Trigger: g.Trigger, EventTime: g.EventTime}
			m.mu.Lock()
			delete(m.gates, keys[i])
			m.appendOutcomeLocked(oc)
			m.mu.Unlock()
			results = append(results, oc)
			continue
		}

		if timeout > 0 && elapsed > delay+timeout {
			oc := gateOutcome{Kind: OutcomeExpired, Key: keys[i], Trigger: g.Trigger,
				EventTime: g.EventTime, Message: "conditions not met before timeout"}
			m.mu.Lock()
			delete(m.gates, keys[i])
			m.appendOutcomeLocked(oc)
			m.mu.Unlock()
			results = append(results, oc)
		}
	}
	return results
}

// FireSkipped records a "skipped due to concurrencyPolicy" outcome without
// invoking the firer. Called by firerFn's caller when concurrency check
// short-circuits the fire path. Kept as a public entry so the firer body
// stays a pure "create TestRun" without concurrency branch logic muddying
// the outcome accounting.
//
// Also removes the gate from the pending set — a concurrency skip is a
// terminal decision for THIS event; the next event on the target will
// enqueue a fresh gate.
func (m *gateManager) FireSkipped(key gateKey, t *testsv1alpha1.TestTrigger, eventTime time.Time, reason string) {
	oc := gateOutcome{Kind: OutcomeSkipped, Key: key, Trigger: t, EventTime: eventTime, Message: reason}
	m.mu.Lock()
	delete(m.gates, key)
	m.appendOutcomeLocked(oc)
	m.mu.Unlock()
}

func (m *gateManager) appendOutcomeLocked(oc gateOutcome) {
	m.outcomes = append(m.outcomes, oc)
	if len(m.outcomes) > m.outcomeLimit {
		// Drop the oldest half so we amortize the shift and don't reshuffle
		// on every append.
		m.outcomes = append([]gateOutcome{}, m.outcomes[len(m.outcomes)/2:]...)
	}
}

func gateDelay(g *gate) int32 {
	if g == nil || g.Trigger == nil || g.Trigger.Spec.ConditionSpec == nil {
		return 0
	}
	return g.Trigger.Spec.ConditionSpec.Delay
}

func gateTimeout(g *gate) int32 {
	if g == nil || g.Trigger == nil || g.Trigger.Spec.ConditionSpec == nil {
		return 0
	}
	return g.Trigger.Spec.ConditionSpec.Timeout
}

func gateKeyFor(g *gate) gateKey {
	return gateKey{
		TriggerKey: types.NamespacedName{Namespace: g.Trigger.Namespace, Name: g.Trigger.Name},
		TargetGVK:  g.Target.GroupVersionKind().String(),
		TargetKey:  types.NamespacedName{Namespace: g.Target.GetNamespace(), Name: g.Target.GetName()},
	}
}
