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
