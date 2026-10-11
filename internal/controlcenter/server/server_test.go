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
	"encoding/json"
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

	uiapi "github.com/hinskii/kubetest-alt/internal/controlcenter/api"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/auth"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/clusters"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/config"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/webui"
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
	s := &Server{Clusters: reg, Auth: res, ProbeTimeout: time.Second,
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

func TestClustersAPI_ShowsEachClusterWithItsHealth(t *testing.T) {
	h := mkServer(t, map[string]string{
		"deploy-dev":  fakeCluster(t, http.StatusOK),
		"deploy-prod": fakeCluster(t, http.StatusServiceUnavailable),
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
	req.Header.Set(auth.HeaderEmail, "dev@example.com")
	rec := do(t, h, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var got []uiapi.Cluster
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	byName := map[string]uiapi.Cluster{}
	for _, c := range got {
		byName[c.Name] = c
	}
	assert.True(t, byName["deploy-dev"].Reachable)
	assert.False(t, byName["deploy-prod"].Reachable)
	assert.Contains(t, byName["deploy-prod"].Error, "kubetest API 503")
	assert.NotEmpty(t, rec.Header().Get("Content-Security-Policy"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))

	metrics := do(t, h, httptest.NewRequest(http.MethodGet, "/metrics", nil)).Body.String()
	assert.Contains(t, metrics, `controlcenter_cluster_up{cluster="deploy-dev"} 1`)
	assert.Contains(t, metrics, `controlcenter_cluster_up{cluster="deploy-prod"} 0`)
	assert.Contains(t, metrics, `controlcenter_http_requests_total{code="200",method="get"}`)
}

func TestMeAPI_IsTheSignedInUser(t *testing.T) {
	h := mkServer(t, map[string]string{"deploy-dev": fakeCluster(t, http.StatusOK)})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set(auth.HeaderEmail, "dev@example.com")
	rec := do(t, h, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var me uiapi.Me
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &me))
	assert.Equal(t, uiapi.Me{Email: "dev@example.com", Role: "developer",
		Can: uiapi.Capabilities{Run: true}}, me)
}

func TestHome_IsTheUI(t *testing.T) {
	h := mkServer(t, map[string]string{"deploy-dev": fakeCluster(t, http.StatusOK)})
	rec := do(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
	assert.Contains(t, rec.Body.String(), "<title>")
	assert.Equal(t, webui.CSP, rec.Header().Get("Content-Security-Policy"), "the UI's own policy wins")
}

func TestUnknownAddress_BrowserGetsTheUI(t *testing.T) {
	h := mkServer(t, map[string]string{"deploy-dev": fakeCluster(t, http.StatusOK)})
	req := httptest.NewRequest(http.MethodGet, "/no/such/page", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := do(t, h, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, webui.CSP, rec.Header().Get("Content-Security-Policy"), "the UI's page, not the Go error page")

	rec = do(t, h, httptest.NewRequest(http.MethodGet, "/no/such/page", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.NotEqual(t, webui.CSP, rec.Header().Get("Content-Security-Policy"), "non-browser: the plain 404")
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

func TestNotFoundAndTheme(t *testing.T) {
	h := mkServer(t, map[string]string{"deploy-dev": fakeCluster(t, http.StatusOK)})
	rec := do(t, h, httptest.NewRequest(http.MethodGet, "/nope", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "There is nothing at /nope.\n", rec.Body.String(), "plain text: the pages are the UI's")
	assert.Equal(t, http.StatusNotFound, do(t, h, httptest.NewRequest(http.MethodGet, "/static/app.css", nil)).Code,
		"no server-rendered assets any more")

	rec = do(t, h, httptest.NewRequest(http.MethodGet, "/theme.js", nil))
	assert.Contains(t, []int{http.StatusOK, http.StatusNoContent}, rec.Code, "the UI's pre-paint script (204 unbuilt)")
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
