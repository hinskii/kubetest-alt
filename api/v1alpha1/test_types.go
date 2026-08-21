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

	// Use references TestTemplate names IN THE SAME NAMESPACE to merge under
	// this Test. Templates are merged in order (later wins) BEFORE this Test's
	// own fields overlay (Test always wins over templates). See CLAUDE.md §2
	// (step 13). Empty = no template composition.
	//
	// Because a template may supply container.image and container.command/args,
	// the Test webhook accepts a Test with EMPTY container.image only when
	// spec.use is non-empty; final image+command validity is enforced by the
	// TestRun controller AFTER template resolution (phase=error reason=ResolveFailed).
	// +optional
	Use []string `json:"use,omitempty"`

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

	// Steps turns this Test into a COMPOSITE — a scenario that runs other
	// Tests in ordered steps. Mutually exclusive with the leaf shape:
	// when Steps is non-empty, container/content/use/verdict/services/
	// parallel MUST be empty (webhook-enforced). See CLAUDE.md §step-17.
	//
	// Each step's `execute.tests[]` names Tests in the same namespace;
	// the controller spawns child TestRuns for them, aggregates per step,
	// and sequences steps. Skip-on-fail: when a non-optional step fails,
	// subsequent condition:passed steps are marked skipped in
	// Status.Steps (StepResult.Phase="skipped") — the TestRun-level
	// Phase enum stays unchanged.
	// +optional
	Steps []Step `json:"steps,omitempty"`
}

// Step is one entry in a composite Test's spec.steps[]. Semantics mirror
// TestWorkflow's per-step subset the plan targets (execute-only — no
// in-pod shell/run steps, no matrix/shards, no services, no
// transfer/fetch; those are explicitly out of scope for step 17).
type Step struct {
	// +optional
	Name string `json:"name,omitempty"`

	// Condition gates whether this step runs based on prior-step outcome.
	// "passed" (default) — run only if all prior non-optional steps passed;
	// "always" — run regardless.
	// +kubebuilder:validation:Enum=passed;always
	// +optional
	Condition string `json:"condition,omitempty"`

	// Optional=true means a failure in this step is EXCLUDED from the
	// parent's aggregate (the parent can still pass if every non-optional
	// step passed). Optional does NOT skip the step — it still runs.
	// +optional
	Optional bool `json:"optional,omitempty"`

	// Negative=true INVERTS this step's per-child pass/fail — a child
	// that fails counts as passed for aggregation. Useful for
	// "expected-to-fail" scenarios (canary detection, chaos smoke).
	// +optional
	Negative bool `json:"negative,omitempty"`

	// Retry re-creates FAILED children of this step up to Count times
	// (each retry gets an exec-index suffix -r{N}). Retry does not
	// affect the child Test's own retry semantics — this is
	// step-scoped retry only.
	// +optional
	Retry *RetryPolicy `json:"retry,omitempty"`

	// Timeout is per-step wall-clock, measured from the first child
	// creation. Expiry aborts still-running children (their Jobs get
	// deleted) and the step aggregates to failed.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// Delay is a wait before the FIRST child of this step is created.
	// Simpler than depending on prior-step wall-clock; useful when a
	// prior step provisions state that has its own settle time.
	// +optional
	Delay *metav1.Duration `json:"delay,omitempty"`

	// Execute is the only allowed step body in step 17 (mirrors CLAUDE.md
	// out-of-scope note — no run/shell/service steps). Webhook enforces
	// non-nil.
	// +optional
	Execute *StepExecute `json:"execute,omitempty"`
}

// StepExecute holds the "run these other Tests" body of a step.
type StepExecute struct {
	// Parallelism caps concurrent child TestRuns within THIS step.
	// 0 (default) = unlimited within the step (i.e. every child runs
	// concurrently). Non-zero clamps the fan-out to N in-flight; the
	// controller starts more as prior ones terminate.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Parallelism int32 `json:"parallelism,omitempty"`

	// Tests is the list of Test references (by name, same namespace)
	// this step executes. At least one is required (webhook).
	// +optional
	Tests []StepExecuteTest `json:"tests,omitempty"`
}

// StepExecuteTest is one Test reference in a step's execute.tests[].
type StepExecuteTest struct {
	// Name of a Test in the same namespace.
	// +required
	Name string `json:"name"`

	// Count creates N replicas of this reference (default 1). Each
	// replica gets a distinct child TestRun; `{{ index }}` in Config
	// values resolves to the 0-based replica index. `{{ count }}`
	// resolves to Count itself.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Count int32 `json:"count,omitempty"`

	// Config is a parameter overlay applied to the child Test's
	// spec.config defaults. Values may reference `{{ index }}` /
	// `{{ count }}` which the reconciler renders per replica. All
	// other expression scopes (env, config) inherit from the child
	// Test's own resolve pipeline (step 13).
	// +optional
	Config map[string]string `json:"config,omitempty"`
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
