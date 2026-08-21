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
	"maps"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// baseValidSpec is the minimum spec that passes every validateTest rule.
// Individual test cases mutate a copy of it to isolate the rule under test.
//
// Workflows model: image + command|args required. No spec.type anymore.
func baseValidSpec() testsv1alpha1.TestSpec {
	return testsv1alpha1.TestSpec{
		ConcurrencyPolicy: "Allow",
		Container: testsv1alpha1.ContainerConfig{
			Image: "grafana/k6:2.2.0",
			Args:  []string{"run", "script.js"},
		},
	}
}

// TestValidateTest_ImageRequired: the workflows model has no built-in tool
// images; a Test WITHOUT spec.use must supply spec.container.image directly.
func TestValidateTest_ImageRequired(t *testing.T) {
	spec := baseValidSpec()
	spec.Container.Image = ""
	err := validateTest(&spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.container.image is required")
}

// TestValidateTest_ImageOptional_WhenSpecUse: step 13 — a Test that
// references a TestTemplate via spec.use may leave image empty; the
// template may supply it. Final validation happens in the controller
// post-resolution.
func TestValidateTest_ImageOptional_WhenSpecUse(t *testing.T) {
	spec := baseValidSpec()
	spec.Container.Image = ""
	spec.Container.Command = nil
	spec.Container.Args = nil
	spec.Use = []string{"my-k6-template"}
	err := validateTest(&spec)
	assert.NoError(t, err,
		"webhook must permit missing image/command when spec.use is non-empty; controller re-validates post-resolution")
}

// TestValidateTest_CommandOrArgsRequired: at least one of command / args
// must be set — otherwise the wrapper has no invocation to run.
func TestValidateTest_CommandOrArgsRequired(t *testing.T) {
	spec := baseValidSpec()
	spec.Container.Args = nil
	spec.Container.Command = nil
	err := validateTest(&spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one of command or args is required")

	// Command-only is fine.
	spec.Container.Command = []string{"/opt/tool/entrypoint.sh"}
	assert.NoError(t, validateTest(&spec))
}

func TestValidateTest_Verdict(t *testing.T) {
	cases := []struct {
		name       string
		verdict    *testsv1alpha1.VerdictSpec
		wantErr    bool
		wantSubstr string
	}{
		{"nil verdict (default exitCode)", nil, false, ""},
		{"empty From (treated as exitCode)", &testsv1alpha1.VerdictSpec{}, false, ""},
		{"exitCode explicit", &testsv1alpha1.VerdictSpec{From: "exitCode"}, false, ""},
		{"junit no threshold", &testsv1alpha1.VerdictSpec{From: "junit"}, false, ""},
		{"jtl with threshold 0", &testsv1alpha1.VerdictSpec{From: "jtl", ErrorRateMax: "0"}, false, ""},
		{"jtl with threshold 0.05", &testsv1alpha1.VerdictSpec{From: "jtl", ErrorRateMax: "0.05"}, false, ""},
		{"jtl with threshold 1", &testsv1alpha1.VerdictSpec{From: "jtl", ErrorRateMax: "1"}, false, ""},
		{"invalid From", &testsv1alpha1.VerdictSpec{From: "coinflip"}, true, "spec.verdict.from"},
		{"junit + errorRateMax → 400 (misplaced knob)",
			&testsv1alpha1.VerdictSpec{From: "junit", ErrorRateMax: "0.01"}, true, "only valid when spec.verdict.from=jtl"},
		{"exitCode + errorRateMax → 400",
			&testsv1alpha1.VerdictSpec{From: "exitCode", ErrorRateMax: "0.01"}, true, "only valid when spec.verdict.from=jtl"},
		{"jtl + non-numeric errorRateMax", &testsv1alpha1.VerdictSpec{From: "jtl", ErrorRateMax: "many"}, true, "not a valid float"},
		{"jtl + negative errorRateMax", &testsv1alpha1.VerdictSpec{From: "jtl", ErrorRateMax: "-0.1"}, true, "out of range"},
		{"jtl + errorRateMax > 1", &testsv1alpha1.VerdictSpec{From: "jtl", ErrorRateMax: "1.5"}, true, "out of range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := baseValidSpec()
			spec.Verdict = tc.verdict
			err := validateTest(&spec)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantSubstr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateTest_ConcurrencyPolicy(t *testing.T) {
	cases := []struct {
		name       string
		policy     string
		wantErr    bool
		wantSubstr string
	}{
		{"valid Allow", "Allow", false, ""},
		{"valid Forbid", "Forbid", false, ""},
		{"valid Replace", "Replace", false, ""},
		{"valid empty (defaulter will fill)", "", false, ""},
		{"invalid lowercase", "allow", true, "concurrencyPolicy"},
		{"invalid unknown", "Retry", true, "concurrencyPolicy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := baseValidSpec()
			spec.ConcurrencyPolicy = tc.policy
			err := validateTest(&spec)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantSubstr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateTest_GitURI(t *testing.T) {
	cases := []struct {
		name       string
		git        *testsv1alpha1.GitContent
		wantErr    bool
		wantSubstr string
	}{
		{"valid nil git", nil, false, ""},
		{"valid uri set", &testsv1alpha1.GitContent{URI: "https://example.com/repo.git"}, false, ""},
		{"invalid empty uri", &testsv1alpha1.GitContent{URI: ""}, true, "git.uri"},
		{"invalid non-empty other fields only",
			&testsv1alpha1.GitContent{Revision: "main"}, true, "git.uri"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := baseValidSpec()
			spec.Content.Git = tc.git
			err := validateTest(&spec)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantSubstr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateTest_InlineContentSize(t *testing.T) {
	// The plan's explicit thresholds: 511KB must pass; 513KB must fail with
	// a message containing "use git/tarball".
	cases := []struct {
		name       string
		sizeBytes  int
		wantErr    bool
		wantSubstr string
	}{
		{"empty (no files)", 0, false, ""},
		{"511 KB (below threshold)", 511 * 1024, false, ""},
		{"512 KB exact (still allowed)", 512 * 1024, false, ""},
		{"513 KB (above threshold)", 513 * 1024, true, "use git/tarball"},
		{"1 MB (well above)", 1024 * 1024, true, "use git/tarball"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := baseValidSpec()
			if tc.sizeBytes > 0 {
				spec.Content.Files = []testsv1alpha1.FileContent{
					{Path: "single.txt", Content: strings.Repeat("a", tc.sizeBytes)},
				}
			}
			err := validateTest(&spec)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantSubstr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateTest_InlineContentSize_MultipleFilesAggregate(t *testing.T) {
	// Regression: threshold is a SUM across files, not per-file.
	spec := baseValidSpec()
	spec.Content.Files = []testsv1alpha1.FileContent{
		{Path: "a.txt", Content: strings.Repeat("a", 300*1024)},
		{Path: "b.txt", Content: strings.Repeat("b", 300*1024)}, // 600KB total
	}
	err := validateTest(&spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "use git/tarball")
}

func TestValidateTest_Schedule(t *testing.T) {
	cases := []struct {
		name       string
		schedule   string
		wantErr    bool
		wantSubstr string
	}{
		{"valid empty (manual)", "", false, ""},
		{"valid 5-field daily", "0 6 * * *", false, ""},
		{"valid every minute", "* * * * *", false, ""},
		{"valid steps", "*/15 * * * *", false, ""},
		{"invalid minute out of range", "61 * * * *", true, "cron"},
		{"invalid random garbage", "not-a-cron", true, "cron"},
		{"invalid 6-field (with seconds)", "0 0 6 * * *", true, "cron"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := baseValidSpec()
			spec.Schedule = tc.schedule
			err := validateTest(&spec)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantSubstr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestValidateTest_PodConfigAnnotationsPassThrough is the §8 regression guard.
// PodConfig annotations/labels MUST NOT be rejected or mutated by validation —
// they land verbatim on the pod. Any hardcoded key would be a regression.
func TestValidateTest_PodConfigAnnotationsPassThrough(t *testing.T) {
	// A mix of keys that different infra stacks might inject.
	// If validation ever grew special-casing (e.g. rejecting a mesh key),
	// this test breaks — that's the point.
	tricky := map[string]string{
		"sidecar.istio.io/inject":               "false",
		"linkerd.io/inject":                     "disabled",
		"kubetest.io/user-annotation":           "arbitrary",
		"cluster-autoscaler.kubernetes.io/safe": "true",
		"":                                      "empty-key-should-not-panic",
		"a-key-with-a-very-long-value":          strings.Repeat("x", 4096),
	}
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "gitops",
		"owner":                        "team-a",
	}

	spec := baseValidSpec()
	spec.Pod = &testsv1alpha1.PodConfig{
		Annotations: cloneStringMap(tricky),
		Labels:      cloneStringMap(labels),
	}
	// Snapshot before.
	beforeAnn := cloneStringMap(spec.Pod.Annotations)
	beforeLab := cloneStringMap(spec.Pod.Labels)

	err := validateTest(&spec)
	require.NoError(t, err, "PodConfig annotations must never be rejected by validation")

	// After: no mutation.
	assert.Equal(t, beforeAnn, spec.Pod.Annotations, "annotations were mutated")
	assert.Equal(t, beforeLab, spec.Pod.Labels, "labels were mutated")
}

func TestValidateTestRun_TestRef(t *testing.T) {
	cases := []struct {
		name       string
		ref        string
		wantErr    bool
		wantSubstr string
	}{
		{"valid ref", "my-test", false, ""},
		{"invalid empty", "", true, "spec.testRef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTestRun(&testsv1alpha1.TestRunSpec{TestRef: tc.ref})
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantSubstr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// Nil-spec guards — belt+suspenders, in case a caller ever forgets to
// nil-check before reaching validator hooks.
func TestValidate_NilSpec(t *testing.T) {
	require.Error(t, validateTest(nil))
	require.Error(t, validateTestRun(nil))
}

// TestValidateTest_Composite_ShapeExclusivity is the step-17 sentinel:
// a Test with spec.steps set MUST NOT ALSO carry any leaf-shape field.
// Rejecting these at admission (not at reconcile) surfaces the shape
// bug before a run is ever scheduled — which is what §7 "definitions
// are the source of truth" needs from the webhook.
func TestValidateTest_Composite_ShapeExclusivity(t *testing.T) {
	compositeStep := testsv1alpha1.Step{
		Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "child"}}},
	}

	cases := []struct {
		name    string
		mutate  func(s *testsv1alpha1.TestSpec)
		wantSub string
	}{
		{
			name: "container image forbidden alongside steps",
			mutate: func(s *testsv1alpha1.TestSpec) {
				s.Container.Image = "grafana/k6:2.2.0"
				s.Container.Args = []string{"run", "x"}
			},
			wantSub: "spec.container",
		},
		{
			name:    "use[] forbidden alongside steps",
			mutate:  func(s *testsv1alpha1.TestSpec) { s.Use = []string{"template-x"} },
			wantSub: "spec.use",
		},
		{
			name: "content.files forbidden alongside steps",
			mutate: func(s *testsv1alpha1.TestSpec) {
				s.Content.Files = []testsv1alpha1.FileContent{{Path: "a", Content: "b"}}
			},
			wantSub: "spec.content",
		},
		{
			name:    "verdict forbidden alongside steps",
			mutate:  func(s *testsv1alpha1.TestSpec) { s.Verdict = &testsv1alpha1.VerdictSpec{From: "jtl"} },
			wantSub: "spec.verdict",
		},
		{
			name:    "services forbidden alongside steps",
			mutate:  func(s *testsv1alpha1.TestSpec) { s.Services = map[string]testsv1alpha1.ServiceSpec{"db": {}} },
			wantSub: "spec.services",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := testsv1alpha1.TestSpec{
				ConcurrencyPolicy: "Allow",
				Steps:             []testsv1alpha1.Step{compositeStep},
			}
			tc.mutate(&spec)
			err := validateTest(&spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSub)
			assert.Contains(t, err.Error(), "steps")
		})
	}
}

// TestValidateTest_Composite_StepRules covers the per-step invariants
// that only the webhook can catch (execute non-nil, ≥1 test, condition
// enum, count≥1).
func TestValidateTest_Composite_StepRules(t *testing.T) {
	cases := []struct {
		name    string
		step    testsv1alpha1.Step
		wantSub string
	}{
		{
			name:    "execute is required",
			step:    testsv1alpha1.Step{Name: "smoke"},
			wantSub: "execute is required",
		},
		{
			name:    "execute.tests must have ≥1",
			step:    testsv1alpha1.Step{Execute: &testsv1alpha1.StepExecute{Tests: nil}},
			wantSub: "at least one",
		},
		{
			name: "unknown condition rejected",
			step: testsv1alpha1.Step{
				Condition: "sometimes",
				Execute:   &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "x"}}},
			},
			wantSub: "condition",
		},
		{
			name: "empty ref name rejected",
			step: testsv1alpha1.Step{
				Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: ""}}},
			},
			wantSub: "name is required",
		},
		{
			name: "negative parallelism rejected",
			step: testsv1alpha1.Step{
				Execute: &testsv1alpha1.StepExecute{
					Parallelism: -1,
					Tests:       []testsv1alpha1.StepExecuteTest{{Name: "x"}},
				},
			},
			wantSub: "parallelism",
		},
		{
			name: "retry.count < 1 rejected",
			step: testsv1alpha1.Step{
				Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "x"}}},
				Retry:   &testsv1alpha1.RetryPolicy{Count: 0},
			},
			wantSub: "retry.count must be >= 1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := testsv1alpha1.TestSpec{
				ConcurrencyPolicy: "Allow",
				Steps:             []testsv1alpha1.Step{tc.step},
			}
			err := validateTest(&spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSub)
		})
	}
}

// TestValidateTest_Composite_HappyPath: a minimal, valid composite passes.
func TestValidateTest_Composite_HappyPath(t *testing.T) {
	spec := testsv1alpha1.TestSpec{
		ConcurrencyPolicy: "Allow",
		Steps: []testsv1alpha1.Step{
			{Name: "smoke", Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "smoke-test", Count: 2}}}},
			{Name: "load", Condition: "passed", Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "load-test"}}}},
			{Name: "cleanup", Condition: "always", Optional: true, Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "cleanup-test"}}}},
		},
	}
	assert.NoError(t, validateTest(&spec))
}

func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	maps.Copy(out, m)
	return out
}
