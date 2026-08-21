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

// Package helm holds chart-side tests: RBAC parity between the chart's
// operator ClusterRole and controller-gen's config/rbac/role.yaml, plus
// golden-render assertions against 2-3 values combos. Skipped when
// `helm` isn't on PATH (dev machines without helm still get every
// other gate).
package helm

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	yaml "sigs.k8s.io/yaml"
)

// helmBinary returns the path to the helm binary, skipping the test
// when unavailable. CI installs helm; local dev may not.
func helmBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not on PATH; skipping chart tests (CI job installs helm before running these)")
	}
	return path
}

// chartDir is repo-root-relative — every test resolves from findRepoRoot().
const chartDir = "charts/kubetest-alt"

// findRepoRoot walks up from cwd until it finds go.mod.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "no go.mod above cwd")
		dir = parent
	}
}

// TestHelmLint runs `helm lint charts/kubetest-alt/`. Guards against
// obvious template errors (missing values, bad YAML, illegal metadata).
func TestHelmLint(t *testing.T) {
	helm := helmBinary(t)
	root := findRepoRoot(t)
	// #nosec G204 -- helm is resolved via LookPath; chartDir is a repo-local const.
	out, err := exec.Command(helm, "lint", filepath.Join(root, chartDir)).CombinedOutput()
	assert.NoErrorf(t, err, "helm lint failed:\n%s", string(out))
}

// TestHelmTemplate_DefaultValuesRenders proves the chart renders with
// zero -f / --set — the "kind e2e default install" path. If a template
// grows a required value without a default, this trips.
func TestHelmTemplate_DefaultValuesRenders(t *testing.T) {
	helm := helmBinary(t)
	root := findRepoRoot(t)
	// #nosec G204 -- helm resolved via LookPath; chartDir const.
	out, err := exec.Command(helm, "template", "test", filepath.Join(root, chartDir),
		"--namespace", "kubetest-alt").CombinedOutput()
	require.NoErrorf(t, err, "helm template default failed:\n%s", string(out))
	// Sanity: the four load-bearing objects come out.
	got := string(out)
	assert.Contains(t, got, "kind: Deployment")
	assert.Contains(t, got, "kind: Secret")
	assert.Contains(t, got, "kind: ValidatingWebhookConfiguration")
	assert.Contains(t, got, "kind: MutatingWebhookConfiguration")
	// Dev default is helm self-signed → no cert-manager Certificate.
	assert.NotContains(t, got, "kind: Certificate",
		"dev default should NOT emit a cert-manager Certificate (kind e2e budget)")
}

// TestHelmTemplate_CertManagerToggle: enabling values.webhook.certManager
// swaps the Secret for a Certificate and drops the inline caBundle.
func TestHelmTemplate_CertManagerToggle(t *testing.T) {
	helm := helmBinary(t)
	root := findRepoRoot(t)
	// #nosec G204 -- helm resolved via LookPath; chartDir const.
	out, err := exec.Command(helm, "template", "test", filepath.Join(root, chartDir),
		"--namespace", "kubetest-alt",
		"--set", "webhook.certManager.enabled=true").CombinedOutput()
	require.NoErrorf(t, err, "helm template with cert-manager failed:\n%s", string(out))
	got := string(out)
	assert.Contains(t, got, "kind: Certificate", "certManager.enabled=true emits Certificate")
	assert.Contains(t, got, "cert-manager.io/inject-ca-from",
		"webhook configs must be annotated for cert-manager injection")
	assert.NotContains(t, got, "type: kubernetes.io/tls",
		"cert-manager path should NOT render the self-signed Secret")
}

// TestHelmTemplate_MinioPostgresValues renders with MinIO + Postgres
// wiring enabled. Asserts the CLI args land verbatim on the operator +
// apiserver Deployments so ops running scrapes / grepping ps output see
// what they expect.
func TestHelmTemplate_MinioPostgresValues(t *testing.T) {
	helm := helmBinary(t)
	root := findRepoRoot(t)
	// #nosec G204 -- helm resolved via LookPath; chartDir const.
	out, err := exec.Command(helm, "template", "test", filepath.Join(root, chartDir),
		"--namespace", "kubetest-alt",
		"--set", "minio.endpoint=minio.default.svc:9000",
		"--set", "minio.secretName=kubetest-minio-creds",
		"--set", "postgresql.dsn=postgres://user:pass@postgres.default.svc:5432/kubetest",
	).CombinedOutput()
	require.NoErrorf(t, err, "helm template with minio+postgres failed:\n%s", string(out))
	got := string(out)
	assert.Contains(t, got, "--minio-endpoint=minio.default.svc:9000")
	assert.Contains(t, got, "--minio-secret-name=kubetest-minio-creds")
	assert.Contains(t, got, "--postgres-dsn=postgres://user:pass@postgres.default.svc:5432/kubetest")
}

// TestHelmTemplate_RBACParity is the plan's exact requirement: the
// operator ClusterRole rendered by the chart MUST equal the ClusterRole
// controller-gen produces in config/rbac/role.yaml (which is the source
// of truth — comes from +kubebuilder:rbac markers).
//
// Comparison is order-independent per rule: parse rules from both,
// canonicalize (sort verbs/resources within each rule, sort rules by
// key), diff.
//
// Failure = a chart hand-edit drifted from the operator's real needs.
// Fix flow: run `make manifests`, copy the rules block from
// config/rbac/role.yaml into charts/kubetest-alt/templates/
// clusterrole-operator.yaml.
func TestHelmTemplate_RBACParity(t *testing.T) {
	helm := helmBinary(t)
	root := findRepoRoot(t)

	// Render the chart's ClusterRoles.
	// #nosec G204 -- helm resolved via LookPath; chartDir const.
	out, err := exec.Command(helm, "template", "test", filepath.Join(root, chartDir),
		"--namespace", "kubetest-alt").CombinedOutput()
	require.NoErrorf(t, err, "helm template failed:\n%s", string(out))

	// Extract the operator ClusterRole from the rendered stream.
	chartRole, ok := extractOperatorClusterRole(string(out))
	require.True(t, ok, "operator ClusterRole (name suffix -manager-role) not found in helm output")

	// Load the kubebuilder-generated one.
	// #nosec G304 -- path built from findRepoRoot + literal constants.
	genBytes, err := os.ReadFile(filepath.Join(root, "config", "rbac", "role.yaml"))
	require.NoError(t, err)
	var genRole rbacv1.ClusterRole
	require.NoError(t, yaml.Unmarshal(genBytes, &genRole))

	// Canonicalize + compare.
	assert.Equal(t, canonicalize(genRole.Rules), canonicalize(chartRole.Rules),
		`chart RBAC drifted from config/rbac/role.yaml.

Fix: run 'make manifests' to regenerate config/rbac/role.yaml, then copy
its rules block into charts/kubetest-alt/templates/clusterrole-operator.yaml.
Every rule the operator needs comes from +kubebuilder:rbac markers under
internal/; the chart is downstream, not the source of truth.`)
}

// extractOperatorClusterRole walks the multi-doc helm output looking for
// the ClusterRole whose name ends with "-manager-role".
func extractOperatorClusterRole(rendered string) (rbacv1.ClusterRole, bool) {
	for doc := range strings.SplitSeq(rendered, "\n---") {
		if !strings.Contains(doc, "kind: ClusterRole\n") {
			continue
		}
		if !strings.Contains(doc, "-manager-role\n") {
			continue
		}
		var cr rbacv1.ClusterRole
		if err := yaml.Unmarshal([]byte(doc), &cr); err != nil {
			continue
		}
		if cr.Kind != "ClusterRole" {
			continue
		}
		if !strings.HasSuffix(cr.Name, "-manager-role") {
			continue
		}
		return cr, true
	}
	return rbacv1.ClusterRole{}, false
}

// canonicalize sorts each rule's slices + sorts the rule list by a
// stable key so slice-order differences don't cause false positives.
func canonicalize(rules []rbacv1.PolicyRule) []rbacv1.PolicyRule {
	out := make([]rbacv1.PolicyRule, len(rules))
	for i, r := range rules {
		copyR := r
		copyR.APIGroups = append([]string(nil), r.APIGroups...)
		copyR.Resources = append([]string(nil), r.Resources...)
		copyR.Verbs = append([]string(nil), r.Verbs...)
		copyR.ResourceNames = append([]string(nil), r.ResourceNames...)
		copyR.NonResourceURLs = append([]string(nil), r.NonResourceURLs...)
		slices.Sort(copyR.APIGroups)
		slices.Sort(copyR.Resources)
		slices.Sort(copyR.Verbs)
		slices.Sort(copyR.ResourceNames)
		slices.Sort(copyR.NonResourceURLs)
		out[i] = copyR
	}
	slices.SortFunc(out, func(a, b rbacv1.PolicyRule) int {
		if ruleKey(a) < ruleKey(b) {
			return -1
		}
		if ruleKey(a) > ruleKey(b) {
			return 1
		}
		return 0
	})
	return out
}

func ruleKey(r rbacv1.PolicyRule) string {
	return strings.Join(r.APIGroups, ",") + "|" +
		strings.Join(r.Resources, ",") + "|" +
		strings.Join(r.Verbs, ",")
}
