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
		kind := "ConfigMap"
		if d.Secret != "" {
			kind = "Secret"
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
