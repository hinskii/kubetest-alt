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
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// Reasons surfaced in TestRunStatus.Message. Kept as constants so tests can
// assert on exact substrings without duplicating literals.
const (
	ReasonTestNotFound       = "TestNotFound"
	ReasonInvalidConfig      = "InvalidConfig"
	ReasonCompileError       = "CompileError"
	ReasonOrphanJobMissing   = "OrphanJobMissing"
	ReasonOOMKilled          = "OOMKilled"
	ReasonImagePull          = "ImagePullBackOff"
	ReasonJobDeadline        = "JobActiveDeadlineExceeded"
	ReasonContentFetchFailed = "ContentFetchFailed"

	// ReasonResolveFailed is set on TestRun.status.message when template
	// resolution, config coercion, expression eval, or post-resolution
	// validation fails. Step 13 introduced this because those checks
	// happen in the controller (cross-object lookup + template resolution)
	// rather than the webhook.
	ReasonResolveFailed = "ResolveFailed"

	// k8sReasonDeadlineExceeded is the string batch/v1 sets on JobCondition
	// when activeDeadlineSeconds fires. Kept as an unexported const so goconst
	// doesn't complain about the literal appearing on the reader-side too.
	k8sReasonDeadlineExceeded = "DeadlineExceeded"
	ReasonAborted             = "AbortedByConcurrency"
	ReasonMissingResult       = "MissingResult"
)

// IsTerminalPhase reports whether a Phase means "done, no more transitions".
// The reconciler treats terminal phases as no-op — subsequent reconciles
// return early without any writes, which is what keeps event-driven design
// from hot-looping on its own updates.
func IsTerminalPhase(p testsv1alpha1.Phase) bool {
	switch p {
	case testsv1alpha1.PhasePassed,
		testsv1alpha1.PhaseFailed,
		testsv1alpha1.PhaseError,
		testsv1alpha1.PhaseAborted:
		return true
	}
	return false
}

// JobConclusion is the terminal Job state — succeeded or failed.
type JobConclusion int

const (
	JobStillRunning JobConclusion = iota
	JobSucceeded
	JobFailedConclusion
)

// InspectJob classifies a Job as still-running, succeeded, or failed by
// walking its conditions. batchv1 sets JobComplete=true or JobFailed=true
// exactly once when the Job terminates.
func InspectJob(job *batchv1.Job) JobConclusion {
	if job == nil {
		return JobStillRunning
	}
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return JobSucceeded
		case batchv1.JobFailed:
			return JobFailedConclusion
		}
	}
	return JobStillRunning
}

// InfraFailure captures a non-test failure (infra) detected from Pod status.
// Empty Reason means no infra failure — Pod is in a normal state.
type InfraFailure struct {
	Reason  string
	Message string
}

// AnalyzePod looks for infrastructure failures on a Pod that should terminate
// the TestRun as phase=error rather than phase=failed (§15.3).
//
// Detected today (checked in order):
//   - ImagePullBackOff / ErrImagePull on any container → ReasonImagePull
//   - OOMKilled on any container → ReasonOOMKilled
//   - Init container Terminated with ExitCode>0 → ReasonContentFetchFailed,
//     message from Terminated.Message (which k8s populates from
//     /dev/termination-log — the fetcher writes FETCH_ERROR there).
//
// Returns zero value (empty Reason) if the pod looks healthy or is nil.
func AnalyzePod(pod *corev1.Pod) InfraFailure {
	if pod == nil {
		return InfraFailure{}
	}

	// Check init containers first — content-fetcher failures should surface
	// with their own image name in the message.
	allStatuses := append([]corev1.ContainerStatus(nil), pod.Status.InitContainerStatuses...)
	allStatuses = append(allStatuses, pod.Status.ContainerStatuses...)

	// ImagePullBackOff is exposed on Waiting state — check it first because a
	// pod stuck pulling never reaches Terminated. k8s emits either
	// "ImagePullBackOff" (retry loop) or "ErrImagePull" (single failure);
	// treat both as the same infra reason.
	const k8sReasonErrImagePull = "ErrImagePull"
	for _, cs := range allStatuses {
		if cs.State.Waiting == nil {
			continue
		}
		r := cs.State.Waiting.Reason
		if r == ReasonImagePull || r == k8sReasonErrImagePull {
			return InfraFailure{
				Reason:  ReasonImagePull,
				Message: fmt.Sprintf("infra: %s %s", r, cs.Image),
			}
		}
	}

	// OOMKilled — check both current and last termination state.
	for _, cs := range allStatuses {
		if term := cs.State.Terminated; term != nil && term.Reason == ReasonOOMKilled {
			return InfraFailure{
				Reason:  ReasonOOMKilled,
				Message: "OOMKilled — raise spec.container.resources",
			}
		}
		if term := cs.LastTerminationState.Terminated; term != nil && term.Reason == ReasonOOMKilled {
			return InfraFailure{
				Reason:  ReasonOOMKilled,
				Message: "OOMKilled — raise spec.container.resources",
			}
		}
	}

	// Init container non-zero exit → content-fetcher (step 06) failed.
	// Message comes from Terminated.Message which k8s populates from
	// /dev/termination-log; the fetcher writes "FETCH_ERROR: <reason>" there.
	for _, cs := range pod.Status.InitContainerStatuses {
		term := cs.State.Terminated
		if term == nil || term.ExitCode == 0 {
			continue
		}
		msg := term.Message
		if msg == "" {
			msg = fmt.Sprintf("init container %q exited with code %d", cs.Name, term.ExitCode)
		}
		return InfraFailure{
			Reason:  ReasonContentFetchFailed,
			Message: msg,
		}
	}
	return InfraFailure{}
}

// IsPodRunning reports whether the pod has transitioned to Running phase.
// Callers use this to promote TestRun phase queued→running as soon as the
// wrapper container actually starts, not merely once the Job is admitted.
func IsPodRunning(pod *corev1.Pod) bool {
	return pod != nil && pod.Status.Phase == corev1.PodRunning
}

// FallbackPhaseFromJobFailure derives a TestRun phase when the Job is Failed
// but the wrapper didn't write a result.json (crash/SIGKILL). §15.2:
// "Missing result.json → fallback path = container exit code + pod
// terminated state → phase error with reason. Never assume result.json exists."
//
// If the Job's failure was ADSExceeded (activeDeadlineSeconds), map to
// ReasonJobDeadline; otherwise generic MissingResult.
func FallbackPhaseFromJobFailure(job *batchv1.Job) (testsv1alpha1.Phase, string, string) {
	reason := ReasonMissingResult
	message := "wrapper produced no result.json; container likely crashed or was SIGKILL'd"
	if job != nil {
		for _, c := range job.Status.Conditions {
			if c.Status == corev1.ConditionTrue && c.Type == batchv1.JobFailed &&
				c.Reason == k8sReasonDeadlineExceeded {
				reason = ReasonJobDeadline
				message = "Job exceeded activeDeadlineSeconds; wrapper should have flushed within its own timeout"
			}
		}
	}
	return testsv1alpha1.PhaseError, reason, message
}
