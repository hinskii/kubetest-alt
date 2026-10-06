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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/hinskii/kubetest-alt/pkg/storage"
)

func wrapperEnv(t *testing.T, opts Options) (map[string]string, []corev1.EnvFromSource) {
	t.Helper()
	job, _, err := Compile(canonicalTest(), canonicalTestRun(), opts)
	require.NoError(t, err)
	main := getMainContainer(t, job)
	env := map[string]string{}
	for _, e := range main.Env {
		env[e.Name] = e.Value
	}
	return env, main.EnvFrom
}

// No object storage → no storage env, no envFrom beyond the user's.
func TestCompile_StorageDisabled_NothingInjected(t *testing.T) {
	env, envFrom := wrapperEnv(t, defaultOpts())
	for name := range env {
		assert.False(t, strings.HasPrefix(name, "KUBETEST_STORAGE_") || strings.HasPrefix(name, "KUBETEST_S3_") ||
			strings.HasPrefix(name, "KUBETEST_GCS_"), "unexpected %s", name)
	}
	assert.Empty(t, envFrom)
}

func TestCompile_StorageS3_EnvAndSecretRef(t *testing.T) {
	opts := defaultOpts()
	opts.Storage = StorageOptions{
		Config: storage.Config{Type: storage.TypeS3, Bucket: "artifacts", S3: storage.S3Config{
			Endpoint: "minio.internal:9000", UseSSL: true, Region: "eu-west-1",
			AccessKey: "AKIA-must-not-leak", SecretKey: "secret-must-not-leak",
		}},
		SecretName: "s3-creds",
	}
	env, envFrom := wrapperEnv(t, opts)
	assert.Equal(t, storage.TypeS3, env[storage.EnvType])
	assert.Equal(t, "artifacts", env[storage.EnvBucket])
	assert.Equal(t, "minio.internal:9000", env[storage.EnvS3Endpoint])
	assert.Equal(t, "true", env[storage.EnvS3UseSSL])
	assert.Equal(t, "eu-west-1", env[storage.EnvS3Region])

	// Credentials only via the Secret reference — never as env values,
	// even when the operator's own config happens to hold keys.
	require.Len(t, envFrom, 1)
	assert.Equal(t, "s3-creds", envFrom[0].SecretRef.Name)
	for name, v := range env {
		assert.NotContains(t, v, "must-not-leak", "credential in env %s", name)
	}
}

// S3 without a Secret: the pod uses the AWS credential chain (e.g. IRSA).
func TestCompile_StorageS3_NoSecret(t *testing.T) {
	opts := defaultOpts()
	opts.Storage = StorageOptions{Config: storage.Config{Type: storage.TypeS3, Bucket: "b"}}
	env, envFrom := wrapperEnv(t, opts)
	assert.Equal(t, "b", env[storage.EnvBucket])
	assert.Empty(t, envFrom)
}

// GCS: Workload Identity via the pod's service account — a configured
// SecretName must not be projected.
func TestCompile_StorageGCS_NoSecretEverProjected(t *testing.T) {
	opts := defaultOpts()
	// #nosec G101 -- SecretName is the name of a Secret, not a credential.
	opts.Storage = StorageOptions{
		Config:     storage.Config{Type: storage.TypeGCS, Bucket: "gcs-bucket", GCS: storage.GCSConfig{Endpoint: "fake-gcs:4443"}},
		SecretName: "ignored-for-gcs",
	}
	env, envFrom := wrapperEnv(t, opts)
	assert.Equal(t, storage.TypeGCS, env[storage.EnvType])
	assert.Equal(t, "gcs-bucket", env[storage.EnvBucket])
	assert.Equal(t, "fake-gcs:4443", env[storage.EnvGCSEndpoint])
	assert.NotContains(t, env, storage.EnvS3Endpoint)
	assert.Empty(t, envFrom)
}
