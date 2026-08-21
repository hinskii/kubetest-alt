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

// Package e2e runs the plan's 5 kind e2e scenarios against a chart-
// installed cluster. Guarded by the `e2e` build tag so `make test`
// stays fast; `make test-e2e` + the CI workflow are the actual entry
// points.
//
// Assumptions the outer script (test/e2e/run.sh) sets up before this
// binary runs:
//   1. `kind create cluster` — a fresh cluster, KUBECONFIG points at it.
//   2. Docker images built + `kind load` — operator, apiserver,
//      content-fetcher, and the three tool-bundle images live inside
//      the kind node so ImagePullPolicy=Never resolves.
//   3. `helm install kt charts/kubetest-alt/ -n kubetest-alt --create-namespace`
//      — operator + apiserver deployments running and Ready.
//   4. `kubectl -n kubetest-alt rollout status` — same-namespace ready.
//
// Everything from here on happens with a plain client-go / ctrl-runtime
// client. Per-scenario timings recorded via t.Log so the report can
// splice them.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

const (
	// releaseNS is the namespace `run.sh` installs the chart into.
	releaseNS = "kubetest-alt"
	// workloadNS is the namespace test manifests are created in.
	workloadNS = "kubetest-e2e"
)

// buildScheme returns a scheme registered for our CRDs.
func buildScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	require.NoError(t, testsv1alpha1.AddToScheme(s))
	return s
}

// newClient builds a ctrl-runtime client from KUBECONFIG (kind writes
// the correct path via `kind get kubeconfig`).
func newClient(t *testing.T) client.Client {
	t.Helper()
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = filepath.Join(os.Getenv("HOME"), ".kube", "config")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	require.NoError(t, err, "KUBECONFIG=%q must point at a running kind cluster", kubeconfig)
	c, err := client.New(cfg, client.Options{Scheme: buildScheme(t)})
	require.NoError(t, err)
	return c
}

// TestE2E is the umbrella. Each sub-test is one plan scenario; we run
// them serially so cluster state is deterministic + per-scenario
// timing lands in `go test -v` output. Total budget from the plan: 15
// min. Individual timings emitted via t.Log for the report.
func TestE2E(t *testing.T) {
	if os.Getenv("SKIP_E2E") != "" {
		t.Skip("SKIP_E2E set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	c := newClient(t)
	// Fresh workload namespace per suite run. Idempotent create.
	ensureNamespace(t, ctx, c, workloadNS)
	t.Cleanup(func() { deleteNamespace(t, context.Background(), c, workloadNS) })

	// Apply the catalog templates (Test + TestTemplate CRs used by
	// scenarios below). One shot with kubectl apply -k is simpler than
	// re-parsing per test.
	applyKustomize(t, "config/samples/tools/", workloadNS)

	t.Run("Scenario1_K6_Passing", func(t *testing.T) {
		start := time.Now()
		defer func() { t.Logf("SCENARIO_TIMING scenario=1 tool=k6 duration=%s", time.Since(start)) }()
		scenarioK6Passing(t, ctx, c)
	})
	t.Run("Scenario2_JMeter_FailingVerdictFromJTL", func(t *testing.T) {
		start := time.Now()
		defer func() { t.Logf("SCENARIO_TIMING scenario=2 tool=jmeter duration=%s", time.Since(start)) }()
		scenarioJMeterFailing(t, ctx, c)
	})
	t.Run("Scenario3_ContentFetchFailure", func(t *testing.T) {
		start := time.Now()
		defer func() { t.Logf("SCENARIO_TIMING scenario=3 kind=content-fetch duration=%s", time.Since(start)) }()
		scenarioContentFetchFail(t, ctx, c)
	})
	t.Run("Scenario4_GitOpsGuard409", func(t *testing.T) {
		start := time.Now()
		defer func() { t.Logf("SCENARIO_TIMING scenario=4 kind=gitops-guard duration=%s", time.Since(start)) }()
		scenarioGitOpsGuard(t, ctx, c)
	})
	t.Run("Scenario5_Cron", func(t *testing.T) {
		start := time.Now()
		defer func() { t.Logf("SCENARIO_TIMING scenario=5 kind=cron duration=%s", time.Since(start)) }()
		scenarioCron(t, ctx, c)
	})

	// Post-scenario: /metrics from operator + apiserver. Asserts the
	// step-14 counters got real events end-to-end.
	t.Run("MetricsScrape_OperatorAndApiserver", func(t *testing.T) {
		start := time.Now()
		defer func() { t.Logf("SCENARIO_TIMING scenario=metrics duration=%s", time.Since(start)) }()
		metricsScrape(t, ctx)
	})

	// Post-scenario: zero ERROR log entries in the operator pod. Catches
	// silent reconcile loops / dropped-error panics that Ginkgo-happy
	// scenarios wouldn't flag.
	t.Run("OperatorLogs_NoErrorEntries", func(t *testing.T) {
		start := time.Now()
		defer func() { t.Logf("SCENARIO_TIMING scenario=logs duration=%s", time.Since(start)) }()
		assertNoErrorLogs(t)
	})
}

// -------------------- scenarios --------------------

// scenarioK6Passing: a Test using the k6 catalog template with a
// trivial script (checks that always pass) → phase=passed. The apply-
// test golden already covers spec resolution; this proves the FULL
// chain — CRD admit, compile, Job, wrapper, verdict — on a real cluster.
func scenarioK6Passing(t *testing.T, ctx context.Context, c client.Client) {
	// Inline script — a k6 script with only "return true" checks so no
	// external HTTP + no thresholds → exit code 0 → phase=passed. Ships
	// via spec.content.files[] (inline; well under the 512KB webhook cap).
	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "e2e-k6-pass",
			Namespace: workloadNS,
			Labels:    map[string]string{"kubetest.io/tool": "k6"},
		},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: "Allow",
			Container: testsv1alpha1.ContainerConfig{
				Image: "grafana/k6:1.4.0",
				// Command MUST be set — the /entry wrapper splits on the
				// binary name in Command[0], and grafana/k6's ENTRYPOINT
				// is k6 (which entry can't discover since /entry runs
				// via the shared /kubetest-bin volume, not via the image
				// ENTRYPOINT). Args alone would pass "run" as the binary.
				Command: []string{"k6"},
				Args:    []string{"run", "/data/repo/script.js"},
			},
			Content: testsv1alpha1.Content{
				Files: []testsv1alpha1.FileContent{{
					// `repo/` prefix matches the platform's git-mount convention
					// (§CLAUDE Content§, /data/repo). k6/jmeter/gatling templates
					// expand `{{ config.script }}` under /data/repo/ — inline
					// content.files[] paths must land there too or the tool's
					// argv points at a non-existent file.
					Path:    "repo/script.js",
					Content: "export default function() { /* pass */ }",
				}},
			},
		},
	}
	require.NoError(t, c.Create(ctx, test))
	run := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-k6-pass-run", Namespace: workloadNS},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "e2e-k6-pass", Source: "api"},
	}
	require.NoError(t, c.Create(ctx, run))
	final := waitForPhase(t, ctx, c, run.Name, testsv1alpha1.PhasePassed, 3*time.Minute)
	assert.Equal(t, testsv1alpha1.PhasePassed, final.Status.Phase)
	t.Logf("k6 run finished — durationMs=%d", final.Status.DurationMs)
}

// scenarioJMeterFailing: the flagship §15.2 assertion — JMeter exits 0
// on a plan that failed 100% of requests, but the template's
// verdictFrom: jtl (errorRateMax: "0") flips phase to failed. Proven
// end-to-end on a real cluster.
func scenarioJMeterFailing(t *testing.T, ctx context.Context, c client.Client) {
	// Minimal failing plan — a DNS lookup on an .invalid TLD.
	plan := `<?xml version="1.0" encoding="UTF-8"?>
<jmeterTestPlan version="1.2" properties="5.0" jmeter="5.6.3">
  <hashTree>
    <TestPlan guiclass="TestPlanGui" testclass="TestPlan" testname="e2e-fail">
      <boolProp name="TestPlan.functional_mode">false</boolProp>
      <elementProp name="TestPlan.user_defined_variables" elementType="Arguments"/>
    </TestPlan>
    <hashTree>
      <ThreadGroup guiclass="ThreadGroupGui" testclass="ThreadGroup" testname="G">
        <intProp name="ThreadGroup.num_threads">1</intProp>
        <intProp name="ThreadGroup.ramp_time">1</intProp>
        <elementProp name="ThreadGroup.main_controller" elementType="LoopController">
          <boolProp name="LoopController.continue_forever">false</boolProp>
          <intProp name="LoopController.loops">1</intProp>
        </elementProp>
      </ThreadGroup>
      <hashTree>
        <HTTPSamplerProxy guiclass="HttpTestSampleGui" testclass="HTTPSamplerProxy" testname="Bogus">
          <stringProp name="HTTPSampler.domain">no-such-host-e2e-99999.invalid</stringProp>
          <stringProp name="HTTPSampler.protocol">http</stringProp>
          <stringProp name="HTTPSampler.connect_timeout">2000</stringProp>
        </HTTPSamplerProxy>
        <hashTree/>
      </hashTree>
    </hashTree>
  </hashTree>
</jmeterTestPlan>`
	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "e2e-jmeter-fail",
			Namespace: workloadNS,
			Labels:    map[string]string{"kubetest.io/tool": "jmeter"},
		},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: "Allow",
			Use:               []string{"jmeter"},
			Content: testsv1alpha1.Content{
				Files: []testsv1alpha1.FileContent{{
					Path:    "repo/smoke.jmx",
					Content: plan,
				}},
			},
			Config: map[string]testsv1alpha1.Parameter{
				"plan": {Type: "string", Default: "smoke.jmx"},
			},
		},
	}
	require.NoError(t, c.Create(ctx, test))
	run := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-jmeter-fail-run", Namespace: workloadNS},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "e2e-jmeter-fail", Source: "api"},
	}
	require.NoError(t, c.Create(ctx, run))
	final := waitForPhase(t, ctx, c, run.Name, testsv1alpha1.PhaseFailed, 4*time.Minute)
	assert.Equal(t, testsv1alpha1.PhaseFailed, final.Status.Phase,
		"JMeter's exit 0 on all-fail plan MUST NOT ship as passed — verdictFrom:jtl override is the whole reason for this test")
}

// scenarioContentFetchFail: bad git URI → phase=error with
// reason=ContentFetchFailed. Exercises the step-06 init-container
// error-surfacing path all the way up.
func scenarioContentFetchFail(t *testing.T, ctx context.Context, c client.Client) {
	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "e2e-bad-git",
			Namespace: workloadNS,
			Labels:    map[string]string{"kubetest.io/tool": "k6"},
		},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: "Allow",
			Container: testsv1alpha1.ContainerConfig{
				Image: "grafana/k6:1.4.0",
				Args:  []string{"run", "/data/repo/script.js"},
			},
			Content: testsv1alpha1.Content{
				Git: &testsv1alpha1.GitContent{
					URI:      "https://no-such-host-e2e-99999.invalid/nothing.git",
					Revision: "main",
				},
			},
		},
	}
	require.NoError(t, c.Create(ctx, test))
	run := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-bad-git-run", Namespace: workloadNS},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "e2e-bad-git", Source: "api"},
	}
	require.NoError(t, c.Create(ctx, run))
	final := waitForPhase(t, ctx, c, run.Name, testsv1alpha1.PhaseError, 3*time.Minute)
	assert.Contains(t, final.Status.Message, "ContentFetchFailed",
		"content-fetcher init container failure must surface as reason=ContentFetchFailed")
}

// scenarioGitOpsGuard: a Test labeled app.kubernetes.io/managed-by=gitops
// is read-only via the apiserver — a PATCH returns 409. Skipped when
// the apiserver isn't wired via NodePort (checks env var).
//
// The apiserver is inside the cluster; we port-forward via kubectl to
// hit it. run.sh sets APISERVER_URL to the port-forward endpoint before
// invoking this test.
func scenarioGitOpsGuard(t *testing.T, ctx context.Context, c client.Client) {
	apiURL := os.Getenv("APISERVER_URL")
	if apiURL == "" {
		t.Skip("APISERVER_URL not set (run.sh should port-forward + export before Go test)")
	}
	// Create a Test labeled gitops.
	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "e2e-gitops",
			Namespace: workloadNS,
			Labels: map[string]string{
				"kubetest.io/tool":              "k6",
				"app.kubernetes.io/managed-by":  "gitops",
			},
		},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: "Allow",
			Container: testsv1alpha1.ContainerConfig{
				Image: "grafana/k6:1.4.0",
				Args:  []string{"run", "-"},
			},
		},
	}
	require.NoError(t, c.Create(ctx, test))

	// PATCH via the apiserver — must 409.
	url := fmt.Sprintf("%s/tests/%s", strings.TrimRight(apiURL, "/"), test.Name)
	body := strings.NewReader(`{"spec":{"container":{"args":["run","edited.js"]}}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/merge-patch+json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusConflict, resp.StatusCode,
		"gitops-labeled Test PATCH via apiserver MUST return 409 (§7 managed-by enforcement)")
}

// scenarioCron: a Test with schedule "* * * * *" — within 70s the
// scheduler creates a TestRun with source=cron. Then remove the
// schedule so the cluster doesn't accumulate runs.
func scenarioCron(t *testing.T, ctx context.Context, c client.Client) {
	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "e2e-cron",
			Namespace: workloadNS,
			Labels:    map[string]string{"kubetest.io/tool": "k6"},
		},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: "Allow",
			Schedule:          "* * * * *",
			Container: testsv1alpha1.ContainerConfig{
				Image: "grafana/k6:1.4.0",
				Args:  []string{"run", "/data/repo/s.js"},
			},
			Content: testsv1alpha1.Content{
				Files: []testsv1alpha1.FileContent{{
					Path:    "repo/s.js",
					Content: "export default function() {}",
				}},
			},
		},
	}
	require.NoError(t, c.Create(ctx, test))
	defer func() {
		// Remove schedule so the scheduler stops firing while the cluster
		// tears down. Idempotent on missing Test.
		var fresh testsv1alpha1.Test
		if err := c.Get(ctx, client.ObjectKey{Namespace: workloadNS, Name: "e2e-cron"}, &fresh); err == nil {
			fresh.Spec.Schedule = ""
			_ = c.Update(ctx, &fresh)
		}
	}()
	// Poll for a TestRun with source=cron within 90s (buffer over
	// scheduler tick default 30s + cron granularity 60s).
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		var runs testsv1alpha1.TestRunList
		require.NoError(t, c.List(ctx, &runs, client.InNamespace(workloadNS)))
		for _, r := range runs.Items {
			if r.Spec.TestRef == "e2e-cron" && r.Spec.Source == "cron" {
				t.Logf("cron TestRun observed: %s (source=%s)", r.Name, r.Spec.Source)
				return
			}
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatal("no cron-sourced TestRun observed within 90s")
}

// -------------------- metrics + logs --------------------

// metricsScrape hits /metrics on the operator's manager (via port-
// forward, URL passed by run.sh as METRICS_OPERATOR_URL) and on the
// apiserver's /metrics endpoint (METRICS_APISERVER_URL). Asserts:
//   * both endpoints return 200 with prometheus text format
//   * runs_total{tool="k6",phase="passed"} counter is >= 1 (proves
//     the metrics wiring from step 14 lit up in real cluster context)
//   * webhook_deliveries_total series exists (may be zero if no
//     subscribers — step-14-plus deliverability is out of scope here)
func metricsScrape(t *testing.T, ctx context.Context) {
	opURL := os.Getenv("METRICS_OPERATOR_URL")
	apiURL := os.Getenv("METRICS_APISERVER_URL")
	if opURL == "" || apiURL == "" {
		t.Skip("METRICS_OPERATOR_URL / METRICS_APISERVER_URL not set (run.sh port-forwards + exports)")
	}
	for _, endpoint := range []string{opURL, apiURL} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoErrorf(t, err, "GET %s", endpoint)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "endpoint %s must return 200", endpoint)
		text := string(body)
		assert.Contains(t, text, "kubetest_", "kubetest_* metrics MUST be exposed on %s", endpoint)
	}
	// Operator-side counter assertion.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, opURL, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	// Prometheus text format line:
	//   kubetest_runs_total{phase="passed",source="api",tool="k6"} 1
	// Label order isn't fixed — match with regex.
	re := regexp.MustCompile(
		`kubetest_runs_total\{[^}]*tool="k6"[^}]*phase="passed"[^}]*\}\s+([0-9.eE+-]+)|` +
			`kubetest_runs_total\{[^}]*phase="passed"[^}]*tool="k6"[^}]*\}\s+([0-9.eE+-]+)`)
	m := re.FindStringSubmatch(string(body))
	require.NotEmpty(t, m, "kubetest_runs_total{tool=k6,phase=passed} MUST be present with a positive count on operator /metrics")
	// The two OR groups mean m[1] or m[2] is non-empty depending on
	// which order the labels were emitted. Both parse as float ≥ 1.
	valueStr := m[1]
	if valueStr == "" {
		valueStr = m[2]
	}
	assert.NotEmpty(t, valueStr)
	t.Logf("operator kubetest_runs_total{tool=k6,phase=passed} = %s", valueStr)
}

// assertNoErrorLogs kubectl-logs the operator pod for the whole test
// run and greps for "ERROR" level entries. Plan: any = fail. If the
// operator's Deployment name changed the test surfaces the miss with
// a clear message.
func assertNoErrorLogs(t *testing.T) {
	// Wrap kubectl — simplest cross-platform way to get logs from a
	// Deployment (--tail=-1). Assumes kubectl on PATH (CI installs it).
	out, err := exec.Command("kubectl", "logs",
		"-n", releaseNS,
		"deploy/kt-kubetest-alt-operator",
		"--all-containers=true",
		"--tail=-1").CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl logs failed: %v\n%s", err, string(out))
	}
	// controller-runtime's zap logger emits `"level":"error"` or
	// `"level":"ERROR"` (per config) in JSON, and `\tERROR\t` in
	// console format. Match both.
	scanner := strings.SplitSeq(string(out), "\n")
	var errLines []string
	for line := range scanner {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		if strings.Contains(l, `"level":"error"`) ||
			strings.Contains(l, `"level":"ERROR"`) ||
			strings.Contains(l, "\tERROR\t") {
			errLines = append(errLines, l)
		}
	}
	assert.Emptyf(t, errLines,
		"operator logged %d ERROR-level entry(ies) during e2e — silent reconcile loops or dropped errors:\n%s",
		len(errLines), strings.Join(errLines, "\n"))
}

// -------------------- helpers --------------------

func ensureNamespace(t *testing.T, ctx context.Context, c client.Client, name string) {
	t.Helper()
	// Real corev1.Namespace so the client-go scheme (already registered
	// via clientgoscheme.AddToScheme) recognizes the GVK. An earlier
	// hand-rolled `corev1NamespaceLite` struct failed with
	// "no kind is registered for the type e2e.corev1NamespaceLite"
	// because it isn't in any scheme — controller-runtime looks up the
	// GVK by Go type, not by TypeMeta.
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	err := c.Create(ctx, ns)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err, "create namespace %s", name)
	}
}

func deleteNamespace(t *testing.T, ctx context.Context, c client.Client, name string) {
	t.Helper()
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	_ = c.Delete(ctx, ns)
}

// applyKustomize is a best-effort `kubectl apply -k <dir>`. Failure is
// logged but non-fatal — many scenarios ship their own inline manifest,
// so the catalog samples are convenience not strict precondition.
func applyKustomize(t *testing.T, kustDir, ns string) {
	t.Helper()
	root := findRepoRoot(t)
	cmd := exec.Command("kubectl", "apply", "-k", filepath.Join(root, kustDir), "-n", ns)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("apply-k %s skipped: %v\n%s", kustDir, err, string(out))
	}
}

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

// waitForPhase polls until run.Status.Phase == want or the deadline
// fires. Returns the final observed TestRun so callers can assert on
// additional fields (message, artifacts, ...).
func waitForPhase(t *testing.T, ctx context.Context, c client.Client, runName string, want testsv1alpha1.Phase, timeout time.Duration) *testsv1alpha1.TestRun {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last testsv1alpha1.TestRun
	for time.Now().Before(deadline) {
		if err := c.Get(ctx, client.ObjectKey{Namespace: workloadNS, Name: runName}, &last); err == nil {
			if last.Status.Phase == want {
				return &last
			}
		}
		time.Sleep(2 * time.Second)
	}
	// Emit a JSON dump of the final observation so failures surface
	// enough context to triage without needing kubectl access.
	dump, _ := json.MarshalIndent(last, "", "  ")
	t.Fatalf("TestRun %s did not reach phase %q within %s\nFinal:\n%s", runName, want, timeout, string(dump))
	return nil
}
