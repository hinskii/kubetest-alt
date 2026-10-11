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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/compiler"
)

func at(sec int) metav1.Time {
	return metav1.NewTime(time.Date(2026, 10, 11, 12, 0, sec, 0, time.UTC))
}

func atp(sec int) *metav1.Time { t := at(sec); return &t }

func running(name string, start int) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{
		Running: &corev1.ContainerStateRunning{StartedAt: at(start)}}}
}

func done(name string, start, end int, exit int32, msg string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{StartedAt: at(start), FinishedAt: at(end), ExitCode: exit, Message: msg}}}
}

func podWith(init, main []corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{compiler.LabelKubetestTool: "k6"}},
		Status:     corev1.PodStatus{InitContainerStatuses: init, ContainerStatuses: main},
	}
}

type brief struct {
	Name  string
	Phase testsv1alpha1.StepPhase
	Start *metav1.Time
	End   *metav1.Time
}

func briefs(st []testsv1alpha1.Stage) []brief {
	out := make([]brief, 0, len(st))
	for _, s := range st {
		out = append(out, brief{s.Name, s.Phase, s.StartedAt, s.FinishedAt})
	}
	return out
}

func TestDeriveStages_ACourseInOrder(t *testing.T) {
	const fetcher, wrapper = compiler.ContainerContentFetcher, compiler.ContainerWrapper
	base := stageInputs{queuedAt: atp(0), fetchName: stageCloneGit, tool: "k6"}

	t.Run("no pod yet: all queued, the queue running", func(t *testing.T) {
		assert.Equal(t, []brief{
			{"Queued", testsv1alpha1.StepPhaseRunning, atp(0), nil},
			{stageCloneGit, testsv1alpha1.StepPhaseQueued, nil, nil},
			{"Run k6", testsv1alpha1.StepPhaseQueued, nil, nil},
			{"Collect results", testsv1alpha1.StepPhaseQueued, nil, nil},
		}, briefs(deriveStages(base)))
	})

	t.Run("cloning", func(t *testing.T) {
		in := base
		in.pod = podWith([]corev1.ContainerStatus{done("kubetest-bin-install", 2, 3, 0, ""), running(fetcher, 3)}, nil)
		got := briefs(deriveStages(in))
		assert.Equal(t, brief{"Queued", testsv1alpha1.StepPhasePassed, atp(0), atp(3)}, got[0])
		assert.Equal(t, brief{stageCloneGit, testsv1alpha1.StepPhaseRunning, atp(3), nil}, got[1])
	})

	t.Run("the tool runs", func(t *testing.T) {
		in := base
		in.pod = podWith([]corev1.ContainerStatus{done(fetcher, 3, 5, 0, "")}, []corev1.ContainerStatus{running(wrapper, 6)})
		got := briefs(deriveStages(in))
		assert.Equal(t, brief{stageCloneGit, testsv1alpha1.StepPhasePassed, atp(3), atp(5)}, got[1])
		assert.Equal(t, brief{"Run k6", testsv1alpha1.StepPhaseRunning, atp(6), nil}, got[2])
		assert.Equal(t, testsv1alpha1.StepPhaseQueued, got[3].Phase)
	})

	t.Run("done: the result splits the tool from the collecting", func(t *testing.T) {
		in := base
		in.pod = podWith([]corev1.ContainerStatus{done(fetcher, 3, 5, 0, "")}, []corev1.ContainerStatus{done(wrapper, 6, 20, 1, "")})
		in.toolFinishedAt, in.toolPhase = atp(17), testsv1alpha1.PhaseFailed
		got := briefs(deriveStages(in))
		assert.Equal(t, brief{"Run k6", testsv1alpha1.StepPhaseFailed, atp(6), atp(17)}, got[2])
		assert.Equal(t, brief{"Collect results", testsv1alpha1.StepPhasePassed, atp(17), atp(20)}, got[3])
	})

	t.Run("the fetch failed", func(t *testing.T) {
		in := base
		in.pod = podWith([]corev1.ContainerStatus{done(fetcher, 3, 4, 1, "FETCH_ERROR: repository not found")}, nil)
		st := deriveStages(in)
		assert.Equal(t, testsv1alpha1.StepPhaseError, st[1].Phase)
		assert.Equal(t, "FETCH_ERROR: repository not found", st[1].Message)
	})

	t.Run("no content: no fetch stage, the queue ends when the tool starts", func(t *testing.T) {
		in := base
		in.fetchName = ""
		in.pod = podWith([]corev1.ContainerStatus{done(fetcher, 3, 4, 0, "")}, []corev1.ContainerStatus{running(wrapper, 6)})
		got := briefs(deriveStages(in))
		require.Len(t, got, 3)
		assert.Equal(t, brief{"Queued", testsv1alpha1.StepPhasePassed, atp(0), atp(6)}, got[0])
	})
}

func TestFinishStages(t *testing.T) {
	now := at(30)
	course := func() []testsv1alpha1.Stage {
		return deriveStages(stageInputs{queuedAt: atp(0), fetchName: stageWriteFiles, tool: "k6",
			pod: podWith([]corev1.ContainerStatus{done(compiler.ContainerContentFetcher, 1, 2, 0, "")},
				[]corev1.ContainerStatus{running(compiler.ContainerWrapper, 3)})})
	}

	t.Run("an infra failure ends the stage the run was in; the rest is skipped", func(t *testing.T) {
		st := finishStages(course(), testsv1alpha1.PhaseError, "OOMKilled: raise memory", now)
		assert.Equal(t, testsv1alpha1.StepPhasePassed, st[1].Phase)
		assert.Equal(t, testsv1alpha1.StepPhaseError, st[2].Phase)
		assert.Equal(t, "OOMKilled: raise memory", st[2].Message)
		assert.Equal(t, &now, st[2].FinishedAt)
		assert.Equal(t, testsv1alpha1.StepPhaseSkipped, st[3].Phase)
	})

	t.Run("the container never started: the queue stage says why", func(t *testing.T) {
		st := deriveStages(stageInputs{queuedAt: atp(0), tool: "k6"})
		st = finishStages(st, testsv1alpha1.PhaseError, `CreateContainerConfigError: secret "s3" not found`, now)
		assert.Equal(t, testsv1alpha1.StepPhaseError, st[0].Phase)
		assert.Contains(t, st[0].Message, "secret")
		assert.Equal(t, testsv1alpha1.StepPhaseSkipped, st[1].Phase)
	})

	t.Run("a failed fetch: nothing after it ran", func(t *testing.T) {
		st := deriveStages(stageInputs{queuedAt: atp(0), fetchName: stageCloneGit,
			pod: podWith([]corev1.ContainerStatus{done(compiler.ContainerContentFetcher, 1, 2, 1, "FETCH_ERROR: x")}, nil)})
		st = finishStages(st, testsv1alpha1.PhaseError, "ContentFetchFailed: FETCH_ERROR: x", now)
		assert.Equal(t, "FETCH_ERROR: x", st[1].Message, "the fetcher's own message stays")
		assert.Equal(t, testsv1alpha1.StepPhaseSkipped, st[2].Phase)
		assert.Equal(t, testsv1alpha1.StepPhaseSkipped, st[3].Phase)
	})

	t.Run("the tool failed: results still collected, the run stage says why", func(t *testing.T) {
		st := deriveStages(stageInputs{queuedAt: atp(0), tool: "k6", toolFinishedAt: atp(10), toolPhase: testsv1alpha1.PhaseFailed,
			pod: podWith(nil, []corev1.ContainerStatus{done(compiler.ContainerWrapper, 3, 12, 1, "")})})
		st = finishStages(st, testsv1alpha1.PhaseFailed, "1 of 2 checks failed", now)
		assert.Equal(t, testsv1alpha1.StepPhaseFailed, st[1].Phase)
		assert.Equal(t, "1 of 2 checks failed", st[1].Message)
		assert.Equal(t, testsv1alpha1.StepPhasePassed, st[2].Phase)
	})

	t.Run("passed: every stage passed", func(t *testing.T) {
		for _, s := range finishStages(course(), testsv1alpha1.PhasePassed, "", now) {
			assert.Equal(t, testsv1alpha1.StepPhasePassed, s.Phase, s.Name)
		}
	})
}

func TestFetchNameOf(t *testing.T) {
	for spec, want := range map[string]string{
		`{"content":{"git":{"uri":"https://x"}}}`:               stageCloneGit,
		`{"content":{"files":[{"path":"a.js","content":"x"}]}}`: stageWriteFiles,
		`{"content":{"tarball":[{"url":"https://x/t.tgz"}]}}`:   stageFetchTar,
		`{"content":{"testData":[{"configMap":"d"}]}}`:          stageMountData,
		`{}`: "",
		``:   "",
	} {
		run := &testsv1alpha1.TestRun{Status: testsv1alpha1.TestRunStatus{ResolvedSpec: spec}}
		assert.Equal(t, want, fetchNameOf(run), spec)
	}
}

func TestForbidMessage_NamesTheRunsItWaitsFor(t *testing.T) {
	assert.Equal(t, "waiting for a to finish (concurrencyPolicy: Forbid)", forbidMessage([]string{"a"}))
	assert.Equal(t, "waiting for a, b to finish (concurrencyPolicy: Forbid)", forbidMessage([]string{"a", "b"}))
	assert.Equal(t, "waiting for a, b, c and 2 more to finish (concurrencyPolicy: Forbid)",
		forbidMessage([]string{"a", "b", "c", "d", "e"}))
	assert.Contains(t, forbidMessage(nil), "a previous run")
	self := testsv1alpha1.TestRun{ObjectMeta: metav1.ObjectMeta{Name: "me", UID: "3"}}
	assert.Equal(t, []string{"a", "c"}, activePriorNames([]testsv1alpha1.TestRun{
		{ObjectMeta: metav1.ObjectMeta{Name: "c", UID: "1"}, Status: testsv1alpha1.TestRunStatus{Phase: "running"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "b", UID: "2"}, Status: testsv1alpha1.TestRunStatus{Phase: "passed"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "a", UID: "4"}, Status: testsv1alpha1.TestRunStatus{Phase: "queued"}},
		self,
	}, &self))
}
