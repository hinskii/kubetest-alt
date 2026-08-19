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
//
// Workflows model (step 11 refactor): a Test is an image + command, not a
// tool identity. There is no `spec.type` — tool identity, when it matters
// for UI grouping, lives in the `kubetest.io/tool` label. The verdict is
// derived from the process exit code by default; tools whose exit codes
// lie (JMeter, some CI runners) opt into a declarative Verdict processor
// (JUnit / JTL) — see spec.verdict below.
type TestSpec struct {
	// +optional
	Content Content `json:"content,omitempty"`

	// Container carries image + command/args + env + resources. image is
	// REQUIRED (webhook-enforced); at least one of command/args is REQUIRED
	// so the wrapper knows what to invoke.
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

	// Verdict overrides the default exit-code verdict rule. Unset (or From=
	// exitCode) means "trust the process exit code" — passed on 0, failed
	// on non-zero. From=junit or From=jtl runs the matching processor
	// AFTER the tool exits and OVERRIDES the exit-code verdict in both
	// directions (jmeter exit 0 + failing JTL → failed; flaky non-zero +
	// clean JUnit → passed). See CLAUDE.md §11 + §15.2.
	// +optional
	Verdict *VerdictSpec `json:"verdict,omitempty"`
}

// VerdictSpec is the declarative "verdict-from" processor selector.
// Kept intentionally tiny — one enum, one string. Tool-specific knobs
// (JMeter threads, k6 summary keys) belong in the Test's container.args
// or in a TestTemplate, NOT here.
type VerdictSpec struct {
	// From is the verdict source. Defaults to exitCode when unset.
	// +kubebuilder:validation:Enum=exitCode;junit;jtl
	// +kubebuilder:default=exitCode
	From string `json:"from,omitempty"`

	// ErrorRateMax is the JTL error-rate threshold (fraction in [0,1]) —
	// only meaningful when From=jtl. String rather than float so the CRD
	// admits either "0" or "0.01" naturally without JSON-number gotchas
	// (0.1 round-trip surprises, no way to say "unset"). Webhook parses
	// this as a float in [0,1] and requires From=jtl.
	// +optional
	ErrorRateMax string `json:"errorRateMax,omitempty"`
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
// +kubebuilder:printcolumn:name="Tool",type=string,JSONPath=`.metadata.labels['kubetest\.io/tool']`
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
