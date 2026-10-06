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

// Package clusters turns the configured clusters into authenticated
// clients. Every cluster is reached through its Kubernetes API: the
// kubetest API server via the service proxy (so Kubernetes RBAC on
// services/proxy is the access control), pods directly.
package clusters

import (
	"context"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/hinskii/kubetest-alt/internal/controlcenter/config"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// gcpScope is what GKE accepts for Kubernetes API access.
const gcpScope = "https://www.googleapis.com/auth/cloud-platform"

// Cluster is one configured cluster with its clients.
type Cluster struct {
	config.Cluster
	// REST is the Kubernetes API config (for pod-level calls).
	REST *rest.Config
	// API is the kubetest API server, through the service proxy.
	API *apiclient.Client
}

// Registry holds the clusters in config order.
type Registry struct {
	list   []*Cluster
	byName map[string]*Cluster
}

// Options are test hooks.
type Options struct {
	// GCPTokenSource overrides Google application-default credentials.
	GCPTokenSource oauth2.TokenSource
	// InClusterConfig overrides rest.InClusterConfig.
	InClusterConfig func() (*rest.Config, error)
}

// New builds clients for every cluster. Credentials are resolved now, so
// a misconfigured cluster fails startup instead of the first page view.
// Tokens are fetched lazily and cached until expiry.
func New(ctx context.Context, clusters []config.Cluster, o Options) (*Registry, error) {
	if o.InClusterConfig == nil {
		o.InClusterConfig = rest.InClusterConfig
	}
	r := &Registry{byName: map[string]*Cluster{}}
	for _, c := range clusters {
		rc, err := restConfig(ctx, c, &o)
		if err != nil {
			return nil, fmt.Errorf("cluster %s: %w", c.Name, err)
		}
		hc, err := rest.HTTPClientFor(rc)
		if err != nil {
			return nil, fmt.Errorf("cluster %s: http client: %w", c.Name, err)
		}
		api, err := apiclient.New(
			apiclient.ServiceProxyURL(rc.Host, c.APIServer.Namespace, c.APIServer.Service, c.APIServer.Port), hc)
		if err != nil {
			return nil, fmt.Errorf("cluster %s: %w", c.Name, err)
		}
		cl := &Cluster{Cluster: c, REST: rc, API: api}
		r.list = append(r.list, cl)
		r.byName[c.Name] = cl
	}
	return r, nil
}

// List returns the clusters in config order.
func (r *Registry) List() []*Cluster { return r.list }

// Get returns a cluster by name, or nil.
func (r *Registry) Get(name string) *Cluster { return r.byName[name] }

func restConfig(ctx context.Context, c config.Cluster, o *Options) (*rest.Config, error) {
	var rc *rest.Config
	switch c.Auth.Type {
	case config.AuthGCP:
		ts := o.GCPTokenSource
		if ts == nil {
			var err error
			// DefaultTokenSource caches and refreshes the token itself.
			if ts, err = google.DefaultTokenSource(ctx, gcpScope); err != nil {
				return nil, fmt.Errorf("google credentials: %w", err)
			}
		}
		rc = &rest.Config{
			Host:            c.Server,
			TLSClientConfig: rest.TLSClientConfig{CAFile: c.CAFile},
			WrapTransport: func(rt http.RoundTripper) http.RoundTripper {
				return &oauth2.Transport{Source: ts, Base: rt}
			},
		}
	case config.AuthServiceAccount:
		var err error
		if rc, err = o.InClusterConfig(); err != nil {
			return nil, fmt.Errorf("in-cluster config: %w", err)
		}
	case config.AuthKubeconfig:
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		rules.ExplicitPath = c.Auth.Kubeconfig
		var err error
		rc, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules,
			&clientcmd.ConfigOverrides{CurrentContext: c.Auth.Context}).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("kubeconfig: %w", err)
		}
	default:
		return nil, fmt.Errorf("unknown auth type %q", c.Auth.Type)
	}
	if c.Server != "" {
		rc.Host = c.Server
	}
	if c.CAFile != "" {
		rc.CAFile, rc.CAData = c.CAFile, nil
	}
	if rc.Insecure {
		return nil, fmt.Errorf("TLS verification is disabled in the cluster credentials; refusing")
	}
	rc.UserAgent = "kubetest-control-center"
	return rc, nil
}
