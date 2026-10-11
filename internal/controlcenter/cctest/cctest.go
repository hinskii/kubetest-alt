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

// Package cctest gives Control Center's tests a real kubetest API server
// (fake Kubernetes client, fake object storage) behind a Kubernetes
// service-proxy path, and a cluster registry pointing at it: pages and
// the UI's API are tested against the real API, not a mock of it.
package cctest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/apiserver"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/clusters"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/config"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

const (
	// Cluster is the registry's one cluster.
	Cluster = "dev"
	// Bucket is the object storage bucket the API server reads.
	Bucket = "kubetest"
	// ProxyPath is where the Kubernetes service proxy serves the API.
	ProxyPath = "/api/v1/namespaces/kubetest-alt/services/http:kubetest-alt-apiserver:8080/proxy"
	// Developer and Admin are the emails RBAC gives those roles; anyone
	// else is a viewer.
	Developer = "dev@example.com"
	Admin     = "boss@example.com"
)

// Options add to the API server; zero values leave a feature off.
type Options struct {
	Store   apiserver.RunReader
	Deleter apiserver.RunDeleter
	Cases   apiserver.CaseReader
}

// Env is the kubetest API server and Control Center's view of it.
type Env struct {
	Clusters *clusters.Registry
	K8s      client.Client
	Objects  *storage.Fake
	// RBAC is the config whose roles Developer and Admin have.
	RBAC config.RBAC
}

// New starts the API server over objs.
func New(t *testing.T, o Options, objs ...client.Object) *Env {
	t.Helper()
	sch := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(sch))
	require.NoError(t, testsv1alpha1.AddToScheme(sch))
	k8s := fake.NewClientBuilder().WithScheme(sch).WithObjects(objs...).Build()
	objects := storage.NewFake()
	api := &apiserver.Server{K8sClient: k8s, Bucket: Bucket,
		Downloader: objects, Lister: objects, Presigner: objects, Remover: objects,
		Store: o.Store, Deleter: o.Deleter, Cases: o.Cases}

	mux := http.NewServeMux()
	mux.Handle(ProxyPath+"/", http.StripPrefix(ProxyPath, api.Handler()))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	reg, err := clusters.New(t.Context(), []config.Cluster{{
		Name: Cluster, DisplayName: "Deploy (dev)", Server: ts.URL,
		Auth:      config.ClusterAuth{Type: config.AuthServiceAccount},
		APIServer: config.APIServerRef{Namespace: "kubetest-alt", Service: "kubetest-alt-apiserver", Port: 8080},
	}}, clusters.Options{InClusterConfig: func() (*rest.Config, error) { return &rest.Config{}, nil }})
	require.NoError(t, err)
	return &Env{Clusters: reg, K8s: k8s, Objects: objects,
		RBAC: config.RBAC{Admins: []string{Admin}, Developers: []string{Developer}}}
}
