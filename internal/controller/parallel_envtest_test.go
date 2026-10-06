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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/compiler"
)

func parallelTest(ns, name string) *testsv1alpha1.Test {
	t := newTestFixture(ns, name)
	t.Spec.Parallel = &testsv1alpha1.ParallelSpec{Matrix: map[string][]string{"browser": {"chrome", "firefox"}}}
	t.Spec.Container.Args = []string{"run", "--browser={{ matrix.browser }}"}
	return t
}

func completeJob(t *testing.T, ctx context.Context, ns, name string) {
	t.Helper()
	patchJobConditions(t, ctx, client.ObjectKey{Namespace: ns, Name: name},
		[]batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}})
}

// fixes.md #17: spec.parallel was accepted and ignored. Now one Job per
// worker with the worker's values filled in; the run aggregates them.
func TestReconcile_Parallel_WorkersAndAggregate(t *testing.T) {
	fakeResults.Reset()
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, parallelTest(ns, "matrix")))
	run := newRunFixture(ns, "par-run", "matrix")
	require.NoError(t, k8sClient.Create(ctx, run))
	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}

	for i, browser := range []string{"chrome", "firefox"} {
		key := client.ObjectKey{Namespace: ns, Name: compiler.WorkerJobName(run, i)}
		waitForJob(t, ctx, key, 5*time.Second)
		var job batchv1.Job
		require.NoError(t, k8sClient.Get(ctx, key, &job))
		assert.Equal(t, []string{"run", "--browser=" + browser}, job.Spec.Template.Spec.Containers[0].Args[len(job.Spec.Template.Spec.Containers[0].Args)-2:])
		assert.Equal(t, run.Name, job.Labels[compiler.LabelRunID])
	}
	assert.False(t, hasJob(ctx, runKey), "a parallel run has no Job of its own")
	waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseRunning, 5*time.Second)

	fakeResults.Set(run.Name+"/w0", &RunResult{Phase: testsv1alpha1.PhasePassed,
		TestCounts: &testsv1alpha1.TestCounts{Total: 3, Passed: 3},
		Artifacts:  []testsv1alpha1.ArtifactRef{{Path: "report.html"}}})
	fakeResults.Set(run.Name+"/w1", &RunResult{Phase: testsv1alpha1.PhaseFailed, ErrorMessage: "exit code 1",
		TestCounts: &testsv1alpha1.TestCounts{Total: 3, Passed: 2, Failed: 1}})
	completeJob(t, ctx, ns, compiler.WorkerJobName(run, 0))
	completeJob(t, ctx, ns, compiler.WorkerJobName(run, 1))

	final := waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseFailed, 10*time.Second)
	assert.Equal(t, "1 of 2 workers failed", final.Status.Message)
	assert.Equal(t, testsv1alpha1.StepPhase("passed"), final.Status.Steps["worker-0"].Phase)
	assert.Equal(t, "exit code 1", final.Status.Steps["worker-1"].Message)
	assert.Equal(t, &testsv1alpha1.TestCounts{Total: 6, Passed: 5, Failed: 1}, final.Status.TestCounts)
	assert.Equal(t, []testsv1alpha1.ArtifactRef{{Path: "workers/0/artifacts/report.html"}}, final.Status.ArtifactRefs)
	assert.Eventually(t, func() bool {
		var j batchv1.Job
		err := k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: compiler.WorkerJobName(run, 1)}, &j)
		return apierrors.IsNotFound(err) || j.DeletionTimestamp != nil
	}, 5*time.Second, 50*time.Millisecond, "finished worker Jobs are deleted")
}

func TestReconcile_Parallel_AllPassed(t *testing.T) {
	fakeResults.Reset()
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, parallelTest(ns, "ok")))
	run := newRunFixture(ns, "ok-run", "ok")
	require.NoError(t, k8sClient.Create(ctx, run))
	for i := range 2 {
		waitForJob(t, ctx, client.ObjectKey{Namespace: ns, Name: compiler.WorkerJobName(run, i)}, 5*time.Second)
		fakeResults.Set(run.Name+"/w"+string(rune('0'+i)), &RunResult{Phase: testsv1alpha1.PhasePassed})
		completeJob(t, ctx, ns, compiler.WorkerJobName(run, i))
	}
	final := waitForPhase(t, ctx, client.ObjectKey{Namespace: ns, Name: run.Name}, testsv1alpha1.PhasePassed, 10*time.Second)
	assert.Equal(t, "all 2 workers passed", final.Status.Message, "%+v", final.Status.Steps)
}

func TestReconcile_Parallel_AbortStopsWorkers(t *testing.T) {
	fakeResults.Reset()
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, parallelTest(ns, "stop")))
	run := newRunFixture(ns, "stop-run", "stop")
	require.NoError(t, k8sClient.Create(ctx, run))
	key := client.ObjectKey{Namespace: ns, Name: run.Name}
	for i := range 2 {
		waitForJob(t, ctx, client.ObjectKey{Namespace: ns, Name: compiler.WorkerJobName(run, i)}, 5*time.Second)
	}
	setAbort(t, ctx, key, testsv1alpha1.AbortRequest{Reason: testsv1alpha1.AbortReasonUser, RequestedBy: "carol@example.com"})
	waitForPhase(t, ctx, key, testsv1alpha1.PhaseAborted, 5*time.Second)
	for i := range 2 {
		assert.Eventually(t, func() bool {
			var j batchv1.Job
			err := k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: compiler.WorkerJobName(run, i)}, &j)
			return apierrors.IsNotFound(err) || j.DeletionTimestamp != nil
		}, 5*time.Second, 50*time.Millisecond, "worker %d stopped", i)
	}
}
