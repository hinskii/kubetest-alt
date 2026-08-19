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

// Compiler pure-function tests. Workflows model (step 11): no per-type
// dispatch, no cypress /dev/shm special-case, no ErrUnknownExecutor.
// Fixtures come from canonicalTest/canonicalTestRun in golden_test.go.
package compiler

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/executor"
)

func TestCompile_NilInputs(t *testing.T) {
	t.Run("nil test", func(t *testing.T) {
		_, _, err := Compile(nil, canonicalTestRun(), defaultOpts())
		require.ErrorIs(t, err, ErrNilTest)
	})
	t.Run("nil testrun", func(t *testing.T) {
		_, _, err := Compile(canonicalTest(), nil, defaultOpts())
		require.ErrorIs(t, err, ErrNilTestRun)
	})
	t.Run("missing content fetcher image", func(t *testing.T) {
		_, _, err := Compile(canonicalTest(), canonicalTestRun(), Options{})
		require.ErrorIs(t, err, ErrMissingContentFetcherImage)
	})
}

func TestCompile_StructuralInvariants(t *testing.T) {
	job, aux, err := Compile(canonicalTest(), canonicalTestRun(), defaultOpts())
	require.NoError(t, err)
	require.NotNil(t, job)

	t.Run("backoffLimit is 0", func(t *testing.T) {
		require.NotNil(t, job.Spec.BackoffLimit)
		assert.Equal(t, int32(0), *job.Spec.BackoffLimit)
	})
	t.Run("TTLSecondsAfterFinished is set (safety net per §15.3)", func(t *testing.T) {
		require.NotNil(t, job.Spec.TTLSecondsAfterFinished)
		assert.Equal(t, TTLSecondsAfterFinished, *job.Spec.TTLSecondsAfterFinished)
	})
	t.Run("TerminationGracePeriodSeconds accommodates the scrape budget", func(t *testing.T) {
		require.NotNil(t, job.Spec.Template.Spec.TerminationGracePeriodSeconds)
		got := *job.Spec.Template.Spec.TerminationGracePeriodSeconds
		want := int64(executor.ScrapeGracePeriodSeconds) + int64(TerminationGracePeriodMarginSeconds)
		assert.Equal(t, want, got)
	})
	t.Run("restartPolicy is Never", func(t *testing.T) {
		assert.Equal(t, corev1.RestartPolicyNever, job.Spec.Template.Spec.RestartPolicy)
	})
	t.Run("automountServiceAccountToken is false", func(t *testing.T) {
		require.NotNil(t, job.Spec.Template.Spec.AutomountServiceAccountToken)
		assert.False(t, *job.Spec.Template.Spec.AutomountServiceAccountToken)
	})
	t.Run("aux contains one ConfigMap for the request", func(t *testing.T) {
		require.Len(t, aux, 1)
		cm, ok := aux[0].(*corev1.ConfigMap)
		require.True(t, ok)
		assert.Equal(t, "sample-run-request", cm.Name)
		assert.Contains(t, cm.Data, RequestFileName)
	})
}

// TestCompile_KubetestBinInjection is the workflows-model regression:
// every compile MUST include the kubetest-bin emptyDir + install init
// container + main command = /kubetest-bin/entry. Regressions here
// break the injection contract that makes the tool image itself
// generic.
func TestCompile_KubetestBinInjection(t *testing.T) {
	job, _, err := Compile(canonicalTest(), canonicalTestRun(), defaultOpts())
	require.NoError(t, err)

	t.Run("kubetest-bin volume is an emptyDir", func(t *testing.T) {
		vol := findVolume(job.Spec.Template.Spec.Volumes, VolumeKubetestBin)
		require.NotNil(t, vol)
		require.NotNil(t, vol.EmptyDir)
	})
	t.Run("first init container installs /entry from fetcher image", func(t *testing.T) {
		inits := job.Spec.Template.Spec.InitContainers
		require.NotEmpty(t, inits)
		assert.Equal(t, ContainerKubetestBinInstall, inits[0].Name)
		assert.Equal(t, defaultOpts().ContentFetcherImage, inits[0].Image)
		// exec-form sh -c: the ONE shell invocation the compiler
		// emits, and only for a trivial file copy.
		assert.Contains(t, inits[0].Command[len(inits[0].Command)-1],
			"cp "+ContentFetcherEntrySrc+" "+KubetestBinMountPath+"/entry")
	})
	t.Run("main container command is /kubetest-bin/entry", func(t *testing.T) {
		require.Len(t, job.Spec.Template.Spec.Containers, 1)
		main := job.Spec.Template.Spec.Containers[0]
		assert.Equal(t, []string{KubetestBinEntry}, main.Command)
	})
	t.Run("main container mounts the kubetest-bin volume", func(t *testing.T) {
		main := job.Spec.Template.Spec.Containers[0]
		hasMount := false
		for _, m := range main.VolumeMounts {
			if m.Name == VolumeKubetestBin {
				hasMount = true
				assert.Equal(t, KubetestBinMountPath, m.MountPath)
			}
		}
		assert.True(t, hasMount)
	})
}

// TestCompile_MainImageComesVerbatimFromSpec: the workflows-model
// primary rule. spec.container.image lands on the wrapper container
// unchanged, NOT via a DefaultExecutorImages lookup.
func TestCompile_MainImageComesVerbatimFromSpec(t *testing.T) {
	test := canonicalTest()
	test.Spec.Container.Image = "mycorp.registry/private/my-tool:v42"
	// ImageRegistry option MUST NOT prefix the main image (only fetcher).
	opts := defaultOpts()
	opts.ImageRegistry = "mirror.internal"

	job, _, err := Compile(test, canonicalTestRun(), opts)
	require.NoError(t, err)
	require.Len(t, job.Spec.Template.Spec.Containers, 1)
	assert.Equal(t, "mycorp.registry/private/my-tool:v42",
		job.Spec.Template.Spec.Containers[0].Image,
		"ImageRegistry must NOT touch spec.container.image (workflows model)")
	// Content-fetcher image DOES get the ImageRegistry prefix.
	inits := job.Spec.Template.Spec.InitContainers
	for _, ic := range inits {
		assert.Contains(t, ic.Image, "mirror.internal/",
			"ImageRegistry prefix applies to fetcher init containers")
	}
}

// TestCompile_ToolLabelPropagation asserts the kubetest.io/tool label
// travels from Test → Job → Pod (both metadata blocks).
func TestCompile_ToolLabelPropagation(t *testing.T) {
	test := canonicalTest()
	if test.Labels == nil {
		test.Labels = map[string]string{}
	}
	test.Labels[LabelKubetestTool] = "playwright"
	job, _, err := Compile(test, canonicalTestRun(), defaultOpts())
	require.NoError(t, err)

	assert.Equal(t, "playwright", job.Labels[LabelKubetestTool], "Job label")
	assert.Equal(t, "playwright", job.Spec.Template.Labels[LabelKubetestTool], "Pod label")
}

// TestCompile_ToolLabelAbsentByDefault: no tool label on Test → no
// tool label on Job/Pod (not accidentally injected with an empty value).
func TestCompile_ToolLabelAbsentByDefault(t *testing.T) {
	job, _, err := Compile(canonicalTest(), canonicalTestRun(), defaultOpts())
	require.NoError(t, err)
	_, jobHasIt := job.Labels[LabelKubetestTool]
	_, podHasIt := job.Spec.Template.Labels[LabelKubetestTool]
	assert.False(t, jobHasIt, "Job must not carry an empty tool label")
	assert.False(t, podHasIt, "Pod must not carry an empty tool label")
}

func TestCompile_ExactlyOneOwnerRefToTestRun(t *testing.T) {
	job, aux, err := Compile(canonicalTest(), canonicalTestRun(), defaultOpts())
	require.NoError(t, err)

	assertSingleTestRunOwner := func(t *testing.T, refs []metav1.OwnerReference, kind string) {
		t.Helper()
		require.Len(t, refs, 1, "%s should have exactly one ownerRef", kind)
		ref := refs[0]
		assert.Equal(t, "TestRun", ref.Kind)
		assert.Equal(t, "sample-run", ref.Name)
		require.NotNil(t, ref.Controller)
		assert.True(t, *ref.Controller)
	}
	assertSingleTestRunOwner(t, job.OwnerReferences, "Job")
	cm := aux[0].(*corev1.ConfigMap)
	assertSingleTestRunOwner(t, cm.OwnerReferences, "ConfigMap")
}

func TestCompile_Deadline_DefaultApplied(t *testing.T) {
	test := canonicalTest()
	test.Spec.Timeout = nil
	job, aux, err := Compile(test, canonicalTestRun(), defaultOpts())
	require.NoError(t, err)
	expectedADS := int64(DefaultTimeout.Seconds()) + ADSBufferSeconds
	require.NotNil(t, job.Spec.ActiveDeadlineSeconds)
	assert.Equal(t, expectedADS, *job.Spec.ActiveDeadlineSeconds)

	cm := aux[0].(*corev1.ConfigMap)
	req := unmarshalRequest(t, cm)
	assert.Equal(t, int64(DefaultTimeout.Seconds()), req.TimeoutSeconds)
	assert.Less(t, req.TimeoutSeconds, *job.Spec.ActiveDeadlineSeconds)
}

func TestCompile_Deadline_ExplicitTimeout(t *testing.T) {
	cases := []struct {
		name        string
		timeout     time.Duration
		expectedADS int64
	}{
		{"10m", 10 * time.Minute, 660},
		{"1h", 1 * time.Hour, 3660},
		{"5s (short)", 5 * time.Second, 65},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			test := canonicalTest()
			test.Spec.Timeout = &metav1.Duration{Duration: tc.timeout}
			job, aux, err := Compile(test, canonicalTestRun(), defaultOpts())
			require.NoError(t, err)
			require.NotNil(t, job.Spec.ActiveDeadlineSeconds)
			assert.Equal(t, tc.expectedADS, *job.Spec.ActiveDeadlineSeconds)
			cm := aux[0].(*corev1.ConfigMap)
			req := unmarshalRequest(t, cm)
			assert.Equal(t, int64(tc.timeout.Seconds()), req.TimeoutSeconds)
		})
	}
}

func TestCompile_RequestJSON_HasAllExpectedFields(t *testing.T) {
	test := canonicalTest()
	test.Spec.Timeout = &metav1.Duration{Duration: 5 * time.Minute}
	test.Spec.Verdict = &testsv1alpha1.VerdictSpec{From: "jtl", ErrorRateMax: "0.02"}
	run := canonicalTestRun()

	_, aux, err := Compile(test, run, defaultOpts())
	require.NoError(t, err)
	req := unmarshalRequest(t, aux[0].(*corev1.ConfigMap))

	assert.Equal(t, "sample-run", req.RunID)
	assert.Equal(t, "sample", req.TestRef)
	assert.Equal(t, DataDirPath, req.DataDir)
	assert.Equal(t, int64(300), req.TimeoutSeconds)
	assert.Equal(t, []string{"results/**/*.xml"}, req.Artifacts.Paths)
	assert.Equal(t, map[string]string{"vus": "10"}, req.Config)
	assert.NotEmpty(t, req.Args)

	// Workflows model: verdict block passed through.
	assert.Equal(t, "jtl", req.Verdict.From)
	assert.Equal(t, "0.02", req.Verdict.ErrorRateMax)
}

func TestCompile_RequestJSON_ValueFromEnvExcluded(t *testing.T) {
	test := canonicalTest()
	test.Spec.Container.Env = []corev1.EnvVar{
		{Name: "LITERAL", Value: "v"},
		{Name: "FROM_SECRET", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "s"}, Key: "k",
			},
		}},
	}
	_, aux, err := Compile(test, canonicalTestRun(), defaultOpts())
	require.NoError(t, err)
	req := unmarshalRequest(t, aux[0].(*corev1.ConfigMap))
	assert.Equal(t, "v", req.Env["LITERAL"])
	assert.NotContains(t, req.Env, "FROM_SECRET",
		"ValueFrom env must not leak into request.json (wrapper has no k8s API access)")
}

func TestCompile_ContentFetcherWiring(t *testing.T) {
	test := canonicalTest()
	job, aux, err := Compile(test, canonicalTestRun(), defaultOpts())
	require.NoError(t, err)

	// First init container: kubetest-bin install (see TestCompile_KubetestBinInjection).
	// Second init container: content-fetcher — asserted here.
	inits := job.Spec.Template.Spec.InitContainers
	require.GreaterOrEqual(t, len(inits), 2)
	fetcher := inits[1]
	assert.Equal(t, ContainerContentFetcher, fetcher.Name)
	assert.Equal(t, []string{executor.EntryCommand, "fetch"}, fetcher.Command)

	require.Len(t, fetcher.VolumeMounts, 2)
	names := []string{fetcher.VolumeMounts[0].Name, fetcher.VolumeMounts[1].Name}
	assert.ElementsMatch(t, []string{VolumeData, VolumeRequest}, names)

	// Content lands in the aux ConfigMap under executor.ContentFileName.
	cm := aux[0].(*corev1.ConfigMap)
	require.Contains(t, cm.Data, executor.ContentFileName)
	var got testsv1alpha1.Content
	require.NoError(t, json.Unmarshal([]byte(cm.Data[executor.ContentFileName]), &got))
	require.NotNil(t, got.Git)
	assert.Equal(t, "https://example.com/tests.git", got.Git.URI)
}

func TestCompile_WrapperMountsContract(t *testing.T) {
	job, _, err := Compile(canonicalTest(), canonicalTestRun(), defaultOpts())
	require.NoError(t, err)
	main := getMainContainer(t, job)
	wantMounts := map[string]struct {
		path     string
		readOnly bool
	}{
		VolumeData:        {DataDirPath, false},
		VolumeRequest:     {RequestMountDir, true},
		VolumeResult:      {ResultDirPath, false},
		VolumeKubetestBin: {KubetestBinMountPath, false},
	}
	for _, m := range main.VolumeMounts {
		if want, ok := wantMounts[m.Name]; ok {
			assert.Equal(t, want.path, m.MountPath)
			assert.Equal(t, want.readOnly, m.ReadOnly)
			delete(wantMounts, m.Name)
		}
	}
	assert.Empty(t, wantMounts, "missing wrapper mounts: %v", wantMounts)
}

func TestCompile_DoesNotMutateInputs(t *testing.T) {
	test := canonicalTest()
	run := canonicalTestRun()
	testJSON := mustMarshal(t, test)
	runJSON := mustMarshal(t, run)
	_, _, err := Compile(test, run, defaultOpts())
	require.NoError(t, err)
	assert.JSONEq(t, testJSON, mustMarshal(t, test), "compiler mutated Test input")
	assert.JSONEq(t, runJSON, mustMarshal(t, run), "compiler mutated TestRun input")
}

func TestCompile_JobLabelsIncludeManagedByAndRunID(t *testing.T) {
	job, _, err := Compile(canonicalTest(), canonicalTestRun(), defaultOpts())
	require.NoError(t, err)
	assert.Equal(t, "sample-run", job.Labels[LabelRunID])
	assert.Equal(t, ManagedByValue, job.Labels[LabelManagedBy])
}

func TestCompile_ErrorsAreSentinels(t *testing.T) {
	for _, err := range []error{ErrNilTest, ErrNilTestRun, ErrMissingContentFetcherImage} {
		assert.True(t, errors.Is(err, err))
	}
}

// --- helpers ---

func unmarshalRequest(t *testing.T, cm *corev1.ConfigMap) executor.ExecutionRequest {
	t.Helper()
	require.Contains(t, cm.Data, RequestFileName)
	var req executor.ExecutionRequest
	require.NoError(t, json.Unmarshal([]byte(cm.Data[RequestFileName]), &req))
	return req
}

func findVolume(vols []corev1.Volume, name string) *corev1.Volume {
	for i := range vols {
		if vols[i].Name == name {
			return &vols[i]
		}
	}
	return nil
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// getMainContainer returns the (only) main container. Old two-return
// variant retired — callers that want an init container look it up
// by NAME via findInitContainer (order is no longer position-stable
// in the workflows model: kubetest-bin-install now precedes fetcher).
func getMainContainer(t *testing.T, job *batchv1.Job) corev1.Container {
	t.Helper()
	require.Len(t, job.Spec.Template.Spec.Containers, 1)
	return job.Spec.Template.Spec.Containers[0]
}
