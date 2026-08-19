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

// Compiler goldens for the workflows-model (step 11 refactor). The old
// per-tool goldens (k6.yaml, cypress.yaml, ...) are GONE — with the
// per-type dispatch removed the compiler produces the SAME shape for
// every tool. Fixtures now cover the axes that DO differ:
//   - default: canonical Test + minimal invocation
//   - tool-label: kubetest.io/tool label propagated to Job + Pod
//   - verdict-jtl: spec.verdict.from=jtl passed through into request.json
//   - minio-enabled: MinIO env + envFrom on the wrapper
//   - git-auth: content.git.token / sshKey secret refs on the init container
package compiler

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// updateGolden regenerates fixtures instead of asserting. Run with
// `go test ./internal/compiler/... -run TestGolden -update` after any
// intentional compiler-output change, review the diff, commit.
var updateGolden = flag.Bool("update", false, "update golden fixtures")

// canonicalTest returns the fixed input used by every golden. Exercises
// the fields that shape output: pod annotations (§8), content.git,
// container env, artifacts, explicit timeout.
func canonicalTest() *testsv1alpha1.Test {
	return &testsv1alpha1.Test{
		TypeMeta: metav1.TypeMeta{APIVersion: testsv1alpha1.SchemeGroupVersion.String(), Kind: "Test"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sample",
			Namespace: "kubetest-samples",
		},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: "Allow",
			Timeout:           &metav1.Duration{Duration: 5 * time.Minute},
			Content: testsv1alpha1.Content{
				Git: &testsv1alpha1.GitContent{
					URI:      "https://example.com/tests.git",
					Revision: "main",
				},
			},
			Container: testsv1alpha1.ContainerConfig{
				Image: "grafana/k6:2.2.0",
				Args:  []string{"run", "script.js"},
				Env:   []corev1.EnvVar{{Name: "FEATURE_X", Value: "on"}},
			},
			Pod: &testsv1alpha1.PodConfig{
				Annotations: map[string]string{
					"sidecar.istio.io/inject": "false",
					"foo/bar":                 "baz",
				},
				Labels: map[string]string{"team": "sre"},
			},
			Artifacts: &testsv1alpha1.ArtifactSpec{Paths: []string{"results/**/*.xml"}},
		},
	}
}

func canonicalTestRun() *testsv1alpha1.TestRun {
	return &testsv1alpha1.TestRun{
		TypeMeta: metav1.TypeMeta{APIVersion: testsv1alpha1.SchemeGroupVersion.String(), Kind: "TestRun"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sample-run",
			Namespace: "kubetest-samples",
			UID:       types.UID("00000000-0000-0000-0000-0000cafe0001"),
		},
		Spec: testsv1alpha1.TestRunSpec{
			TestRef: "sample",
			Source:  "api",
			Config:  map[string]string{"vus": "10"},
		},
	}
}

func defaultOpts() Options {
	return Options{
		ContentFetcherImage: "ghcr.io/hinskii/kubetest-alt/content-fetcher:v0.0.0",
	}
}

func TestGolden_Default(t *testing.T) {
	assertGolden(t, "default.yaml", canonicalTest(), canonicalTestRun(), defaultOpts())
}

// TestGolden_ToolLabel proves the compiler propagates kubetest.io/tool
// from Test labels to Job labels AND Pod labels.
func TestGolden_ToolLabel(t *testing.T) {
	test := canonicalTest()
	if test.Labels == nil {
		test.Labels = map[string]string{}
	}
	test.Labels[LabelKubetestTool] = "k6"
	assertGolden(t, "tool-label.yaml", test, canonicalTestRun(), defaultOpts())
}

// TestGolden_VerdictJTL: spec.verdict.from=jtl + errorRateMax must land
// in request.json's verdict block so the wrapper's JTL processor picks
// them up.
func TestGolden_VerdictJTL(t *testing.T) {
	test := canonicalTest()
	test.Spec.Verdict = &testsv1alpha1.VerdictSpec{
		From:         "jtl",
		ErrorRateMax: "0.01",
	}
	assertGolden(t, "verdict-jtl.yaml", test, canonicalTestRun(), defaultOpts())
}

// TestGolden_MinioEnabled asserts the MinIO env + envFrom scaffolding
// lands on the wrapper container when Options.MinIO is populated.
func TestGolden_MinioEnabled(t *testing.T) {
	opts := defaultOpts()
	// #nosec G101 -- SecretName is a k8s Secret name (label), not a credential value.
	opts.MinIO = MinIOOptions{
		Endpoint:   "minio.kubetest.svc:9000",
		Bucket:     "kubetest-artifacts",
		SecretName: "kubetest-minio-creds",
	}
	assertGolden(t, "minio-enabled.yaml", canonicalTest(), canonicalTestRun(), opts)
}

// TestGolden_GitAuth asserts token+username secret refs land on the
// init container's env when content.git.tokenFrom/usernameFrom are set.
func TestGolden_GitAuth(t *testing.T) {
	test := canonicalTest()
	test.Spec.Content.Git.AuthType = "basic"
	test.Spec.Content.Git.UsernameFrom = &corev1.EnvVarSource{
		SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "git-creds"},
			Key:                  "username",
		},
	}
	test.Spec.Content.Git.TokenFrom = &corev1.EnvVarSource{
		SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "git-creds"},
			Key:                  "token",
		},
	}
	assertGolden(t, "git-auth.yaml", test, canonicalTestRun(), defaultOpts())
}

// assertGolden compiles + marshals the (test, run, opts) triple and
// asserts byte-equal to the on-disk fixture. Under -update, rewrites.
func assertGolden(t *testing.T, name string, test *testsv1alpha1.Test,
	run *testsv1alpha1.TestRun, opts Options) {
	t.Helper()
	job, aux, err := Compile(test, run, opts)
	require.NoError(t, err)
	require.Len(t, aux, 1, "expected one aux object (ConfigMap)")
	cm, ok := aux[0].(*corev1.ConfigMap)
	require.True(t, ok, "aux[0] must be *corev1.ConfigMap")

	got, err := marshalMultiDoc(job, cm)
	require.NoError(t, err)

	path := filepath.Join("testdata", "golden", name)
	if *updateGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		// #nosec G306 -- test fixture, world-readable is fine for review.
		require.NoError(t, os.WriteFile(path, got, 0o644))
		t.Logf("updated golden %s", path)
		return
	}
	// #nosec G304 -- path built from constants + literal.
	want, err := os.ReadFile(path)
	require.NoError(t, err, "read golden (run with -update to regenerate)")

	if !bytes.Equal(got, want) {
		assert.Equal(t, string(want), string(got),
			"golden mismatch — inspect diff, then rerun with -update if intentional")
	}
}

// marshalMultiDoc produces a stable multi-document YAML: Job first, then
// aux objects separated by "---".
func marshalMultiDoc(docs ...any) ([]byte, error) {
	var buf bytes.Buffer
	for i, d := range docs {
		if i > 0 {
			buf.WriteString("---\n")
		}
		b, err := yaml.Marshal(d)
		if err != nil {
			return nil, err
		}
		buf.Write(b)
	}
	return buf.Bytes(), nil
}
