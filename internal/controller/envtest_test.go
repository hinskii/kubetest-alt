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
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/compiler"
)

// Small counter to make each test namespace unique across the shared envtest.
var nsCounter atomic.Uint32

func uniqueNamespace(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("t-%d", nsCounter.Add(1))
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	require.NoError(t, k8sClient.Create(context.Background(), ns))
	return name
}

// newTestFixture returns a Test object with sensible defaults. Callers mutate
// the returned pointer before Create.
func newTestFixture(namespace, name string) *testsv1alpha1.Test {
	return &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: testsv1alpha1.TestSpec{
			Type:              "k6",
			ConcurrencyPolicy: PolicyAllow,
			Timeout:           &metav1.Duration{Duration: 5 * time.Minute},
		},
	}
}

func newRunFixture(namespace, name, testRef string) *testsv1alpha1.TestRun {
	return &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: testsv1alpha1.TestRunSpec{
			TestRef: testRef,
			Source:  "api",
		},
	}
}

// waitForPhase polls until TestRun.Status.Phase == want or the timeout fires.
// Prints the last observed state in the failure message — we build the msg
// AFTER Eventually returns because Eventuallyf's args are eagerly evaluated.
func waitForPhase(t *testing.T, ctx context.Context, key client.ObjectKey, want testsv1alpha1.Phase, timeout time.Duration) *testsv1alpha1.TestRun {
	t.Helper()
	var run testsv1alpha1.TestRun
	last := "(never fetched)"
	ok := assert.Eventually(t, func() bool {
		if err := k8sClient.Get(ctx, key, &run); err != nil {
			last = "get error: " + err.Error()
			return false
		}
		last = fmt.Sprintf("phase=%q msg=%q jobName=%q resolvedSpec-empty=%v",
			run.Status.Phase, run.Status.Message, run.Status.JobName, run.Status.ResolvedSpec == "")
		return run.Status.Phase == want
	}, timeout, 50*time.Millisecond)
	if !ok {
		t.Fatalf("TestRun %s did not reach phase %q within %s; last=%s", key, want, timeout, last)
	}
	return &run
}

// waitForJob polls until the Job named after the TestRun exists.
func waitForJob(t *testing.T, ctx context.Context, key client.ObjectKey, timeout time.Duration) {
	t.Helper()
	var job batchv1.Job
	require.Eventually(t, func() bool {
		return k8sClient.Get(ctx, key, &job) == nil
	}, timeout, 50*time.Millisecond, "Job %s was never created", key)
}

// createPodForJob simulates the kubelet — envtest has no Job controller, so
// we manually create pods carrying the compiler-set run-id label so the
// Watches(Pod) mapper enqueues the TestRun.
func createPodForJob(t *testing.T, ctx context.Context, namespace, runID, name string, phase corev1.PodPhase, containerStatuses []corev1.ContainerStatus) {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{compiler.LabelRunID: runID},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{Name: "wrapper", Image: "test/wrapper:v0"},
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, pod))
	// Set status.
	pod.Status.Phase = phase
	pod.Status.ContainerStatuses = containerStatuses
	require.NoError(t, k8sClient.Status().Update(ctx, pod))
}

// patchJobConditions marks a Job as Complete or Failed via a status subresource
// patch. envtest doesn't auto-transition Jobs.
//
// k8s ≥1.36 tightens Job status validation:
//   - StartTime is required on finished Jobs;
//   - CompletionTime is required when Complete=True;
//   - Complete=True must be preceded by SuccessCriteriaMet=True;
//   - Failed=True must be preceded by FailureTarget=True.
//
// This helper prepends the required precursor conditions so tests read cleanly.
func patchJobConditions(t *testing.T, ctx context.Context, jobKey client.ObjectKey, conditions []batchv1.JobCondition) {
	t.Helper()
	var job batchv1.Job
	require.NoError(t, k8sClient.Get(ctx, jobKey, &job))
	now := metav1.Now()
	if job.Status.StartTime == nil {
		job.Status.StartTime = &now
	}
	needsFailureTarget := false
	needsSuccess := false
	for _, c := range conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobFailed:
			needsFailureTarget = true
		case batchv1.JobComplete:
			needsSuccess = true
			if job.Status.CompletionTime == nil {
				job.Status.CompletionTime = &now
			}
		}
	}
	final := make([]batchv1.JobCondition, 0, len(conditions)+2)
	if needsFailureTarget {
		final = append(final, batchv1.JobCondition{
			Type:               batchv1.JobFailureTarget,
			Status:             corev1.ConditionTrue,
			Reason:             "TestPatch",
			LastTransitionTime: now,
		})
	}
	if needsSuccess {
		final = append(final, batchv1.JobCondition{
			Type:               batchv1.JobSuccessCriteriaMet,
			Status:             corev1.ConditionTrue,
			Reason:             "TestPatch",
			LastTransitionTime: now,
		})
	}
	for i := range conditions {
		if conditions[i].LastTransitionTime.IsZero() {
			conditions[i].LastTransitionTime = now
		}
		final = append(final, conditions[i])
	}
	job.Status.Conditions = final
	require.NoError(t, k8sClient.Status().Update(ctx, &job))
}

// TestReconcile_HappyPath is the primary end-to-end scenario:
// create Test+TestRun → controller creates Job → simulate pod running →
// simulate Job success + fake result Passed → assert TestRun.Phase=passed
// and Job deleted, ResolvedSpec populated.
func TestReconcile_HappyPath(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	test := newTestFixture(ns, "happy-test")
	require.NoError(t, k8sClient.Create(ctx, test))

	run := newRunFixture(ns, "happy-run", "happy-test")
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}

	// Wait for Job.
	jobKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForJob(t, ctx, jobKey, 5*time.Second)

	// Wait for phase to reach queued.
	waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseQueued, 3*time.Second)

	// Assert resolvedSpec was snapshotted and matches Test.Spec at this moment.
	{
		var fresh testsv1alpha1.TestRun
		require.NoError(t, k8sClient.Get(ctx, runKey, &fresh))
		require.NotEmpty(t, fresh.Status.ResolvedSpec)
		var snap testsv1alpha1.TestSpec
		require.NoError(t, json.Unmarshal([]byte(fresh.Status.ResolvedSpec), &snap))
		assert.Equal(t, test.Spec.Type, snap.Type)
	}

	// Simulate pod running.
	createPodForJob(t, ctx, ns, run.Name, "happy-run-pod",
		corev1.PodRunning,
		[]corev1.ContainerStatus{{
			Name:  "wrapper",
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}},
	)
	waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseRunning, 3*time.Second)

	// Preload result BEFORE flipping job to complete so the reconciler sees
	// it on the terminal Job event.
	fakeResults.Set(run.Name, &RunResult{Phase: testsv1alpha1.PhasePassed})

	patchJobConditions(t, ctx, jobKey, []batchv1.JobCondition{
		{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, Reason: "Complete"},
	})

	// Final phase.
	final := waitForPhase(t, ctx, runKey, testsv1alpha1.PhasePassed, 5*time.Second)

	// Job deleted after status persisted (§15.3).
	assert.Eventually(t, func() bool {
		return apierrors.IsNotFound(k8sClient.Get(ctx, jobKey, &batchv1.Job{}))
	}, 3*time.Second, 50*time.Millisecond, "Job should be deleted after terminal phase")

	// Assertion on reconcile count — bounded, no hot-loop.
	count := reconcileCount(runKey)
	assert.LessOrEqualf(t, count, int32(20),
		"reconcile count %d unexpectedly high — possible hot-loop", count)

	// Timestamps + duration populated.
	assert.NotNil(t, final.Status.StartedAt)
	assert.NotNil(t, final.Status.FinishedAt)
	assert.GreaterOrEqual(t, final.Status.DurationMs, int64(0))
}

// TestReconcile_SpecSnapshot verifies §15.5: mutating the Test AFTER the run
// starts does not change TestRun.Status.ResolvedSpec.
func TestReconcile_SpecSnapshot(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	test := newTestFixture(ns, "snap-test")
	require.NoError(t, k8sClient.Create(ctx, test))
	run := newRunFixture(ns, "snap-run", "snap-test")
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseQueued, 3*time.Second)

	// Capture the snapshot NOW.
	var snapshot testsv1alpha1.TestRun
	require.NoError(t, k8sClient.Get(ctx, runKey, &snapshot))
	firstSnap := snapshot.Status.ResolvedSpec
	require.NotEmpty(t, firstSnap)

	// Mutate Test.
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: test.Name}, test))
	test.Spec.Timeout = &metav1.Duration{Duration: 99 * time.Hour}
	require.NoError(t, k8sClient.Update(ctx, test))

	// Force reconcile by triggering a status update on the TestRun.
	// A brief delay lets the reconciler run again and (incorrectly) re-snapshot
	// if the logic were broken.
	time.Sleep(500 * time.Millisecond)

	var after testsv1alpha1.TestRun
	require.NoError(t, k8sClient.Get(ctx, runKey, &after))
	assert.Equal(t, firstSnap, after.Status.ResolvedSpec,
		"ResolvedSpec must not change after Test is mutated (§15.5)")
}

// TestReconcile_Idempotency runs steady-state reconciles and asserts:
// - only one Job exists after settling;
// - reconcile count stays bounded (no hot-loop) after quiescence.
func TestReconcile_Idempotency(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	test := newTestFixture(ns, "idem-test")
	require.NoError(t, k8sClient.Create(ctx, test))
	run := newRunFixture(ns, "idem-run", "idem-test")
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseQueued, 3*time.Second)

	// Let it steady-state for a bit; the fallback requeue is 500ms in tests,
	// so 2s = ~4 opportunities to hot-loop if broken.
	time.Sleep(2 * time.Second)

	// Only one Job.
	var jobs batchv1.JobList
	require.NoError(t, k8sClient.List(ctx, &jobs, client.InNamespace(ns)))
	assert.Len(t, jobs.Items, 1)

	// Reconcile count stays bounded — this is what step-04 acceptance requires.
	count := reconcileCount(runKey)
	assert.Lessf(t, count, int32(30),
		"idempotency broken: reconciler ran %d times in 2s", count)
}

// TestReconcile_OOMPath simulates OOMKilled on the wrapper container. Reconciler
// must transition to error with "OOMKilled" in the message.
func TestReconcile_OOMPath(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	test := newTestFixture(ns, "oom-test")
	require.NoError(t, k8sClient.Create(ctx, test))
	run := newRunFixture(ns, "oom-run", "oom-test")
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	jobKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForJob(t, ctx, jobKey, 5*time.Second)

	createPodForJob(t, ctx, ns, run.Name, "oom-pod",
		corev1.PodFailed,
		[]corev1.ContainerStatus{{
			Name: "wrapper",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 137, Reason: "OOMKilled",
			}},
		}},
	)

	final := waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseError, 5*time.Second)
	assert.Contains(t, final.Status.Message, "OOMKilled")
}

// TestReconcile_ContentFetchFailed exercises the step-06 init-container path:
// the fetcher writes FETCH_ERROR to /dev/termination-log, k8s surfaces it on
// Pod.Status.InitContainerStatuses[i].State.Terminated.Message, and
// AnalyzePod translates it into phase=error with reason ContentFetchFailed.
func TestReconcile_ContentFetchFailed(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	test := newTestFixture(ns, "cff-test")
	require.NoError(t, k8sClient.Create(ctx, test))
	run := newRunFixture(ns, "cff-run", "cff-test")
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForJob(t, ctx, client.ObjectKey{Namespace: ns, Name: run.Name}, 5*time.Second)

	// Simulate the failed init container the way k8s would set it:
	// pod stays Pending, initContainerStatuses[0] is Terminated with a
	// non-zero exit code AND a Message copied from /dev/termination-log.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cff-pod",
			Namespace: ns,
			Labels:    map[string]string{compiler.LabelRunID: run.Name},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			InitContainers: []corev1.Container{
				{Name: "content-fetcher", Image: "test/fetcher:v0"},
			},
			Containers: []corev1.Container{
				{Name: "wrapper", Image: "test/wrapper:v0"},
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, pod))
	pod.Status = corev1.PodStatus{
		Phase: corev1.PodPending,
		InitContainerStatuses: []corev1.ContainerStatus{{
			Name: "content-fetcher",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 1,
				Message:  "FETCH_ERROR: git fetch: authentication failed",
				Reason:   "Error",
			}},
		}},
	}
	require.NoError(t, k8sClient.Status().Update(ctx, pod))

	final := waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseError, 5*time.Second)
	assert.Contains(t, final.Status.Message, ReasonContentFetchFailed)
	assert.Contains(t, final.Status.Message, "FETCH_ERROR")
	assert.Contains(t, final.Status.Message, "authentication failed")
}

// TestReconcile_ImagePullBackOff mirrors the OOM path for pull failures.
// Together they exercise the two infra-failure branches of AnalyzePod.
func TestReconcile_ImagePullBackOff(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	test := newTestFixture(ns, "ipbo-test")
	require.NoError(t, k8sClient.Create(ctx, test))
	run := newRunFixture(ns, "ipbo-run", "ipbo-test")
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForJob(t, ctx, client.ObjectKey{Namespace: ns, Name: run.Name}, 5*time.Second)

	createPodForJob(t, ctx, ns, run.Name, "ipbo-pod",
		corev1.PodPending,
		[]corev1.ContainerStatus{{
			Name:  "wrapper",
			Image: "test/does-not-exist:v0",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
		}},
	)

	final := waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseError, 5*time.Second)
	assert.Contains(t, final.Status.Message, "ImagePullBackOff")
	assert.Contains(t, final.Status.Message, "test/does-not-exist:v0")
}

// TestReconcile_Orphan simulates the §15.3 orphan scenario: Job disappears
// after the reconciler has already recorded JobName in status. The next
// reconcile must flip the run to error (never eternal running).
func TestReconcile_Orphan(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	test := newTestFixture(ns, "orphan-test")
	require.NoError(t, k8sClient.Create(ctx, test))
	run := newRunFixture(ns, "orphan-run", "orphan-test")
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	jobKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForJob(t, ctx, jobKey, 5*time.Second)

	// Wait for the reconciler to record JobName in status.
	require.Eventually(t, func() bool {
		var r testsv1alpha1.TestRun
		if err := k8sClient.Get(ctx, runKey, &r); err != nil {
			return false
		}
		return r.Status.JobName == run.Name
	}, 3*time.Second, 50*time.Millisecond)

	// Delete the Job behind the controller's back. Background propagation is
	// required in envtest: Foreground adds a finalizer that a garbage collector
	// (absent in envtest) would normally remove, so the Job would sit forever
	// with DeletionTimestamp and the reconciler's Get would still succeed —
	// masking the orphan path this test is here to exercise.
	propagation := metav1.DeletePropagationBackground
	require.NoError(t, k8sClient.Delete(ctx, &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: run.Name, Namespace: ns},
	}, &client.DeleteOptions{PropagationPolicy: &propagation}))

	final := waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseError, 5*time.Second)
	assert.Contains(t, final.Status.Message, ReasonOrphanJobMissing)
}

// TestReconcile_Finalizer verifies deletion of a running TestRun kills the
// Job first, then removes the finalizer, then the object is gone.
func TestReconcile_Finalizer(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	test := newTestFixture(ns, "fin-test")
	require.NoError(t, k8sClient.Create(ctx, test))
	run := newRunFixture(ns, "fin-run", "fin-test")
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	jobKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForJob(t, ctx, jobKey, 5*time.Second)

	// Wait for finalizer to appear so we're deleting a "protected" object.
	require.Eventually(t, func() bool {
		var r testsv1alpha1.TestRun
		if err := k8sClient.Get(ctx, runKey, &r); err != nil {
			return false
		}
		return slices.Contains(r.Finalizers, FinalizerName)
	}, 3*time.Second, 50*time.Millisecond, "finalizer never applied")

	// Delete the TestRun.
	require.NoError(t, k8sClient.Delete(ctx, &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: run.Name, Namespace: ns},
	}))

	// Job must be gone (finalizer path deletes it).
	require.Eventually(t, func() bool {
		return apierrors.IsNotFound(k8sClient.Get(ctx, jobKey, &batchv1.Job{}))
	}, 5*time.Second, 50*time.Millisecond, "Job should be deleted by finalizer")

	// TestRun object must be gone once finalizer removed.
	require.Eventually(t, func() bool {
		return apierrors.IsNotFound(k8sClient.Get(ctx, runKey, &testsv1alpha1.TestRun{}))
	}, 5*time.Second, 50*time.Millisecond, "TestRun should be fully deleted after finalizer removed")
}

// TestReconcile_TestNotFound: TestRun.spec.testRef points nowhere → phase=error
// with TestNotFound reason.
func TestReconcile_TestNotFound(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	run := newRunFixture(ns, "ghost-run", "test-that-doesnt-exist")
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	final := waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseError, 3*time.Second)
	assert.Contains(t, final.Status.Message, ReasonTestNotFound)
	assert.Contains(t, final.Status.Message, "test-that-doesnt-exist")
}

// TestReconcile_InvalidConfigKey: TestRun.spec.config carries a key not in
// Test.spec.config → phase=error with InvalidConfig reason (this is the
// validation intentionally deferred from the webhook to the controller).
func TestReconcile_InvalidConfigKey(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	test := newTestFixture(ns, "cfg-test")
	test.Spec.Config = map[string]testsv1alpha1.Parameter{
		"vus": {Type: "integer", Default: "10"},
	}
	require.NoError(t, k8sClient.Create(ctx, test))

	run := newRunFixture(ns, "cfg-run", "cfg-test")
	run.Spec.Config = map[string]string{"vus": "50", "nope": "x"}
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	final := waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseError, 3*time.Second)
	assert.Contains(t, final.Status.Message, ReasonInvalidConfig)
	assert.Contains(t, final.Status.Message, `"nope"`)
}

// TestReconcile_Concurrency_Forbid: second TestRun for same Test must sit in
// queued while the first is active, then proceed once it terminates.
func TestReconcile_Concurrency_Forbid(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	test := newTestFixture(ns, "forbid-test")
	test.Spec.ConcurrencyPolicy = PolicyForbid
	require.NoError(t, k8sClient.Create(ctx, test))

	// First run: get it to running-ish state (queued is enough for gating).
	run1 := newRunFixture(ns, "forbid-run-1", "forbid-test")
	require.NoError(t, k8sClient.Create(ctx, run1))
	waitForPhase(t, ctx, client.ObjectKey{Namespace: ns, Name: run1.Name},
		testsv1alpha1.PhaseQueued, 3*time.Second)

	// Second run — must sit queued waiting on first.
	run2 := newRunFixture(ns, "forbid-run-2", "forbid-test")
	require.NoError(t, k8sClient.Create(ctx, run2))

	run2Key := client.ObjectKey{Namespace: ns, Name: run2.Name}
	// Assert it settles into queued with a waiting message.
	require.Eventually(t, func() bool {
		var r testsv1alpha1.TestRun
		if err := k8sClient.Get(ctx, run2Key, &r); err != nil {
			return false
		}
		return r.Status.Phase == testsv1alpha1.PhaseQueued &&
			strings.Contains(r.Status.Message, "waiting")
	}, 3*time.Second, 50*time.Millisecond)

	// No Job for run2 while it's waiting.
	require.True(t, apierrors.IsNotFound(k8sClient.Get(ctx,
		client.ObjectKey{Namespace: ns, Name: run2.Name}, &batchv1.Job{})),
		"Forbid: no Job should be created for run2 while run1 is active")

	// Terminate run1 (skip Pod, just push job to Complete + preload result).
	waitForJob(t, ctx, client.ObjectKey{Namespace: ns, Name: run1.Name}, 3*time.Second)
	fakeResults.Set(run1.Name, &RunResult{Phase: testsv1alpha1.PhasePassed})
	patchJobConditions(t, ctx, client.ObjectKey{Namespace: ns, Name: run1.Name},
		[]batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}})
	waitForPhase(t, ctx, client.ObjectKey{Namespace: ns, Name: run1.Name},
		testsv1alpha1.PhasePassed, 5*time.Second)

	// Now run2 should proceed — Job for run2 gets created.
	waitForJob(t, ctx, client.ObjectKey{Namespace: ns, Name: run2.Name}, 3*time.Second)
}

// TestReconcile_Concurrency_Replace: creating run2 with policy=Replace aborts
// the still-active run1.
func TestReconcile_Concurrency_Replace(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	test := newTestFixture(ns, "replace-test")
	test.Spec.ConcurrencyPolicy = PolicyReplace
	require.NoError(t, k8sClient.Create(ctx, test))

	run1 := newRunFixture(ns, "replace-run-1", "replace-test")
	require.NoError(t, k8sClient.Create(ctx, run1))
	waitForPhase(t, ctx, client.ObjectKey{Namespace: ns, Name: run1.Name},
		testsv1alpha1.PhaseQueued, 3*time.Second)
	waitForJob(t, ctx, client.ObjectKey{Namespace: ns, Name: run1.Name}, 3*time.Second)

	// New run — should trigger abort on run1.
	run2 := newRunFixture(ns, "replace-run-2", "replace-test")
	require.NoError(t, k8sClient.Create(ctx, run2))

	// run1 flips to aborted.
	final := waitForPhase(t, ctx, client.ObjectKey{Namespace: ns, Name: run1.Name},
		testsv1alpha1.PhaseAborted, 5*time.Second)
	assert.Contains(t, final.Status.Message, ReasonAborted)

	// run2 proceeds.
	waitForJob(t, ctx, client.ObjectKey{Namespace: ns, Name: run2.Name}, 5*time.Second)
}

// TestReconcile_DeletionRace exercises §15.3's harder deletion scenario:
// Job succeeds, then vanishes before the reconciler records status. The
// reconciler must still land the run terminal, not sit in eternal running.
//
// We simulate by:
//  1. letting reconciler create the Job;
//  2. deleting the Job immediately (no status set);
//  3. reconciler sees Job missing while JobName!="" → orphan → error.
func TestReconcile_DeletionRace(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	test := newTestFixture(ns, "race-test")
	require.NoError(t, k8sClient.Create(ctx, test))
	run := newRunFixture(ns, "race-run", "race-test")
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	jobKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForJob(t, ctx, jobKey, 5*time.Second)

	// Wait for JobName recorded in status (the race window starts here).
	require.Eventually(t, func() bool {
		var r testsv1alpha1.TestRun
		if err := k8sClient.Get(ctx, runKey, &r); err != nil {
			return false
		}
		return r.Status.JobName == run.Name
	}, 3*time.Second, 50*time.Millisecond)

	// Delete Job before any status transition beyond queued. Background
	// propagation (envtest has no garbage collector — see Orphan test comment).
	propagation := metav1.DeletePropagationBackground
	require.NoError(t, k8sClient.Delete(ctx, &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: run.Name, Namespace: ns},
	}, &client.DeleteOptions{PropagationPolicy: &propagation}))

	// Must reach terminal — never sit at running.
	waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseError, 5*time.Second)
}

// TestReconcile_MissingResult_FallbackToPodTerminated exercises §15.2 fallback:
// Job succeeded but no result.json → phase=error, reason=MissingResult (or
// JobDeadline if the failure was ADSExceeded).
func TestReconcile_MissingResult_FallbackToPodTerminated(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	test := newTestFixture(ns, "miss-test")
	require.NoError(t, k8sClient.Create(ctx, test))
	run := newRunFixture(ns, "miss-run", "miss-test")
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	jobKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForJob(t, ctx, jobKey, 5*time.Second)

	// No result preloaded — fake reader returns ErrResultNotFound.
	patchJobConditions(t, ctx, jobKey, []batchv1.JobCondition{
		{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"},
	})

	final := waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseError, 5*time.Second)
	// Reason is MissingResult (not the k8s condition Reason) — our own classification.
	assert.Contains(t, final.Status.Message, ReasonMissingResult)
}

// TestReconcile_MissingResult_JobDeadline exercises the DeadlineExceeded
// branch of FallbackPhaseFromJobFailure.
func TestReconcile_MissingResult_JobDeadline(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	test := newTestFixture(ns, "dead-test")
	require.NoError(t, k8sClient.Create(ctx, test))
	run := newRunFixture(ns, "dead-run", "dead-test")
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	jobKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForJob(t, ctx, jobKey, 5*time.Second)

	patchJobConditions(t, ctx, jobKey, []batchv1.JobCondition{
		{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded"},
	})

	final := waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseError, 5*time.Second)
	assert.Contains(t, final.Status.Message, ReasonJobDeadline)
}

// TestReconcile_PodMappingIgnoresOtherPods verifies the label-based mapper
// filter — pods without the run-id label MUST NOT trigger reconciles.
// Otherwise unrelated cluster churn could DDoS our reconciler.
func TestReconcile_PodMappingIgnoresOtherPods(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	// Create an unrelated pod with no run-id label.
	unrelated := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unrelated",
			Namespace: ns,
			Labels:    map[string]string{"app": "not-us"},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{Name: "c", Image: "test/whatever:v0"},
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, unrelated))
	unrelated.Status.Phase = corev1.PodRunning
	require.NoError(t, k8sClient.Status().Update(ctx, unrelated))

	time.Sleep(300 * time.Millisecond)
	// No reconciles for the unrelated pod (no run-id label → predicate filters out).
	got := reconcileCount(client.ObjectKey{Namespace: ns, Name: "unrelated"})
	assert.Zero(t, got, "unrelated pod must not trigger reconciles")
}

// _ compile-time hint that we import types we might not otherwise reference.
var _ = types.NamespacedName{}
