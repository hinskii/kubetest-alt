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
	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// ConcurrencyAction is what the reconciler should do about a new TestRun when
// prior runs for the same Test are still active.
type ConcurrencyAction int

const (
	// ConcurrencyProceed means the new run may start immediately.
	ConcurrencyProceed ConcurrencyAction = iota
	// ConcurrencyWait means the new run stays queued; the reconciler requeues
	// and rechecks periodically. Used by concurrencyPolicy=Forbid.
	ConcurrencyWait
	// ConcurrencyReplacePrior means active priors must be aborted first.
	// Used by concurrencyPolicy=Replace.
	ConcurrencyReplacePrior
)

// Concurrency policy string values (mirror the CRD enum on Test.spec.concurrencyPolicy).
const (
	PolicyAllow   = "Allow"
	PolicyForbid  = "Forbid"
	PolicyReplace = "Replace"
)

// activePriorCount counts prior runs that are neither terminal nor the run
// under consideration. `self` may be nil to count all non-terminal.
func activePriorCount(prior []testsv1alpha1.TestRun, self *testsv1alpha1.TestRun) int {
	n := 0
	for i := range prior {
		p := &prior[i]
		if self != nil && p.UID == self.UID {
			continue
		}
		if !IsTerminalPhase(p.Status.Phase) {
			n++
		}
	}
	return n
}

// DecideConcurrency picks the action for a NEW run given the priors (all
// TestRuns for the same Test in the same namespace, EXCLUDING self) and the
// Test's ConcurrencyPolicy. Empty policy is treated as Allow.
//
// Pure function — trivially unit-testable.
func DecideConcurrency(prior []testsv1alpha1.TestRun, self *testsv1alpha1.TestRun, policy string) ConcurrencyAction {
	active := activePriorCount(prior, self)
	if active == 0 {
		return ConcurrencyProceed
	}
	switch policy {
	case PolicyForbid:
		return ConcurrencyWait
	case PolicyReplace:
		return ConcurrencyReplacePrior
	default:
		// PolicyAllow, empty, or unknown — proceed (defensive default).
		return ConcurrencyProceed
	}
}

// SelectPriorsToAbort returns the subset of priors that should be aborted
// under Replace. Currently returns every active prior (i.e. we abort all
// still-running runs before starting the new one). Extracted so tests can
// pin the policy explicitly.
func SelectPriorsToAbort(prior []testsv1alpha1.TestRun, self *testsv1alpha1.TestRun) []*testsv1alpha1.TestRun {
	var out []*testsv1alpha1.TestRun
	for i := range prior {
		p := &prior[i]
		if self != nil && p.UID == self.UID {
			continue
		}
		if !IsTerminalPhase(p.Status.Phase) {
			out = append(out, p)
		}
	}
	return out
}
