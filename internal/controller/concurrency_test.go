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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

func runWith(uid string, phase testsv1alpha1.Phase) testsv1alpha1.TestRun {
	return testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r-" + uid, UID: types.UID(uid)},
		Status:     testsv1alpha1.TestRunStatus{Phase: phase},
	}
}

// younger gives r a creation time after self's (zero) one.
func younger(r testsv1alpha1.TestRun) testsv1alpha1.TestRun {
	r.CreationTimestamp = metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	return r
}

func startedRun(uid string) testsv1alpha1.TestRun {
	r := runWith(uid, testsv1alpha1.PhaseRunning)
	r.Status.ResolvedSpec = "{}"
	return r
}

// fixes.md #5: two runs waiting behind a finished Forbid run used to count
// each other as active and wait forever. Exactly one — the older — goes.
func TestDecideConcurrency_ForbidWaitingRunsDontDeadlock(t *testing.T) {
	t0 := metav1.NewTime(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	older := runWith("older", testsv1alpha1.PhaseQueued)
	older.CreationTimestamp = t0
	younger := runWith("younger", testsv1alpha1.PhaseQueued)
	younger.CreationTimestamp = metav1.NewTime(t0.Add(time.Second))
	done := startedRun("done")
	done.Status.Phase = testsv1alpha1.PhasePassed
	all := []testsv1alpha1.TestRun{done, older, younger}

	assert.Equal(t, ConcurrencyProceed, DecideConcurrency(all, &older, PolicyForbid))
	assert.Equal(t, ConcurrencyWait, DecideConcurrency(all, &younger, PolicyForbid))

	// Once the older one has started, the younger keeps waiting for it.
	older.Status.ResolvedSpec, older.Status.Phase = "{}", testsv1alpha1.PhaseRunning
	assert.Equal(t, ConcurrencyWait, DecideConcurrency([]testsv1alpha1.TestRun{done, older, younger}, &younger, PolicyForbid))

	// Same creation second: name breaks the tie, still exactly one goes.
	a, b := runWith("a", testsv1alpha1.PhaseQueued), runWith("b", testsv1alpha1.PhaseQueued)
	pair := []testsv1alpha1.TestRun{a, b}
	assert.Equal(t, ConcurrencyProceed, DecideConcurrency(pair, &a, PolicyForbid))
	assert.Equal(t, ConcurrencyWait, DecideConcurrency(pair, &b, PolicyForbid))
}

func TestDecideConcurrency_TableDriven(t *testing.T) {
	// self is always distinct (uid=self); priors are inspected for their phase.
	self := &testsv1alpha1.TestRun{ObjectMeta: metav1.ObjectMeta{Name: "self", UID: "self"}}

	cases := []struct {
		name   string
		prior  []testsv1alpha1.TestRun
		policy string
		want   ConcurrencyAction
	}{
		// No active priors → always Proceed regardless of policy.
		{"no priors, Allow", nil, PolicyAllow, ConcurrencyProceed},
		{"no priors, Forbid", nil, PolicyForbid, ConcurrencyProceed},
		{"no priors, Replace", nil, PolicyReplace, ConcurrencyProceed},
		{"no priors, empty policy defaults Allow", nil, "", ConcurrencyProceed},

		// All priors terminal → Proceed.
		{
			"all priors passed, Forbid",
			[]testsv1alpha1.TestRun{
				runWith("a", testsv1alpha1.PhasePassed),
				runWith("b", testsv1alpha1.PhaseFailed),
				runWith("c", testsv1alpha1.PhaseError),
				runWith("d", testsv1alpha1.PhaseAborted),
			},
			PolicyForbid,
			ConcurrencyProceed,
		},

		// One active prior + policy variants.
		{"1 running prior, Allow", []testsv1alpha1.TestRun{runWith("a", testsv1alpha1.PhaseRunning)}, PolicyAllow, ConcurrencyProceed},
		{"1 running prior, Forbid", []testsv1alpha1.TestRun{runWith("a", testsv1alpha1.PhaseRunning)}, PolicyForbid, ConcurrencyWait},
		{"1 running prior, Replace", []testsv1alpha1.TestRun{runWith("a", testsv1alpha1.PhaseRunning)}, PolicyReplace, ConcurrencyReplacePrior},

		// An older queued prior goes first (runs queue in creation order:
		// "r-a" sorts before "self" at equal creation time).
		{"1 older queued prior, Forbid", []testsv1alpha1.TestRun{runWith("a", testsv1alpha1.PhaseQueued)}, PolicyForbid, ConcurrencyWait},
		// A younger queued prior that hasn't started doesn't block self.
		{"1 younger queued prior, Forbid", []testsv1alpha1.TestRun{younger(runWith("z", testsv1alpha1.PhaseQueued))}, PolicyForbid, ConcurrencyProceed},
		// A younger prior that already started does.
		{"1 younger started prior, Forbid", []testsv1alpha1.TestRun{younger(startedRun("z"))}, PolicyForbid, ConcurrencyWait},

		// Paused counts as active (test isn't done).
		{"1 paused prior, Forbid", []testsv1alpha1.TestRun{runWith("a", testsv1alpha1.PhasePaused)}, PolicyForbid, ConcurrencyWait},

		// Mixed — 1 active out of many terminal.
		{
			"mixed priors with 1 active, Forbid",
			[]testsv1alpha1.TestRun{
				runWith("a", testsv1alpha1.PhasePassed),
				runWith("b", testsv1alpha1.PhaseRunning),
				runWith("c", testsv1alpha1.PhaseFailed),
			},
			PolicyForbid,
			ConcurrencyWait,
		},

		// Unknown policy → treated as Allow (defensive).
		{"unknown policy defaults Allow", []testsv1alpha1.TestRun{runWith("a", testsv1alpha1.PhaseRunning)}, "SomeFuturePolicy", ConcurrencyProceed},

		// Self appears in priors — must be skipped.
		{
			"self in list is not counted",
			[]testsv1alpha1.TestRun{{
				ObjectMeta: metav1.ObjectMeta{Name: "self", UID: "self"},
				Status:     testsv1alpha1.TestRunStatus{Phase: testsv1alpha1.PhaseRunning},
			}},
			PolicyForbid,
			ConcurrencyProceed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideConcurrency(tc.prior, self, tc.policy)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSelectPriorsToAbort_SkipsTerminalAndSelf(t *testing.T) {
	self := &testsv1alpha1.TestRun{ObjectMeta: metav1.ObjectMeta{UID: "self"}}
	prior := []testsv1alpha1.TestRun{
		runWith("a", testsv1alpha1.PhaseRunning),
		runWith("b", testsv1alpha1.PhasePassed),
		{ObjectMeta: metav1.ObjectMeta{UID: "self"}, Status: testsv1alpha1.TestRunStatus{Phase: testsv1alpha1.PhaseRunning}},
		runWith("c", testsv1alpha1.PhaseQueued),
	}
	out := SelectPriorsToAbort(prior, self)
	assert.Len(t, out, 2, "should abort 'a' (Running) and 'c' (Queued), skip terminal and self")
	uids := map[types.UID]bool{}
	for _, r := range out {
		uids[r.UID] = true
	}
	assert.True(t, uids["a"])
	assert.True(t, uids["c"])
}

func TestIsTerminalPhase(t *testing.T) {
	terminal := []testsv1alpha1.Phase{
		testsv1alpha1.PhasePassed,
		testsv1alpha1.PhaseFailed,
		testsv1alpha1.PhaseError,
		testsv1alpha1.PhaseAborted,
	}
	nonTerminal := []testsv1alpha1.Phase{
		testsv1alpha1.PhaseQueued,
		testsv1alpha1.PhaseRunning,
		testsv1alpha1.PhasePaused,
		"", // empty
	}
	for _, p := range terminal {
		assert.True(t, IsTerminalPhase(p), "phase %q must be terminal", p)
	}
	for _, p := range nonTerminal {
		assert.False(t, IsTerminalPhase(p), "phase %q must NOT be terminal", p)
	}
}
