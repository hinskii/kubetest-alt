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
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// deploymentWithConditions builds an unstructured Deployment with the given
// status.conditions array. LastTransitionTime is set on each entry if the
// entry's LTT is not zero (helper defaults to now-1h if the caller passed a
// zero time — otherwise TTL checks would trivially pass). Also used by the
// gate manager tests below; name is intentionally a parameter for future
// scenarios where more than one target object matters.
// nolint:unparam
func deploymentWithConditions(name string, conds []conditionEntry) *unstructured.Unstructured {
	obj := makeObj(name, "default", nil)
	rawConds := make([]any, 0, len(conds))
	for _, c := range conds {
		entry := map[string]any{
			"type":   c.Type,
			"status": c.Status,
			"reason": c.Reason,
		}
		if !c.LastTransitionTime.IsZero() {
			entry["lastTransitionTime"] = c.LastTransitionTime.UTC().Format(time.RFC3339)
		}
		rawConds = append(rawConds, entry)
	}
	_ = unstructured.SetNestedSlice(obj.Object, rawConds, "status", "conditions")
	return obj
}

// TestConditionsMet_Table covers TTL, reason matching, missing condition,
// multi-condition AND semantics.
func TestConditionsMet_Table(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		spec *testsv1alpha1.TriggerConditionSpec
		obj  *unstructured.Unstructured
		want bool
	}{
		{
			name: "nil spec ⇒ trivially met",
			spec: nil,
			obj:  deploymentWithConditions("d", nil),
			want: true,
		},
		{
			name: "empty conditions ⇒ trivially met",
			spec: &testsv1alpha1.TriggerConditionSpec{},
			obj:  deploymentWithConditions("d", nil),
			want: true,
		},
		{
			name: "single condition present and matching status ⇒ true",
			spec: &testsv1alpha1.TriggerConditionSpec{
				Conditions: []testsv1alpha1.TriggerCondition{
					{Type: "Available", Status: "True"},
				},
			},
			obj: deploymentWithConditions("d", []conditionEntry{
				{Type: "Available", Status: "True"},
			}),
			want: true,
		},
		{
			name: "condition present but wrong status ⇒ false",
			spec: &testsv1alpha1.TriggerConditionSpec{
				Conditions: []testsv1alpha1.TriggerCondition{
					{Type: "Available", Status: "True"},
				},
			},
			obj: deploymentWithConditions("d", []conditionEntry{
				{Type: "Available", Status: "False"},
			}),
			want: false,
		},
		{
			name: "condition missing entirely ⇒ false",
			spec: &testsv1alpha1.TriggerConditionSpec{
				Conditions: []testsv1alpha1.TriggerCondition{
					{Type: "Available", Status: "True"},
				},
			},
			obj:  deploymentWithConditions("d", nil),
			want: false,
		},
		{
			name: "reason match required and matches ⇒ true",
			spec: &testsv1alpha1.TriggerConditionSpec{
				Conditions: []testsv1alpha1.TriggerCondition{
					{Type: "Progressing", Status: "True", Reason: "NewReplicaSetAvailable"},
				},
			},
			obj: deploymentWithConditions("d", []conditionEntry{
				{Type: "Progressing", Status: "True", Reason: "NewReplicaSetAvailable"},
			}),
			want: true,
		},
		{
			name: "reason match required but mismatches ⇒ false",
			spec: &testsv1alpha1.TriggerConditionSpec{
				Conditions: []testsv1alpha1.TriggerCondition{
					{Type: "Progressing", Status: "True", Reason: "NewReplicaSetAvailable"},
				},
			},
			obj: deploymentWithConditions("d", []conditionEntry{
				{Type: "Progressing", Status: "True", Reason: "ReplicaSetUpdated"},
			}),
			want: false,
		},
		{
			name: "TTL=60s but condition transitioned 30s ago ⇒ false (not settled)",
			spec: &testsv1alpha1.TriggerConditionSpec{
				Conditions: []testsv1alpha1.TriggerCondition{
					{Type: "Available", Status: "True", TTL: 60},
				},
			},
			obj: deploymentWithConditions("d", []conditionEntry{
				{Type: "Available", Status: "True", LastTransitionTime: now.Add(-30 * time.Second)},
			}),
			want: false,
		},
		{
			name: "TTL=60s and condition transitioned 90s ago ⇒ true (settled)",
			spec: &testsv1alpha1.TriggerConditionSpec{
				Conditions: []testsv1alpha1.TriggerCondition{
					{Type: "Available", Status: "True", TTL: 60},
				},
			},
			obj: deploymentWithConditions("d", []conditionEntry{
				{Type: "Available", Status: "True", LastTransitionTime: now.Add(-90 * time.Second)},
			}),
			want: true,
		},
		{
			name: "TTL=0 ⇒ no settlement requirement, matches immediately",
			spec: &testsv1alpha1.TriggerConditionSpec{
				Conditions: []testsv1alpha1.TriggerCondition{
					{Type: "Available", Status: "True", TTL: 0},
				},
			},
			obj: deploymentWithConditions("d", []conditionEntry{
				{Type: "Available", Status: "True"},
			}),
			want: true,
		},
		{
			name: "multi-condition AND — one required missing ⇒ false",
			spec: &testsv1alpha1.TriggerConditionSpec{
				Conditions: []testsv1alpha1.TriggerCondition{
					{Type: "Available", Status: "True"},
					{Type: "Progressing", Status: "True", Reason: "NewReplicaSetAvailable"},
				},
			},
			obj: deploymentWithConditions("d", []conditionEntry{
				{Type: "Available", Status: "True"},
				// Progressing missing entirely.
			}),
			want: false,
		},
		{
			name: "multi-condition AND — all required present + matching ⇒ true",
			spec: &testsv1alpha1.TriggerConditionSpec{
				Conditions: []testsv1alpha1.TriggerCondition{
					{Type: "Available", Status: "True"},
					{Type: "Progressing", Status: "True", Reason: "NewReplicaSetAvailable"},
				},
			},
			obj: deploymentWithConditions("d", []conditionEntry{
				{Type: "Available", Status: "True"},
				{Type: "Progressing", Status: "True", Reason: "NewReplicaSetAvailable"},
			}),
			want: true,
		},
		{
			name: "object has status.conditions=null → treat as no conditions ⇒ false when required",
			spec: &testsv1alpha1.TriggerConditionSpec{
				Conditions: []testsv1alpha1.TriggerCondition{
					{Type: "Available", Status: "True"},
				},
			},
			obj:  makeObj("d", "default", nil), // no status field at all
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ConditionsMet(tc.spec, tc.obj, now))
		})
	}
}
