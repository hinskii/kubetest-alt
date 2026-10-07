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
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"

	"github.com/hinskii/kubetest-alt/internal/controlcenter/auth"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/clusters"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/config"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/views"
)

// fakeCluster is a "Kubernetes API" whose service proxy answers /healthz
// with status.
func fakeCluster(t *testing.T, status int) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/proxy/healthz") {
			w.WriteHeader(status)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

func mkServer(t *testing.T, hosts map[string]string) http.Handler {
	t.Helper()
	cfg := &config.Config{
		Environment: config.EnvProduction,
		RBAC:        config.RBAC{Developers: []string{"dev@example.com"}},
	}
	for _, name := range []string{"deploy-dev", "deploy-prod"} {
		if _, ok := hosts[name]; !ok {
			continue
		}
		cfg.Clusters = append(cfg.Clusters, config.Cluster{
			Name: name, Auth: config.ClusterAuth{Type: config.AuthServiceAccount},
			APIServer: config.APIServerRef{Namespace: "kubetest-alt", Service: "kubetest-alt-apiserver", Port: 8080},
		})
	}
	// Server overrides the in-cluster host: point each cluster at its fake.
	reg, err := clusters.New(t.Context(), withServers(cfg.Clusters, hosts), clusters.Options{
		InClusterConfig: func() (*rest.Config, error) { return &rest.Config{}, nil },
	})
	require.NoError(t, err)
	res, err := auth.NewResolver(cfg, "")
	require.NoError(t, err)
	v, err := views.New()
	require.NoError(t, err)
	s := &Server{Clusters: reg, Auth: res, Views: v, ProbeTimeout: time.Second,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return s.Handler()
}

func withServers(cs []config.Cluster, hosts map[string]string) []config.Cluster {
	out := make([]config.Cluster, len(cs))
	for i, c := range cs {
		c.Server = hosts[c.Name]
		out[i] = c
	}
	return out
}

func do(t *testing.T, h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHome_ShowsEachClusterWithItsHealth(t *testing.T) {
	h := mkServer(t, map[string]string{
		"deploy-dev":  fakeCluster(t, http.StatusOK),
		"deploy-prod": fakeCluster(t, http.StatusServiceUnavailable),
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(auth.HeaderEmail, "dev@example.com")
	rec := do(t, h, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `<html lang="en">`)
	assert.Contains(t, body, `href="/clusters/deploy-dev"`)
	assert.Contains(t, body, "connected")
	assert.Contains(t, body, "unreachable")
	assert.Contains(t, body, "kubetest API 503")
	assert.Contains(t, body, "dev@example.com")
	assert.Contains(t, body, ">developer<")
	assert.NotEmpty(t, rec.Header().Get("Content-Security-Policy"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))

	metrics := do(t, h, httptest.NewRequest(http.MethodGet, "/metrics", nil)).Body.String()
	assert.Contains(t, metrics, `controlcenter_cluster_up{cluster="deploy-dev"} 1`)
	assert.Contains(t, metrics, `controlcenter_cluster_up{cluster="deploy-prod"} 0`)
	assert.Contains(t, metrics, `controlcenter_http_requests_total{code="200",method="get"}`)
}

func TestHome_EscapesUserInput(t *testing.T) {
	h := mkServer(t, map[string]string{"deploy-dev": fakeCluster(t, http.StatusOK)})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(auth.HeaderEmail, "<script>x</script>@example.com")
	body := do(t, h, req).Body.String()
	assert.NotContains(t, body, "<script>x")
	assert.Contains(t, body, "&lt;script&gt;")
}

func TestProbesAreLocal(t *testing.T) {
	// The only cluster is down; the pod must stay ready.
	h := mkServer(t, map[string]string{"deploy-dev": fakeCluster(t, http.StatusBadGateway)})
	for _, p := range []string{"/healthz", "/readyz"} {
		assert.Equal(t, http.StatusOK, do(t, h, httptest.NewRequest(http.MethodGet, p, nil)).Code, p)
	}
}

func TestCrossSitePostIsRejected(t *testing.T) {
	h := mkServer(t, map[string]string{"deploy-dev": fakeCluster(t, http.StatusOK)})
	req := httptest.NewRequest(http.MethodPost, "/anything", strings.NewReader("x=1"))
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	assert.Equal(t, http.StatusForbidden, do(t, h, req).Code)

	req = httptest.NewRequest(http.MethodPost, "/anything", strings.NewReader("x=1"))
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	assert.Equal(t, http.StatusNotFound, do(t, h, req).Code, "same-origin passes the CSRF check")
}

func TestNotFoundAndStatic(t *testing.T) {
	h := mkServer(t, map[string]string{"deploy-dev": fakeCluster(t, http.StatusOK)})
	rec := do(t, h, httptest.NewRequest(http.MethodGet, "/nope", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "There is nothing at /nope.")

	rec = do(t, h, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "--primary")
}

func TestOpsHandler_ProbesAndMetricsOnly(t *testing.T) {
	res, err := auth.NewResolver(&config.Config{Environment: config.EnvProduction}, "")
	require.NoError(t, err)
	s := &Server{Auth: res}
	ui := s.Handler()
	ops := s.OpsHandler()
	for _, p := range []string{"/healthz", "/readyz", "/metrics"} {
		rec := httptest.NewRecorder()
		ops.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		assert.Equal(t, http.StatusOK, rec.Code, p)
	}
	rec := httptest.NewRecorder()
	ui.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	rec = httptest.NewRecorder()
	ops.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code, "no UI on the ops listener")
	rec = httptest.NewRecorder()
	ops.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	assert.Contains(t, rec.Body.String(), "controlcenter_http_requests_total", "the UI's request counter, shared registry")
}
