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
	"fmt"
	"regexp"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// SelectorMatches reports whether obj passes the TestTrigger.spec.
// resourceSelector. A nil selector matches everything. Every present field
// is ANDed — a partial mismatch (e.g. name matches but labels don't) is a
// non-match, not a match.
//
// Rules:
//   - Name           : exact match on obj.GetName()
//   - NameRegex      : regexp match on obj.GetName()
//   - Namespace      : exact match on obj.GetNamespace()
//   - NamespaceRegex : regexp match on obj.GetNamespace()
//   - LabelSelector  : k8s labels.Selector on obj.GetLabels()
//
// Regex compilation errors and malformed label selectors return an error so
// the trigger controller can emit a Warning event on the TestTrigger CR
// instead of silently dropping every incoming event.
func SelectorMatches(sel *testsv1alpha1.TriggerResourceSelector, obj *unstructured.Unstructured) (bool, error) {
	if sel == nil {
		return true, nil
	}
	if obj == nil {
		return false, nil
	}
	if sel.Name != "" && obj.GetName() != sel.Name {
		return false, nil
	}
	if sel.NameRegex != "" {
		m, err := regexp.MatchString(sel.NameRegex, obj.GetName())
		if err != nil {
			return false, fmt.Errorf("nameRegex %q: %w", sel.NameRegex, err)
		}
		if !m {
			return false, nil
		}
	}
	if sel.Namespace != "" && obj.GetNamespace() != sel.Namespace {
		return false, nil
	}
	if sel.NamespaceRegex != "" {
		m, err := regexp.MatchString(sel.NamespaceRegex, obj.GetNamespace())
		if err != nil {
			return false, fmt.Errorf("namespaceRegex %q: %w", sel.NamespaceRegex, err)
		}
		if !m {
			return false, nil
		}
	}
	if sel.LabelSelector != nil {
		selector, err := metav1.LabelSelectorAsSelector(sel.LabelSelector)
		if err != nil {
			return false, fmt.Errorf("labelSelector: %w", err)
		}
		if !selector.Matches(labels.Set(obj.GetLabels())) {
			return false, nil
		}
	}
	return true, nil
}
