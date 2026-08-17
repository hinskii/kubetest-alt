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

		// Queued prior counts as active too.
		{"1 queued prior, Forbid", []testsv1alpha1.TestRun{runWith("a", testsv1alpha1.PhaseQueued)}, PolicyForbid, ConcurrencyWait},

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
