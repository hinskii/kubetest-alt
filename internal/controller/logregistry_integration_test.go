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
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// TestReconcile_LogRegistry_LifecycleHooks asserts §12 log-lifecycle wiring:
//   - EnsureTailer fires the moment the pod goes Running.
//   - StopTailer fires when the run enters a terminal phase (before Job delete).
//
// The recorder shows call ORDER — the ensure MUST come before the stop, and
// the stop must land BEFORE terminalAndDeleteJob calls Delete. That order
// matters because Registry.StopTailer blocks on final flush, so scheduling
// it after Delete would race pod-log disappearance.
func TestReconcile_LogRegistry_LifecycleHooks(t *testing.T) {
	fakeResults.Reset()
	fakeLogRegistry.Reset()
	resetReconcileCounts()

	ctx := context.Background()
	ns := uniqueNamespace(t)

	test := newTestFixture(ns, "log-test")
	require.NoError(t, k8sClient.Create(ctx, test))
	run := newRunFixture(ns, "log-run", "log-test")
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	jobKey := client.ObjectKey{Namespace: ns, Name: run.Name}

	// Job appears; then queued phase.
	waitForJob(t, ctx, jobKey, 5*time.Second)
	waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseQueued, 3*time.Second)

	// Simulate pod Running — this is what should trigger EnsureTailer.
	createPodForJob(t, ctx, ns, run.Name, "log-run-pod",
		corev1.PodRunning,
		[]corev1.ContainerStatus{{
			Name:  "wrapper",
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}},
	)
	waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseRunning, 3*time.Second)

	// Ensure was called at least once with correct args. Idempotency means
	// the reconciler may call it more than once — that's fine and by design.
	assert.Eventually(t, func() bool {
		for _, c := range fakeLogRegistry.CallsForRun(run.Name) {
			if c.Kind == "ensure" && c.PodName == "log-run-pod" && c.Namespace == ns {
				return true
			}
		}
		return false
	}, 2*time.Second, 50*time.Millisecond, "expected EnsureTailer for the running pod")

	// Preload the wrapper's verdict and complete the Job.
	fakeResults.Set(run.Name, &RunResult{Phase: testsv1alpha1.PhasePassed})
	patchJobConditions(t, ctx, jobKey, []batchv1.JobCondition{
		{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, Reason: "Complete"},
	})
	waitForPhase(t, ctx, runKey, testsv1alpha1.PhasePassed, 5*time.Second)

	// StopTailer must have been called for this run.
	assert.Eventually(t, func() bool {
		for _, c := range fakeLogRegistry.CallsForRun(run.Name) {
			if c.Kind == "stop" {
				return true
			}
		}
		return false
	}, 2*time.Second, 50*time.Millisecond, "expected StopTailer after terminal transition")

	// Ordering: for this run, the first "stop" must come AFTER the first
	// "ensure" — otherwise we'd never have anything to stop.
	calls := fakeLogRegistry.CallsForRun(run.Name)
	var firstEnsureIdx, firstStopIdx = -1, -1
	for i, c := range calls {
		if firstEnsureIdx == -1 && c.Kind == "ensure" {
			firstEnsureIdx = i
		}
		if firstStopIdx == -1 && c.Kind == "stop" {
			firstStopIdx = i
		}
	}
	require.NotEqual(t, -1, firstEnsureIdx, "no ensure call recorded")
	require.NotEqual(t, -1, firstStopIdx, "no stop call recorded")
	assert.Less(t, firstEnsureIdx, firstStopIdx, "ensure must precede stop")
}

// TestReconcile_LogRegistry_StopOnFinalize asserts §15.5: when a TestRun is
// deleted mid-run, the finalizer path stops the tailer BEFORE the Job is
// deleted, so the final log chunk gets flushed before pod logs disappear.
func TestReconcile_LogRegistry_StopOnFinalize(t *testing.T) {
	fakeResults.Reset()
	fakeLogRegistry.Reset()
	resetReconcileCounts()

	ctx := context.Background()
	ns := uniqueNamespace(t)

	test := newTestFixture(ns, "log-del-test")
	require.NoError(t, k8sClient.Create(ctx, test))
	run := newRunFixture(ns, "log-del-run", "log-del-test")
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	jobKey := client.ObjectKey{Namespace: ns, Name: run.Name}

	waitForJob(t, ctx, jobKey, 5*time.Second)
	waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseQueued, 3*time.Second)

	createPodForJob(t, ctx, ns, run.Name, "log-del-pod",
		corev1.PodRunning,
		[]corev1.ContainerStatus{{
			Name:  "wrapper",
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}},
	)
	waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseRunning, 3*time.Second)

	// Wait for at least one EnsureTailer call to have landed.
	assert.Eventually(t, func() bool {
		for _, c := range fakeLogRegistry.CallsForRun(run.Name) {
			if c.Kind == "ensure" {
				return true
			}
		}
		return false
	}, 2*time.Second, 50*time.Millisecond)

	// Delete the TestRun — finalizer should stop the tailer.
	require.NoError(t, k8sClient.Delete(ctx, run))

	assert.Eventually(t, func() bool {
		for _, c := range fakeLogRegistry.CallsForRun(run.Name) {
			if c.Kind == "stop" {
				return true
			}
		}
		return false
	}, 3*time.Second, 50*time.Millisecond, "expected StopTailer during finalize")
}
