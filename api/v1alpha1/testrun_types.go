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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// TestRunSpec is a single execution request for a Test.
type TestRunSpec struct {
	// TestRef names the Test in the same namespace. Non-empty is enforced by
	// the validating webhook. Cross-object existence is checked by the
	// controller (webhook stays shape-only, per step-02).
	TestRef string `json:"testRef"`

	// Config overrides for Test.spec.config parameters. The webhook validates
	// shape only; key existence is checked by the controller after resolving
	// the referenced Test.
	// +optional
	Config map[string]string `json:"config,omitempty"`

	// Per-run pod metadata overrides — merged onto Test.spec.pod at compile
	// time (CLAUDE.md §8). No hardcoded annotations anywhere.
	// +optional
	Pod *PodConfig `json:"pod,omitempty"`

	// +optional
	Tags map[string]string `json:"tags,omitempty"`

	// Source records provenance. Defaults to "api" via the defaulting webhook.
	// +kubebuilder:validation:Enum=ui;api;cli;cron;trigger;gitops
	// +optional
	Source string `json:"source,omitempty"`

	// Abort, once set, stops the run: the controller kills the Job (or
	// aborts the children of a composite run), ends the run as "aborted",
	// persists it to run history and fires webhooks — the same path every
	// other terminal transition takes. One-way: the validating webhook
	// rejects clearing or changing it. No effect on an already-terminal run.
	// +optional
	Abort *AbortRequest `json:"abort,omitempty"`

	// NotBefore schedules the run: the controller keeps it queued until
	// this time, then resolves the Test as it is THEN and starts it. Unset
	// or in the past starts it now. A waiting run doesn't count against
	// its Test's concurrencyPolicy. Cancel it by aborting it.
	// +optional
	NotBefore *metav1.Time `json:"notBefore,omitempty"`
}

// Abort reasons. The reason is part of the run's final status message so
// history shows WHY a run was stopped, not just that it was.
const (
	// AbortReasonUser: a person asked for it (GUI/API/CLI).
	AbortReasonUser = "User"
	// AbortReasonConcurrency: superseded by a newer run of a Test with
	// concurrencyPolicy=Replace.
	AbortReasonConcurrency = "Concurrency"
	// AbortReasonParent: the composite parent stopped this child (parent
	// aborted, or the step timed out).
	AbortReasonParent = "Parent"
)

// AbortRequest asks the controller to stop a run.
type AbortRequest struct {
	// +kubebuilder:validation:Enum=User;Concurrency;Parent
	Reason string `json:"reason"`

	// Message is free text shown in the run's status.
	// +kubebuilder:validation:MaxLength=512
	// +optional
	Message string `json:"message,omitempty"`

	// RequestedBy identifies who asked (user e-mail, or the run that
	// superseded this one).
	// +kubebuilder:validation:MaxLength=256
	// +optional
	RequestedBy string `json:"requestedBy,omitempty"`
}

// TestRunStatus captures per-run progress. Timestamps are populated by the
// controller (step 04+).
type TestRunStatus struct {
	// +optional
	Phase Phase `json:"phase,omitempty"`

	// +optional
	QueuedAt *metav1.Time `json:"queuedAt,omitempty"`

	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`

	// +optional
	DurationMs int64 `json:"durationMs,omitempty"`

	// +optional
	JobName string `json:"jobName,omitempty"`

	// Tool is the run's kubetest.io/tool identity, captured with the
	// resolvedSpec snapshot: the Test's label, else the last template in
	// spec.use that carries one. Propagated to the Job/Pod labels, metrics
	// and run history — the TestRun's own labels usually don't carry it.
	// +optional
	Tool string `json:"tool,omitempty"`

	// ResolvedSpec is a JSON snapshot of the Test spec taken at run start.
	// Historical runs remain interpretable after the Test is edited (§15.5).
	// +optional
	ResolvedSpec string `json:"resolvedSpec,omitempty"`

	// +optional
	Steps map[string]StepResult `json:"steps,omitempty"`

	// LogsRef is the object-storage key for streamed logs (step 08).
	// +optional
	LogsRef string `json:"logsRef,omitempty"`

	// +optional
	ArtifactRefs []ArtifactRef `json:"artifactRefs,omitempty"`

	// Metrics mirrors the wrapper's ExecutionResult.Metrics — a flat name→value
	// map of tool-emitted numeric metrics (p95_ms, rps, checks_passed/total).
	// Populated on passed AND failed runs; nil on error/aborted.
	// +optional
	Metrics map[string]string `json:"metrics,omitempty"`

	// TestCounts aggregates JUnit-derived counts across all scraped XML files.
	// Nil when the tool didn't emit JUnit output.
	// +optional
	TestCounts *TestCounts `json:"testCounts,omitempty"`

	// +optional
	Message string `json:"message,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Test",type=string,JSONPath=`.spec.testRef`
// +kubebuilder:printcolumn:name="Tool",type=string,JSONPath=`.status.tool`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Started",type=date,JSONPath=`.status.startedAt`

// TestRun is a single execution of a Test.
type TestRun struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec TestRunSpec `json:"spec"`

	// +optional
	Status TestRunStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// TestRunList contains a list of TestRun.
type TestRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []TestRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &TestRun{}, &TestRunList{})
		return nil
	})
}
