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

// TestTemplateSpec is a reusable, parameterizable partial Test spec. Step-02
// keeps this shape minimal; the compose/inclusion semantics land with step 13
// (templates + expression engine).
type TestTemplateSpec struct {
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

	// +optional
	Services map[string]ServiceSpec `json:"services,omitempty"`

	// +optional
	Parallel *ParallelSpec `json:"parallel,omitempty"`
}

// TestTemplateStatus is intentionally minimal — templates are pure definitions.
type TestTemplateStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// TestTemplate is a reusable partial Test spec.
type TestTemplate struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec TestTemplateSpec `json:"spec"`

	// +optional
	Status TestTemplateStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// TestTemplateList contains a list of TestTemplate.
type TestTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []TestTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &TestTemplate{}, &TestTemplateList{})
		return nil
	})
}
