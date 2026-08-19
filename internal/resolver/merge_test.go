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

package resolver

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// TestResolve_Template_ContributesAllFragmentTypes: exercises every
// TestTemplateSpec field the resolver merges — content, container,
// pod, config, artifacts, timeout, retry, services, parallel — so a
// future refactor that drops a merge branch trips this test.
func TestResolve_Template_ContributesAllFragmentTypes(t *testing.T) {
	one := int32(1)
	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "sample", Namespace: "ns"},
		Spec: testsv1alpha1.TestSpec{
			// Only spec.use — everything else comes from the template.
			Use: []string{"complete"},
		},
	}
	tmpl := &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "complete", Namespace: "ns"},
		Spec: testsv1alpha1.TestTemplateSpec{
			Content: testsv1alpha1.Content{
				Git: &testsv1alpha1.GitContent{
					URI: "https://example.com/x.git", Revision: "main",
				},
				Files: []testsv1alpha1.FileContent{
					{Path: "one.txt", Content: "one"},
				},
				Tarball: []testsv1alpha1.Tarball{
					{URL: "https://example.com/x.tgz"},
				},
			},
			Container: testsv1alpha1.ContainerConfig{
				Image: "grafana/k6:2.2.0",
				Args:  []string{"run", "-"},
				Env:   []corev1.EnvVar{{Name: "A", Value: "1"}},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("100m"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
				},
			},
			Pod: &testsv1alpha1.PodConfig{
				ServiceAccountName: "runner",
				NodeSelector:       map[string]string{"role": "test"},
				Tolerations:        []corev1.Toleration{{Key: "dedicated", Value: "test"}},
				Volumes:            []corev1.Volume{{Name: "extra"}},
				ImagePullSecrets:   []corev1.LocalObjectReference{{Name: "regcred"}},
			},
			Config: map[string]testsv1alpha1.Parameter{
				"vus": {Type: "integer", Default: "10"},
			},
			Artifacts: &testsv1alpha1.ArtifactSpec{Paths: []string{"results/**"}},
			Timeout:   &metav1.Duration{Duration: 7 * time.Minute},
			Retry:     &testsv1alpha1.RetryPolicy{Count: 2},
			Services: map[string]testsv1alpha1.ServiceSpec{
				"redis": {Image: "redis:7"},
			},
			Parallel: &testsv1alpha1.ParallelSpec{Count: &one},
		},
	}
	store := MapStore{"ns/complete": tmpl}

	spec, err := Resolve(test, mkRun(), store, Options{})
	require.NoError(t, err)

	// Content
	require.NotNil(t, spec.Content.Git)
	assert.Equal(t, "https://example.com/x.git", spec.Content.Git.URI)
	assert.Len(t, spec.Content.Files, 1)
	assert.Len(t, spec.Content.Tarball, 1)
	// Container
	assert.Equal(t, "grafana/k6:2.2.0", spec.Container.Image)
	assert.Equal(t, []string{"run", "-"}, spec.Container.Args)
	assert.Len(t, spec.Container.Env, 1)
	assert.NotEmpty(t, spec.Container.Resources.Requests)
	assert.NotEmpty(t, spec.Container.Resources.Limits)
	// Pod
	require.NotNil(t, spec.Pod)
	assert.Equal(t, "runner", spec.Pod.ServiceAccountName)
	assert.Equal(t, map[string]string{"role": "test"}, spec.Pod.NodeSelector)
	assert.Len(t, spec.Pod.Tolerations, 1)
	assert.Len(t, spec.Pod.Volumes, 1)
	assert.Len(t, spec.Pod.ImagePullSecrets, 1)
	// Config / Artifacts / Timeout / Retry / Services / Parallel
	assert.NotEmpty(t, spec.Config)
	require.NotNil(t, spec.Artifacts)
	assert.Equal(t, []string{"results/**"}, spec.Artifacts.Paths)
	require.NotNil(t, spec.Timeout)
	assert.Equal(t, 7*time.Minute, spec.Timeout.Duration)
	require.NotNil(t, spec.Retry)
	assert.Equal(t, int32(2), spec.Retry.Count)
	assert.NotEmpty(t, spec.Services)
	require.NotNil(t, spec.Parallel)
}

// TestResolve_TestOverridesTemplate_AcrossAllFields: mirrors the previous
// test but has the Test explicitly override every field. Test wins.
func TestResolve_TestOverridesTemplate_AcrossAllFields(t *testing.T) {
	two := int32(2)
	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "sample", Namespace: "ns"},
		Spec: testsv1alpha1.TestSpec{
			Use: []string{"base"},
			Content: testsv1alpha1.Content{
				Git: &testsv1alpha1.GitContent{URI: "test/uri.git"},
			},
			Container: testsv1alpha1.ContainerConfig{
				Image: "test-override:v1",
				Args:  []string{"test-args"},
			},
			Pod: &testsv1alpha1.PodConfig{
				ServiceAccountName: "test-sa",
				Tolerations:        []corev1.Toleration{{Key: "test-key"}},
				Volumes:            []corev1.Volume{{Name: "test-vol"}},
				ImagePullSecrets:   []corev1.LocalObjectReference{{Name: "test-secret"}},
			},
			Artifacts:         &testsv1alpha1.ArtifactSpec{Paths: []string{"test/**"}},
			Timeout:           &metav1.Duration{Duration: 1 * time.Minute},
			Retry:             &testsv1alpha1.RetryPolicy{Count: 3},
			ConcurrencyPolicy: "Replace",
			Verdict:           &testsv1alpha1.VerdictSpec{From: "junit"},
			Schedule:          "*/5 * * * *",
			Services: map[string]testsv1alpha1.ServiceSpec{
				"kafka": {Image: "kafka:test"},
			},
			Parallel: &testsv1alpha1.ParallelSpec{Count: &two},
		},
	}
	tmpl := &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "base", Namespace: "ns"},
		Spec: testsv1alpha1.TestTemplateSpec{
			Content: testsv1alpha1.Content{
				Git: &testsv1alpha1.GitContent{URI: "tmpl/uri.git"},
			},
			Container: testsv1alpha1.ContainerConfig{
				Image: "tmpl-image:v0",
				Args:  []string{"tmpl-args"},
			},
			Pod: &testsv1alpha1.PodConfig{
				ServiceAccountName: "tmpl-sa",
				Tolerations:        []corev1.Toleration{{Key: "tmpl-key"}},
				Volumes:            []corev1.Volume{{Name: "tmpl-vol"}},
				ImagePullSecrets:   []corev1.LocalObjectReference{{Name: "tmpl-secret"}},
			},
			Artifacts: &testsv1alpha1.ArtifactSpec{Paths: []string{"tmpl/**"}},
			Timeout:   &metav1.Duration{Duration: 99 * time.Minute},
			Retry:     &testsv1alpha1.RetryPolicy{Count: 99},
			Services: map[string]testsv1alpha1.ServiceSpec{
				"redis": {Image: "redis:tmpl"},
			},
			Parallel: &testsv1alpha1.ParallelSpec{Count: pInt32(99)},
		},
	}
	store := MapStore{"ns/base": tmpl}

	spec, err := Resolve(test, mkRun(), store, Options{})
	require.NoError(t, err)
	// Test wins on every set field.
	assert.Equal(t, "test/uri.git", spec.Content.Git.URI)
	assert.Equal(t, "test-override:v1", spec.Container.Image)
	assert.Equal(t, []string{"test-args"}, spec.Container.Args)
	assert.Equal(t, "test-sa", spec.Pod.ServiceAccountName)
	assert.Equal(t, "test-key", spec.Pod.Tolerations[0].Key,
		"Test's tolerations REPLACE template's")
	assert.Equal(t, "test-vol", spec.Pod.Volumes[0].Name)
	assert.Equal(t, "test-secret", spec.Pod.ImagePullSecrets[0].Name)
	assert.Equal(t, []string{"test/**"}, spec.Artifacts.Paths)
	assert.Equal(t, 1*time.Minute, spec.Timeout.Duration)
	assert.Equal(t, int32(3), spec.Retry.Count)
	assert.Equal(t, "Replace", spec.ConcurrencyPolicy)
	assert.Equal(t, "junit", spec.Verdict.From)
	assert.Equal(t, "*/5 * * * *", spec.Schedule)
	assert.Equal(t, "kafka:test", spec.Services["kafka"].Image)
	assert.Equal(t, int32(2), *spec.Parallel.Count)
}

// TestResolve_PodNodeSelector_MergedTemplateFirstTestWins verifies the
// per-key merge semantics on maps that aren't labels/annotations.
func TestResolve_PodNodeSelector_MergedTemplateFirstTestWins(t *testing.T) {
	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "sample", Namespace: "ns"},
		Spec: testsv1alpha1.TestSpec{
			Use: []string{"tpl"},
			Container: testsv1alpha1.ContainerConfig{
				Image: "x", Args: []string{"y"},
			},
			Pod: &testsv1alpha1.PodConfig{
				NodeSelector: map[string]string{
					"role":         "override",
					"only-on-test": "yes",
				},
			},
		},
	}
	tmpl := &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "tpl", Namespace: "ns"},
		Spec: testsv1alpha1.TestTemplateSpec{
			Pod: &testsv1alpha1.PodConfig{
				NodeSelector: map[string]string{
					"role":         "tmpl-default", // Test overrides
					"only-on-tmpl": "true",         // survives (Test doesn't touch)
				},
			},
		},
	}
	store := MapStore{"ns/tpl": tmpl}

	spec, err := Resolve(test, mkRun(), store, Options{})
	require.NoError(t, err)
	require.NotNil(t, spec.Pod)
	assert.Equal(t, "override", spec.Pod.NodeSelector["role"])
	assert.Equal(t, "yes", spec.Pod.NodeSelector["only-on-test"])
	assert.Equal(t, "true", spec.Pod.NodeSelector["only-on-tmpl"])
}

// TestResolve_PodLabels_TemplateFirst_TestWins_ReservedProtectionElsewhere:
// PodConfig.Labels merge with Test overriding template per key. The
// reserved-prefix protection (kubetest.io/*) is applied by the compiler
// (podmerge.go) NOT the resolver — that keeps the resolver §8-compliant
// (zero operator-injected keys) and defers the reserved-key drop to the
// compile step. This test just asserts the merge shape.
func TestResolve_PodLabels_TemplateFirst_TestWins(t *testing.T) {
	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "sample", Namespace: "ns"},
		Spec: testsv1alpha1.TestSpec{
			Use: []string{"tpl"},
			Container: testsv1alpha1.ContainerConfig{
				Image: "x", Args: []string{"y"},
			},
			Pod: &testsv1alpha1.PodConfig{
				Labels: map[string]string{
					"team": "sre",
				},
			},
		},
	}
	tmpl := &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "tpl", Namespace: "ns"},
		Spec: testsv1alpha1.TestTemplateSpec{
			Pod: &testsv1alpha1.PodConfig{
				Labels: map[string]string{
					"team":             "override-me", // Test wins
					"kubetest.io/tool": "k6",          // reserved — dropped by compiler, not us
				},
			},
		},
	}
	store := MapStore{"ns/tpl": tmpl}

	spec, err := Resolve(test, mkRun(), store, Options{})
	require.NoError(t, err)
	require.NotNil(t, spec.Pod)
	assert.Equal(t, "sre", spec.Pod.Labels["team"])
	// Reserved label DOES appear in the resolved spec — the compiler drops
	// it at pod-template-build time via podmerge.mergeLabels.
	assert.Equal(t, "k6", spec.Pod.Labels["kubetest.io/tool"])
}

// TestResolve_UsePropagatedIntoResolvedSpec: TestRun.status.resolvedSpec
// contains spec.use[] so a re-inspection of a historical run reveals
// which templates contributed.
func TestResolve_UsePropagatedIntoResolvedSpec(t *testing.T) {
	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Spec: testsv1alpha1.TestSpec{
			Use: []string{"a", "b"},
			Container: testsv1alpha1.ContainerConfig{
				Image: "x", Args: []string{"y"},
			},
		},
	}
	store := MapStore{
		"ns/a": {
			ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"},
			Spec:       testsv1alpha1.TestTemplateSpec{},
		},
		"ns/b": {
			ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns"},
			Spec:       testsv1alpha1.TestTemplateSpec{},
		},
	}
	spec, err := Resolve(test, mkRun(), store, Options{})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, spec.Use)
}

// TestResolve_PodOnlyOnTemplate: Test has no pod block; template contributes
// full PodConfig — resolver clones it, output is not the same pointer.
func TestResolve_PodOnlyOnTemplate(t *testing.T) {
	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Spec: testsv1alpha1.TestSpec{
			Use: []string{"tpl"},
			Container: testsv1alpha1.ContainerConfig{
				Image: "x", Args: []string{"y"},
			},
		},
	}
	tmpl := &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "tpl", Namespace: "ns"},
		Spec: testsv1alpha1.TestTemplateSpec{
			Pod: &testsv1alpha1.PodConfig{
				Annotations: map[string]string{"foo": "bar"},
			},
		},
	}
	store := MapStore{"ns/tpl": tmpl}
	spec, err := Resolve(test, mkRun(), store, Options{})
	require.NoError(t, err)
	require.NotNil(t, spec.Pod)
	assert.Equal(t, "bar", spec.Pod.Annotations["foo"])
	// Prove decoupled from template — mutating output leaves template alone.
	spec.Pod.Annotations["foo"] = "mutated"
	assert.Equal(t, "bar", tmpl.Spec.Pod.Annotations["foo"],
		"resolver must deep-copy templates, not alias them")
}

// TestResolve_TemplateStoreError_Propagated: non-ErrTemplateNotFound errors
// from the store must be wrapped and surfaced (e.g. transient api-server
// hiccup shouldn't be misclassified as "template missing").
func TestResolve_TemplateStoreError_Propagated(t *testing.T) {
	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Spec: testsv1alpha1.TestSpec{
			Use:       []string{"a"},
			Container: testsv1alpha1.ContainerConfig{Image: "x", Args: []string{"y"}},
		},
	}
	store := errStore{err: assertingError{}}
	_, err := Resolve(test, mkRun(), store, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetch template")
}

type assertingError struct{}

func (assertingError) Error() string { return "backend down" }

type errStore struct{ err error }

func (e errStore) Get(_, _ string) (*testsv1alpha1.TestTemplate, error) {
	return nil, e.err
}

func pInt32(v int32) *int32 { return &v }
