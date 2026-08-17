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
	"maps"
	"strings"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// Reserved label keys the operator owns. User-supplied values for these keys
// are dropped and replaced by the operator's value. See CLAUDE.md §8.
//
// Note the distinction from CLAUDE.md §7's app.kubernetes.io/managed-by usage:
// on CustomResources that value is "gitops" or "ui" (ownership contract for
// the definition), whereas on workloads it is "kubetest-alt" (identifies the
// operator that reconciled the object). Same label key, two different
// contracts, two different code paths — never confuse them.
const (
	LabelRunID     = "kubetest.io/run-id"
	LabelManagedBy = "app.kubernetes.io/managed-by"
	ManagedByValue = "kubetest-alt"

	// ReservedLabelPrefix marks the whole namespace the operator reserves for
	// future labels. Any user label under this prefix is silently dropped.
	ReservedLabelPrefix = "kubetest.io/"
)

// isReservedLabelKey reports whether a user label key is operator-owned.
func isReservedLabelKey(k string) bool {
	return k == LabelManagedBy || strings.HasPrefix(k, ReservedLabelPrefix)
}

// mergeAnnotations produces the annotations that land on the pod template.
// Pure passthrough with TestRun-wins merge — the operator injects NOTHING.
// This is the §8 core invariant; changing this function without a matching
// change to the annotation regression tests will regress the passthrough contract.
func mergeAnnotations(testPod, runPod *testsv1alpha1.PodConfig) map[string]string {
	out := map[string]string{}
	if testPod != nil {
		maps.Copy(out, testPod.Annotations)
	}
	if runPod != nil {
		// TestRun overlay wins per key — CLAUDE.md §8 merge order.
		maps.Copy(out, runPod.Annotations)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// mergeLabels produces the labels that land on the pod template.
// Order: Test → TestRun (TestRun wins) → operator overlay of reserved labels
// (operator always wins). User values under the reserved prefix are dropped
// before the operator overlay so a user cannot smuggle e.g. a fake run-id.
func mergeLabels(testPod, runPod *testsv1alpha1.PodConfig, runID string) map[string]string {
	out := map[string]string{}
	for _, src := range []*testsv1alpha1.PodConfig{testPod, runPod} {
		if src == nil {
			continue
		}
		for k, v := range src.Labels {
			if isReservedLabelKey(k) {
				continue
			}
			out[k] = v
		}
	}
	out[LabelRunID] = runID
	out[LabelManagedBy] = ManagedByValue
	return out
}

// mergePodConfig picks the effective PodConfig for the merged pod template.
// Fields other than Labels/Annotations follow the same TestRun-wins rule for
// scalars; slice/pointer fields on TestRun replace Test's entirely (no
// element-level merge — surprise-avoidance).
func mergePodConfig(testPod, runPod *testsv1alpha1.PodConfig) testsv1alpha1.PodConfig {
	var merged testsv1alpha1.PodConfig
	if testPod != nil {
		merged = *testPod
	}
	if runPod != nil {
		if runPod.ServiceAccountName != "" {
			merged.ServiceAccountName = runPod.ServiceAccountName
		}
		if runPod.NodeSelector != nil {
			merged.NodeSelector = runPod.NodeSelector
		}
		if runPod.Tolerations != nil {
			merged.Tolerations = runPod.Tolerations
		}
		if runPod.Affinity != nil {
			merged.Affinity = runPod.Affinity
		}
		if runPod.SecurityContext != nil {
			merged.SecurityContext = runPod.SecurityContext
		}
		if runPod.Volumes != nil {
			merged.Volumes = runPod.Volumes
		}
		if runPod.ImagePullSecrets != nil {
			merged.ImagePullSecrets = runPod.ImagePullSecrets
		}
	}
	return merged
}
