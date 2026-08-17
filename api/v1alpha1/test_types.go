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

// TestSpec defines the desired state of a Test.
type TestSpec struct {
	// Type selects the executor wrapper image. The validating webhook enforces
	// this enum in addition to the declarative marker so the message is
	// consistent across api-server and webhook rejections.
	// +kubebuilder:validation:Enum=k6;cypress;newman;locust;jmeter
	Type string `json:"type"`

	// +optional
	Content Content `json:"content,omitempty"`

	// +optional
	Container ContainerConfig `json:"container,omitempty"`

	// +optional
	Pod *PodConfig `json:"pod,omitempty"`

	// +optional
	Config map[string]Parameter `json:"config,omitempty"`

	// +optional
	Artifacts *ArtifactSpec `json:"artifacts,omitempty"`

	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// +optional
	Retry *RetryPolicy `json:"retry,omitempty"`

	// Schedule is a standard cron expression. Empty = manual only.
	// The webhook parses this against robfig/cron/v3.
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// +optional
	Services map[string]ServiceSpec `json:"services,omitempty"`

	// +optional
	Parallel *ParallelSpec `json:"parallel,omitempty"`

	// ConcurrencyPolicy defaults to Allow via the defaulting webhook.
	// +kubebuilder:validation:Enum=Allow;Forbid;Replace
	// +optional
	ConcurrencyPolicy string `json:"concurrencyPolicy,omitempty"`
}

// TestStatus defines the observed state of a Test.
type TestStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	LatestRun *RunReference `json:"latestRun,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="LastRun",type=string,JSONPath=`.status.latestRun.phase`

// Test is the definition of a runnable test. Immutable-in-spirit definition;
// executions live in TestRun.
type Test struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec TestSpec `json:"spec"`

	// +optional
	Status TestStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// TestList contains a list of Test.
type TestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Test `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Test{}, &TestList{})
		return nil
	})
}
