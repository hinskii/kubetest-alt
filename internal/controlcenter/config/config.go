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

// Package config is the Control Center configuration file: clusters, RBAC
// lists and environment. Secrets (database DSN) come from the environment,
// never from this file.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"
)

// Environments.
const (
	EnvDevelopment = "development"
	EnvProduction  = "production"
)

// Cluster auth providers.
const (
	// AuthGCP: Google credentials (Workload Identity on GKE, ADC locally)
	// as a bearer token — for GKE clusters.
	AuthGCP = "gcp"
	// AuthServiceAccount: the pod's own service account — Control Center
	// runs in the target cluster.
	AuthServiceAccount = "serviceaccount"
	// AuthKubeconfig: a kubeconfig file/context — local development.
	AuthKubeconfig = "kubeconfig"
)

// Config is the whole file.
type Config struct {
	// Environment is development|production. production refuses the
	// local role override.
	Environment string `json:"environment"`
	RBAC        RBAC   `json:"rbac"`
	// Clusters are the kubetest installations Control Center manages.
	Clusters []Cluster `json:"clusters"`
}

// RBAC maps oauth2-proxy emails to roles. Anyone else is a viewer.
type RBAC struct {
	Admins     []string `json:"admins,omitempty"`
	Developers []string `json:"developers,omitempty"`
}

// Cluster is one kubetest installation.
type Cluster struct {
	// Name is the URL-safe identifier (/clusters/<name>/…).
	Name string `json:"name"`
	// DisplayName defaults to Name.
	DisplayName string `json:"displayName,omitempty"`
	// Auth is how Control Center authenticates to the cluster's K8s API.
	Auth ClusterAuth `json:"auth"`
	// Server is the K8s API URL. Required for gcp; serviceaccount and
	// kubeconfig take it from their source unless set here.
	Server string `json:"server,omitempty"`
	// CAFile is the K8s API CA bundle (PEM). Required for gcp. TLS is
	// always verified.
	CAFile string `json:"caFile,omitempty"`
	// APIServer locates the kubetest API server Service, reached through
	// the K8s API service proxy.
	APIServer APIServerRef `json:"apiServer,omitzero"`
}

// ClusterAuth selects the auth provider.
type ClusterAuth struct {
	Type string `json:"type"`
	// Kubeconfig path (kubeconfig only; empty = default loading rules).
	Kubeconfig string `json:"kubeconfig,omitempty"`
	// Context in the kubeconfig (kubeconfig only; empty = current).
	Context string `json:"context,omitempty"`
}

// APIServerRef is the kubetest API server Service.
type APIServerRef struct {
	Namespace string `json:"namespace,omitempty"` // default kubetest-alt
	Service   string `json:"service,omitempty"`   // default kubetest-alt-apiserver
	Port      int    `json:"port,omitempty"`      // default 8080
	// TokenFile holds the API server's token (its --auth-token-file):
	// mounted from that cluster's Secret. Empty only for an API server
	// running without authentication (development).
	TokenFile string `json:"tokenFile,omitempty"`
}

// Defaults for APIServerRef — what the Helm chart installs.
const (
	DefaultAPIServerNamespace = "kubetest-alt"
	DefaultAPIServerService   = "kubetest-alt-apiserver"
	DefaultAPIServerPort      = 8080
)

// DisplayNameOrName returns the label shown in the UI.
func (c Cluster) DisplayNameOrName() string {
	if c.DisplayName != "" {
		return c.DisplayName
	}
	return c.Name
}

// Load reads, defaults and validates a config file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path is the operator-supplied --config flag
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return Parse(b)
}

// Parse defaults and validates config bytes (YAML or JSON). Unknown fields
// are errors — a typo must not silently disable a cluster setting.
func Parse(b []byte) (*Config, error) {
	var c Config
	if err := yaml.UnmarshalStrict(b, &c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	c.setDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) setDefaults() {
	if c.Environment == "" {
		c.Environment = EnvProduction
	}
	for i := range c.Clusters {
		a := &c.Clusters[i].APIServer
		if a.Namespace == "" {
			a.Namespace = DefaultAPIServerNamespace
		}
		if a.Service == "" {
			a.Service = DefaultAPIServerService
		}
		if a.Port == 0 {
			a.Port = DefaultAPIServerPort
		}
	}
	c.RBAC.Admins = normalizeEmails(c.RBAC.Admins)
	c.RBAC.Developers = normalizeEmails(c.RBAC.Developers)
}

var clusterNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// Validate reports every problem at once.
func (c *Config) Validate() error {
	var errs []error
	if c.Environment != EnvDevelopment && c.Environment != EnvProduction {
		errs = append(errs, fmt.Errorf("environment %q: must be %s or %s", c.Environment, EnvDevelopment, EnvProduction))
	}
	if len(c.Clusters) == 0 {
		errs = append(errs, errors.New("clusters: at least one cluster is required"))
	}
	seen := map[string]bool{}
	for i, cl := range c.Clusters {
		at := fmt.Sprintf("clusters[%d] (%s)", i, cl.Name)
		if !clusterNameRE.MatchString(cl.Name) {
			errs = append(errs, fmt.Errorf("%s: name must be a DNS label", at))
		}
		if seen[cl.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate name", at))
		}
		seen[cl.Name] = true
		switch cl.Auth.Type {
		case AuthGCP:
			if cl.Server == "" || cl.CAFile == "" {
				errs = append(errs, fmt.Errorf("%s: auth gcp needs server and caFile", at))
			}
		case AuthServiceAccount:
		case AuthKubeconfig:
			if c.Environment == EnvProduction {
				errs = append(errs, fmt.Errorf("%s: auth kubeconfig is for development only", at))
			}
		default:
			errs = append(errs, fmt.Errorf("%s: auth.type %q: must be %s, %s or %s",
				at, cl.Auth.Type, AuthGCP, AuthServiceAccount, AuthKubeconfig))
		}
		if cl.Auth.Type != AuthKubeconfig && (cl.Auth.Kubeconfig != "" || cl.Auth.Context != "") {
			errs = append(errs, fmt.Errorf("%s: auth.kubeconfig/context only apply to auth kubeconfig", at))
		}
		if cl.Server != "" && !strings.HasPrefix(cl.Server, "https://") {
			errs = append(errs, fmt.Errorf("%s: server must be https://", at))
		}
		if p := cl.APIServer.Port; p < 1 || p > 65535 {
			errs = append(errs, fmt.Errorf("%s: apiServer.port %d out of range", at, p))
		}
	}
	return errors.Join(errs...)
}

func normalizeEmails(in []string) []string {
	out := make([]string, 0, len(in))
	for _, e := range in {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			out = append(out, e)
		}
	}
	return out
}
