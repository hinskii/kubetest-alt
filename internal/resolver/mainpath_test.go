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

package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

func TestMainPathParam(t *testing.T) {
	params := map[string]testsv1alpha1.Parameter{"script": {Type: "string"}, "projectDir": {Type: "string"}, "vus": {Type: "integer"}}
	cases := []struct {
		name string
		c    testsv1alpha1.ContainerConfig
		want string
	}{
		{"argument", testsv1alpha1.ContainerConfig{Args: []string{"run", "--out", "/data/repo/results/x.json", "/data/repo/{{ config.script }}"}}, "script"},
		{"inside a shell line", testsv1alpha1.ContainerConfig{Command: []string{"sh", "-c"},
			Args: []string{"cd /data/repo/{{config.projectDir}} && npm test --vus {{ config.vus }}"}}, "projectDir"},
		{"working directory", testsv1alpha1.ContainerConfig{WorkingDir: "/data/repo/{{ config.projectDir }}"}, "projectDir"},
		{"not below /data/repo", testsv1alpha1.ContainerConfig{Args: []string{"{{ config.script }}", "/data/{{ config.script }}"}}, ""},
		{"not a parameter", testsv1alpha1.ContainerConfig{Args: []string{"/data/repo/{{ config.other }}"}}, ""},
		{"none", testsv1alpha1.ContainerConfig{Args: []string{"zap-baseline.py", "-t", "{{ config.target }}"}}, ""},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, MainPathParam(tc.c, params), tc.name)
	}
}
