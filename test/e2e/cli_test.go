//go:build e2e
// +build e2e

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

package e2e

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// kubectl-kubetest against the kind cluster as a user runs it: the current
// kubeconfig context, the API Service and token found by itself, every call
// through the Kubernetes API's service proxy. The CI contract: run --wait
// exits 0 for a passed run, 1 for a failed one.
func scenarioCLI(t *testing.T, ctx context.Context, c client.Client) {
	bin := os.Getenv("CLI_BIN")
	if bin == "" {
		t.Skip("CLI_BIN not set (run via test/e2e/run.sh)")
	}
	cli := func(args ...string) (string, string, int) {
		t.Helper()
		cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		var out, errOut bytes.Buffer
		cmd := exec.CommandContext(cctx, bin, append(args, "-n", workloadNS)...) // #nosec G204 -- our own binary
		cmd.Stdout, cmd.Stderr = &out, &errOut
		cmd.Env = append(os.Environ(), "KUBETEST_API_TOKEN=") // discover the token from the Secret
		err := cmd.Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return out.String(), errOut.String(), exit.ExitCode()
		}
		require.NoError(t, err)
		return out.String(), errOut.String(), 0
	}

	k6Test := func(name, script string) {
		require.NoError(t, c.Create(ctx, &testsv1alpha1.Test{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: workloadNS, Labels: map[string]string{"kubetest.io/tool": "k6"}},
			Spec: testsv1alpha1.TestSpec{
				Container: testsv1alpha1.ContainerConfig{Image: "grafana/k6:1.4.0", Command: []string{"k6"},
					Args: []string{"run", "/data/repo/script.js"}},
				Content: testsv1alpha1.Content{Files: []testsv1alpha1.FileContent{{Path: "script.js", Content: script}}},
			},
		}))
	}
	k6Test("e2e-cli-pass", "export default function () {}\n")
	k6Test("e2e-cli-fail", "import { check } from 'k6';\n"+
		"export const options = { thresholds: { checks: ['rate==1'] } };\n"+
		"export default function () { check(1, { 'never true': () => false }); }\n")

	out, errOut, code := cli("tests")
	require.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "e2e-cli-pass")

	out, errOut, code = cli("run", "e2e-cli-pass", "--wait", "--timeout", "4m")
	require.Equal(t, 0, code, "a passed run exits 0:\n%s", errOut)
	runName := strings.TrimSpace(out)
	assert.True(t, strings.HasPrefix(runName, "e2e-cli-pass-"), out)
	assert.Contains(t, errOut, "passed")

	_, errOut, code = cli("run", "e2e-cli-fail", "--wait", "--timeout", "4m")
	assert.Equal(t, 1, code, "a failed run exits 1:\n%s", errOut)
	assert.Contains(t, errOut, "failed")

	out, errOut, code = cli("runs", runName, "-o", "json")
	require.Equal(t, 0, code, errOut)
	assert.Contains(t, out, `"source": "cli"`, "runs from the CLI say so")
	logs, errOut, code := cli("logs", runName)
	require.Equal(t, 0, code, errOut)
	assert.Contains(t, logs, "script.js", "the run's log through the CLI")
}
