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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/auth"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/cctest"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/clusters"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/config"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/views"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

const (
	bucket     = cctest.Bucket
	proxyPath  = cctest.ProxyPath
	developer  = cctest.Developer
	admin      = cctest.Admin
	runUID     = cctest.RunUID
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
	o := cctest.Options{Cases: cctest.Cases{}}
	if archive != nil {
		o.Store, o.Deleter = archive, archive
	}
	env := cctest.New(t, o, objs...)
	res, err := auth.NewResolver(&config.Config{Environment: config.EnvProduction, RBAC: env.RBAC}, "")
	require.NoError(t, err)
	v, err := views.New()
	require.NoError(t, err)
	s := &Server{Clusters: env.Clusters, Auth: res, Views: v, ProbeTimeout: time.Second,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return &world{h: s.Handler(), k8s: env.K8s, objects: env.Objects}
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

func TestRunLogDownload(t *testing.T) {
	w := newWorld(t, smokeTest(), runOf("smoke-live", testsv1alpha1.PhaseRunning))
	keys := storage.ForRun("team-a", runUID)
	require.NoError(t, w.objects.Put(context.Background(), bucket, keys.LogChunk(0), bytes.NewReader([]byte("hello\n")), 6, "text/plain"))
	require.NoError(t, w.objects.Put(context.Background(), bucket, keys.LogChunk(1), bytes.NewReader([]byte("world\n")), 6, "text/plain"))

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
	assert.Equal(t, storageShim+"<script>x</script>", rec.Body.String(), "shown HTML gets the storage shim")
	csp := rec.Header().Get("Content-Security-Policy")
	assert.Equal(t, "sandbox allow-scripts; frame-ancestors 'self'", csp, "report scripts run, in an opaque origin, framed only by Control Center")
	assert.NotContains(t, csp, "allow-same-origin", "workload HTML can't run as Control Center")
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, `inline; filename="index.html"`, rec.Header().Get("Content-Disposition"))
	rec = w.get(t, "/clusters/dev/runs/team-a/smoke-abcde/artifacts/report/index.html?download=1", "")
	assert.Equal(t, `attachment; filename="index.html"`, rec.Header().Get("Content-Disposition"))
	assert.Equal(t, "<script>x</script>", rec.Body.String(), "a download is the file as stored")
	assert.Equal(t, http.StatusNotFound, w.get(t, "/clusters/dev/runs/team-a/smoke-abcde/artifacts/missing", "").Code)
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

	rec := w.get(t, clusterURL+"/schedules", "")
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "Cannot reach the kubetest API of dev")
	assert.Equal(t, http.StatusBadGateway, w.get(t, "/clusters/dev/runs/a/b/logs.txt", "").Code)
	assert.Equal(t, http.StatusNotFound, w.get(t, "/clusters/nope/runs/a/b/logs.txt", "").Code)
}

func TestLogDownload_UnknownRunIs404(t *testing.T) {
	w := newWorld(t)
	assert.Equal(t, http.StatusNotFound, w.get(t, "/clusters/dev/runs/team-a/ghost/logs.txt", "").Code)
}

func TestRedirectGuard(t *testing.T) {
	rec := httptest.NewRecorder()
	redirect(rec, httptest.NewRequest(http.MethodPost, "/", nil), "//evil.example", "")
	assert.Equal(t, "/", rec.Header().Get("Location"))
}

func TestArtifact_VideoWithoutTypePlaysInline(t *testing.T) {
	run := runOf("smoke-abcde", testsv1alpha1.PhaseFailed)
	run.Status.ArtifactRefs = []testsv1alpha1.ArtifactRef{{Path: "cypress/videos/pay.mp4"}}
	w := newWorld(t, smokeTest(), run)
	key := storage.ForRun("team-a", runUID).Artifact("cypress/videos/pay.mp4")
	require.NoError(t, w.objects.Put(context.Background(), bucket, key, strings.NewReader("mp4"), 3, "application/octet-stream"))
	rec := w.get(t, "/clusters/dev/runs/team-a/smoke-abcde/artifacts/cypress/videos/pay.mp4", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "video/mp4", rec.Header().Get("Content-Type"))
}
