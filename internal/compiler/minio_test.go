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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCompile_MinIODisabled_NoEnvOrEnvFromInjected: no --minio-endpoint →
// wrapper container is identical to pre-step-07 (no MINIO_* env, no envFrom
// beyond the user-provided ones). Ensures step-07 is fully opt-in.
func TestCompile_MinIODisabled_NoEnvOrEnvFromInjected(t *testing.T) {
	job, _, err := Compile(canonicalTest(), canonicalTestRun(), defaultOpts())
	require.NoError(t, err)

	main := getMainContainer(t, job)
	for _, e := range main.Env {
		assert.NotContains(t, e.Name, "MINIO_", "no MinIO env when disabled")
	}
	assert.Empty(t, main.EnvFrom, "no envFrom when MinIO disabled + no user envFrom")
}

// TestCompile_MinIOEnabled_InjectsEndpointAndBucketEnv: --minio-endpoint set
// → wrapper gets MINIO_ENDPOINT + MINIO_BUCKET env vars.
func TestCompile_MinIOEnabled_InjectsEndpointAndBucketEnv(t *testing.T) {
	opts := defaultOpts()
	opts.MinIO = MinIOOptions{
		Endpoint:   "minio.internal:9000",
		Bucket:     "my-artifacts",
		SecretName: "minio-creds",
	}
	job, _, err := Compile(canonicalTest(), canonicalTestRun(), opts)
	require.NoError(t, err)

	main := getMainContainer(t, job)
	envByName := map[string]string{}
	for _, e := range main.Env {
		envByName[e.Name] = e.Value
	}
	assert.Equal(t, "minio.internal:9000", envByName[EnvMinIOEndpoint])
	assert.Equal(t, "my-artifacts", envByName[EnvMinIOBucket])
	assert.NotContains(t, envByName, EnvMinIOUseSSL, "SSL off → env not set")
}

// TestCompile_MinIOEnabled_UsesDefaultBucketWhenEmpty:
func TestCompile_MinIOEnabled_UsesDefaultBucketWhenEmpty(t *testing.T) {
	opts := defaultOpts()
	opts.MinIO = MinIOOptions{
		Endpoint:   "minio.internal:9000",
		SecretName: "minio-creds",
		// Bucket empty → MinIODefaultBucket
	}
	job, _, err := Compile(canonicalTest(), canonicalTestRun(), opts)
	require.NoError(t, err)
	main := getMainContainer(t, job)
	for _, e := range main.Env {
		if e.Name == EnvMinIOBucket {
			assert.Equal(t, MinIODefaultBucket, e.Value)
			return
		}
	}
	t.Fatalf("MINIO_BUCKET env var missing")
}

// TestCompile_MinIOEnabled_SecretRefInEnvFromNotEnv is the security invariant:
// creds NEVER appear in env values, only via envFrom secretRef so kubectl
// describe can't leak them.
func TestCompile_MinIOEnabled_SecretRefInEnvFromNotEnv(t *testing.T) {
	opts := defaultOpts()
	opts.MinIO = MinIOOptions{
		Endpoint:   "minio.internal:9000",
		Bucket:     "my-artifacts",
		SecretName: "my-secret",
	}
	job, _, err := Compile(canonicalTest(), canonicalTestRun(), opts)
	require.NoError(t, err)

	main := getMainContainer(t, job)
	// envFrom carries the secret ref.
	require.Len(t, main.EnvFrom, 1)
	require.NotNil(t, main.EnvFrom[0].SecretRef)
	assert.Equal(t, "my-secret", main.EnvFrom[0].SecretRef.Name)

	// No env var value contains a credential-looking string.
	for _, e := range main.Env {
		assert.NotContains(t, e.Value, "AWS_ACCESS_KEY", "creds must NOT appear in env values")
		assert.NotContains(t, e.Value, "AWS_SECRET_ACCESS_KEY")
	}
}

// TestCompile_MinIOEnabled_UseSSLTrue:
func TestCompile_MinIOEnabled_UseSSLTrue(t *testing.T) {
	opts := defaultOpts()
	opts.MinIO = MinIOOptions{
		Endpoint:   "minio.internal:9000",
		SecretName: "s",
		UseSSL:     true,
	}
	job, _, err := Compile(canonicalTest(), canonicalTestRun(), opts)
	require.NoError(t, err)
	main := getMainContainer(t, job)
	found := false
	for _, e := range main.Env {
		if e.Name == EnvMinIOUseSSL {
			found = true
			assert.Equal(t, "true", e.Value)
		}
	}
	assert.True(t, found)
}

// TestCompile_MinIOEndpointSetButNoSecret: still injects the env vars so the
// wrapper's newScraperFromEnv can pick them up, but envFrom stays empty
// (user is responsible for providing creds via test.spec.container.envFrom
// or via a service account with cloud-provider IAM).
func TestCompile_MinIOEndpointSetButNoSecret(t *testing.T) {
	opts := defaultOpts()
	opts.MinIO = MinIOOptions{Endpoint: "minio.internal:9000", Bucket: "b"}
	job, _, err := Compile(canonicalTest(), canonicalTestRun(), opts)
	require.NoError(t, err)
	main := getMainContainer(t, job)

	hasEndpoint := false
	for _, e := range main.Env {
		if e.Name == EnvMinIOEndpoint {
			hasEndpoint = true
		}
	}
	assert.True(t, hasEndpoint, "endpoint env set")
	assert.Empty(t, main.EnvFrom, "no envFrom when SecretName not provided")
}
