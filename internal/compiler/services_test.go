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

package compiler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

func envOf(c corev1.Container) map[string]string {
	out := map[string]string{}
	for _, e := range c.Env {
		out[e.Name] = e.Value
	}
	return out
}

func TestCompileServices(t *testing.T) {
	two := int32(2)
	test := canonicalTest()
	test.Spec.Pod = &testsv1alpha1.PodConfig{
		ServiceAccountName: "runner",
		Annotations:        map[string]string{"sidecar.istio.io/inject": "false"},
		Labels:             map[string]string{"team": "sre"},
	}
	probe := &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(5432)}}}
	test.Spec.Services = map[string]testsv1alpha1.ServiceSpec{
		"db": {Image: "postgres:17", Args: []string{"-c", "fsync=off"}, ReadinessProbe: probe,
			Env: []corev1.EnvVar{{Name: "POSTGRES_PASSWORD", Value: "x"}}},
		"grid": {Image: "selenium/node", Count: &two, Matrix: map[string][]string{"browser": {"chrome", "firefox"}}},
	}
	run := canonicalTestRun()

	got, err := CompileServices(test, run)
	require.NoError(t, err)

	require.Len(t, got.Services, 2)
	db := got.Services[0]
	assert.Equal(t, "sample-run-db", db.Name)
	assert.Equal(t, corev1.ClusterIPNone, db.Spec.ClusterIP)
	assert.Equal(t, map[string]string{LabelServiceOf: "sample-run", LabelService: "db"}, db.Spec.Selector)
	require.Len(t, db.OwnerReferences, 1)

	require.Len(t, got.Replicas, 5, "db ×1 + grid: 2 browsers × count 2")
	p := got.Replicas[0].Pod
	assert.Equal(t, "sample-run-db-0", p.Name)
	assert.Equal(t, "sample-run-db-0", p.Spec.Hostname)
	assert.Equal(t, "sample-run-db", p.Spec.Subdomain)
	assert.Equal(t, corev1.RestartPolicyAlways, p.Spec.RestartPolicy)
	assert.Equal(t, "runner", p.Spec.ServiceAccountName, "spec.pod applies to services")
	assert.Equal(t, "false", p.Annotations["sidecar.istio.io/inject"])
	assert.Equal(t, "sre", p.Labels["team"])
	assert.NotContains(t, p.Labels, LabelRunID, "a replica must never pass for the test's own pod")
	c := p.Spec.Containers[0]
	assert.Equal(t, "postgres:17", c.Image)
	assert.Equal(t, []string{"-c", "fsync=off"}, c.Args)
	assert.Same(t, probe, c.ReadinessProbe)
	assert.Equal(t, map[string]string{EnvServiceIndex: "0", "POSTGRES_PASSWORD": "x"}, envOf(c))

	browsers := make([]string, 0, len(got.Replicas)-1)
	for _, r := range got.Replicas[1:] {
		assert.Equal(t, "grid", r.Service)
		browsers = append(browsers, envOf(r.Pod.Spec.Containers[0])[EnvMatrixPrefix+"BROWSER"])
	}
	assert.Equal(t, []string{"chrome", "chrome", "firefox", "firefox"}, browsers)
	assert.Equal(t, "sample-run-grid-3", got.Replicas[4].Pod.Name)
}

func TestCompileServices_NoneDeclared(t *testing.T) {
	got, err := CompileServices(canonicalTest(), canonicalTestRun())
	require.NoError(t, err)
	assert.Empty(t, got.Services)
	assert.Empty(t, got.Replicas)
	_, err = CompileServices(nil, canonicalTestRun())
	require.ErrorIs(t, err, ErrNilTest)
}

func TestCompile_TestSeesServiceHosts(t *testing.T) {
	test := canonicalTest()
	test.Spec.Services = map[string]testsv1alpha1.ServiceSpec{"mock-api": {Image: "wiremock"}}
	job, _, err := Compile(test, canonicalTestRun(), defaultOpts())
	require.NoError(t, err)
	env := envOf(job.Spec.Template.Spec.Containers[0])
	assert.Equal(t, "sample-run-mock-api.kubetest-samples.svc", env["KUBETEST_SERVICE_MOCK_API_HOST"])
}
