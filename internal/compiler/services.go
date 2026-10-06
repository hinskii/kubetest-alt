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
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/names"
)

// Labels on spec.services objects. Deliberately NOT LabelRunID: the
// controller finds the test's own pod by run-id, and must never take a
// service replica for it.
const (
	// LabelServiceOf names the run a service replica belongs to.
	LabelServiceOf = "kubetest.io/service-of"
	// LabelService is the spec.services key.
	LabelService = "kubetest.io/service"
	// LabelServiceIndex is the replica number.
	LabelServiceIndex = "kubetest.io/service-index"

	// ContainerService is the container of a service replica pod.
	ContainerService = "service"

	// EnvServiceIndex tells a replica its number; matrix values arrive as
	// EnvMatrixPrefix + KEY.
	EnvServiceIndex = "KUBETEST_SERVICE_INDEX"
	EnvMatrixPrefix = "KUBETEST_MATRIX_"
)

// ServiceReplica is one pod of a service.
type ServiceReplica struct {
	Service string // spec.services key
	Index   int
	Pod     *corev1.Pod
}

// CompiledServices is what spec.services turns into.
type CompiledServices struct {
	// Services are the headless Services, one per spec.services entry.
	Services []*corev1.Service
	// Replicas are the pods, in service-name then index order.
	Replicas []ServiceReplica
}

// CompileServices turns spec.services into headless Services and their
// replica Pods. Pure; never mutates its inputs. Replicas: Count copies of
// each matrix combination (keys sorted, values in order).
func CompileServices(test *testsv1alpha1.Test, run *testsv1alpha1.TestRun) (*CompiledServices, error) {
	if test == nil {
		return nil, ErrNilTest
	}
	if run == nil {
		return nil, ErrNilTestRun
	}
	out := &CompiledServices{}
	if len(test.Spec.Services) == 0 {
		return out, nil
	}
	pod := mergePodConfig(test.Spec.Pod, run.Spec.Pod)
	userLabels := mergeLabels(test.Spec.Pod, run.Spec.Pod, run.Name)
	delete(userLabels, LabelRunID)
	annotations := mergeAnnotations(test.Spec.Pod, run.Spec.Pod)

	for _, name := range slices.Sorted(maps.Keys(test.Spec.Services)) {
		svc := test.Spec.Services[name]
		svcName := names.ServiceName(run.Name, name)
		selector := map[string]string{LabelServiceOf: run.Name, LabelService: name}
		out.Services = append(out.Services, &corev1.Service{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
			ObjectMeta: metav1.ObjectMeta{
				Name:            svcName,
				Namespace:       run.Namespace,
				OwnerReferences: ownerRefForTestRun(run),
				Labels:          withLabels(selector, LabelManagedBy, ManagedByValue),
			},
			Spec: corev1.ServiceSpec{
				// Headless: DNS answers with every ready replica, and
				// replica i gets <pod>.<service> through hostname+subdomain.
				ClusterIP: corev1.ClusterIPNone,
				Selector:  selector,
			},
		})
		for i, combo := range replicaEnvs(svc) {
			podName := names.ServiceReplicaName(svcName, i)
			labels := maps.Clone(userLabels)
			if labels == nil {
				labels = map[string]string{}
			}
			maps.Copy(labels, selector)
			labels[LabelServiceIndex] = fmt.Sprintf("%d", i)
			labels[LabelManagedBy] = ManagedByValue

			env := append([]corev1.EnvVar{{Name: EnvServiceIndex, Value: fmt.Sprintf("%d", i)}}, combo...)
			env = append(env, svc.Env...)
			restart := svc.RestartPolicy
			if restart == "" {
				restart = corev1.RestartPolicyAlways
			}
			automount := false
			out.Replicas = append(out.Replicas, ServiceReplica{Service: name, Index: i, Pod: &corev1.Pod{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
				ObjectMeta: metav1.ObjectMeta{
					Name:            podName,
					Namespace:       run.Namespace,
					OwnerReferences: ownerRefForTestRun(run),
					Labels:          labels,
					Annotations:     maps.Clone(annotations),
				},
				Spec: corev1.PodSpec{
					Hostname:                     podName,
					Subdomain:                    svcName,
					RestartPolicy:                restart,
					AutomountServiceAccountToken: &automount,
					ServiceAccountName:           pod.ServiceAccountName,
					NodeSelector:                 pod.NodeSelector,
					Tolerations:                  pod.Tolerations,
					Affinity:                     pod.Affinity,
					SecurityContext:              pod.SecurityContext,
					ImagePullSecrets:             pod.ImagePullSecrets,
					Containers: []corev1.Container{{
						Name:           ContainerService,
						Image:          svc.Image,
						Command:        svc.Command,
						Args:           svc.Args,
						Env:            env,
						Resources:      svc.Resources,
						ReadinessProbe: svc.ReadinessProbe,
					}},
				},
			}})
		}
	}
	return out, nil
}

// replicaEnvs returns one env set per replica: Count copies of every
// matrix combination.
func replicaEnvs(svc testsv1alpha1.ServiceSpec) [][]corev1.EnvVar {
	combos := [][]corev1.EnvVar{nil}
	for _, key := range slices.Sorted(maps.Keys(svc.Matrix)) {
		var next [][]corev1.EnvVar
		for _, c := range combos {
			for _, v := range svc.Matrix[key] {
				e := append(slices.Clip(c), corev1.EnvVar{Name: EnvMatrixPrefix + strings.ToUpper(key), Value: v})
				next = append(next, e)
			}
		}
		combos = next
	}
	count := 1
	if svc.Count != nil && *svc.Count > 0 {
		count = int(*svc.Count)
	}
	out := make([][]corev1.EnvVar, 0, count*len(combos))
	for _, c := range combos {
		for range count {
			out = append(out, c)
		}
	}
	return out
}

// ServiceHostEnv is the env var carrying service name's DNS name into the
// test container: KUBETEST_SERVICE_<NAME>_HOST.
func ServiceHostEnv(name string) string {
	return "KUBETEST_SERVICE_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_HOST"
}

func withLabels(base map[string]string, kv ...string) map[string]string {
	out := maps.Clone(base)
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}
