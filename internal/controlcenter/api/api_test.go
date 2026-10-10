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

package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"

	"github.com/hinskii/kubetest-alt/internal/controlcenter/auth"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/clusters"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/config"
)

// fakeCluster is a Kubernetes API whose service proxy answers /healthz
// with status, after delay.
func fakeCluster(t *testing.T, status int, delay time.Duration) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/proxy/healthz") {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
			w.WriteHeader(status)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

type probe struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (p *probe) record(cluster string, up bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen[cluster] = up
}

// mkAPI serves the API for clusters (name → server URL), in that order.
func mkAPI(t *testing.T, names []string, hosts map[string]string) (http.Handler, *probe) {
	t.Helper()
	cs := make([]config.Cluster, 0, len(names))
	for _, n := range names {
		cs = append(cs, config.Cluster{
			Name: n, DisplayName: strings.ToUpper(n), Server: hosts[n],
			Auth:      config.ClusterAuth{Type: config.AuthServiceAccount},
			APIServer: config.APIServerRef{Namespace: "kubetest-alt", Service: "kubetest-alt-apiserver", Port: 8080},
		})
	}
	reg, err := clusters.New(t.Context(), cs, clusters.Options{
		InClusterConfig: func() (*rest.Config, error) { return &rest.Config{}, nil },
	})
	require.NoError(t, err)
	p := &probe{seen: map[string]bool{}}
	s := &Server{Clusters: reg, ProbeTimeout: 200 * time.Millisecond, OnProbe: p.record,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mux := http.NewServeMux()
	s.Register(mux)
	return mux, p
}

func get(t *testing.T, h http.Handler, path string, u *auth.User) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if u != nil {
		req = req.WithContext(auth.WithUser(req.Context(), *u))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestClusters_ProbesEachInConfigOrder(t *testing.T) {
	h, p := mkAPI(t, []string{"prod", "dev", "slow"}, map[string]string{
		"prod": fakeCluster(t, http.StatusServiceUnavailable, 0),
		"dev":  fakeCluster(t, http.StatusOK, 0),
		"slow": fakeCluster(t, http.StatusOK, 5*time.Second), // past ProbeTimeout
	})
	rec := get(t, h, "/api/v1/clusters", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	var got []Cluster
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 3)
	assert.Equal(t, []string{"prod", "dev", "slow"}, []string{got[0].Name, got[1].Name, got[2].Name})
	assert.Equal(t, "PROD", got[0].DisplayName)
	assert.False(t, got[0].Reachable)
	assert.Contains(t, got[0].Error, "503")
	assert.Equal(t, Cluster{Name: "dev", DisplayName: "DEV", Reachable: true}, got[1])
	assert.False(t, got[2].Reachable, "a probe past the timeout is unreachable")
	assert.NotEmpty(t, got[2].Error)
	assert.Equal(t, map[string]bool{"prod": false, "dev": true, "slow": false}, p.seen)
}

func TestClusters_NoneConfigured_IsAnEmptyList(t *testing.T) {
	h, _ := mkAPI(t, nil, nil)
	rec := get(t, h, "/api/v1/clusters", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, "[]", rec.Body.String())
}

func TestMe_RoleAndWhatItAllows(t *testing.T) {
	h, _ := mkAPI(t, nil, nil)
	for _, tc := range []struct {
		role auth.Role
		want Capabilities
	}{
		{auth.RoleViewer, Capabilities{}},
		{auth.RoleDeveloper, Capabilities{Run: true}},
		{auth.RoleAdmin, Capabilities{Run: true, Delete: true}},
	} {
		u := auth.User{Email: "a@example.com", Role: tc.role}
		rec := get(t, h, "/api/v1/me", &u)
		require.Equal(t, http.StatusOK, rec.Code)
		var me Me
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &me))
		assert.Equal(t, Me{Email: "a@example.com", Role: tc.role.String(), Can: tc.want}, me)
	}
}

func TestUnknownEndpoint_IsAJSON404(t *testing.T) {
	h, _ := mkAPI(t, nil, nil)
	for _, path := range []string{"/api/v1/nope", "/api/v1/"} {
		rec := get(t, h, path, nil)
		assert.Equal(t, http.StatusNotFound, rec.Code, path)
		var e Error
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &e), path)
		assert.Contains(t, e.Error, "no such API endpoint", path)
	}
}

func TestOpenAPI_DescribesEveryRoute(t *testing.T) {
	b, err := OpenAPIJSON()
	require.NoError(t, err)
	var spec struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(b, &spec))
	assert.Contains(t, spec.Paths, "/api/v1/me")
	assert.Contains(t, spec.Paths, "/api/v1/clusters")
	assert.Len(t, spec.Paths, 2, "a route without a spec entry has no typed client")
}
