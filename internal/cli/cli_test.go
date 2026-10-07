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
// Package podpolicy is what a test pod may ask for (fixes.md #1).
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/apiserver"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

const (
	bucket   = "kubetest"
	apiToken = "0123456789abcdef0123456789abcdef-cli"
	proxy    = "/api/v1/namespaces/kubetest-alt/services/http:kt-kubetest-alt-apiserver:8080/proxy"
)

// env is the real API server (fake Kubernetes, fake object storage) behind
// a service-proxy path, with the API token on.
type env struct {
	k8s     client.Client
	objects *storage.Fake
	conn    *Connection
}

func newEnv(t *testing.T, objs ...client.Object) *env {
	t.Helper()
	pollEvery, logsPollMin = 20*time.Millisecond, 20*time.Millisecond
	sch := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(sch))
	require.NoError(t, testsv1alpha1.AddToScheme(sch))
	k8s := fake.NewClientBuilder().WithScheme(sch).WithObjects(objs...).
		WithStatusSubresource(&testsv1alpha1.TestRun{}).Build()
	objects := storage.NewFake()
	srv := &apiserver.Server{K8sClient: k8s, Bucket: bucket, AuthToken: apiToken,
		Downloader: objects, Lister: objects, Presigner: objects, Remover: objects}
	mux := http.NewServeMux()
	mux.Handle(proxy+"/", http.StripPrefix(proxy, srv.Handler()))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	api, err := apiclient.New(apiclient.ServiceProxyURL(ts.URL, "kubetest-alt", "kt-kubetest-alt-apiserver", 8080), ts.Client())
	require.NoError(t, err)
	return &env{k8s: k8s, objects: objects, conn: &Connection{API: api.WithToken(apiToken).AsUser("ci-bot"), Namespace: "shop"}}
}

// cli runs one command line; it returns stdout, stderr and the error.
func (e *env) cli(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := NewRootCmd(func(context.Context, *GlobalFlags) (*Connection, error) { return e.conn, nil }, &out, &errOut)
	root.SetArgs(args)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := root.ExecuteContext(ctx)
	return out.String(), errOut.String(), err
}

func smoke() *testsv1alpha1.Test {
	return &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "shop", Labels: map[string]string{"kubetest.io/tool": "k6"}},
		Spec: testsv1alpha1.TestSpec{Schedule: "0 2 * * *",
			Container: testsv1alpha1.ContainerConfig{Image: "grafana/k6:1.4.0", Args: []string{"run", "s.js"}},
			Config:    map[string]testsv1alpha1.Parameter{"vus": {Type: "integer", Default: "10"}, "target": {Type: "string"}}},
	}
}

// finishRunsAs plays the operator: every new run of the namespace ends in
// phase once it appears.
func (e *env) finishRunsAs(t *testing.T, phase testsv1alpha1.Phase, message string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for ctx.Err() == nil {
			var runs testsv1alpha1.TestRunList
			if e.k8s.List(ctx, &runs) == nil {
				for _, r := range runs.Items {
					if r.Status.Phase == "" {
						now := metav1.Now()
						r.Status = testsv1alpha1.TestRunStatus{Phase: phase, Message: message, StartedAt: &now, FinishedAt: &now, DurationMs: 4200}
						_ = e.k8s.Status().Update(ctx, &r)
					}
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
}

func TestTests_TableAndJSON(t *testing.T) {
	e := newEnv(t, smoke())
	out, _, err := e.cli(t, "tests")
	require.NoError(t, err)
	assert.Contains(t, out, "NAME")
	assert.Regexp(t, `checkout\s+k6\s+0 2 \* \* \*\s+-\s+-`, out)

	out, _, err = e.cli(t, "tests", "-o", "json")
	require.NoError(t, err)
	var tests []testsv1alpha1.Test
	require.NoError(t, json.Unmarshal([]byte(out), &tests))
	require.Len(t, tests, 1)

	_, _, err = e.cli(t, "tests", "-o", "xml")
	require.ErrorContains(t, err, "must be json or yaml")

	out, _, err = e.cli(t, "test", "checkout")
	require.NoError(t, err)
	assert.Regexp(t, `target\s+string\s+\(required\)`, out)
	assert.Regexp(t, `vus\s+integer\s+10`, out)
}

// The CI contract: run --wait exits with the verdict.
func TestRun_WaitExitCodes(t *testing.T) {
	for phase, code := range map[testsv1alpha1.Phase]int{
		testsv1alpha1.PhasePassed:  ExitPassed,
		testsv1alpha1.PhaseFailed:  ExitFailed,
		testsv1alpha1.PhaseError:   ExitOther,
		testsv1alpha1.PhaseAborted: ExitOther,
	} {
		t.Run(string(phase), func(t *testing.T) {
			e := newEnv(t, smoke())
			e.finishRunsAs(t, phase, "verdict "+string(phase))
			out, errOut, err := e.cli(t, "run", "checkout", "--wait", "--config", "vus=50", "--config", "target=http://shop")
			assert.Contains(t, errOut, "started")
			assert.Contains(t, errOut, string(phase)+" in 4.2s: verdict "+string(phase))
			if code == ExitPassed {
				require.NoError(t, err)
				assert.True(t, strings.HasPrefix(out, "checkout-"), "the run name on stdout for scripts: %q", out)
			} else {
				var exit *ExitError
				require.ErrorAs(t, err, &exit)
				assert.Equal(t, code, exit.Code)
			}
			var runs testsv1alpha1.TestRunList
			require.NoError(t, e.k8s.List(context.Background(), &runs))
			require.Len(t, runs.Items, 1)
			r := runs.Items[0]
			assert.Equal(t, "cli", r.Spec.Source)
			assert.Equal(t, map[string]string{"vus": "50", "target": "http://shop"}, r.Spec.Config)
			assert.Equal(t, "ci-bot", r.Spec.Tags[apiclient.TagCreatedBy])
		})
	}
}

func TestRun_NoWaitPrintsTheNameAndTimeoutIsExit2(t *testing.T) {
	e := newEnv(t, smoke())
	out, _, err := e.cli(t, "run", "checkout")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(out), "checkout-"))

	_, _, err = e.cli(t, "run", "checkout", "--wait", "--timeout", "100ms") // nobody finishes it
	var exit *ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, ExitOther, exit.Code)
	assert.Contains(t, err.Error(), "did not finish within 100ms")

	_, _, err = e.cli(t, "run", "checkout", "--config", "novalue")
	require.ErrorContains(t, err, "want NAME=VALUE")
	_, _, err = e.cli(t, "run", "checkout", "--at", "tomorrow")
	require.ErrorContains(t, err, "RFC 3339")
}

func finishedRun(name string) *testsv1alpha1.TestRun {
	now := metav1.Now()
	return &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: types.UID("00000000-0000-0000-0000-0000000000c1")},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "checkout", Source: "ui"},
		Status: testsv1alpha1.TestRunStatus{Phase: testsv1alpha1.PhaseFailed, Message: "exit code 1", StartedAt: &now, FinishedAt: &now,
			DurationMs: 61000, Metrics: map[string]string{"p95_ms": "120"},
			ArtifactRefs: []testsv1alpha1.ArtifactRef{{Path: "report/index.html"}, {Path: "../../escape.txt"}}},
	}
}

func TestRuns_LogsArtifactsAbort(t *testing.T) {
	run := finishedRun("checkout-abc")
	live := finishedRun("checkout-live")
	live.UID, live.Status = "00000000-0000-0000-0000-0000000000c2", testsv1alpha1.TestRunStatus{Phase: testsv1alpha1.PhaseRunning}
	e := newEnv(t, smoke(), run, live)
	keys := storage.ForRun("shop", string(run.UID))
	ctx := context.Background()
	require.NoError(t, e.objects.Put(ctx, bucket, keys.LogChunk(0), strings.NewReader("hello from k6\n"), -1, "text/plain"))
	require.NoError(t, e.objects.Put(ctx, bucket, keys.Artifact("report/index.html"), strings.NewReader("<html>"), -1, "text/html"))
	require.NoError(t, e.objects.Put(ctx, bucket, keys.Artifact("../../escape.txt"), strings.NewReader("x"), -1, "text/plain"))

	out, _, err := e.cli(t, "runs")
	require.NoError(t, err)
	assert.Regexp(t, `checkout-abc\s+checkout\s+failed`, out)

	out, _, err = e.cli(t, "runs", "checkout-abc")
	require.NoError(t, err)
	assert.Contains(t, out, "exit code 1")
	assert.Regexp(t, `Metric p95_ms:\s+120`, out)

	out, _, err = e.cli(t, "logs", "checkout-abc")
	require.NoError(t, err)
	assert.Equal(t, "hello from k6\n", out)
	out, _, err = e.cli(t, "logs", "checkout-abc", "-f")
	require.NoError(t, err)
	assert.Equal(t, "hello from k6\n", out, "follow on a finished run prints it once")

	dir := t.TempDir()
	out, errOut, err := e.cli(t, "artifacts", "checkout-abc", "--download", dir)
	b, rerr := os.ReadFile(filepath.Join(dir, "report", "index.html")) // #nosec G304 -- t.TempDir()
	require.NoError(t, rerr)
	assert.Equal(t, "<html>", string(b))
	assert.Contains(t, errOut, "skipped ../../escape.txt", "one bad artifact doesn't stop the others")
	var exit *ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, ExitOther, exit.Code)
	assert.NoFileExists(t, filepath.Join(filepath.Dir(dir), "escape.txt"), "nothing written outside --download")
	assert.NotContains(t, out, "..")

	_, errOut, err = e.cli(t, "abort", "checkout-live", "--message", "deploy rolled back")
	require.NoError(t, err)
	assert.Contains(t, errOut, "abort of checkout-live requested")
	var got testsv1alpha1.TestRun
	require.NoError(t, e.k8s.Get(ctx, types.NamespacedName{Namespace: "shop", Name: "checkout-live"}, &got))
	require.NotNil(t, got.Spec.Abort)
	assert.Equal(t, "deploy rolled back", got.Spec.Abort.Message)
}

func TestSafeJoin(t *testing.T) {
	for in, want := range map[string]string{
		"report/index.html": filepath.Join("d", "report", "index.html"),
		"../../etc/passwd":  filepath.Join("d", "etc", "passwd"),
		"/abs/file":         filepath.Join("d", "abs", "file"),
		"a/../../../b":      filepath.Join("d", "b"),
	} {
		got, err := safeJoin("d", in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	_, err := safeJoin("d", "../")
	require.Error(t, err)
}

func apiService(name string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kubetest-alt", Labels: map[string]string{
			"app.kubernetes.io/name": "kubetest-alt", "app.kubernetes.io/component": "apiserver"}},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "metrics", Port: 9090}, {Name: "http", Port: 8080}}},
	}
}

func TestDiscover(t *testing.T) {
	f := &GlobalFlags{KubetestNamespace: "kubetest-alt"}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kt-kubetest-alt-api-token", Namespace: "kubetest-alt"},
		Data: map[string][]byte{"token": []byte(apiToken + "\n")}}
	ep, err := discover(context.Background(), k8sfake.NewClientset(apiService("kt-kubetest-alt-apiserver"), secret), f)
	require.NoError(t, err)
	assert.Equal(t, endpoint{service: "kt-kubetest-alt-apiserver", port: 8080, token: apiToken}, ep)

	t.Setenv(EnvToken, "from-the-pipeline")
	ep, err = discover(context.Background(), k8sfake.NewClientset(apiService("kt-kubetest-alt-apiserver")), f)
	require.NoError(t, err)
	assert.Equal(t, "from-the-pipeline", ep.token, "$KUBETEST_API_TOKEN needs no Secret access")
	t.Setenv(EnvToken, "")

	_, err = discover(context.Background(), k8sfake.NewClientset(apiService("kt-kubetest-alt-apiserver")), f)
	require.ErrorContains(t, err, "no API token Secret kubetest-alt/kt-kubetest-alt-api-token")
	_, err = discover(context.Background(), k8sfake.NewClientset(), f)
	require.ErrorContains(t, err, "no kubetest API Service in namespace kubetest-alt")
	_, err = discover(context.Background(), k8sfake.NewClientset(apiService("a-apiserver"), apiService("b-apiserver")), f)
	require.ErrorContains(t, err, "--api-service")
}
