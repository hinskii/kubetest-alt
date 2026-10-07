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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/compiler"
	"github.com/hinskii/kubetest-alt/pkg/executor"
)

// fixes.md #6: no result.json (no object storage, or the upload failed)
// used to end every run as error/MissingResult. The verdict the wrapper
// left in its termination message now decides — failed stays failed.
func TestReconcile_NoResultJSON_VerdictFromTerminationMessage(t *testing.T) {
	fakeResults.Reset()
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, newTestFixture(ns, "noobj")))
	run := newRunFixture(ns, "noobj-run", "noobj")
	require.NoError(t, k8sClient.Create(ctx, run))
	key := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForJob(t, ctx, key, 5*time.Second)

	summary := executor.TerminationSummary(executor.ExecutionResult{
		Phase: executor.PhaseFailed, ErrorMessage: "exit code 99",
		TestCounts: &executor.TestCounts{Total: 4, Passed: 3, Failed: 1},
	})
	createPodForJob(t, ctx, ns, run.Name, "noobj-pod", corev1.PodSucceeded, []corev1.ContainerStatus{{
		Name: compiler.ContainerWrapper,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			ExitCode: 0, Reason: "Completed", Message: string(summary),
		}},
	}})
	patchJobConditions(t, ctx, key, []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}})

	final := waitForPhase(t, ctx, key, testsv1alpha1.PhaseFailed, 5*time.Second)
	assert.Contains(t, final.Status.Message, "exit code 99")
	require.NotNil(t, final.Status.TestCounts)
	assert.Equal(t, 1, final.Status.TestCounts.Failed)
}

// Wrapper killed before writing anything: error, with the exit code.
func TestReconcile_NoResultAtAll_ReportsExitCode(t *testing.T) {
	fakeResults.Reset()
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, newTestFixture(ns, "killed")))
	run := newRunFixture(ns, "killed-run", "killed")
	require.NoError(t, k8sClient.Create(ctx, run))
	key := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForJob(t, ctx, key, 5*time.Second)

	createPodForJob(t, ctx, ns, run.Name, "killed-pod", corev1.PodFailed, []corev1.ContainerStatus{{
		Name:  compiler.ContainerWrapper,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "Error"}},
	}})
	patchJobConditions(t, ctx, key, []batchv1.JobCondition{
		{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"},
	})
	final := waitForPhase(t, ctx, key, testsv1alpha1.PhaseError, 5*time.Second)
	assert.Contains(t, final.Status.Message, ReasonMissingResult)
	assert.Contains(t, final.Status.Message, "exited with code 137")
}

// fixes.md #10: a malformed result.json ends the run instead of being
// re-read forever.
func TestReconcile_MalformedResult_EndsAsError(t *testing.T) {
	fakeResults.Reset()
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, newTestFixture(ns, "garbled")))
	run := newRunFixture(ns, "garbled-run", "garbled")
	require.NoError(t, k8sClient.Create(ctx, run))
	key := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForJob(t, ctx, key, 5*time.Second)

	fakeResults.SetErr(run.Name, fmt.Errorf("%w: runs/x/result.json: unexpected EOF", ErrResultMalformed))
	patchJobConditions(t, ctx, key, []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}})

	final := waitForPhase(t, ctx, key, testsv1alpha1.PhaseError, 5*time.Second)
	assert.Contains(t, final.Status.Message, ReasonMalformedResult)
	assert.Contains(t, final.Status.Message, "unexpected EOF")
}

func latestRunOf(ctx context.Context, ns, test string) *testsv1alpha1.RunReference {
	var t testsv1alpha1.Test
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: test}, &t); err != nil {
		return nil
	}
	return t.Status.LatestRun
}

// fixes.md #18: Test.status.latestRun follows the most recently started
// run; an older run finishing later doesn't take it back.
func TestReconcile_LatestRunOnTest(t *testing.T) {
	fakeResults.Reset()
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, newTestFixture(ns, "latest")))
	key := func(n string) client.ObjectKey { return client.ObjectKey{Namespace: ns, Name: n} }

	require.NoError(t, k8sClient.Create(ctx, newRunFixture(ns, "latest-old", "latest")))
	waitForJob(t, ctx, key("latest-old"), 5*time.Second)
	assert.Eventually(t, func() bool {
		lr := latestRunOf(ctx, ns, "latest")
		return lr != nil && lr.Name == "latest-old"
	}, 3*time.Second, 50*time.Millisecond)

	require.NoError(t, k8sClient.Create(ctx, newRunFixture(ns, "latest-new", "latest")))
	waitForJob(t, ctx, key("latest-new"), 5*time.Second)
	fakeResults.Set("latest-new", &RunResult{Phase: testsv1alpha1.PhasePassed})
	patchJobConditions(t, ctx, key("latest-new"), []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}})
	waitForPhase(t, ctx, key("latest-new"), testsv1alpha1.PhasePassed, 5*time.Second)
	assert.Eventually(t, func() bool {
		lr := latestRunOf(ctx, ns, "latest")
		return lr != nil && lr.Name == "latest-new" && lr.Phase == testsv1alpha1.PhasePassed && lr.FinishedAt != nil
	}, 3*time.Second, 50*time.Millisecond)

	fakeResults.Set("latest-old", &RunResult{Phase: testsv1alpha1.PhaseFailed})
	patchJobConditions(t, ctx, key("latest-old"), []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}})
	waitForPhase(t, ctx, key("latest-old"), testsv1alpha1.PhaseFailed, 5*time.Second)
	assert.Never(t, func() bool {
		lr := latestRunOf(ctx, ns, "latest")
		return lr == nil || lr.Name != "latest-new"
	}, time.Second, 100*time.Millisecond, "an older run finishing late must not overwrite latestRun")
}

// A retried leaf run lists its tries in status.steps (fixes.md #16).
func TestReconcile_RetriedRun_AttemptsInSteps(t *testing.T) {
	fakeResults.Reset()
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, newTestFixture(ns, "flaky")))
	run := newRunFixture(ns, "flaky-run", "flaky")
	require.NoError(t, k8sClient.Create(ctx, run))
	key := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForJob(t, ctx, key, 5*time.Second)

	fakeResults.Set(run.Name, &RunResult{Phase: testsv1alpha1.PhasePassed, Attempts: []executor.AttemptResult{
		{Phase: executor.PhaseFailed, ErrorMessage: "exit code 1"},
		{Phase: executor.PhasePassed},
	}})
	patchJobConditions(t, ctx, key, []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}})
	final := waitForPhase(t, ctx, key, testsv1alpha1.PhasePassed, 5*time.Second)
	assert.Equal(t, testsv1alpha1.StepResult{Phase: "failed", Message: "exit code 1"}, final.Status.Steps[AttemptStepKey(1)])
	assert.Equal(t, testsv1alpha1.StepPhase("passed"), final.Status.Steps[AttemptStepKey(2)].Phase)
}

// Step 18-2f: a finished run's JUnit cases reach run history (and never
// the CR).
func TestReconcile_TestCasesPersisted(t *testing.T) {
	fakeResults.Reset()
	fakeRunStore.Reset()
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, newTestFixture(ns, "suite")))
	run := newRunFixture(ns, "suite-run", "suite")
	require.NoError(t, k8sClient.Create(ctx, run))
	key := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForJob(t, ctx, key, 5*time.Second)

	cases := []executor.TestCase{
		{Class: "shop", Name: "login", Status: executor.CasePassed},
		{Class: "shop", Name: "checkout", Status: executor.CaseFailed, Message: "timeout"},
	}
	fakeResults.Set(run.Name, &RunResult{Phase: testsv1alpha1.PhaseFailed, TestCases: cases,
		TestCounts: &testsv1alpha1.TestCounts{Total: 2, Passed: 1, Failed: 1}})
	patchJobConditions(t, ctx, key, []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}})
	final := waitForPhase(t, ctx, key, testsv1alpha1.PhaseFailed, 5*time.Second)
	assert.Eventually(t, func() bool {
		return len(fakeRunStore.CasesForUID(string(final.UID))) == 2
	}, 3*time.Second, 50*time.Millisecond)
	assert.Equal(t, cases, fakeRunStore.CasesForUID(string(final.UID)))
}
