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

package v1alpha1

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

func TestTestDefaulter_ConcurrencyPolicy(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"unset -> Allow", "", DefaultConcurrencyPolicy},
		{"Allow preserved", "Allow", "Allow"},
		{"Forbid preserved", "Forbid", "Forbid"},
		{"Replace preserved", "Replace", "Replace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obj := &testsv1alpha1.Test{Spec: testsv1alpha1.TestSpec{
				Type:              "k6",
				ConcurrencyPolicy: tc.in,
			}}
			require.NoError(t, (&TestCustomDefaulter{}).Default(context.Background(), obj))
			assert.Equal(t, tc.want, obj.Spec.ConcurrencyPolicy)
		})
	}
}

// TestTestDefaulter_DoesNotTouchPodAnnotations is the §8 regression guard on
// the defaulter path (the validator has its own guard).
func TestTestDefaulter_DoesNotTouchPodAnnotations(t *testing.T) {
	ann := map[string]string{
		"sidecar.istio.io/inject": "false",
		"custom.io/anything":      strings.Repeat("x", 1024),
	}
	labels := map[string]string{"team": "sre"}
	obj := &testsv1alpha1.Test{Spec: testsv1alpha1.TestSpec{
		Type: "k6",
		Pod: &testsv1alpha1.PodConfig{
			Annotations: cloneStringMap(ann),
			Labels:      cloneStringMap(labels),
		},
	}}
	before := cloneStringMap(obj.Spec.Pod.Annotations)
	beforeLab := cloneStringMap(obj.Spec.Pod.Labels)

	require.NoError(t, (&TestCustomDefaulter{}).Default(context.Background(), obj))

	assert.Equal(t, before, obj.Spec.Pod.Annotations, "defaulter mutated pod annotations")
	assert.Equal(t, beforeLab, obj.Spec.Pod.Labels, "defaulter mutated pod labels")
}

func TestTestRunDefaulter_Source(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"unset -> api", "", DefaultSource},
		{"ui preserved", "ui", "ui"},
		{"cli preserved", "cli", "cli"},
		{"cron preserved", "cron", "cron"},
		{"trigger preserved", "trigger", "trigger"},
		{"gitops preserved", "gitops", "gitops"},
		{"api explicit preserved", "api", "api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obj := &testsv1alpha1.TestRun{Spec: testsv1alpha1.TestRunSpec{
				TestRef: "my-test",
				Source:  tc.in,
			}}
			require.NoError(t, (&TestRunCustomDefaulter{}).Default(context.Background(), obj))
			assert.Equal(t, tc.want, obj.Spec.Source)
		})
	}
}

// TestTestRunDefaulter_DoesNotTouchPodAnnotations mirrors the Test-side guard.
func TestTestRunDefaulter_DoesNotTouchPodAnnotations(t *testing.T) {
	ann := map[string]string{"sidecar.istio.io/inject": "false"}
	obj := &testsv1alpha1.TestRun{Spec: testsv1alpha1.TestRunSpec{
		TestRef: "my-test",
		Pod:     &testsv1alpha1.PodConfig{Annotations: cloneStringMap(ann)},
	}}
	before := cloneStringMap(obj.Spec.Pod.Annotations)

	require.NoError(t, (&TestRunCustomDefaulter{}).Default(context.Background(), obj))

	assert.Equal(t, before, obj.Spec.Pod.Annotations)
}
