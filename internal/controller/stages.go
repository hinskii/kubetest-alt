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
	"reflect"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/compiler"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

// A leaf run's stages (step 20a): its course as the UI draws it, from what
// the reconciler already sees — the TestRun, its pod, the wrapper's result.
//
//	queue    queuedAt → the content fetcher (or the wrapper) starts
//	fetch    the content-fetcher init container (only with content)
//	run      the wrapper starts → the tool exits (result's toolFinishedAt)
//	collect  the tool exits → the wrapper exits (scrape, uploads)

// stageQueuedName names the queue stage.
const stageQueuedName = "Queued"

// Names of the fetch stage, by the run's content.
const (
	stageCloneGit   = "Clone git repository"
	stageFetchTar   = "Fetch tarball"
	stageWriteFiles = "Write files"
	stageMountData  = "Mount test data"
)

// stageInputs is what the stages are derived from.
type stageInputs struct {
	queuedAt *metav1.Time
	pod      *corev1.Pod
	// fetchName names the fetch stage; empty: the run has no content.
	fetchName string
	tool      string
	// toolFinishedAt is when the tool exited (from the result), if known.
	toolFinishedAt *metav1.Time
	// toolPhase is the tool's verdict, once there is one.
	toolPhase testsv1alpha1.Phase
}

// fetchNameOf names the fetch stage after the run's content (its resolved
// spec): empty when there is none to fetch.
func fetchNameOf(run *testsv1alpha1.TestRun) string {
	var spec struct {
		Content testsv1alpha1.Content `json:"content"`
	}
	if run.Status.ResolvedSpec == "" || json.Unmarshal([]byte(run.Status.ResolvedSpec), &spec) != nil {
		return ""
	}
	c := spec.Content
	switch {
	case c.Git != nil:
		return stageCloneGit
	case len(c.Tarball) > 0:
		return stageFetchTar
	case len(c.Files) > 0:
		return stageWriteFiles
	case len(c.TestData) > 0:
		return stageMountData
	}
	return ""
}

// toolOf names the run stage: the tool label, else the image.
func toolOf(pod *corev1.Pod) string {
	if pod == nil {
		return ""
	}
	if t := pod.Labels[compiler.LabelKubetestTool]; t != "" {
		return t
	}
	for _, c := range pod.Spec.Containers {
		if c.Name == compiler.ContainerWrapper {
			return c.Image
		}
	}
	return ""
}

func containerStatus(list []corev1.ContainerStatus, name string) *corev1.ContainerStatus {
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

// startEnd is when a container started and, once it did, finished.
func startEnd(cs *corev1.ContainerStatus) (start, end *metav1.Time, exit int32, msg string) {
	if cs == nil {
		return nil, nil, 0, ""
	}
	switch {
	case cs.State.Running != nil:
		t := cs.State.Running.StartedAt
		return &t, nil, 0, ""
	case cs.State.Terminated != nil:
		st, ft := cs.State.Terminated.StartedAt, cs.State.Terminated.FinishedAt
		return &st, &ft, cs.State.Terminated.ExitCode, cs.State.Terminated.Message
	}
	return nil, nil, 0, ""
}

func stage(kind testsv1alpha1.StageKind, name string, start, end *metav1.Time) testsv1alpha1.Stage {
	s := testsv1alpha1.Stage{Kind: kind, Name: name, Phase: testsv1alpha1.StepPhaseQueued,
		StartedAt: start, FinishedAt: end}
	switch {
	case end != nil:
		s.Phase = testsv1alpha1.StepPhasePassed
	case start != nil:
		s.Phase = testsv1alpha1.StepPhaseRunning
	}
	return s
}

// deriveStages lays out a leaf run's stages from its pod. Stages not
// reached yet are listed as queued, so the UI draws the whole course.
func deriveStages(in stageInputs) []testsv1alpha1.Stage {
	var fetchStart, fetchEnd, runStart, runEnd *metav1.Time
	var fetchExit int32
	var fetchMsg string
	if in.pod != nil {
		fetchStart, fetchEnd, fetchExit, fetchMsg = startEnd(containerStatus(in.pod.Status.InitContainerStatuses, compiler.ContainerContentFetcher))
		runStart, runEnd, _, _ = startEnd(containerStatus(in.pod.Status.ContainerStatuses, compiler.ContainerWrapper))
	}

	queueEnd := runStart
	if in.fetchName != "" && fetchStart != nil {
		queueEnd = fetchStart
	}
	out := []testsv1alpha1.Stage{stage(testsv1alpha1.StageQueue, stageQueuedName, in.queuedAt, queueEnd)}

	if in.fetchName != "" {
		f := stage(testsv1alpha1.StageFetch, in.fetchName, fetchStart, fetchEnd)
		if fetchEnd != nil && fetchExit != 0 {
			f.Phase, f.Message = testsv1alpha1.StepPhaseError, fetchMsg
		}
		out = append(out, f)
	}

	name := "Run test"
	if in.tool != "" {
		name = "Run " + in.tool
	}
	toolEnd := in.toolFinishedAt
	if toolEnd == nil {
		toolEnd = runEnd
	}
	r := stage(testsv1alpha1.StageRun, name, runStart, toolEnd)
	if toolEnd != nil && in.toolPhase != "" {
		r.Phase = testsv1alpha1.StepPhase(in.toolPhase)
	}
	out = append(out, r)

	var collectStart *metav1.Time
	if in.toolFinishedAt != nil {
		collectStart = in.toolFinishedAt
	} else if runEnd != nil {
		collectStart = runEnd
	}
	out = append(out, stage(testsv1alpha1.StageCollect, "Collect results", collectStart, runEnd))
	return out
}

// finishStages closes the stages when the run ends. The stage the run was
// in takes the run's verdict, with the reason as its message; the stages
// after it are skipped. A fetch that failed on its own ends the course
// there too. A tool that failed doesn't: results are still collected.
func finishStages(stages []testsv1alpha1.Stage, phase testsv1alpha1.Phase, message string, now metav1.Time) []testsv1alpha1.Stage {
	out := make([]testsv1alpha1.Stage, len(stages))
	copy(out, stages)
	stopped := false
	for i := range out {
		s := &out[i]
		open := s.Phase == testsv1alpha1.StepPhaseQueued || s.Phase == testsv1alpha1.StepPhaseRunning
		switch {
		case stopped:
			if open {
				s.Phase = testsv1alpha1.StepPhaseSkipped
			}
		case open:
			if phase == testsv1alpha1.PhasePassed {
				s.Phase = testsv1alpha1.StepPhasePassed
			} else {
				s.Phase, s.Message, stopped = testsv1alpha1.StepPhase(phase), message, true
			}
			if s.StartedAt == nil {
				s.StartedAt = &now
			}
			if s.FinishedAt == nil {
				s.FinishedAt = &now
			}
		case s.Kind == testsv1alpha1.StageFetch && s.Phase != testsv1alpha1.StepPhasePassed:
			stopped = true
		case s.Kind == testsv1alpha1.StageRun && s.Phase != testsv1alpha1.StepPhasePassed && s.Message == "":
			s.Message = message // the tool's own verdict, say why
		}
	}
	return out
}

// sameStages reports whether two stage lists say the same (for status
// writes only when something changed).
func sameStages(a, b []testsv1alpha1.Stage) bool {
	return reflect.DeepEqual(a, b)
}

// stagesNow derives the run's stages from its pod and result.
func (r *TestRunReconciler) stagesNow(run *testsv1alpha1.TestRun, pod *corev1.Pod, result *RunResult) []testsv1alpha1.Stage {
	in := stageInputs{queuedAt: run.Status.QueuedAt, pod: pod, fetchName: fetchNameOf(run), tool: toolOf(pod)}
	if in.queuedAt == nil {
		t := run.CreationTimestamp
		in.queuedAt = &t
	}
	if result != nil {
		in.toolPhase = result.Phase
		if result.ToolFinishedAt != nil {
			t := metav1.NewTime(*result.ToolFinishedAt)
			in.toolFinishedAt = &t
		}
	}
	return deriveStages(in)
}

// fetchTailerID keys the content fetcher's log tailer (the fetch stage's
// log, stored under the run's stages/fetch/).
func fetchTailerID(run *testsv1alpha1.TestRun) string { return tailerID(run) + "/fetch" }

// ensureFetchTailer starts tailing a pod's content-fetcher log, into keys,
// once its container has started — while the pod is still Pending — so
// the fetch stage has a log of its own, live. Best-effort, like the run's
// log. id keys the tailer.
func (r *TestRunReconciler) ensureFetchTailer(ctx context.Context, run *testsv1alpha1.TestRun, pod *corev1.Pod,
	id string, keys storage.RunKeys) {
	if r.LogRegistry == nil || pod == nil || fetchNameOf(run) == "" {
		return
	}
	start, _, _, _ := startEnd(containerStatus(pod.Status.InitContainerStatuses, compiler.ContainerContentFetcher))
	if start == nil {
		return
	}
	if err := r.LogRegistry.EnsureContainerTailer(ctx, id, keys, pod.Namespace, pod.Name,
		compiler.ContainerContentFetcher); err != nil {
		log.FromContext(ctx).Error(err, "EnsureContainerTailer (content fetcher) failed", "run", run.Name, "tailer", id)
	}
}

// waitingStages is the course of a run held back before it has a pod — by
// its Test's concurrencyPolicy (Forbid) or its notBefore: a queue stage
// that runs, with why. The rest of its course follows once it starts.
func waitingStages(queuedAt *metav1.Time, why string) []testsv1alpha1.Stage {
	return []testsv1alpha1.Stage{{Kind: testsv1alpha1.StageQueue, Name: stageQueuedName,
		Phase: testsv1alpha1.StepPhaseRunning, StartedAt: queuedAt, Message: why}}
}

// activePriorNames are the names of the runs a Forbid run waits for.
func activePriorNames(prior []testsv1alpha1.TestRun, self *testsv1alpha1.TestRun) []string {
	var out []string
	for i := range prior {
		p := &prior[i]
		if p.UID != self.UID && !IsTerminalPhase(p.Status.Phase) {
			out = append(out, p.Name)
		}
	}
	slices.Sort(out)
	return out
}

// forbidMessage says which runs a Forbid run waits for.
func forbidMessage(names []string) string {
	switch {
	case len(names) == 0:
		return "waiting: a previous run of this Test is still going (concurrencyPolicy: Forbid)"
	case len(names) > 3:
		return fmt.Sprintf("waiting for %s and %d more to finish (concurrencyPolicy: Forbid)",
			strings.Join(names[:3], ", "), len(names)-3)
	}
	return "waiting for " + strings.Join(names, ", ") + " to finish (concurrencyPolicy: Forbid)"
}
