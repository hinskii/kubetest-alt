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
// spec.pod and spec.container are a passthrough to the test pod (CLAUDE.md
// §8), so whoever can create a Test or a TestRun — through the API or
// kubectl — would otherwise pick any service account, mount the node's
// disk or run privileged. The operator checks the resolved spec (Test,
// templates and the TestRun's pod override) before a pod exists; the
// admission webhooks check the same rules early, on what they can see.
//
// The allowlists are the platform's, set in the chart — not per Test.
package podpolicy

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// Policy is the platform's test-pod policy. The zero value is the strict
// default: the namespace's default service account only, no hostPath.
type Policy struct {
	// ServiceAccounts a test pod may use besides "default" (e.g. one bound
	// to a Google service account for GCS). Names apply in any namespace.
	ServiceAccounts []string
	// AllowHostPath admits hostPath volumes — the node's filesystem.
	AllowHostPath bool
}

// defaultSA is the namespace's own service account — always allowed.
const defaultSA = "default"

// baselineCapabilities may be added to a test container: Pod Security
// Standards "baseline" — anything else (SYS_ADMIN, NET_ADMIN, …) is how a
// container leaves its sandbox.
var baselineCapabilities = []corev1.Capability{
	"AUDIT_WRITE", "CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "MKNOD",
	"NET_BIND_SERVICE", "SETFCAP", "SETGID", "SETPCAP", "SETUID", "SYS_CHROOT",
}

// Check reports every violation in spec (resolved or as written) and an
// optional TestRun pod override. Nil when the pod is allowed.
func (p Policy) Check(spec *testsv1alpha1.TestSpec, runPod *testsv1alpha1.PodConfig) error {
	if spec == nil {
		return nil
	}
	errs := make([]error, 0, 4)
	errs = append(errs, p.checkPod("spec.pod", spec.Pod)...)
	errs = append(errs, p.checkPod("spec.pod (TestRun)", runPod)...)
	errs = append(errs, checkContainer("spec.container", spec.Container.SecurityContext)...)
	return errors.Join(errs...)
}

func (p Policy) checkPod(at string, pod *testsv1alpha1.PodConfig) []error {
	if pod == nil {
		return nil
	}
	var errs []error
	if sa := pod.ServiceAccountName; sa != "" && sa != defaultSA && !slices.Contains(p.ServiceAccounts, sa) {
		errs = append(errs, fmt.Errorf("%s.serviceAccountName %q is not allowed for test pods (allowed: %s) — "+
			"ask the platform admins to add it to testPods.allowedServiceAccounts", at, sa, p.allowedSAs()))
	}
	for _, v := range pod.Volumes {
		if kind := volumeKind(v.VolumeSource); !p.volumeAllowed(kind) {
			errs = append(errs, fmt.Errorf("%s.volumes[%s]: %s volumes are not allowed for test pods", at, v.Name, kind))
		}
	}
	return errs
}

func (p Policy) allowedSAs() string {
	return strings.Join(append([]string{defaultSA}, p.ServiceAccounts...), ", ")
}

// Volume kinds, as the API spells them.
const (
	kindEmptyDir              = "emptyDir"
	kindConfigMap             = "configMap"
	kindSecret                = "secret"
	kindProjected             = "projected"
	kindDownwardAPI           = "downwardAPI"
	kindPersistentVolumeClaim = "persistentVolumeClaim"
	kindEphemeral             = "ephemeral"
	kindCsi                   = "csi"
	kindHostPath              = "hostPath"
)

// allowedVolumes never reach outside the pod's own storage and API objects.
var allowedVolumes = []string{
	kindEmptyDir, kindConfigMap, kindSecret, kindProjected, kindDownwardAPI,
	kindPersistentVolumeClaim, kindEphemeral, kindCsi,
}

func (p Policy) volumeAllowed(kind string) bool {
	return slices.Contains(allowedVolumes, kind) || (kind == kindHostPath && p.AllowHostPath)
}

// volumeKind names a volume's source the way the API spells it.
func volumeKind(s corev1.VolumeSource) string {
	switch {
	case s.EmptyDir != nil:
		return kindEmptyDir
	case s.ConfigMap != nil:
		return kindConfigMap
	case s.Secret != nil:
		return kindSecret
	case s.Projected != nil:
		return kindProjected
	case s.DownwardAPI != nil:
		return kindDownwardAPI
	case s.PersistentVolumeClaim != nil:
		return kindPersistentVolumeClaim
	case s.Ephemeral != nil:
		return kindEphemeral
	case s.CSI != nil:
		return kindCsi
	case s.HostPath != nil:
		return kindHostPath
	}
	return "this kind of" // nfs, iscsi, rbd, … — node- or network-level storage
}

func checkContainer(at string, sc *corev1.SecurityContext) []error {
	if sc == nil {
		return nil
	}
	var errs []error
	if sc.Privileged != nil && *sc.Privileged {
		errs = append(errs, fmt.Errorf("%s.securityContext.privileged is not allowed for test pods", at))
	}
	if sc.Capabilities != nil {
		for _, c := range sc.Capabilities.Add {
			if !slices.Contains(baselineCapabilities, c) {
				errs = append(errs, fmt.Errorf("%s.securityContext.capabilities.add %q is not allowed for test pods", at, c))
			}
		}
	}
	return errs
}
