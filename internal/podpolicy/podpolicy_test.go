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
// Package podpolicy is what a test pod may ask for (fixes.md #1).
package podpolicy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

func vol(name string, src corev1.VolumeSource) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: src}
}

func TestCheck_DefaultIsStrict(t *testing.T) {
	var p Policy
	ok := &testsv1alpha1.TestSpec{
		Pod: &testsv1alpha1.PodConfig{ServiceAccountName: "default", Volumes: []corev1.Volume{
			vol("shm", corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}}),
			vol("cfg", corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{}}),
			vol("creds", corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{}}),
			vol("data", corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{}}),
		}},
		Container: testsv1alpha1.ContainerConfig{SecurityContext: &corev1.SecurityContext{
			Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_BIND_SERVICE"}, Drop: []corev1.Capability{"ALL"}}}},
	}
	require.NoError(t, p.Check(ok, nil), "what the catalog uses is fine")
	require.NoError(t, p.Check(&testsv1alpha1.TestSpec{}, nil))
	require.NoError(t, p.Check(nil, nil))

	bad := &testsv1alpha1.TestSpec{
		Pod: &testsv1alpha1.PodConfig{ServiceAccountName: "cluster-admin-sa", Volumes: []corev1.Volume{
			vol("node", corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}}),
			vol("share", corev1.VolumeSource{NFS: &corev1.NFSVolumeSource{Server: "x", Path: "/"}}),
		}},
		Container: testsv1alpha1.ContainerConfig{SecurityContext: &corev1.SecurityContext{
			Privileged:   ptr.To(true),
			Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"SYS_ADMIN"}}}},
	}
	err := p.Check(bad, &testsv1alpha1.PodConfig{ServiceAccountName: "other"})
	require.Error(t, err)
	for _, want := range []string{
		`spec.pod.serviceAccountName "cluster-admin-sa" is not allowed`,
		"spec.pod.volumes[node]: hostPath volumes are not allowed",
		"spec.pod.volumes[share]: this kind of volumes are not allowed",
		"spec.container.securityContext.privileged is not allowed",
		`capabilities.add "SYS_ADMIN" is not allowed`,
		`spec.pod (TestRun).serviceAccountName "other"`,
		"testPods.allowedServiceAccounts",
	} {
		assert.Contains(t, err.Error(), want)
	}
}

func TestCheck_Allowlists(t *testing.T) {
	p := Policy{ServiceAccounts: []string{"gcs-writer"}, AllowHostPath: true}
	spec := &testsv1alpha1.TestSpec{Pod: &testsv1alpha1.PodConfig{ServiceAccountName: "gcs-writer",
		Volumes: []corev1.Volume{vol("node", corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/tmp"}})}}}
	require.NoError(t, p.Check(spec, &testsv1alpha1.PodConfig{ServiceAccountName: "gcs-writer"}))
	assert.ErrorContains(t, p.Check(spec, &testsv1alpha1.PodConfig{ServiceAccountName: "x"}), "allowed: default, gcs-writer")
}

// Every volume kind the policy admits, by the name the API uses.
func TestCheck_AllowedVolumeKinds(t *testing.T) {
	var p Policy
	sources := map[string]corev1.VolumeSource{
		"emptyDir":              {EmptyDir: &corev1.EmptyDirVolumeSource{}},
		"configMap":             {ConfigMap: &corev1.ConfigMapVolumeSource{}},
		"secret":                {Secret: &corev1.SecretVolumeSource{}},
		"projected":             {Projected: &corev1.ProjectedVolumeSource{}},
		"downwardAPI":           {DownwardAPI: &corev1.DownwardAPIVolumeSource{}},
		"persistentVolumeClaim": {PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{}},
		"ephemeral":             {Ephemeral: &corev1.EphemeralVolumeSource{}},
		"csi":                   {CSI: &corev1.CSIVolumeSource{Driver: "secrets-store.csi.k8s.io"}},
	}
	for kind, src := range sources {
		assert.Equal(t, kind, volumeKind(src))
		assert.NoError(t, p.Check(&testsv1alpha1.TestSpec{Pod: &testsv1alpha1.PodConfig{Volumes: []corev1.Volume{vol("v", src)}}}, nil), kind)
	}
}
