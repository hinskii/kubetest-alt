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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGolden_CompileWithMinIO is a NEW golden fixture — separate from the
// five per-executor goldens so this feature can evolve without churning
// their diffs. Same pattern as testdata/golden/git-auth.yaml (step 06).
//
// The fixture demonstrates the step-07 additions: MINIO_ENDPOINT+BUCKET
// literal env vars on the wrapper container, envFrom secretRef for creds
// (creds themselves NEVER appear in the golden — only the ref name).
func TestGolden_CompileWithMinIO(t *testing.T) {
	test := canonicalTest("k6")
	test.Name = "sample-minio"

	run := canonicalTestRun("k6")
	run.Name = "sample-minio-run"
	run.Spec.TestRef = "sample-minio"

	opts := Options{
		ContentFetcherImage: "ghcr.io/hinskii/kubetest-alt/content-fetcher:v0.0.0",
		MinIO: MinIOOptions{
			Endpoint: "minio.kubetest.svc:9000",
			Bucket:   "kubetest-artifacts",
			// #nosec G101 -- SecretName is a k8s Secret NAME, not a credential value.
			SecretName: "kubetest-minio-creds",
			UseSSL:     false,
		},
	}
	job, aux, err := Compile(test, run, opts)
	require.NoError(t, err)
	require.Len(t, aux, 1)

	got, err := marshalMultiDoc(job, aux[0])
	require.NoError(t, err)

	path := filepath.Join("testdata", "golden", "minio-enabled.yaml")
	if *updateGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		// #nosec G306 -- committed fixture; perms match sibling goldens.
		require.NoError(t, os.WriteFile(path, got, 0o644))
		t.Logf("updated %s", path)
		return
	}
	// #nosec G304 -- path is a compile-time-literal test fixture location.
	want, err := os.ReadFile(path)
	require.NoError(t, err, "read golden (run with -update to regenerate)")
	if strings.TrimSpace(string(got)) != strings.TrimSpace(string(want)) {
		assert.Equal(t, string(want), string(got),
			"golden mismatch — rerun with -update if intentional")
	}
	// Security invariant baked into the golden: credentials must NOT appear
	// as literal Values. Only secretKeyRef structure allowed for auth data.
	assert.NotContains(t, string(got), "aws_access_key_id: ",
		"creds must ride envFrom, never appear as literal env values")
}
