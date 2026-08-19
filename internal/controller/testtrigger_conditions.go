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
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// conditionEntry is the flat shape we pull out of an unstructured object's
// status.conditions. Missing fields become empty strings / zero time
// (permissive on the read side; strict on the match side).
type conditionEntry struct {
	Type               string
	Status             string
	Reason             string
	LastTransitionTime time.Time
}

// extractConditions walks obj.status.conditions and returns a flat slice.
// Returns nil on missing / malformed status.
func extractConditions(obj *unstructured.Unstructured) []conditionEntry {
	if obj == nil {
		return nil
	}
	raw, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !found || err != nil {
		return nil
	}
	out := make([]conditionEntry, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		e := conditionEntry{}
		if v, ok := m["type"].(string); ok {
			e.Type = v
		}
		if v, ok := m["status"].(string); ok {
			e.Status = v
		}
		if v, ok := m["reason"].(string); ok {
			e.Reason = v
		}
		if v, ok := m["lastTransitionTime"].(string); ok {
			if ts, err := time.Parse(time.RFC3339, v); err == nil {
				e.LastTransitionTime = ts
			}
		}
		out = append(out, e)
	}
	return out
}

// ConditionsMet is true iff every required condition on `spec.conditions`
// matches on obj. AND semantics: any single required condition that fails
// to match causes the whole gate to fail.
//
// Per-condition rules:
//   - condition with matching Type MUST exist on obj
//   - condition.Status MUST equal req.Status (e.g. "True")
//   - if req.Reason set: condition.Reason MUST equal req.Reason
//   - if req.TTL > 0: (now - condition.LastTransitionTime) MUST be >= req.TTL seconds
//     (the condition must have been in the required state for at least TTL —
//     mirrors Testkube's "condition has been stable for TTL" gate)
//
// Timeout+Delay from ConditionSpec are gate-manager concerns (elapsed
// timing), not this pure function.
//
// A nil / empty spec matches trivially (no conditions required).
func ConditionsMet(spec *testsv1alpha1.TriggerConditionSpec, obj *unstructured.Unstructured, now time.Time) bool {
	if spec == nil || len(spec.Conditions) == 0 {
		return true
	}
	got := extractConditions(obj)
	// `want` = one required condition from the trigger spec. Kept out of
	// the shortnames `req` / `cond` so the grep_guard_test.go regex on
	// `\breq\.Type\b` doesn't confuse this NEW field with the retired
	// ExecutionRequest.Type from the pre-workflows model.
	for _, want := range spec.Conditions {
		match := findCondition(got, want.Type)
		if match == nil {
			return false
		}
		if match.Status != want.Status {
			return false
		}
		if want.Reason != "" && match.Reason != want.Reason {
			return false
		}
		if want.TTL > 0 {
			settled := now.Sub(match.LastTransitionTime)
			if settled < time.Duration(want.TTL)*time.Second {
				return false
			}
		}
	}
	return true
}

func findCondition(cs []conditionEntry, typ string) *conditionEntry {
	for i := range cs {
		if cs[i].Type == typ {
			return &cs[i]
		}
	}
	return nil
}
