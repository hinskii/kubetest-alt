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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/compiler"
)

// setAbort patches spec.abort the way the API server does.
func setAbort(t *testing.T, ctx context.Context, key client.ObjectKey, req testsv1alpha1.AbortRequest) {
	t.Helper()
	var run testsv1alpha1.TestRun
	require.NoError(t, k8sClient.Get(ctx, key, &run))
	patch := client.MergeFrom(run.DeepCopy())
	run.Spec.Abort = &req
	require.NoError(t, k8sClient.Patch(ctx, &run, patch))
}

// persistedPhase reports the phase the run store recorded for uid, or "".
func persistedPhase(uid string) testsv1alpha1.Phase {
	saves := fakeRunStore.SavesForUID(uid)
	if len(saves) == 0 {
		return ""
	}
	return saves[len(saves)-1].Phase
}

// A user abort must take the full terminal path: aborted phase with the
// requester in the message, Job deleted, run persisted, tailer stopped.
// The old GUI "stop" deleted the CR instead, which skipped persistence.
func TestReconcile_Abort_UserAbortsRunningRun(t *testing.T) {
	fakeResults.Reset()
	fakeRunStore.Reset()
	fakeLogRegistry.Reset()
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, newTestFixture(ns, "abort-me")))
	run := newRunFixture(ns, "abort-me-run", "abort-me")
	require.NoError(t, k8sClient.Create(ctx, run))
	key := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForJob(t, ctx, key, 3*time.Second)

	setAbort(t, ctx, key, testsv1alpha1.AbortRequest{
		Reason: testsv1alpha1.AbortReasonUser, RequestedBy: "alice@example.com",
	})

	final := waitForPhase(t, ctx, key, testsv1alpha1.PhaseAborted, 5*time.Second)
	assert.Contains(t, final.Status.Message, ReasonAbortedByUser)
	assert.Contains(t, final.Status.Message, "alice@example.com")
	assert.NotNil(t, final.Status.FinishedAt)

	assert.Eventually(t, func() bool {
		return persistedPhase(string(final.UID)) == testsv1alpha1.PhaseAborted
	}, 3*time.Second, 50*time.Millisecond, "aborted run must reach run history")

	assert.Eventually(t, func() bool {
		var job batchv1.Job
		err := k8sClient.Get(ctx, key, &job)
		return apierrors.IsNotFound(err) || job.DeletionTimestamp != nil
	}, 3*time.Second, 50*time.Millisecond, "Job must be deleted on abort")

	assert.Eventually(t, func() bool {
		for _, c := range fakeLogRegistry.CallsForRun(run.Name) {
			if c.Kind == "stop" {
				return true
			}
		}
		return false
	}, 3*time.Second, 50*time.Millisecond, "log tailer must be stopped on abort")
}

// A run still waiting behind a Forbid prior (queued, no Job) can be
// aborted too, and must never get a Job afterwards.
func TestReconcile_Abort_QueuedWaitingRun(t *testing.T) {
	fakeResults.Reset()
	fakeRunStore.Reset()
	ctx := context.Background()
	ns := uniqueNamespace(t)
	test := newTestFixture(ns, "forbid-abort")
	test.Spec.ConcurrencyPolicy = PolicyForbid
	require.NoError(t, k8sClient.Create(ctx, test))

	run1 := newRunFixture(ns, "forbid-abort-1", "forbid-abort")
	require.NoError(t, k8sClient.Create(ctx, run1))
	waitForJob(t, ctx, client.ObjectKey{Namespace: ns, Name: run1.Name}, 3*time.Second)

	run2 := newRunFixture(ns, "forbid-abort-2", "forbid-abort")
	require.NoError(t, k8sClient.Create(ctx, run2))
	key2 := client.ObjectKey{Namespace: ns, Name: run2.Name}
	waitForPhase(t, ctx, key2, testsv1alpha1.PhaseQueued, 3*time.Second)

	setAbort(t, ctx, key2, testsv1alpha1.AbortRequest{Reason: testsv1alpha1.AbortReasonUser})
	final := waitForPhase(t, ctx, key2, testsv1alpha1.PhaseAborted, 5*time.Second)
	assert.Eventually(t, func() bool {
		return persistedPhase(string(final.UID)) == testsv1alpha1.PhaseAborted
	}, 3*time.Second, 50*time.Millisecond)

	// Releasing run1 must not resurrect run2.
	fakeResults.Set(run1.Name, &RunResult{Phase: testsv1alpha1.PhasePassed})
	patchJobConditions(t, ctx, client.ObjectKey{Namespace: ns, Name: run1.Name},
		[]batchv1.JobCondition{{Type: batchv1.JobComplete, Status: "True"}})
	waitForPhase(t, ctx, client.ObjectKey{Namespace: ns, Name: run1.Name}, testsv1alpha1.PhasePassed, 5*time.Second)
	assert.Never(t, func() bool {
		var job batchv1.Job
		return k8sClient.Get(ctx, key2, &job) == nil
	}, 1500*time.Millisecond, 100*time.Millisecond, "an aborted run must never get a Job")
}

// Aborting a composite parent aborts its running children (they stay in
// the cluster and reach history) and ends the parent as aborted.
func TestReconcile_Abort_CompositeParentAbortsChildren(t *testing.T) {
	fakeResults.Reset()
	fakeRunStore.Reset()
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, newTestFixture(ns, "leaf")))
	parent := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "comp-abort", Namespace: ns},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: PolicyAllow,
			Steps: []testsv1alpha1.Step{{
				Name:    "only",
				Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "leaf"}}},
			}},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, parent))
	parentRun := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{
			Name: "comp-abort-run", Namespace: ns,
			Labels: map[string]string{compiler.LabelKubetestTool: "composite"},
		},
		Spec: testsv1alpha1.TestRunSpec{TestRef: "comp-abort", Source: "api"},
	}
	require.NoError(t, k8sClient.Create(ctx, parentRun))
	parentKey := client.ObjectKey{Namespace: ns, Name: parentRun.Name}
	waitForPhase(t, ctx, parentKey, testsv1alpha1.PhaseRunning, 5*time.Second)
	require.Eventually(t, func() bool { return len(listChildren(t, ctx, ns, parentRun.Name)) == 1 },
		3*time.Second, 50*time.Millisecond)

	setAbort(t, ctx, parentKey, testsv1alpha1.AbortRequest{Reason: testsv1alpha1.AbortReasonUser, RequestedBy: "bob"})

	final := waitForPhase(t, ctx, parentKey, testsv1alpha1.PhaseAborted, 5*time.Second)
	assert.Contains(t, final.Status.Message, ReasonAbortedByUser)
	assert.Eventually(t, func() bool {
		kids := listChildren(t, ctx, ns, parentRun.Name)
		return len(kids) == 1 && kids[0].Status.Phase == testsv1alpha1.PhaseAborted &&
			kids[0].Spec.Abort != nil && kids[0].Spec.Abort.Reason == testsv1alpha1.AbortReasonParent
	}, 5*time.Second, 50*time.Millisecond, "child must be aborted (not deleted) with reason Parent")
	assert.Eventually(t, func() bool {
		return persistedPhase(string(final.UID)) == testsv1alpha1.PhaseAborted
	}, 3*time.Second, 50*time.Millisecond, "composite parent must reach run history")
}

// Step timeout used to DELETE children; ensureStepChildren then re-created
// them on every requeue (fixes.md #7). Now the child is aborted and kept,
// and the step's "step timeout exceeded" verdict sticks while LATER steps
// keep the parent reconciling. (With a single step the parent terminates
// in the same reconcile and the bug never shows — hence the second,
// condition=always step that stays running.)
func TestReconcile_Composite_StepTimeoutAbortsChildrenOnce(t *testing.T) {
	fakeResults.Reset()
	fakeRunStore.Reset()
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, newTestFixture(ns, "slow")))
	require.NoError(t, k8sClient.Create(ctx, newTestFixture(ns, "cleanup")))
	parent := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "comp-timeout", Namespace: ns},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: PolicyAllow,
			Steps: []testsv1alpha1.Step{
				{
					Name:    "slow-step",
					Timeout: &metav1.Duration{Duration: time.Second},
					Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "slow"}}},
				},
				{
					Name:      "cleanup-step",
					Condition: "always",
					Execute:   &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "cleanup"}}},
				},
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, parent))
	parentRun := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{
			Name: "comp-timeout-run", Namespace: ns,
			Labels: map[string]string{compiler.LabelKubetestTool: "composite"},
		},
		Spec: testsv1alpha1.TestRunSpec{TestRef: "comp-timeout", Source: "api"},
	}
	require.NoError(t, k8sClient.Create(ctx, parentRun))
	parentKey := client.ObjectKey{Namespace: ns, Name: parentRun.Name}

	stepKids := func(step string) []testsv1alpha1.TestRun {
		var out []testsv1alpha1.TestRun
		for _, k := range listChildren(t, ctx, ns, parentRun.Name) {
			if k.Labels[compiler.LabelStep] == step {
				out = append(out, k)
			}
		}
		return out
	}

	// Step 0 times out → its child is aborted; step 1 starts and stays
	// running (nothing drives its phase in envtest).
	require.Eventually(t, func() bool {
		k0 := stepKids("0")
		return len(k0) == 1 && k0[0].Status.Phase == testsv1alpha1.PhaseAborted && len(stepKids("1")) == 1
	}, 15*time.Second, 100*time.Millisecond, "step-0 child aborted and step 1 started")

	// While step 1 keeps the parent reconciling (3s requeues), step 0 must
	// keep its timeout verdict and must not get a new child.
	assert.Never(t, func() bool {
		var p testsv1alpha1.TestRun
		if err := k8sClient.Get(ctx, parentKey, &p); err != nil {
			return true
		}
		s0 := p.Status.Steps["s0"]
		return len(stepKids("0")) != 1 || s0.Phase != testsv1alpha1.StepPhaseFailed ||
			s0.Message != "step timeout exceeded"
	}, 7*time.Second, 200*time.Millisecond, "step-0 verdict must stick and its child must not be re-created")

	forceChildPhase(t, ctx, ns, stepKids("1")[0].Name, testsv1alpha1.PhasePassed)
	final := waitForPhase(t, ctx, parentKey, testsv1alpha1.PhaseFailed, 10*time.Second)
	assert.Equal(t, "step timeout exceeded", final.Status.Steps["s0"].Message)
}
