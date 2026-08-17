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

// TriggerResourceSelector filters resource events by name/namespace/labels.
type TriggerResourceSelector struct {
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	NameRegex string `json:"nameRegex,omitempty"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// +optional
	NamespaceRegex string `json:"namespaceRegex,omitempty"`
	// +optional
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
}

// TriggerCondition describes a required k8s status condition (see CLAUDE.md §5).
type TriggerCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	// +optional
	Reason string `json:"reason,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	TTL int32 `json:"ttl,omitempty"`
}

// TriggerConditionSpec gates trigger firing on target-resource conditions.
type TriggerConditionSpec struct {
	// +optional
	Conditions []TriggerCondition `json:"conditions,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	Timeout int32 `json:"timeout,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	Delay int32 `json:"delay,omitempty"`
}

// TriggerTestSelector picks the Test to run when the trigger fires.
type TriggerTestSelector struct {
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// +optional
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
}

// TriggerActionParameters overrides for the resulting TestRun.
type TriggerActionParameters struct {
	// +optional
	Config map[string]string `json:"config,omitempty"`
	// +optional
	Tags map[string]string `json:"tags,omitempty"`
}

// TestTriggerSpec fires a Test when a watched k8s resource changes (CLAUDE.md §5).
// The controller landing in step 12 implements the watch/gate/dispatch flow;
// step 02 stops at type + schema so GitOps manifests can be authored today.
type TestTriggerSpec struct {
	// Resource is the k8s resource type to watch (e.g. deployment, pod, or any CRD by GVK).
	Resource string `json:"resource"`

	// +optional
	ResourceSelector *TriggerResourceSelector `json:"resourceSelector,omitempty"`

	// +kubebuilder:validation:Enum=created;modified;deleted
	Event string `json:"event"`

	// +optional
	ConditionSpec *TriggerConditionSpec `json:"conditionSpec,omitempty"`

	// +kubebuilder:validation:Enum=run
	Action string `json:"action"`

	// +kubebuilder:validation:Enum=workflow;test
	Execution string `json:"execution"`

	// +kubebuilder:validation:Enum=allow;forbid;replace
	// +optional
	ConcurrencyPolicy string `json:"concurrencyPolicy,omitempty"`

	// +optional
	TestSelector *TriggerTestSelector `json:"testSelector,omitempty"`

	// +optional
	ActionParameters *TriggerActionParameters `json:"actionParameters,omitempty"`
}

// TestTriggerStatus tracks last-fire state (populated in step 12).
type TestTriggerStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	LastFiredAt *metav1.Time `json:"lastFiredAt,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Resource",type=string,JSONPath=`.spec.resource`
// +kubebuilder:printcolumn:name="Event",type=string,JSONPath=`.spec.event`
// +kubebuilder:printcolumn:name="Action",type=string,JSONPath=`.spec.action`

// TestTrigger fires a Test when a watched k8s resource changes.
type TestTrigger struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec TestTriggerSpec `json:"spec"`

	// +optional
	Status TestTriggerStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// TestTriggerList contains a list of TestTrigger.
type TestTriggerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []TestTrigger `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &TestTrigger{}, &TestTriggerList{})
		return nil
	})
}
