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
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// baseValidWireSpec is the minimum spec that clears both the OpenAPI
// schema and the validating webhook. Kept here (not shared with the pure
// unit tests) so an envtest failure points at the actual wire spec.
func baseValidWireSpec() testsv1alpha1.TestSpec {
	return testsv1alpha1.TestSpec{
		Container: testsv1alpha1.ContainerConfig{
			Image: "grafana/k6:2.2.0",
			Args:  []string{"run", "script.js"},
		},
	}
}

// TestEnvtest_CreateValidTest exercises the full admission path: CRD schema +
// mutating webhook (defaults concurrencyPolicy) + validating webhook.
func TestEnvtest_CreateValidTest(t *testing.T) {
	ctx := context.Background()
	obj := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "valid-k6", Namespace: "default"},
		Spec:       baseValidWireSpec(),
	}
	require.NoError(t, k8sClient.Create(ctx, obj))
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, obj) })

	// Defaulting webhook filled concurrencyPolicy.
	assert.Equal(t, DefaultConcurrencyPolicy, obj.Spec.ConcurrencyPolicy)
}

// TestEnvtest_RejectMissingImage is one webhook-hit sentinel for the new
// workflows model: image is required by webhook code only (openAPI marker
// on ContainerConfig doesn't enforce presence — the type is optional).
// If this passes without our webhook wired, the error would land somewhere
// else (compile) later; we want it at admission.
func TestEnvtest_RejectMissingImage(t *testing.T) {
	ctx := context.Background()
	obj := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "no-image", Namespace: "default"},
		Spec: testsv1alpha1.TestSpec{
			Container: testsv1alpha1.ContainerConfig{Args: []string{"run"}},
		},
	}
	err := k8sClient.Create(ctx, obj)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.container.image is required")
}

// TestEnvtest_RejectMissingCommandAndArgs is a second webhook-only sentinel.
func TestEnvtest_RejectMissingCommandAndArgs(t *testing.T) {
	ctx := context.Background()
	obj := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "no-invocation", Namespace: "default"},
		Spec: testsv1alpha1.TestSpec{
			Container: testsv1alpha1.ContainerConfig{Image: "grafana/k6:2.2.0"},
		},
	}
	err := k8sClient.Create(ctx, obj)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "command or args")
}

// TestEnvtest_RejectVerdictCrossFieldRule is a third webhook-hit sentinel:
// "errorRateMax only valid when from=jtl" is a cross-field predicate the
// openAPI schema can't express. If a payload with junit+errorRateMax
// slips through, the webhook is off the path.
func TestEnvtest_RejectVerdictCrossFieldRule(t *testing.T) {
	ctx := context.Background()
	spec := baseValidWireSpec()
	spec.Verdict = &testsv1alpha1.VerdictSpec{From: "junit", ErrorRateMax: "0.01"}
	obj := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-verdict", Namespace: "default"},
		Spec:       spec,
	}
	err := k8sClient.Create(ctx, obj)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only valid when spec.verdict.from=jtl")
}

// TestEnvtest_RejectInvalidTest_InlineSize is one of two webhook-hit sentinels:
// aggregate byte-size across content.files[] is NOT expressible in OpenAPI
// schema, so a rejection carrying "use git/tarball" (a string that appears only
// in our webhook's oversizeMessage) proves the webhook is on the request path.
// If this test regresses, either the webhook is unwired or the message drifted.
func TestEnvtest_RejectInvalidTest_InlineSize(t *testing.T) {
	ctx := context.Background()
	obj := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "too-big", Namespace: "default"},
		Spec: func() testsv1alpha1.TestSpec {
			s := baseValidWireSpec()
			s.Content = testsv1alpha1.Content{
				Files: []testsv1alpha1.FileContent{
					{Path: "huge.txt", Content: strings.Repeat("a", 513*1024)},
				},
			}
			return s
		}(),
	}
	err := k8sClient.Create(ctx, obj)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "use git/tarball")
}

// TestEnvtest_RejectInvalidTest_Schedule is the second webhook-hit sentinel:
// cron parsing is not expressible in OpenAPI schema; only our webhook rejects
// "61 * * * *". If both this and _InlineSize regress, the webhook isn't wired.
func TestEnvtest_RejectInvalidTest_Schedule(t *testing.T) {
	ctx := context.Background()
	obj := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-cron", Namespace: "default"},
		Spec: func() testsv1alpha1.TestSpec {
			s := baseValidWireSpec()
			s.Schedule = "61 * * * *"
			return s
		}(),
	}
	err := k8sClient.Create(ctx, obj)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cron")
}

// TestEnvtest_RejectInvalidTest_GitURI is double-covered: content.git.uri
// carries a JSON tag without omitempty, so controller-gen generates
// `required: [uri]` and the API server rejects the empty value at the schema
// layer. Webhook has the same rule for defense-in-depth. See webhook-hit
// sentinels above for the actual webhook-path proof.
func TestEnvtest_RejectInvalidTest_GitURI(t *testing.T) {
	ctx := context.Background()
	obj := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "no-git-uri", Namespace: "default"},
		Spec: func() testsv1alpha1.TestSpec {
			s := baseValidWireSpec()
			s.Content = testsv1alpha1.Content{Git: &testsv1alpha1.GitContent{Revision: "main"}}
			return s
		}(),
	}
	err := k8sClient.Create(ctx, obj)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "git.uri")
}

// TestEnvtest_CreateValidTestRun exercises the TestRun mutating + validating path.
// The controller does the Test-existence check later — the webhook is shape-only,
// so a TestRun referencing a non-existent Test is accepted at admission.
func TestEnvtest_CreateValidTestRun(t *testing.T) {
	ctx := context.Background()
	tr := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "valid-run", Namespace: "default"},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "any-test-controller-checks-later"},
	}
	require.NoError(t, k8sClient.Create(ctx, tr))
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, tr) })

	assert.Equal(t, DefaultSource, tr.Spec.Source)
}

// TestEnvtest_RejectInvalidTestRun_EmptyTestRef is double-covered: testRef
// has no omitempty so schema marks it required. Webhook enforces the same
// rule for consistency with the Test-side pattern.
func TestEnvtest_RejectInvalidTestRun_EmptyTestRef(t *testing.T) {
	ctx := context.Background()
	tr := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "no-ref", Namespace: "default"},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: ""},
	}
	err := k8sClient.Create(ctx, tr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.testRef")
}

// TestEnvtest_PodConfigAnnotationsPassThrough is the §8 regression guard at
// the envtest layer: after a round-trip through defaulting + validating +
// api-server storage, every user-supplied annotation/label must survive
// unchanged and no operator-owned annotation must appear.
func TestEnvtest_PodConfigAnnotationsPassThrough(t *testing.T) {
	ctx := context.Background()
	userAnn := map[string]string{
		"sidecar.istio.io/inject":     "false",
		"linkerd.io/inject":           "disabled",
		"kubetest.io/user-annotation": "arbitrary",
	}
	userLab := map[string]string{"team": "sre", "env": "dev"}
	obj := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-passthrough", Namespace: "default"},
		Spec: func() testsv1alpha1.TestSpec {
			s := baseValidWireSpec()
			s.Pod = &testsv1alpha1.PodConfig{
				Annotations: cloneStringMap(userAnn),
				Labels:      cloneStringMap(userLab),
			}
			return s
		}(),
	}
	require.NoError(t, k8sClient.Create(ctx, obj))
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, obj) })

	assert.Equal(t, userAnn, obj.Spec.Pod.Annotations, "annotations must round-trip verbatim")
	assert.Equal(t, userLab, obj.Spec.Pod.Labels, "labels must round-trip verbatim")
}

// TestEnvtest_InvalidReturns400 gives a wire-level assertion that a webhook
// rejection surfaces as HTTP 400 (step-02 acceptance).
func TestEnvtest_InvalidReturns400(t *testing.T) {
	ctx := context.Background()
	tr := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "no-ref-400", Namespace: "default"},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: ""},
	}
	err := k8sClient.Create(ctx, tr)
	require.Error(t, err)
	// Controller-runtime returns plain errors from CustomValidator as HTTP 400
	// (StatusReasonBadRequest). Both are acceptable indicators of client-side rejection.
	status, ok := err.(apierrors.APIStatus)
	require.True(t, ok, "error should carry API status")
	code := status.Status().Code
	assert.Truef(t, code == 400 || code == 403 || code == 422,
		"unexpected status code %d (want 400/403/422)", code)
}

// TestPhaseEnumMatchesGeneratedCRD reads the generated Test CRD YAML and
// asserts the status.latestRun.phase enum contains every declared Phase value.
// Guards against silent drift between the Go const list and the CRD schema.
func TestPhaseEnumMatchesGeneratedCRD(t *testing.T) {
	root, err := findRepoRoot()
	require.NoError(t, err)
	yamlPath := filepath.Join(root, "config", "crd", "bases", "tests.kubetest.io_tests.yaml")
	// #nosec G304 -- yamlPath is derived from findRepoRoot() + repo-relative
	// constants; no user input reaches this ReadFile call.
	raw, err := os.ReadFile(yamlPath)
	require.NoError(t, err)

	var crd map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &crd))

	phaseEnum := findEnum(t, crd, "spec.versions[0].schema.openAPIV3Schema.properties.status.properties.latestRun.properties.phase")
	want := []testsv1alpha1.Phase{
		testsv1alpha1.PhaseQueued,
		testsv1alpha1.PhaseRunning,
		testsv1alpha1.PhasePaused,
		testsv1alpha1.PhasePassed,
		testsv1alpha1.PhaseFailed,
		testsv1alpha1.PhaseAborted,
		testsv1alpha1.PhaseError,
	}
	require.Len(t, phaseEnum, len(want), "phase enum size mismatch: %v", phaseEnum)
	for _, p := range want {
		assert.Contains(t, phaseEnum, string(p))
	}
}

// findEnum walks a slash-separated path (with "[N]" indexing) into a decoded
// CRD YAML and returns the enum list as []string. Failures are fatal.
func findEnum(t *testing.T, root map[string]any, path string) []string {
	t.Helper()
	var cur any = root
	for _, seg := range splitPath(path) {
		switch s := seg.(type) {
		case string:
			m, ok := cur.(map[string]any)
			require.Truef(t, ok, "expected map at segment %q; got %T", s, cur)
			cur, ok = m[s]
			require.Truef(t, ok, "missing key %q in path %s", s, path)
		case int:
			l, ok := cur.([]any)
			require.Truef(t, ok, "expected list at index %d; got %T", s, cur)
			require.Lessf(t, s, len(l), "index %d out of range (len %d)", s, len(l))
			cur = l[s]
		}
	}
	// cur should be the phase schema node; enum lives at .enum.
	m, ok := cur.(map[string]any)
	require.True(t, ok, "phase node is not a map")
	enumRaw, ok := m["enum"].([]any)
	require.True(t, ok, "phase node has no enum list")
	out := make([]string, 0, len(enumRaw))
	for _, v := range enumRaw {
		s, ok := v.(string)
		require.True(t, ok, "enum value is not a string")
		out = append(out, s)
	}
	return out
}

// splitPath tokenizes "a.b[0].c" into ["a","b",0,"c"]. Simple and only used by
// this test, so no need to over-engineer.
func splitPath(p string) []any {
	var out []any
	for seg := range strings.SplitSeq(p, ".") {
		// Handle "name[N]".
		if i := strings.Index(seg, "["); i >= 0 && strings.HasSuffix(seg, "]") {
			name := seg[:i]
			idxStr := seg[i+1 : len(seg)-1]
			out = append(out, name)
			var idx int
			for _, c := range idxStr {
				idx = idx*10 + int(c-'0')
			}
			out = append(out, idx)
			continue
		}
		out = append(out, seg)
	}
	return out
}
