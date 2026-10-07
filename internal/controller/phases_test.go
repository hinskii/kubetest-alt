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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/compiler"
	"github.com/hinskii/kubetest-alt/pkg/executor"
)

func TestInspectJob(t *testing.T) {
	cases := []struct {
		name string
		job  *batchv1.Job
		want JobConclusion
	}{
		{"nil job", nil, JobStillRunning},
		{"no conditions", &batchv1.Job{}, JobStillRunning},
		{
			"JobComplete True",
			&batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
			}}},
			JobSucceeded,
		},
		{
			"JobFailed True",
			&batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
			}}},
			JobFailedConclusion,
		},
		{
			"JobComplete False → still running",
			&batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionFalse},
			}}},
			JobStillRunning,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, InspectJob(tc.job))
		})
	}
}

func TestAnalyzePod(t *testing.T) {
	cases := []struct {
		name       string
		pod        *corev1.Pod
		wantReason string
		wantSubstr string
	}{
		{"nil pod", nil, "", ""},
		{"empty pod", &corev1.Pod{}, "", ""},
		{
			"healthy running pod",
			&corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "wrapper", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
					},
				},
			},
			"", "",
		},
		{
			"ImagePullBackOff on main container",
			&corev1.Pod{Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:  "wrapper",
					Image: "grafana/k6:nosuch",
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
				}},
			}},
			ReasonImagePull, "grafana/k6:nosuch",
		},
		{
			"ErrImagePull on init container",
			&corev1.Pod{Status: corev1.PodStatus{
				InitContainerStatuses: []corev1.ContainerStatus{{
					Name:  "content-fetcher",
					Image: "myrepo/fetcher:v1",
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull"}},
				}},
			}},
			ReasonImagePull, "myrepo/fetcher:v1",
		},
		{
			"OOMKilled on terminated state",
			&corev1.Pod{Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "wrapper",
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 137, Reason: "OOMKilled",
					}},
				}},
			}},
			ReasonOOMKilled, "OOMKilled",
		},
		{
			"OOMKilled on last termination state (restarted container)",
			&corev1.Pod{Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:  "wrapper",
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
					LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 137, Reason: "OOMKilled",
					}},
				}},
			}},
			ReasonOOMKilled, "OOMKilled",
		},
		{
			"ImagePullBackOff wins over other conditions when both present",
			&corev1.Pod{Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: "wrapper", Image: "bad:image", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}},
				},
			}},
			ReasonImagePull, "bad:image",
		},
		{
			// Step-06: init container failed with a non-zero exit — the fetcher
			// wrote FETCH_ERROR to termination-log which k8s copies into Message.
			"init container failed with FETCH_ERROR message",
			&corev1.Pod{Status: corev1.PodStatus{
				InitContainerStatuses: []corev1.ContainerStatus{{
					Name: "content-fetcher",
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 1,
						Message:  "FETCH_ERROR: git fetch: nonexistent ref",
					}},
				}},
			}},
			ReasonContentFetchFailed, "FETCH_ERROR",
		},
		{
			// Init container failed but wrote no termination message — we
			// still classify correctly, just with a generic message.
			"init container failed with no termination message",
			&corev1.Pod{Status: corev1.PodStatus{
				InitContainerStatuses: []corev1.ContainerStatus{{
					Name: "content-fetcher",
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 42,
					}},
				}},
			}},
			ReasonContentFetchFailed, "exited with code 42",
		},
		{
			// Successful init container (exit 0) → NOT an infra failure.
			"init container exited zero → no infra failure",
			&corev1.Pod{Status: corev1.PodStatus{
				InitContainerStatuses: []corev1.ContainerStatus{{
					Name: "content-fetcher",
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 0,
					}},
				}},
			}},
			"", "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			infra := AnalyzePod(tc.pod, time.Now(), DefaultUnschedulableTimeout)
			assert.Equal(t, tc.wantReason, infra.Reason)
			if tc.wantSubstr != "" {
				assert.Contains(t, infra.Message, tc.wantSubstr)
			}
		})
	}
}

func TestIsPodRunning(t *testing.T) {
	assert.False(t, IsPodRunning(nil))
	assert.False(t, IsPodRunning(&corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending}}))
	assert.True(t, IsPodRunning(&corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning}}))
	assert.False(t, IsPodRunning(&corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed}}))
}

func TestFallbackPhaseFromJobFailure(t *testing.T) {
	t.Run("nil job → error MissingResult", func(t *testing.T) {
		p, r, m := FallbackPhaseFromJobFailure(nil, nil)
		assert.Equal(t, testsv1alpha1.PhaseError, p)
		assert.Equal(t, ReasonMissingResult, r)
		assert.Contains(t, m, "no result")
	})
	t.Run("job with generic failure", func(t *testing.T) {
		p, r, m := FallbackPhaseFromJobFailure(&batchv1.Job{Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}},
		}}, wrapperPod(corev1.ContainerStateTerminated{ExitCode: 137, Reason: "Error"}))
		assert.Equal(t, testsv1alpha1.PhaseError, p)
		assert.Equal(t, ReasonMissingResult, r)
		assert.Equal(t, "wrapper produced no result; container exited with code 137 (Error)", m)
	})
	t.Run("job DeadlineExceeded → JobDeadline reason", func(t *testing.T) {
		p, r, m := FallbackPhaseFromJobFailure(&batchv1.Job{Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{
				Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded",
			}},
		}}, nil)
		assert.Equal(t, testsv1alpha1.PhaseError, p)
		assert.Equal(t, ReasonJobDeadline, r)
		assert.Contains(t, m, "activeDeadlineSeconds")
	})
}

func wrapperPod(term corev1.ContainerStateTerminated) *corev1.Pod {
	return &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
		{Name: "sidecar", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: "not me"}}},
		{Name: compiler.ContainerWrapper, State: corev1.ContainerState{Terminated: &term}},
	}}}
}

// fixes.md #6: with no result.json the verdict comes from the wrapper's
// termination summary — passed AND failed runs, not "error" for all.
func TestResultFromTermination(t *testing.T) {
	failed := executor.ExecutionResult{
		Phase: executor.PhaseFailed, ErrorMessage: "exit code 99",
		TestCounts: &executor.TestCounts{Total: 3, Failed: 1, Passed: 2}, Metrics: map[string]float64{"p95_ms": 120},
		Content: &executor.ContentInfo{GitRevision: "main", GitCommit: "abc123"},
	}
	got := ResultFromTermination(wrapperPod(corev1.ContainerStateTerminated{
		Message: string(executor.TerminationSummary(failed)),
	}))
	require.NotNil(t, got)
	assert.Equal(t, testsv1alpha1.PhaseFailed, got.Phase)
	assert.Equal(t, "exit code 99", got.ErrorMessage)
	assert.Equal(t, &testsv1alpha1.TestCounts{Total: 3, Failed: 1, Passed: 2}, got.TestCounts)
	assert.Equal(t, map[string]float64{"p95_ms": 120}, got.Metrics)
	assert.Equal(t, &testsv1alpha1.ContentStatus{GitRevision: "main", GitCommit: "abc123"}, got.Content)
	run := &testsv1alpha1.TestRun{}
	applyRunResultToStatus(run, got)
	assert.Equal(t, got.Content, run.Status.Content, "the commit reaches the run's status")

	passed := ResultFromTermination(wrapperPod(corev1.ContainerStateTerminated{
		Message: string(executor.TerminationSummary(executor.ExecutionResult{Phase: executor.PhasePassed})),
	}))
	require.NotNil(t, passed)
	assert.Equal(t, testsv1alpha1.PhasePassed, passed.Phase)

	assert.Nil(t, ResultFromTermination(nil))
	assert.Nil(t, ResultFromTermination(wrapperPod(corev1.ContainerStateTerminated{Message: "panic: boom"})))
	assert.Nil(t, ResultFromTermination(&corev1.Pod{}), "no wrapper container status")
}

// fixes.md #19: an unschedulable pod ends the run after the grace period
// instead of hanging until the Job deadline.
func TestAnalyzePod_Unschedulable(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	pod := func(since time.Duration) *corev1.Pod {
		return &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{
			Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
			Message:            "0/3 nodes are available: 3 Insufficient memory.",
			LastTransitionTime: metav1.NewTime(now.Add(-since)),
		}}}}
	}
	assert.Empty(t, AnalyzePod(pod(time.Minute), now, 5*time.Minute).Reason, "autoscaler gets its grace period")
	got := AnalyzePod(pod(6*time.Minute), now, 5*time.Minute)
	assert.Equal(t, ReasonUnschedulable, got.Reason)
	assert.Contains(t, got.Message, "3 Insufficient memory")
}
