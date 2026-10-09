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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/executor"
)

func compileContent(t *testing.T, c testsv1alpha1.Content) (corev1.PodSpec, fetcherContent) {
	t.Helper()
	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "t", Namespace: "ns"},
		Spec: testsv1alpha1.TestSpec{Content: c,
			Container: testsv1alpha1.ContainerConfig{Image: "grafana/k6", Args: []string{"run", "x.js"}}},
	}
	run := &testsv1alpha1.TestRun{ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns", UID: "u"},
		Spec: testsv1alpha1.TestRunSpec{TestRef: "t"}}
	job, aux, err := Compile(test, run, Options{ContentFetcherImage: "fetcher"})
	require.NoError(t, err)
	var fc fetcherContent
	require.NoError(t, json.Unmarshal([]byte(aux[0].(*corev1.ConfigMap).Data[executor.ContentFileName]), &fc))
	return job.Spec.Template.Spec, fc
}

func mountsOf(pod corev1.PodSpec) map[string]corev1.VolumeMount {
	out := map[string]corev1.VolumeMount{}
	for _, m := range pod.Containers[0].VolumeMounts {
		out[m.MountPath] = m
	}
	return out
}

func TestCompile_InlineFilesLandInRepoDir(t *testing.T) {
	_, fc := compileContent(t, testsv1alpha1.Content{Files: []testsv1alpha1.FileContent{
		{Path: "load.js", Content: "x"}, {Path: "repo/legacy.js", Content: "y"}}})
	require.Len(t, fc.Files, 2)
	assert.Equal(t, "repo/load.js", fc.Files[0].Path, "relative to /data/repo")
	assert.Equal(t, "repo/legacy.js", fc.Files[1].Path, "a path stored the old way means the same file")
}

func TestCompile_ContentFromIsRefused(t *testing.T) {
	test := &testsv1alpha1.Test{ObjectMeta: metav1.ObjectMeta{Name: "t", Namespace: "ns"}, Spec: testsv1alpha1.TestSpec{
		Container: testsv1alpha1.ContainerConfig{Image: "i", Args: []string{"a"}},
		Content: testsv1alpha1.Content{Files: []testsv1alpha1.FileContent{{Path: "d.csv",
			ContentFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{Key: "k"}}}}}}}
	run := &testsv1alpha1.TestRun{ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"}}
	_, _, err := Compile(test, run, Options{ContentFetcherImage: "fetcher"})
	assert.ErrorIs(t, err, ErrContentFrom)
}

func TestCompile_TestDataVolumes(t *testing.T) {
	pod, fc := compileContent(t, testsv1alpha1.Content{
		Git: &testsv1alpha1.GitContent{URI: "https://x"},
		TestData: []testsv1alpha1.TestDataSource{
			{ConfigMap: "products-stage"},
			{ConfigMap: "fixtures", MountPath: "/data/repo/e2e/data"},
			{Secret: "account", Items: []testsv1alpha1.TestDataItem{{Key: "env", Path: "/data/repo/e2e/.env"}}},
			{Secret: "outside", MountPath: "/etc/app"},
		},
	})
	m := mountsOf(pod)
	def := m["/data/testdata/products-stage"]
	assert.True(t, def.ReadOnly)
	assert.Equal(t, "fixtures", volumeOf(t, pod, m["/data/repo/e2e/data"].Name).ConfigMap.Name)
	item := m["/data/repo/e2e/.env"]
	assert.Equal(t, "env", item.SubPath, "a single key at an exact path")
	assert.Equal(t, "account", volumeOf(t, pod, item.Name).Secret.SecretName)
	assert.Contains(t, pod.Containers[0].Env, corev1.EnvVar{Name: EnvTestDataDir, Value: "/data/testdata"})

	assert.Equal(t, []emptyMount{{Name: "fixtures", Path: "/data/repo/e2e/data"}}, fc.EmptyMounts,
		"the fetcher checks mountPaths below /data — not items, not paths outside")
	for _, c := range pod.InitContainers {
		for _, vm := range c.VolumeMounts {
			assert.NotContains(t, vm.Name, "testdata", "test data reaches the test container only")
		}
	}
}

func volumeOf(t *testing.T, pod corev1.PodSpec, name string) corev1.VolumeSource {
	t.Helper()
	for _, v := range pod.Volumes {
		if v.Name == name {
			return v.VolumeSource
		}
	}
	t.Fatalf("no volume %s", name)
	return corev1.VolumeSource{}
}
