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

package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/apiserver"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/auth"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/clusters"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/config"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/views"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

const (
	bucket     = "kubetest"
	proxyPath  = "/api/v1/namespaces/kubetest-alt/services/http:kubetest-alt-apiserver:8080/proxy"
	developer  = "dev@example.com"
	admin      = "boss@example.com"
	runUID     = "11111111-2222-3333-4444-555555555555"
	clusterURL = "/clusters/dev"
)

// world is Control Center wired to a real kubetest API server (fake
// Kubernetes client, fake object storage) behind a service-proxy path —
// the pages are tested against the real API, not a mock of it.
type world struct {
	h       http.Handler
	k8s     client.Client
	objects *storage.Fake
}

func newWorld(t *testing.T, objs ...client.Object) *world {
	t.Helper()
	return newWorldWithArchive(t, nil, objs...)
}

// newWorldWithArchive also gives the API server a run history (runs whose
// TestRun is gone), when archive is non-nil.
func newWorldWithArchive(t *testing.T, archive *archiveStore, objs ...client.Object) *world {
	t.Helper()
	sch := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(sch))
	require.NoError(t, testsv1alpha1.AddToScheme(sch))
	k8s := fake.NewClientBuilder().WithScheme(sch).WithObjects(objs...).Build()
	objects := storage.NewFake()
	api := &apiserver.Server{K8sClient: k8s, Bucket: bucket,
		Downloader: objects, Lister: objects, Presigner: objects, Remover: objects, Cases: cannedCases{}}
	if archive != nil {
		api.Store, api.Deleter = archive, archive
	}

	mux := http.NewServeMux()
	mux.Handle(proxyPath+"/", http.StripPrefix(proxyPath, api.Handler()))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	cfg := &config.Config{Environment: config.EnvProduction,
		RBAC: config.RBAC{Admins: []string{admin}, Developers: []string{developer}}}
	reg, err := clusters.New(t.Context(), []config.Cluster{{
		Name: "dev", DisplayName: "Deploy (dev)", Server: ts.URL,
		Auth:      config.ClusterAuth{Type: config.AuthServiceAccount},
		APIServer: config.APIServerRef{Namespace: "kubetest-alt", Service: "kubetest-alt-apiserver", Port: 8080},
	}}, clusters.Options{InClusterConfig: func() (*rest.Config, error) { return &rest.Config{}, nil }})
	require.NoError(t, err)
	res, err := auth.NewResolver(cfg, "")
	require.NoError(t, err)
	v, err := views.New()
	require.NoError(t, err)
	s := &Server{Clusters: reg, Auth: res, Views: v, ProbeTimeout: time.Second,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return &world{h: s.Handler(), k8s: k8s, objects: objects}
}

func (w *world) get(t *testing.T, path, user string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if user != "" {
		req.Header.Set(auth.HeaderEmail, user)
	}
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	return rec
}

func (w *world) post(t *testing.T, path, user string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if user != "" {
		req.Header.Set(auth.HeaderEmail, user)
	}
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	return rec
}

func smokeTest() *testsv1alpha1.Test {
	return &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "smoke", Namespace: "team-a",
			Labels: map[string]string{"kubetest.io/tool": "k6", "app.kubernetes.io/managed-by": "ui"}},
		Spec: testsv1alpha1.TestSpec{
			Container: testsv1alpha1.ContainerConfig{Image: "grafana/k6:1.4.0", Args: []string{"run", "s.js"}},
			Config: map[string]testsv1alpha1.Parameter{
				"vus":    {Type: "integer", Default: "10"},
				"env":    {Type: "string", Default: "stage", Enum: []string{"stage", "prod"}},
				"target": {Type: "string"},
			},
		},
		Status: testsv1alpha1.TestStatus{LatestRun: &testsv1alpha1.RunReference{Name: "smoke-abcde", Phase: "failed"}},
	}
}

func gitopsTest() *testsv1alpha1.Test {
	return &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "team-b", Labels: map[string]string{"kubetest.io/tool": "cypress"}},
		Spec:       testsv1alpha1.TestSpec{Container: testsv1alpha1.ContainerConfig{Image: "cypress/included", Args: []string{"run"}}},
	}
}

func runOf(name string, phase testsv1alpha1.Phase) *testsv1alpha1.TestRun {
	now := metav1.NewTime(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	r := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team-a", UID: types.UID(runUID)},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "smoke", Source: "ui", Tags: map[string]string{"kubetest.io/created-by": developer}},
		Status: testsv1alpha1.TestRunStatus{Phase: phase, StartedAt: &now, Message: "1 of 2 workers failed",
			Steps: map[string]testsv1alpha1.StepResult{
				"worker-10": {Phase: "passed"}, "worker-2": {Phase: "failed", Message: "exit code 1"},
			}},
	}
	if phase != testsv1alpha1.PhaseRunning {
		r.Status.FinishedAt = &now
		r.Status.ArtifactRefs = []testsv1alpha1.ArtifactRef{{Path: "report/index.html", ContentType: "text/html"}}
	}
	return r
}

func TestTestsPage_GroupsByToolWithLatestRunAndLock(t *testing.T) {
	w := newWorld(t, smokeTest(), gitopsTest())
	rec := w.get(t, clusterURL, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, "2 of 2 tests")
	assert.Less(t, strings.Index(body, ">cypress<"), strings.Index(body, ">k6<"), "groups sorted by tool")
	assert.Contains(t, body, `href="/clusters/dev/tests/team-a/smoke"`)
	assert.Contains(t, body, `href="/clusters/dev/runs/team-a/smoke-abcde"`)
	assert.Contains(t, body, "status-failed")
	assert.Equal(t, 1, strings.Count(body, ">read-only<"), "only the Test not created by the GUI is locked")

	filtered := w.get(t, clusterURL+"?tool=k6", "").Body.String()
	assert.Contains(t, filtered, "1 of 2 tests")
	assert.NotContains(t, filtered, "checkout")
}

func TestTestPage_RunFormOnlyForDevelopers(t *testing.T) {
	w := newWorld(t, smokeTest(), runOf("smoke-abcde", testsv1alpha1.PhaseFailed))
	page := "/clusters/dev/tests/team-a/smoke"

	dev := w.get(t, page, developer).Body.String()
	assert.Contains(t, dev, `name="config.vus" value="10" step="1"`)
	assert.Contains(t, dev, `<option value="stage" selected>stage</option>`)
	assert.Contains(t, dev, `name="config.target" value="" required`)
	assert.Contains(t, dev, `href="/clusters/dev/runs/team-a/smoke-abcde"`, "run history")
	assert.Contains(t, dev, developer, "who started it")

	viewer := w.get(t, page, "someone@example.com").Body.String()
	assert.NotContains(t, viewer, "Run test")

	assert.Equal(t, http.StatusNotFound, w.get(t, "/clusters/dev/tests/team-a/nope", "").Code)
	assert.Equal(t, http.StatusNotFound, w.get(t, "/clusters/prod/tests/team-a/smoke", "").Code)
}

func TestStartRun_SendsOnlyChangedParamsAndRedirects(t *testing.T) {
	w := newWorld(t, smokeTest())
	page := "/clusters/dev/tests/team-a/smoke"
	rec := w.post(t, page+"/run", developer, url.Values{
		"config.vus": {"50"}, "config.env": {"stage"}, "config.target": {"https://shop"},
		"notBefore": {"2030-01-02T03:04"},
	})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	loc := rec.Header().Get("Location")
	assert.True(t, strings.HasPrefix(loc, "/clusters/dev/runs/team-a/smoke-"), loc)
	assert.Contains(t, loc, "notice=Run+scheduled+for+2030-01-02+03%3A04+UTC.")

	var runs testsv1alpha1.TestRunList
	require.NoError(t, w.k8s.List(context.Background(), &runs))
	require.Len(t, runs.Items, 1)
	r := runs.Items[0]
	assert.Equal(t, map[string]string{"vus": "50", "target": "https://shop"}, r.Spec.Config, "defaults are not sent")
	assert.Equal(t, developer, r.Spec.Tags["kubetest.io/created-by"])
	require.NotNil(t, r.Spec.NotBefore)
	assert.Equal(t, time.Date(2030, 1, 2, 3, 4, 0, 0, time.UTC), r.Spec.NotBefore.UTC())

	missing := w.post(t, page+"/run", developer, url.Values{"config.vus": {"5"}})
	assert.Contains(t, missing.Header().Get("Location"), "Parameter+target+is+required")

	assert.Equal(t, http.StatusForbidden, w.post(t, page+"/run", "someone@example.com", url.Values{}).Code)
}

func TestRunPage_FinishedRun(t *testing.T) {
	w := newWorld(t, smokeTest(), runOf("smoke-abcde", testsv1alpha1.PhaseFailed))
	page := "/clusters/dev/runs/team-a/smoke-abcde"
	body := w.get(t, page, admin).Body.String()

	assert.Contains(t, body, "1 of 2 workers failed")
	assert.Less(t, strings.Index(body, "worker-2"), strings.Index(body, "worker-10"), "natural order")
	assert.Contains(t, body, `href="/clusters/dev/runs/team-a/smoke-abcde/artifacts/report/index.html"`)
	assert.Contains(t, body, `data-live="false"`)
	assert.Contains(t, body, `action="/clusters/dev/runs/team-a/smoke-abcde/comment"`)
	assert.Contains(t, body, "Delete run", "admins may delete finished runs")
	assert.NotContains(t, body, "Abort run")

	dev := w.get(t, page, developer).Body.String()
	assert.NotContains(t, dev, "Delete run")
	notice := w.get(t, page+"?notice=%3Cb%3Ehi%3C%2Fb%3E", "").Body.String()
	assert.Contains(t, notice, "&lt;b&gt;hi&lt;/b&gt;", "notices are escaped")
}

func TestRunPage_LiveRunCanBeAborted(t *testing.T) {
	w := newWorld(t, smokeTest(), runOf("smoke-live", testsv1alpha1.PhaseRunning))
	page := "/clusters/dev/runs/team-a/smoke-live"
	body := w.get(t, page, developer).Body.String()
	assert.Contains(t, body, `data-live="true"`)
	assert.Contains(t, body, "Abort run")
	assert.Contains(t, body, "A comment can be added once the run has finished.")

	rec := w.post(t, page+"/abort", developer, url.Values{"message": {"wrong env"}})
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, page+"?notice=Abort+requested.", rec.Header().Get("Location"))
	var r testsv1alpha1.TestRun
	require.NoError(t, w.k8s.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "smoke-live"}, &r))
	require.NotNil(t, r.Spec.Abort)
	assert.Equal(t, developer, r.Spec.Abort.RequestedBy)
	assert.Equal(t, "wrong env", r.Spec.Abort.Message)

	// A comment on a live run is refused by the API; the page says why.
	rec = w.post(t, page+"/comment", developer, url.Values{"text": {"x"}})
	assert.Contains(t, rec.Header().Get("Location"), "Could+not+save+the+comment")
}

func TestRunLogPoll_FromOffsetWithPhase(t *testing.T) {
	w := newWorld(t, smokeTest(), runOf("smoke-live", testsv1alpha1.PhaseRunning))
	keys := storage.ForRun("team-a", runUID)
	require.NoError(t, w.objects.Put(context.Background(), bucket, keys.LogChunk(0), bytes.NewReader([]byte("hello\n")), 6, "text/plain"))
	require.NoError(t, w.objects.Put(context.Background(), bucket, keys.LogChunk(1), bytes.NewReader([]byte("world\n")), 6, "text/plain"))

	rec := w.get(t, "/clusters/dev/runs/team-a/smoke-live/log?offset=0", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "hello\nworld\n", rec.Body.String())
	assert.Equal(t, "running", rec.Header().Get("X-Run-Phase"))
	assert.Equal(t, "12", rec.Header().Get("X-Next-Offset"))

	rec = w.get(t, "/clusters/dev/runs/team-a/smoke-live/log?offset=6", "")
	assert.Equal(t, "world\n", rec.Body.String())
	assert.Equal(t, "12", rec.Header().Get("X-Next-Offset"))

	dl := w.get(t, "/clusters/dev/runs/team-a/smoke-live/logs.txt", "")
	assert.Equal(t, "hello\nworld\n", dl.Body.String())
	assert.Contains(t, dl.Header().Get("Content-Disposition"), `attachment; filename="smoke-live.log"`)
}

func TestRunArtifact_SandboxedStream(t *testing.T) {
	w := newWorld(t, smokeTest(), runOf("smoke-abcde", testsv1alpha1.PhaseFailed))
	key := storage.ForRun("team-a", runUID).Artifact("report/index.html")
	require.NoError(t, w.objects.Put(context.Background(), bucket, key, strings.NewReader("<script>x</script>"), 18, "text/html"))

	rec := w.get(t, "/clusters/dev/runs/team-a/smoke-abcde/artifacts/report/index.html", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "<script>x</script>", rec.Body.String())
	csp := rec.Header().Get("Content-Security-Policy")
	assert.Equal(t, "sandbox allow-scripts", csp, "report scripts run, in an opaque origin")
	assert.NotContains(t, csp, "allow-same-origin", "workload HTML can't run as Control Center")
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, `inline; filename="index.html"`, rec.Header().Get("Content-Disposition"))
	rec = w.get(t, "/clusters/dev/runs/team-a/smoke-abcde/artifacts/report/index.html?download=1", "")
	assert.Equal(t, `attachment; filename="index.html"`, rec.Header().Get("Content-Disposition"))
	assert.Equal(t, http.StatusNotFound, w.get(t, "/clusters/dev/runs/team-a/smoke-abcde/artifacts/missing", "").Code)
}

func TestDeleteRun_AdminOnly(t *testing.T) {
	w := newWorld(t, smokeTest(), runOf("smoke-abcde", testsv1alpha1.PhaseFailed))
	page := "/clusters/dev/runs/team-a/smoke-abcde"
	assert.Equal(t, http.StatusForbidden, w.post(t, page+"/delete", developer, url.Values{}).Code)

	rec := w.post(t, page+"/delete", admin, url.Values{})
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/clusters/dev/tests/team-a/smoke?notice=Run+smoke-abcde+deleted.", rec.Header().Get("Location"))
	assert.Equal(t, http.StatusNotFound, w.get(t, page, "").Code)
}

func TestComment_SaveAndRemove(t *testing.T) {
	w := newWorld(t, smokeTest(), runOf("smoke-abcde", testsv1alpha1.PhaseFailed))
	page := "/clusters/dev/runs/team-a/smoke-abcde"
	// No run history store behind this API server: the API refuses, and
	// the page reports it instead of failing.
	rec := w.post(t, page+"/comment", developer, url.Values{"text": {"known flake"}})
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "Could+not+save+the+comment")
	rec = w.post(t, page+"/comment", developer, url.Values{"text": {"  "}})
	assert.Contains(t, rec.Header().Get("Location"), "Could+not+remove+the+comment")
	assert.Equal(t, http.StatusForbidden, w.post(t, page+"/comment", "", url.Values{"text": {"x"}}).Code)
}

func TestClusterUnreachable(t *testing.T) {
	w := newWorld(t)
	// Point the registry's only cluster at a closed port.
	reg, err := clusters.New(t.Context(), []config.Cluster{{
		Name: "dev", Server: "http://127.0.0.1:1", Auth: config.ClusterAuth{Type: config.AuthServiceAccount},
		APIServer: config.APIServerRef{Namespace: "x", Service: "y", Port: 1},
	}}, clusters.Options{InClusterConfig: func() (*rest.Config, error) { return &rest.Config{}, nil }})
	require.NoError(t, err)
	v, err := views.New()
	require.NoError(t, err)
	res, err := auth.NewResolver(&config.Config{Environment: config.EnvProduction}, "")
	require.NoError(t, err)
	w.h = (&Server{Clusters: reg, Auth: res, Views: v, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()

	rec := w.get(t, clusterURL, "")
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "Cannot reach the kubetest API of dev")
	assert.Equal(t, http.StatusBadGateway, w.get(t, "/clusters/dev/runs/a/b/log", "").Code)
	assert.Equal(t, http.StatusNotFound, w.get(t, "/clusters/nope/runs/a/b/log", "").Code)
}

func TestLogDownload_UnknownRunIs404(t *testing.T) {
	w := newWorld(t)
	assert.Equal(t, http.StatusNotFound, w.get(t, "/clusters/dev/runs/team-a/ghost/logs.txt", "").Code)
}

func TestGeneratePrefixAndRedirectGuard(t *testing.T) {
	assert.Equal(t, "smoke-", generatePrefix("smoke"))
	long := generatePrefix(strings.Repeat("a", 70))
	assert.Len(t, long, 51)
	rec := httptest.NewRecorder()
	redirect(rec, httptest.NewRequest(http.MethodPost, "/", nil), "//evil.example", "")
	assert.Equal(t, "/", rec.Header().Get("Location"))
}

func TestTestPage_Paging(t *testing.T) {
	w := newWorld(t, smokeTest())
	body := w.get(t, "/clusters/dev/tests/team-a/smoke?after=garbage", "")
	assert.Equal(t, http.StatusBadGateway, body.Code, "a bad cursor is the API's 400, shown as such")
	assert.Contains(t, body.Body.String(), "malformed cursor")
	assert.Contains(t, w.get(t, "/clusters/dev/tests/team-a/smoke", "").Body.String(), "No runs yet.")
}

func TestRunAndTestPages_ShowCommit(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	run := runOf("smoke-abcde", testsv1alpha1.PhasePassed)
	run.Status.ResolvedSpec = `{"content":{"git":{"uri":"https://bot:s3cret@github.com/acme/tests.git"}}}`
	run.Status.Content = &testsv1alpha1.ContentStatus{GitRevision: "main", GitCommit: sha}
	w := newWorld(t, smokeTest(), run)

	body := w.get(t, "/clusters/dev/runs/team-a/smoke-abcde", "").Body.String()
	assert.Contains(t, body, `href="https://github.com/acme/tests/commit/`+sha+`"`)
	assert.Contains(t, body, ">0123456</a>")
	assert.Contains(t, body, "· main")
	assert.NotContains(t, body, "s3cret")

	test := w.get(t, "/clusters/dev/tests/team-a/smoke", "").Body.String()
	assert.Contains(t, test, "<th>Commit</th>")
	assert.Contains(t, test, ">0123456</a>")

	plain := newWorld(t, smokeTest(), runOf("smoke-abcde", testsv1alpha1.PhasePassed))
	assert.NotContains(t, plain.get(t, "/clusters/dev/tests/team-a/smoke", "").Body.String(), "<th>Commit</th>",
		"no git source, no commit column")
}

func TestRunPage_OpenReport(t *testing.T) {
	run := runOf("smoke-abcde", testsv1alpha1.PhasePassed)
	run.Status.ResolvedSpec = `{"artifacts":{"report":"report/index.html"}}`
	body := newWorld(t, smokeTest(), run).get(t, "/clusters/dev/runs/team-a/smoke-abcde", "").Body.String()
	assert.Contains(t, body, `href="/clusters/dev/runs/team-a/smoke-abcde/artifacts/report/index.html" target="_blank" rel="noopener">Open report</a>`)

	plain := newWorld(t, smokeTest(), runOf("smoke-abcde", testsv1alpha1.PhasePassed))
	assert.NotContains(t, plain.get(t, "/clusters/dev/runs/team-a/smoke-abcde", "").Body.String(), "Open report")
}
