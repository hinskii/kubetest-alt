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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

func scheduledRun(ns, name, testRef string, in time.Duration) *testsv1alpha1.TestRun {
	run := newRunFixture(ns, name, testRef)
	at := metav1.NewTime(time.Now().Add(in))
	run.Spec.NotBefore = &at
	return run
}

func hasJob(ctx context.Context, key client.ObjectKey) bool {
	var job batchv1.Job
	return k8sClient.Get(ctx, key, &job) == nil
}

// A scheduled run waits in queued with no Job and no resolved spec, then
// starts on its own once notBefore passes.
func TestReconcile_NotBefore_WaitsThenStarts(t *testing.T) {
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, newTestFixture(ns, "later")))
	run := scheduledRun(ns, "later-run", "later", 2*time.Second)
	require.NoError(t, k8sClient.Create(ctx, run))
	key := client.ObjectKey{Namespace: ns, Name: run.Name}

	waiting := waitForPhase(t, ctx, key, testsv1alpha1.PhaseQueued, 3*time.Second)
	assert.Contains(t, waiting.Status.Message, "scheduled: starts at ")
	assert.Empty(t, waiting.Status.ResolvedSpec, "the Test is resolved when the run starts, not when it is scheduled")
	assert.NotNil(t, waiting.Status.QueuedAt)
	assert.False(t, hasJob(ctx, key))

	waitForJob(t, ctx, key, 5*time.Second)
}

// A run scheduled for later is not active: an immediate run of a Forbid
// Test must not wait for it, and a Replace run must not abort it.
func TestReconcile_NotBefore_IgnoredByConcurrency(t *testing.T) {
	for _, policy := range []string{PolicyForbid, PolicyReplace} {
		t.Run(policy, func(t *testing.T) {
			ctx := context.Background()
			ns := uniqueNamespace(t)
			test := newTestFixture(ns, "conc")
			test.Spec.ConcurrencyPolicy = policy
			require.NoError(t, k8sClient.Create(ctx, test))

			later := scheduledRun(ns, "conc-later", "conc", time.Hour)
			require.NoError(t, k8sClient.Create(ctx, later))
			laterKey := client.ObjectKey{Namespace: ns, Name: later.Name}
			waitForPhase(t, ctx, laterKey, testsv1alpha1.PhaseQueued, 3*time.Second)

			now := newRunFixture(ns, "conc-now", "conc")
			require.NoError(t, k8sClient.Create(ctx, now))
			waitForJob(t, ctx, client.ObjectKey{Namespace: ns, Name: now.Name}, 3*time.Second)

			var got testsv1alpha1.TestRun
			require.NoError(t, k8sClient.Get(ctx, laterKey, &got))
			assert.Equal(t, testsv1alpha1.PhaseQueued, got.Status.Phase)
			assert.Nil(t, got.Spec.Abort, "a scheduled run must not be replaced")
		})
	}
}

// Cancelling a scheduled run = aborting it; it never gets a Job.
func TestReconcile_NotBefore_AbortCancels(t *testing.T) {
	fakeRunStore.Reset()
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, newTestFixture(ns, "cancel")))
	run := scheduledRun(ns, "cancel-run", "cancel", 2*time.Second)
	require.NoError(t, k8sClient.Create(ctx, run))
	key := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForPhase(t, ctx, key, testsv1alpha1.PhaseQueued, 3*time.Second)

	setAbort(t, ctx, key, testsv1alpha1.AbortRequest{Reason: testsv1alpha1.AbortReasonUser, RequestedBy: "bob@example.com"})
	final := waitForPhase(t, ctx, key, testsv1alpha1.PhaseAborted, 5*time.Second)
	assert.Contains(t, final.Status.Message, "bob@example.com")
	assert.Eventually(t, func() bool {
		return persistedPhase(string(final.UID)) == testsv1alpha1.PhaseAborted
	}, 3*time.Second, 50*time.Millisecond, "a cancelled schedule is still recorded in history")
	assert.Never(t, func() bool { return hasJob(ctx, key) },
		3*time.Second, 100*time.Millisecond, "past notBefore, an aborted run must stay without a Job")
}

func TestWaitingForSchedule(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	future := metav1.NewTime(now.Add(time.Minute))
	past := metav1.NewTime(now.Add(-time.Minute))
	run := func(nb *metav1.Time, resolved string) *testsv1alpha1.TestRun {
		return &testsv1alpha1.TestRun{
			Spec:   testsv1alpha1.TestRunSpec{NotBefore: nb},
			Status: testsv1alpha1.TestRunStatus{ResolvedSpec: resolved},
		}
	}
	assert.True(t, waitingForSchedule(run(&future, ""), now))
	assert.False(t, waitingForSchedule(run(&past, ""), now))
	assert.False(t, waitingForSchedule(run(nil, ""), now))
	assert.False(t, waitingForSchedule(run(&future, "{}"), now), "already set up (notBefore edited later) runs on")
}
