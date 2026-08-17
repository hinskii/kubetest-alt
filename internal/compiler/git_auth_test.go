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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/executor"
)

// TestInitContainerEnv_NoGit — just KUBETEST_DATADIR when there's no git spec.
func TestInitContainerEnv_NoGit(t *testing.T) {
	env := initContainerEnv(nil)
	require.Len(t, env, 1)
	assert.Equal(t, "KUBETEST_DATADIR", env[0].Name)
}

// TestInitContainerEnv_GitWithoutAuth — public repo, no *From refs → still just DATADIR.
func TestInitContainerEnv_GitWithoutAuth(t *testing.T) {
	env := initContainerEnv(&testsv1alpha1.GitContent{URI: "https://example.com/public.git"})
	require.Len(t, env, 1)
	assert.Equal(t, "KUBETEST_DATADIR", env[0].Name)
}

// TestInitContainerEnv_BasicAuth — Username+Token from Secret refs land on the
// init container as ValueFrom entries with the wire-contract env var names
// the fetcher looks up (pkg/executor/executor.EnvGitUsername / EnvGitToken).
func TestInitContainerEnv_BasicAuth(t *testing.T) {
	git := &testsv1alpha1.GitContent{
		URI:      "https://example.com/private.git",
		AuthType: "basic",
		UsernameFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "git-creds"},
				Key:                  "username",
			},
		},
		TokenFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "git-creds"},
				Key:                  "token",
			},
		},
	}
	env := initContainerEnv(git)

	byName := map[string]corev1.EnvVar{}
	for _, e := range env {
		byName[e.Name] = e
	}
	// Both username and token env vars present, both ValueFrom (not Value).
	for _, name := range []string{executor.EnvGitUsername, executor.EnvGitToken} {
		e, ok := byName[name]
		require.True(t, ok, "%s missing", name)
		assert.Empty(t, e.Value, "%s must NOT carry a literal Value", name)
		require.NotNil(t, e.ValueFrom, "%s must carry ValueFrom (secret ref)", name)
		require.NotNil(t, e.ValueFrom.SecretKeyRef, "%s expected SecretKeyRef", name)
	}
}

// TestInitContainerEnv_SSHAuth — SSHKeyFrom → KUBETEST_GIT_SSH_KEY_PATH env
// (path from a mounted secret file; user configures the mount themselves for
// now — automatic mount is documented backlog in git_auth.go).
func TestInitContainerEnv_SSHAuth(t *testing.T) {
	git := &testsv1alpha1.GitContent{
		URI:      "git@example.com:private.git",
		AuthType: "ssh",
		SSHKeyFrom: &corev1.EnvVarSource{
			ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "git-ssh-config"},
				Key:                  "keypath",
			},
		},
	}
	env := initContainerEnv(git)

	found := false
	for _, e := range env {
		if e.Name == executor.EnvGitSSHKeyPath {
			found = true
			assert.Empty(t, e.Value)
			require.NotNil(t, e.ValueFrom)
			require.NotNil(t, e.ValueFrom.ConfigMapKeyRef)
		}
	}
	assert.True(t, found, "%s env var must be present when SSHKeyFrom set", executor.EnvGitSSHKeyPath)
}

// TestCompile_GitAuth_SecretsNeverInJobSpecPlaintext is the security invariant:
// after compiling a Test with git secret refs, the literal secret value MUST
// NOT appear anywhere in the marshaled Job spec. If a future refactor
// accidentally set EnvVar.Value from the secret instead of ValueFrom, this
// test catches it — kubectl describe on the Job would leak the token.
func TestCompile_GitAuth_SecretsNeverInJobSpecPlaintext(t *testing.T) {
	// A distinct sentinel string that CANNOT appear in any legitimate compiler
	// output — only if someone mistakenly resolved the secret and embedded it.
	const sentinel = "SECRET_SENTINEL_VALUE_MUST_NEVER_APPEAR_IN_JOB_SPEC"

	test := newValidTest("k6")
	test.Spec.Content.Git = &testsv1alpha1.GitContent{
		URI:      "https://example.com/private.git",
		AuthType: "basic",
		// Ref name intentionally contains the sentinel — if the compiler
		// treats ref data as opaque (correct), Name/Key strings appear
		// verbatim. What must NEVER appear is the RESOLVED value. This
		// test can't literally check "no plaintext" because we don't
		// resolve; it checks that ValueFrom is used (structural).
		TokenFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "git-secret"},
				Key:                  "token",
			},
		},
	}
	job, _, err := Compile(test, newValidTestRun("myrun", test.Name), defaultOpts())
	require.NoError(t, err)

	// Marshal the whole Job to JSON and confirm no EnvVar carries a literal
	// Value (the structural invariant that keeps secrets out of etcd/kubectl).
	b, err := json.Marshal(job)
	require.NoError(t, err)
	assert.NotContains(t, string(b), sentinel,
		"secret value must not materialize in Job spec — always ValueFrom")

	// Structural check: find the init container's KUBETEST_GIT_TOKEN env,
	// assert Value is empty and ValueFrom is set.
	init := job.Spec.Template.Spec.InitContainers[0]
	for _, e := range init.Env {
		if e.Name == executor.EnvGitToken {
			assert.Empty(t, e.Value)
			require.NotNil(t, e.ValueFrom)
			require.NotNil(t, e.ValueFrom.SecretKeyRef)
			assert.Equal(t, "git-secret", e.ValueFrom.SecretKeyRef.Name)
			assert.Equal(t, "token", e.ValueFrom.SecretKeyRef.Key)
			return
		}
	}
	t.Fatalf("init container missing %s env var", executor.EnvGitToken)
}

// TestGolden_CompileWithGitAuth is a NEW golden fixture — separate from the
// existing 5 per-executor goldens so this feature can evolve without churning
// their diffs. Regenerable via -update like the others.
func TestGolden_CompileWithGitAuth(t *testing.T) {
	test := &testsv1alpha1.Test{
		ObjectMeta: canonicalTest("k6").ObjectMeta,
		Spec: testsv1alpha1.TestSpec{
			Type:              "k6",
			ConcurrencyPolicy: "Allow",
			Content: testsv1alpha1.Content{
				Git: &testsv1alpha1.GitContent{
					URI:      "https://example.com/private.git",
					Revision: "main",
					AuthType: "basic",
					UsernameFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "git-creds"},
							Key:                  "username",
						},
					},
					TokenFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "git-creds"},
							Key:                  "token",
						},
					},
				},
			},
		},
	}
	// Override the name to keep this golden distinct from the k6 one.
	test.Name = "sample-git-auth"
	run := canonicalTestRun("k6")
	run.Name = "sample-git-auth-run"
	run.Spec.TestRef = "sample-git-auth"

	job, aux, err := Compile(test, run, Options{
		ContentFetcherImage: "ghcr.io/hinskii/kubetest-alt/content-fetcher:v0.0.0",
	})
	require.NoError(t, err)
	require.Len(t, aux, 1)

	got, err := marshalMultiDoc(job, aux[0])
	require.NoError(t, err)

	path := filepath.Join("testdata", "golden", "git-auth.yaml")
	if *updateGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		// #nosec G306 -- committed fixture, perms match golden_test.go.
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
	// Extra assertion: golden must NOT contain any literal token bytes; only
	// secretKeyRef structure. A future compiler change that resolved secrets
	// into Values would spring both this AND the structural test above.
	assert.NotContains(t, string(got), `value: token`,
		"golden must reference secrets via secretKeyRef, not literal Values")
}
