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
	corev1 "k8s.io/api/core/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/executor"
)

// initContainerEnv builds the env slice for the content-fetcher init container.
//
// Always includes:
//   - KUBETEST_DATADIR — where fetched content should land.
//
// Conditionally includes git-auth env vars when Test.spec.content.git.*From
// references are set. The critical invariant: secret values NEVER appear in
// the returned EnvVar.Value — they always ride ValueFrom, so kubectl describe
// on the Job shows only the ref, and etcd sees the same ref (not the secret
// bytes). Enforced by tests in git_auth_test.go.
//
// SSH note: KUBETEST_GIT_SSH_KEY_PATH is a filesystem path env var. The
// user is currently responsible for mounting the key file itself via
// Test.spec.pod.volumes + volumeMounts — automatic SSH key mounting is
// backlog (would require the compiler to also inject a volume + volumeMount
// on the init container from the same secret ref, non-trivial vs. the value
// this feature carries at v1).
func initContainerEnv(git *testsv1alpha1.GitContent) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: executor.EnvDataDir, Value: DataDirPath},
	}
	if git == nil {
		return env
	}
	if git.UsernameFrom != nil {
		env = append(env, corev1.EnvVar{
			Name:      executor.EnvGitUsername,
			ValueFrom: git.UsernameFrom,
		})
	}
	if git.TokenFrom != nil {
		env = append(env, corev1.EnvVar{
			Name:      executor.EnvGitToken,
			ValueFrom: git.TokenFrom,
		})
	}
	if git.SSHKeyFrom != nil {
		env = append(env, corev1.EnvVar{
			Name:      executor.EnvGitSSHKeyPath,
			ValueFrom: git.SSHKeyFrom,
		})
	}
	return env
}
