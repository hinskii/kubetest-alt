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

package compiler

import (
	"maps"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// TestAnnotations_PassThrough_NoInjection is the §8 core invariant expressed
// as a compiler-level test. The plan (step-03) demands: "assert compiler output
// contains ZERO annotations not present in input (no hardcoded injection)".
//
// The subtests below cover the four practical shapes: nil, empty, arbitrary
// keys (including infra ones the operator would be tempted to touch), and the
// TestRun-wins merge. Each subtest is a diff of pod-template annotations
// against the input union — the operator adds nothing outside that union.
func TestAnnotations_PassThrough_NoInjection(t *testing.T) {
	cases := []struct {
		name    string
		testPod *testsv1alpha1.PodConfig
		runPod  *testsv1alpha1.PodConfig
		want    map[string]string // expected pod template annotations (nil = no annotations at all)
	}{
		{
			name: "nil pod on both -> nil annotations",
			want: nil,
		},
		{
			name:    "empty annotations on both -> nil annotations",
			testPod: &testsv1alpha1.PodConfig{Annotations: map[string]string{}},
			runPod:  &testsv1alpha1.PodConfig{Annotations: map[string]string{}},
			want:    nil,
		},
		{
			name: "Test-only annotations pass through verbatim",
			testPod: &testsv1alpha1.PodConfig{Annotations: map[string]string{
				"sidecar.istio.io/inject": "false",
				"foo/bar":                 "baz",
			}},
			want: map[string]string{
				"sidecar.istio.io/inject": "false",
				"foo/bar":                 "baz",
			},
		},
		{
			name: "TestRun-only annotations pass through verbatim",
			runPod: &testsv1alpha1.PodConfig{Annotations: map[string]string{
				"custom.io/marker": "run-only",
			}},
			want: map[string]string{"custom.io/marker": "run-only"},
		},
		{
			name:    "TestRun value wins on key conflict",
			testPod: &testsv1alpha1.PodConfig{Annotations: map[string]string{"k": "from-test"}},
			runPod:  &testsv1alpha1.PodConfig{Annotations: map[string]string{"k": "from-run"}},
			want:    map[string]string{"k": "from-run"},
		},
		{
			name: "union of Test + TestRun keys",
			testPod: &testsv1alpha1.PodConfig{Annotations: map[string]string{
				"a": "1", "b": "2",
			}},
			runPod: &testsv1alpha1.PodConfig{Annotations: map[string]string{
				"c": "3", "a": "overridden",
			}},
			want: map[string]string{"a": "overridden", "b": "2", "c": "3"},
		},
		{
			name: "arbitrary long values do not get truncated or normalized",
			testPod: &testsv1alpha1.PodConfig{Annotations: map[string]string{
				"long":     strings.Repeat("x", 4096),
				"quotes":   `has "quotes" and \backslashes`,
				"newlines": "line1\nline2\r\nline3",
				"utf8":     "√ 你好 🚀",
			}},
			want: map[string]string{
				"long":     strings.Repeat("x", 4096),
				"quotes":   `has "quotes" and \backslashes`,
				"newlines": "line1\nline2\r\nline3",
				"utf8":     "√ 你好 🚀",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			test := canonicalTest()
			test.Spec.Pod = tc.testPod
			run := canonicalTestRun()
			run.Spec.Pod = tc.runPod

			job, _, err := Compile(test, run, defaultOpts())
			require.NoError(t, err)

			got := job.Spec.Template.Annotations

			// Assertion 1: output matches expected exactly.
			assert.Equal(t, tc.want, got, "pod template annotations mismatch")

			// Assertion 2 (regression guard): every output key must appear in
			// the input union — the operator MUST NOT inject annotations of its own.
			inputUnion := unionAnnotationKeys(tc.testPod, tc.runPod)
			for k := range got {
				assert.Contains(t, inputUnion, k,
					"compiler injected an annotation not in input: %q — this is a §8 regression", k)
			}
		})
	}
}

// TestLabels_MergeAndReservedOverlay covers the label side (which does have
// operator-owned keys, unlike annotations). Rules from CLAUDE.md §8 + step-03:
//   - Test → TestRun overlay (TestRun wins on key conflict);
//   - reserved kubetest.io/* + app.kubernetes.io/managed-by are ALWAYS operator-set;
//   - user attempts to set reserved keys are silently dropped.
func TestLabels_MergeAndReservedOverlay(t *testing.T) {
	t.Run("TestRun value wins on non-reserved key conflict", func(t *testing.T) {
		test := canonicalTest()
		test.Spec.Pod = &testsv1alpha1.PodConfig{Labels: map[string]string{"team": "sre"}}
		run := canonicalTestRun()
		run.Spec.Pod = &testsv1alpha1.PodConfig{Labels: map[string]string{"team": "platform"}}

		job, _, err := Compile(test, run, defaultOpts())
		require.NoError(t, err)
		assert.Equal(t, "platform", job.Spec.Template.Labels["team"])
	})

	t.Run("user cannot override kubetest.io/run-id", func(t *testing.T) {
		test := canonicalTest()
		test.Spec.Pod = &testsv1alpha1.PodConfig{Labels: map[string]string{
			"kubetest.io/run-id": "pwned",
		}}
		run := canonicalTestRun()
		run.Spec.Pod = &testsv1alpha1.PodConfig{Labels: map[string]string{
			"kubetest.io/run-id": "also-pwned",
		}}

		job, _, err := Compile(test, run, defaultOpts())
		require.NoError(t, err)
		assert.Equal(t, "sample-run", job.Spec.Template.Labels[LabelRunID],
			"operator must overwrite user-supplied kubetest.io/run-id")
	})

	t.Run("user cannot override app.kubernetes.io/managed-by on workloads", func(t *testing.T) {
		// Distinct from CRD-level managed-by (gitops|ui) per §7 — on workloads
		// this label is the operator's badge, always kubetest-alt.
		test := canonicalTest()
		test.Spec.Pod = &testsv1alpha1.PodConfig{Labels: map[string]string{
			LabelManagedBy: "helm",
		}}
		job, _, err := Compile(test, canonicalTestRun(), defaultOpts())
		require.NoError(t, err)
		assert.Equal(t, ManagedByValue, job.Spec.Template.Labels[LabelManagedBy])
	})

	t.Run("arbitrary kubetest.io/* keys are dropped (future-reserved prefix)", func(t *testing.T) {
		// Guards the reservation of the whole kubetest.io/ namespace — even
		// keys we haven't defined yet must not be smuggled in by user config.
		test := canonicalTest()
		test.Spec.Pod = &testsv1alpha1.PodConfig{Labels: map[string]string{
			"kubetest.io/future-feature-flag": "on",
			"team":                            "sre",
		}}
		job, _, err := Compile(test, canonicalTestRun(), defaultOpts())
		require.NoError(t, err)
		assert.NotContains(t, job.Spec.Template.Labels, "kubetest.io/future-feature-flag",
			"reserved kubetest.io/* prefix must drop user values")
		assert.Equal(t, "sre", job.Spec.Template.Labels["team"], "non-reserved user label preserved")
	})

	t.Run("operator labels always present, even when user set no labels", func(t *testing.T) {
		test := canonicalTest()
		test.Spec.Pod = nil // no user labels
		job, _, err := Compile(test, canonicalTestRun(), defaultOpts())
		require.NoError(t, err)
		assert.Equal(t, "sample-run", job.Spec.Template.Labels[LabelRunID])
		assert.Equal(t, ManagedByValue, job.Spec.Template.Labels[LabelManagedBy])
	})
}

// TestPodConfig_ScalarFields_MergedFromTestRunFirst verifies non-label/non-annotation
// PodConfig fields follow the TestRun-wins rule too — regression guard so a
// future refactor of mergePodConfig doesn't silently drop TestRun overrides.
func TestPodConfig_ScalarFields_MergedFromTestRunFirst(t *testing.T) {
	test := canonicalTest()
	test.Spec.Pod = &testsv1alpha1.PodConfig{
		ServiceAccountName: "test-sa",
		NodeSelector:       map[string]string{"disktype": "ssd"},
	}
	run := canonicalTestRun()
	run.Spec.Pod = &testsv1alpha1.PodConfig{
		ServiceAccountName: "override-sa",
		NodeSelector:       map[string]string{"disktype": "nvme", "env": "prod"},
	}

	job, _, err := Compile(test, run, defaultOpts())
	require.NoError(t, err)
	assert.Equal(t, "override-sa", job.Spec.Template.Spec.ServiceAccountName)
	assert.Equal(t, map[string]string{"disktype": "nvme", "env": "prod"},
		job.Spec.Template.Spec.NodeSelector,
		"NodeSelector: TestRun map replaces Test map wholesale")
}

// unionAnnotationKeys returns the set of keys present in Test.Pod.Annotations
// or TestRun.Pod.Annotations. Used by the regression-guard assertion above.
func unionAnnotationKeys(testPod, runPod *testsv1alpha1.PodConfig) map[string]struct{} {
	out := map[string]struct{}{}
	if testPod != nil {
		for k := range testPod.Annotations {
			out[k] = struct{}{}
		}
	}
	if runPod != nil {
		for k := range runPod.Annotations {
			out[k] = struct{}{}
		}
	}
	return out
}

// TestMergeAnnotations_DoesNotAliasInput ensures the returned map is a copy —
// mutating the compiler's output map must not touch the input.
func TestMergeAnnotations_DoesNotAliasInput(t *testing.T) {
	in := &testsv1alpha1.PodConfig{Annotations: map[string]string{"k": "v"}}
	out := mergeAnnotations(in, nil)
	out["k"] = "mutated"
	assert.Equal(t, "v", in.Annotations["k"], "output must be a copy, not an alias")
}

// TestReservedLabelPrefixConst exists so a rename or typo in the reserved
// prefix constant surfaces as a test failure rather than a silent regression.
func TestReservedLabelPrefixConst(t *testing.T) {
	assert.Equal(t, "kubetest.io/", ReservedLabelPrefix)
	assert.True(t, strings.HasPrefix(LabelRunID, ReservedLabelPrefix))
}

// TestMergeLabels_EmptyInputsStillYieldReserved is defensive — even with no
// user labels, the operator labels must be present (drives selector semantics
// in later steps).
func TestMergeLabels_EmptyInputsStillYieldReserved(t *testing.T) {
	got := mergeLabels(nil, nil, "myrun")
	require.NotNil(t, got)
	assert.Equal(t, "myrun", got[LabelRunID])
	assert.Equal(t, ManagedByValue, got[LabelManagedBy])
	// Only the reserved keys — nothing else.
	assert.Len(t, got, 2)
}

// Smoke: mergeAnnotations returns nil rather than empty map when no inputs.
// Empty vs nil matters for golden diffs — an empty map serializes as `{}`
// which would show up as a spurious diff in Job YAML.
func TestMergeAnnotations_EmptyIsNil(t *testing.T) {
	assert.Nil(t, mergeAnnotations(nil, nil))
	assert.Nil(t, mergeAnnotations(&testsv1alpha1.PodConfig{}, &testsv1alpha1.PodConfig{}))
}

// Guard on maps.Copy semantics — belt+suspenders for the merge path.
func TestMergeAnnotations_KeyOrderIrrelevant(t *testing.T) {
	// Just makes sure we don't accidentally depend on map iteration order.
	a := &testsv1alpha1.PodConfig{Annotations: map[string]string{"a": "1", "b": "2"}}
	b := &testsv1alpha1.PodConfig{Annotations: map[string]string{"b": "3", "c": "4"}}
	got := mergeAnnotations(a, b)
	want := map[string]string{"a": "1", "b": "3", "c": "4"}
	assert.True(t, maps.Equal(got, want))
}
