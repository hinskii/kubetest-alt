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

package controller

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// errTestDataKey: an item names a key its ConfigMap/Secret doesn't have.
var errTestDataKey = errors.New("key not found")

// checkTestData verifies every content.testData object exists (and every
// item's key in it). It reads uncached: the operator may only get
// Secrets, not list or watch them.
func (r *TestRunReconciler) checkTestData(ctx context.Context, ns string, entries []testsv1alpha1.TestDataSource) error {
	reader := client.Reader(r.Client)
	if r.APIReader != nil {
		reader = r.APIReader
	}
	for i, d := range entries {
		keys := map[string]bool{}
		key := types.NamespacedName{Namespace: ns, Name: d.ConfigMap + d.Secret}
		kind := kindConfigMap
		if d.Secret != "" {
			kind = kindSecret
			var s corev1.Secret
			if err := reader.Get(ctx, key, &s); err != nil {
				return testDataErr(i, kind, key.Name, ns, err)
			}
			for k := range s.Data {
				keys[k] = true
			}
		} else {
			var cm corev1.ConfigMap
			if err := reader.Get(ctx, key, &cm); err != nil {
				return testDataErr(i, kind, key.Name, ns, err)
			}
			for k := range cm.Data {
				keys[k] = true
			}
			for k := range cm.BinaryData {
				keys[k] = true
			}
		}
		for _, it := range d.Items {
			if !keys[it.Key] {
				return fmt.Errorf("content.testData[%d]: %s %s has no key %q: %w", i, kind, key.Name, it.Key, errTestDataKey)
			}
		}
	}
	return nil
}

func testDataErr(i int, kind, name, ns string, err error) error {
	if apierrors.IsNotFound(err) {
		return apierrors.NewNotFound(schema.GroupResource{Resource: kind}, fmt.Sprintf(
			"%s (content.testData[%d]: no %s %s in namespace %s)", name, i, kind, name, ns))
	}
	return err
}

// errVolumeSource: a pod volume names a Secret/ConfigMap (or a key of it)
// that doesn't exist.
var errVolumeSource = errors.New("volume source missing")

// volumeRef is one Secret or ConfigMap a pod volume needs.
type volumeRef struct {
	volume, kind, name string
	keys               []string
}

// volumeRefs lists what the pod's volumes need to exist: the Secrets and
// ConfigMaps of secret, configMap and projected volumes, unless optional.
func volumeRefs(vols []corev1.Volume) []volumeRef {
	var out []volumeRef
	keysOf := func(items []corev1.KeyToPath) []string {
		keys := make([]string, 0, len(items))
		for _, it := range items {
			keys = append(keys, it.Key)
		}
		return keys
	}
	required := func(optional *bool) bool { return optional == nil || !*optional }
	for _, v := range vols {
		switch s := v.VolumeSource; {
		case s.Secret != nil && required(s.Secret.Optional):
			out = append(out, volumeRef{v.Name, kindSecret, s.Secret.SecretName, keysOf(s.Secret.Items)})
		case s.ConfigMap != nil && required(s.ConfigMap.Optional):
			out = append(out, volumeRef{v.Name, kindConfigMap, s.ConfigMap.Name, keysOf(s.ConfigMap.Items)})
		case s.Projected != nil:
			for _, src := range s.Projected.Sources {
				if src.Secret != nil && required(src.Secret.Optional) {
					out = append(out, volumeRef{v.Name, kindSecret, src.Secret.Name, keysOf(src.Secret.Items)})
				}
				if src.ConfigMap != nil && required(src.ConfigMap.Optional) {
					out = append(out, volumeRef{v.Name, kindConfigMap, src.ConfigMap.Name, keysOf(src.ConfigMap.Items)})
				}
			}
		}
	}
	return out
}

const (
	kindSecret    = "Secret"
	kindConfigMap = "ConfigMap"
)

// checkPodVolumes verifies the Secrets and ConfigMaps the pod's volumes
// mount exist (with the keys they pick): a missing one leaves the pod in
// ContainerCreating (FailedMount, only an event) until the Job's deadline.
// Uncached, like checkTestData.
func (r *TestRunReconciler) checkPodVolumes(ctx context.Context, ns string, vols []corev1.Volume) error {
	reader := client.Reader(r.Client)
	if r.APIReader != nil {
		reader = r.APIReader
	}
	for _, ref := range volumeRefs(vols) {
		keys := map[string]bool{}
		key := types.NamespacedName{Namespace: ns, Name: ref.name}
		var err error
		if ref.kind == kindSecret {
			var s corev1.Secret
			if err = reader.Get(ctx, key, &s); err == nil {
				for k := range s.Data {
					keys[k] = true
				}
			}
		} else {
			var cm corev1.ConfigMap
			if err = reader.Get(ctx, key, &cm); err == nil {
				for k := range cm.Data {
					keys[k] = true
				}
				for k := range cm.BinaryData {
					keys[k] = true
				}
			}
		}
		switch {
		case apierrors.IsNotFound(err):
			return fmt.Errorf("pod volume %s: no %s %s in namespace %s: %w", ref.volume, ref.kind, ref.name, ns, errVolumeSource)
		case err != nil:
			return err
		}
		for _, k := range ref.keys {
			if !keys[k] {
				return fmt.Errorf("pod volume %s: %s %s has no key %q: %w", ref.volume, ref.kind, ref.name, k, errVolumeSource)
			}
		}
	}
	return nil
}

func podVolumes(p *testsv1alpha1.PodConfig) []corev1.Volume {
	if p == nil {
		return nil
	}
	return p.Volumes
}
