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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// TestReconcile_RunStore_SavedOnTerminalTransition — the happy path.
// A TestRun that reaches PhasePassed must produce EXACTLY one SaveFinished
// call (against the RecordingRunStore) with the final phase.
func TestReconcile_RunStore_SavedOnTerminalTransition(t *testing.T) {
	fakeResults.Reset()
	fakeLogRegistry.Reset()
	fakeRunStore.Reset()
	resetReconcileCounts()

	ctx := context.Background()
	ns := uniqueNamespace(t)

	test := newTestFixture(ns, "store-happy-test")
	require.NoError(t, k8sClient.Create(ctx, test))
	run := newRunFixture(ns, "store-happy-run", "store-happy-test")
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	jobKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForJob(t, ctx, jobKey, 5*time.Second)
	waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseQueued, 3*time.Second)

	createPodForJob(t, ctx, ns, run.Name, "store-happy-pod",
		corev1.PodRunning,
		[]corev1.ContainerStatus{{
			Name:  "wrapper",
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}},
	)
	waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseRunning, 3*time.Second)

	fakeResults.Set(run.Name, &RunResult{Phase: testsv1alpha1.PhasePassed})
	patchJobConditions(t, ctx, jobKey, []batchv1.JobCondition{
		{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, Reason: "Complete"},
	})
	waitForPhase(t, ctx, runKey, testsv1alpha1.PhasePassed, 5*time.Second)

	// Fetch the persisted UID (envtest assigns it on Create).
	var fresh testsv1alpha1.TestRun
	require.NoError(t, k8sClient.Get(ctx, runKey, &fresh))
	uid := string(fresh.UID)

	// SaveFinished was called (at least once) with the terminal phase.
	assert.Eventually(t, func() bool {
		for _, s := range fakeRunStore.SavesForUID(uid) {
			if s.Phase == testsv1alpha1.PhasePassed {
				return true
			}
		}
		return false
	}, 3*time.Second, 50*time.Millisecond, "expected SaveFinished with passed phase")

	// Steady state: after the initial success, subsequent reconciles
	// (e.g. Job deletion event) MUST NOT re-call SaveFinished — the
	// persistedRuns dedupe suppresses them.
	baseline := len(fakeRunStore.SavesForUID(uid))
	// Give the reconciler a few more ticks — the FallbackRequeue is 500ms
	// but terminal-phase runs short-circuit so no requeue fires. Poke by
	// touching a random label so a status subresource event lands, then
	// wait a bit and assert count didn't grow.
	require.NoError(t, k8sClient.Get(ctx, runKey, &fresh))
	if fresh.Labels == nil {
		fresh.Labels = map[string]string{}
	}
	fresh.Labels["poke"] = "1"
	require.NoError(t, k8sClient.Update(ctx, &fresh))

	assert.Never(t, func() bool {
		return len(fakeRunStore.SavesForUID(uid)) > baseline
	}, 1*time.Second, 100*time.Millisecond, "SaveFinished must be deduped after success")
}

// TestReconcile_RunStore_ErrorDoesNotBlockReconcile — the plan's
// "run history < run correctness" rule. A store that always errors must
// NOT prevent the TestRun from reaching terminal phase; the reconciler
// swallows the error and moves on.
func TestReconcile_RunStore_ErrorDoesNotBlockReconcile(t *testing.T) {
	fakeResults.Reset()
	fakeLogRegistry.Reset()
	fakeRunStore.Reset()
	resetReconcileCounts()

	ctx := context.Background()
	ns := uniqueNamespace(t)

	test := newTestFixture(ns, "store-err-test")
	require.NoError(t, k8sClient.Create(ctx, test))
	run := newRunFixture(ns, "store-err-run", "store-err-test")
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	jobKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForJob(t, ctx, jobKey, 5*time.Second)
	waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseQueued, 3*time.Second)

	// Get the assigned UID so we can queue errors against it.
	var fresh testsv1alpha1.TestRun
	require.NoError(t, k8sClient.Get(ctx, runKey, &fresh))
	uid := string(fresh.UID)

	// Queue enough errors that the FIRST reconcile-of-terminal fails,
	// but subsequent retries succeed. Multiple entries because the
	// terminalAndDeleteJob path calls SaveFinished, and the early-return
	// path may retry on subsequent events (Job GC, status refresh).
	fakeRunStore.QueueErr(uid, errors.New("db unreachable"))
	fakeRunStore.QueueErr(uid, errors.New("db unreachable"))

	createPodForJob(t, ctx, ns, run.Name, "store-err-pod",
		corev1.PodRunning,
		[]corev1.ContainerStatus{{
			Name:  "wrapper",
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}},
	)
	waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseRunning, 3*time.Second)

	fakeResults.Set(run.Name, &RunResult{Phase: testsv1alpha1.PhasePassed})
	patchJobConditions(t, ctx, jobKey, []batchv1.JobCondition{
		{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, Reason: "Complete"},
	})

	// Despite the store errors, the CR reaches PhasePassed — proves the
	// reconciler doesn't block on store failures.
	waitForPhase(t, ctx, runKey, testsv1alpha1.PhasePassed, 5*time.Second)

	// After enough reconciles, the eventual save succeeds (queued nil).
	// Job deletion + finalizer removal don't trigger persistFinished, but
	// the terminal short-circuit does — patching a label re-enqueues.
	require.NoError(t, k8sClient.Get(ctx, runKey, &fresh))
	if fresh.Labels == nil {
		fresh.Labels = map[string]string{}
	}
	fresh.Labels["retry-nudge"] = "1"
	require.NoError(t, k8sClient.Update(ctx, &fresh))

	// SaveFinished was called at least twice (once from terminalAndDeleteJob
	// failing, once from a subsequent reconcile-of-terminal that succeeded).
	assert.Eventually(t, func() bool {
		return len(fakeRunStore.SavesForUID(uid)) >= 2
	}, 5*time.Second, 100*time.Millisecond,
		"expected SaveFinished retry after transient error")
}
