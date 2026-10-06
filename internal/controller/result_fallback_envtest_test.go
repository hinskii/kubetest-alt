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
