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
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// deploymentGVK is the apps/v1 Deployment GVK — the primary test-driver GVK
// for step 12.
var deploymentGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}

// makeObj constructs an unstructured deployment-shaped object with given
// name/namespace/labels for selector tests.
func makeObj(name, ns string, labels map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(deploymentGVK)
	obj.SetName(name)
	obj.SetNamespace(ns)
	obj.SetLabels(labels)
	return obj
}

// TestSelectorMatches_Table covers every field independently plus the AND-of-
// all-present-fields interaction. Each row asserts a specific rule; a broken
// selector implementation would fail multiple rows and localize the bug.
func TestSelectorMatches_Table(t *testing.T) {
	cases := []struct {
		name    string
		sel     *testsv1alpha1.TriggerResourceSelector
		obj     *unstructured.Unstructured
		want    bool
		wantErr bool
	}{
		{
			name: "nil selector matches everything",
			sel:  nil,
			obj:  makeObj("any", "default", nil),
			want: true,
		},
		{
			name: "empty selector matches everything",
			sel:  &testsv1alpha1.TriggerResourceSelector{},
			obj:  makeObj("any", "default", nil),
			want: true,
		},
		{
			name: "name exact match",
			sel:  &testsv1alpha1.TriggerResourceSelector{Name: "frontend"},
			obj:  makeObj("frontend", "default", nil),
			want: true,
		},
		{
			name: "name mismatch",
			sel:  &testsv1alpha1.TriggerResourceSelector{Name: "frontend"},
			obj:  makeObj("backend", "default", nil),
			want: false,
		},
		{
			name: "nameRegex matches",
			sel:  &testsv1alpha1.TriggerResourceSelector{NameRegex: "^front.*"},
			obj:  makeObj("frontend-canary", "default", nil),
			want: true,
		},
		{
			name: "nameRegex mismatch",
			sel:  &testsv1alpha1.TriggerResourceSelector{NameRegex: "^front.*"},
			obj:  makeObj("backend", "default", nil),
			want: false,
		},
		{
			name:    "nameRegex invalid regex ⇒ error",
			sel:     &testsv1alpha1.TriggerResourceSelector{NameRegex: "(bad"},
			obj:     makeObj("frontend", "default", nil),
			wantErr: true,
		},
		{
			name: "namespace exact match",
			sel:  &testsv1alpha1.TriggerResourceSelector{Namespace: "prod"},
			obj:  makeObj("frontend", "prod", nil),
			want: true,
		},
		{
			name: "namespace mismatch",
			sel:  &testsv1alpha1.TriggerResourceSelector{Namespace: "prod"},
			obj:  makeObj("frontend", "staging", nil),
			want: false,
		},
		{
			name: "namespaceRegex matches",
			sel:  &testsv1alpha1.TriggerResourceSelector{NamespaceRegex: "^prod-.*"},
			obj:  makeObj("frontend", "prod-us-east", nil),
			want: true,
		},
		{
			name: "labelSelector match",
			sel: &testsv1alpha1.TriggerResourceSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"tier": "web"},
				},
			},
			obj:  makeObj("frontend", "prod", map[string]string{"tier": "web"}),
			want: true,
		},
		{
			name: "labelSelector mismatch",
			sel: &testsv1alpha1.TriggerResourceSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"tier": "web"},
				},
			},
			obj:  makeObj("frontend", "prod", map[string]string{"tier": "cache"}),
			want: false,
		},
		{
			name: "AND — name matches BUT labels don't ⇒ overall false",
			sel: &testsv1alpha1.TriggerResourceSelector{
				Name: "frontend",
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"tier": "web"},
				},
			},
			obj:  makeObj("frontend", "prod", map[string]string{"tier": "cache"}),
			want: false,
		},
		{
			name: "AND — all fields match ⇒ true",
			sel: &testsv1alpha1.TriggerResourceSelector{
				Name:      "frontend",
				Namespace: "prod",
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"tier": "web"},
				},
			},
			obj:  makeObj("frontend", "prod", map[string]string{"tier": "web"}),
			want: true,
		},
		{
			name: "name + nameRegex both set — both must match ⇒ true",
			sel: &testsv1alpha1.TriggerResourceSelector{
				Name:      "frontend",
				NameRegex: "^front.*",
			},
			obj:  makeObj("frontend", "prod", nil),
			want: true,
		},
		{
			name: "name + nameRegex both set — regex matches but exact doesn't ⇒ false",
			sel: &testsv1alpha1.TriggerResourceSelector{
				Name:      "frontend",
				NameRegex: "^front.*",
			},
			obj:  makeObj("frontend-canary", "prod", nil),
			want: false,
		},
		{
			name: "nil obj with non-nil selector ⇒ false, no crash",
			sel:  &testsv1alpha1.TriggerResourceSelector{Name: "x"},
			obj:  nil,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SelectorMatches(tc.sel, tc.obj)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
