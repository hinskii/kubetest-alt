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

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const good = `
rbac:
  admins: [" Boss@Example.com "]
  developers: [dev@example.com]
clusters:
  - name: deploy-dev
    displayName: Deploy (dev)
    auth: {type: gcp}
    server: https://10.0.0.1
    caFile: /etc/cc/dev-ca.crt
  - name: local
    auth: {type: serviceaccount}
    apiServer: {namespace: tests, service: kt-apiserver, port: 9090}
`

func TestParse_DefaultsAndNormalizes(t *testing.T) {
	c, err := Parse([]byte(good))
	require.NoError(t, err)
	assert.Equal(t, EnvProduction, c.Environment, "production unless stated")
	assert.Equal(t, []string{"boss@example.com"}, c.RBAC.Admins)
	require.Len(t, c.Clusters, 2)
	assert.Equal(t, APIServerRef{Namespace: DefaultAPIServerNamespace, Service: DefaultAPIServerService, Port: DefaultAPIServerPort},
		c.Clusters[0].APIServer)
	assert.Equal(t, APIServerRef{Namespace: "tests", Service: "kt-apiserver", Port: 9090}, c.Clusters[1].APIServer)
	assert.Equal(t, "Deploy (dev)", c.Clusters[0].DisplayNameOrName())
	assert.Equal(t, "local", c.Clusters[1].DisplayNameOrName())
}

func TestLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cc.yaml")
	require.NoError(t, os.WriteFile(p, []byte(good), 0o600))
	_, err := Load(p)
	require.NoError(t, err)
	_, err = Load(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
}

func TestParse_Rejects(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"unknown field":   {`clusters: [{name: a, auth: {type: serviceaccount}, sever: x}]`, "unknown field"},
		"no clusters":     {`environment: development`, "at least one cluster"},
		"bad env":         {`{environment: staging, clusters: [{name: a, auth: {type: serviceaccount}}]}`, "staging"},
		"bad name":        {`clusters: [{name: Deploy_Dev, auth: {type: serviceaccount}}]`, "DNS label"},
		"duplicate":       {`clusters: [{name: a, auth: {type: serviceaccount}}, {name: a, auth: {type: serviceaccount}}]`, "duplicate"},
		"gcp needs CA":    {`clusters: [{name: a, auth: {type: gcp}, server: "https://x"}]`, "server and caFile"},
		"bad auth":        {`clusters: [{name: a, auth: {type: token}}]`, `auth.type "token"`},
		"kubeconfig prod": {`clusters: [{name: a, auth: {type: kubeconfig}}]`, "development only"},
		"plain http":      {`clusters: [{name: a, auth: {type: serviceaccount}, server: "http://x"}]`, "https://"},
		"stray context":   {`clusters: [{name: a, auth: {type: serviceaccount, context: x}}]`, "only apply"},
		"bad port":        {`clusters: [{name: a, auth: {type: serviceaccount}, apiServer: {port: 70000}}]`, "out of range"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestParse_KubeconfigInDevelopment(t *testing.T) {
	_, err := Parse([]byte(`{environment: development, clusters: [{name: kind, auth: {type: kubeconfig, context: kind-kubetest}}]}`))
	require.NoError(t, err)
}
