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
func baseValidSpec() testsv1alpha1.TestSpec {
	return testsv1alpha1.TestSpec{
		Type:              "k6",
		ConcurrencyPolicy: "Allow",
	}
}

func TestValidateTest_Type(t *testing.T) {
	cases := []struct {
		name       string
		typ        string
		wantErr    bool
		wantSubstr string
	}{
		{"valid k6", "k6", false, ""},
		{"valid cypress", "cypress", false, ""},
		{"valid newman", "newman", false, ""},
		{"valid locust", "locust", false, ""},
		{"valid jmeter", "jmeter", false, ""},
		{"invalid empty", "", true, "spec.type"},
		{"invalid unknown tool", "artillery", true, "spec.type"},
		{"invalid case sensitive", "K6", true, "spec.type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := baseValidSpec()
			spec.Type = tc.typ
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

func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	maps.Copy(out, m)
	return out
}
