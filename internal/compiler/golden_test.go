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

// updateGolden regenerates the fixtures instead of asserting against them.
// Run: `go test ./internal/compiler/... -run TestGolden -update` after any
// intentional compiler output change, review the diff, commit.
var updateGolden = flag.Bool("update", false, "update golden fixtures under testdata/golden/")

// canonicalTest returns a fixed Test that exercises the features that shape
// the Job/ConfigMap output: pod annotations (§8 passthrough), content.git,
// container env, artifacts, explicit timeout. The same input is used for every
// executor type so goldens differ only where the executor demands it (image,
// default args, cypress /dev/shm).
func canonicalTest(execType string) *testsv1alpha1.Test {
	return &testsv1alpha1.Test{
		TypeMeta: metav1.TypeMeta{APIVersion: testsv1alpha1.SchemeGroupVersion.String(), Kind: "Test"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sample-" + execType,
			Namespace: "kubetest-samples",
		},
		Spec: testsv1alpha1.TestSpec{
			Type:              execType,
			ConcurrencyPolicy: "Allow",
			Timeout:           &metav1.Duration{Duration: 5 * time.Minute},
			Content: testsv1alpha1.Content{
				Git: &testsv1alpha1.GitContent{
					URI:      "https://example.com/tests.git",
					Revision: "main",
				},
			},
			Container: testsv1alpha1.ContainerConfig{
				Env: []corev1.EnvVar{{Name: "FEATURE_X", Value: "on"}},
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

func canonicalTestRun(execType string) *testsv1alpha1.TestRun {
	return &testsv1alpha1.TestRun{
		TypeMeta: metav1.TypeMeta{APIVersion: testsv1alpha1.SchemeGroupVersion.String(), Kind: "TestRun"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sample-" + execType + "-run",
			Namespace: "kubetest-samples",
			UID:       types.UID("00000000-0000-0000-0000-0000cafe0001"),
		},
		Spec: testsv1alpha1.TestRunSpec{
			TestRef: "sample-" + execType,
			Source:  "api",
			Config:  map[string]string{"vus": "10"},
		},
	}
}

// TestGolden_CompilePerExecutor asserts the full Job + ConfigMap YAML matches
// the committed fixture. Under -update, rewrites the fixture instead.
func TestGolden_CompilePerExecutor(t *testing.T) {
	execTypes := []string{"k6", "cypress", "newman", "locust", "jmeter"}
	for _, execType := range execTypes {
		t.Run(execType, func(t *testing.T) {
			test := canonicalTest(execType)
			run := canonicalTestRun(execType)
			opts := Options{
				ContentFetcherImage: "ghcr.io/hinskii/kubetest-alt/content-fetcher:v0.0.0",
			}

			job, aux, err := Compile(test, run, opts)
			require.NoError(t, err)
			require.Len(t, aux, 1, "expected one aux object (ConfigMap)")
			cm, ok := aux[0].(*corev1.ConfigMap)
			require.True(t, ok, "aux[0] must be *corev1.ConfigMap")

			got, err := marshalMultiDoc(job, cm)
			require.NoError(t, err)

			path := filepath.Join("testdata", "golden", execType+".yaml")
			if *updateGolden {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
				// #nosec G306 -- test fixture: readable by owner+group so both the
				// test process and reviewers of committed files can open it.
				require.NoError(t, os.WriteFile(path, got, 0o644))
				t.Logf("updated golden %s", path)
				return
			}
			// #nosec G304 -- path is a repo-relative literal joined from constants.
			want, err := os.ReadFile(path)
			require.NoError(t, err, "read golden (run with -update to regenerate)")

			if !bytes.Equal(got, want) {
				assert.Equal(t, string(want), string(got),
					"golden mismatch — inspect diff, then rerun with -update if intentional")
			}
		})
	}
}

// marshalMultiDoc produces a stable multi-document YAML: Job first, then aux
// objects separated by "---". sigs.k8s.io/yaml respects JSON tags (unlike
// gopkg.in/yaml.v3), which matches how kubectl serializes objects.
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
