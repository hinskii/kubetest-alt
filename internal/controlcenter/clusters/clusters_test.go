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

package clusters

import (
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"k8s.io/client-go/rest"

	"github.com/hinskii/kubetest-alt/internal/controlcenter/config"
)

// fakeK8s is a TLS "Kubernetes API" that records the last request.
type fakeK8s struct {
	*httptest.Server
	mu       sync.Mutex
	path     string
	authz    string
	caFile   string
	caPEMb64 string
}

func newFakeK8s(t *testing.T) *fakeK8s {
	t.Helper()
	f := &fakeK8s{}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.path, f.authz = r.URL.Path, r.Header.Get("Authorization")
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(f.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.Certificate().Raw})
	f.caFile = filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(f.caFile, ca, 0o600))
	f.caPEMb64 = base64.StdEncoding.EncodeToString(ca)
	return f
}

func (f *fakeK8s) last() (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.path, f.authz
}

const wantProxy = "/api/v1/namespaces/kubetest-alt/services/http:kubetest-alt-apiserver:8080/proxy/healthz"

func apiRef() config.APIServerRef {
	return config.APIServerRef{
		Namespace: config.DefaultAPIServerNamespace, Service: config.DefaultAPIServerService, Port: config.DefaultAPIServerPort,
	}
}

func TestGCP_BearerTokenThroughServiceProxy(t *testing.T) {
	k8s := newFakeK8s(t)
	reg, err := New(t.Context(), []config.Cluster{{
		Name: "dev", Auth: config.ClusterAuth{Type: config.AuthGCP},
		Server: k8s.URL, CAFile: k8s.caFile, APIServer: apiRef(),
	}}, Options{GCPTokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "gcp-token"})})
	require.NoError(t, err)

	require.NoError(t, reg.Get("dev").API.Healthz(t.Context()), "CA from caFile must verify the server")
	path, authz := k8s.last()
	assert.Equal(t, wantProxy, path)
	assert.Equal(t, "Bearer gcp-token", authz)
}

func TestKubeconfig(t *testing.T) {
	k8s := newFakeK8s(t)
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(kc, fmt.Appendf(nil, `apiVersion: v1
kind: Config
clusters:
- name: kind
  cluster: {server: %q, certificate-authority-data: %s}
users:
- name: me
  user: {token: kc-token}
contexts:
- name: other
  context: {cluster: kind, user: nobody}
- name: kind-kubetest
  context: {cluster: kind, user: me}
current-context: other
`, k8s.URL, k8s.caPEMb64), 0o600))

	reg, err := New(t.Context(), []config.Cluster{{
		Name: "kind", Auth: config.ClusterAuth{Type: config.AuthKubeconfig, Kubeconfig: kc, Context: "kind-kubetest"},
		APIServer: apiRef(),
	}}, Options{})
	require.NoError(t, err)
	require.NoError(t, reg.Get("kind").API.Healthz(t.Context()))
	_, authz := k8s.last()
	assert.Equal(t, "Bearer kc-token", authz, "the configured context, not current-context")
}

func TestServiceAccount_ServerOverride(t *testing.T) {
	k8s := newFakeK8s(t)
	reg, err := New(t.Context(), []config.Cluster{{
		Name: "self", Auth: config.ClusterAuth{Type: config.AuthServiceAccount}, APIServer: apiRef(),
	}}, Options{InClusterConfig: func() (*rest.Config, error) {
		return &rest.Config{Host: k8s.URL, BearerToken: "sa-token",
			TLSClientConfig: rest.TLSClientConfig{CAFile: k8s.caFile}}, nil
	}})
	require.NoError(t, err)
	require.NoError(t, reg.Get("self").API.Healthz(t.Context()))
	_, authz := k8s.last()
	assert.Equal(t, "Bearer sa-token", authz)
	assert.Equal(t, []*Cluster{reg.Get("self")}, reg.List())
	assert.Nil(t, reg.Get("nope"))
}

func TestRefusesInsecureCredentials(t *testing.T) {
	_, err := New(t.Context(), []config.Cluster{{
		Name: "x", Auth: config.ClusterAuth{Type: config.AuthServiceAccount}, APIServer: apiRef(),
	}}, Options{InClusterConfig: func() (*rest.Config, error) {
		return &rest.Config{Host: "https://x", TLSClientConfig: rest.TLSClientConfig{Insecure: true}}, nil
	}})
	require.ErrorContains(t, err, "TLS verification is disabled")
}

func TestServiceAccount_OutsideClusterFails(t *testing.T) {
	_, err := New(t.Context(), []config.Cluster{{
		Name: "x", Auth: config.ClusterAuth{Type: config.AuthServiceAccount}, APIServer: apiRef(),
	}}, Options{InClusterConfig: func() (*rest.Config, error) { return nil, rest.ErrNotInCluster }})
	require.ErrorContains(t, err, "cluster x: in-cluster config")
}
