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
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/executor"
)

// newValidTest returns a minimal Test that Compile accepts for the given executor
// type. Individual tests mutate a copy to isolate the field under test.
func newValidTest(execType string) *testsv1alpha1.Test {
	return &testsv1alpha1.Test{
		TypeMeta:   metav1.TypeMeta{APIVersion: testsv1alpha1.SchemeGroupVersion.String(), Kind: "Test"},
		ObjectMeta: metav1.ObjectMeta{Name: "sample-" + execType, Namespace: "default"},
		Spec:       testsv1alpha1.TestSpec{Type: execType, ConcurrencyPolicy: "Allow"},
	}
}

// newValidTestRun returns a minimal TestRun referencing the given Test name.
// UID is fixed for deterministic ownerRef assertions.
func newValidTestRun(name, testRef string) *testsv1alpha1.TestRun {
	return &testsv1alpha1.TestRun{
		TypeMeta: metav1.TypeMeta{APIVersion: testsv1alpha1.SchemeGroupVersion.String(), Kind: "TestRun"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       types.UID("00000000-0000-0000-0000-0000000000ff"),
		},
		Spec: testsv1alpha1.TestRunSpec{TestRef: testRef, Source: "api"},
	}
}

func defaultOpts() Options {
	return Options{ContentFetcherImage: "ghcr.io/hinskii/kubetest-alt/content-fetcher:v0.0.0"}
}

// getJobContainer returns (initContainer, mainContainer) for readability in
// tests that assert on both.
func getJobContainers(t *testing.T, job *batchv1.Job) (corev1.Container, corev1.Container) {
	t.Helper()
	require.Len(t, job.Spec.Template.Spec.InitContainers, 1)
	require.Len(t, job.Spec.Template.Spec.Containers, 1)
	return job.Spec.Template.Spec.InitContainers[0], job.Spec.Template.Spec.Containers[0]
}

func TestCompile_NilInputs(t *testing.T) {
	t.Run("nil test", func(t *testing.T) {
		_, _, err := Compile(nil, newValidTestRun("run", "sample-k6"), defaultOpts())
		require.ErrorIs(t, err, ErrNilTest)
	})
	t.Run("nil testrun", func(t *testing.T) {
		_, _, err := Compile(newValidTest("k6"), nil, defaultOpts())
		require.ErrorIs(t, err, ErrNilTestRun)
	})
	t.Run("missing content fetcher image", func(t *testing.T) {
		_, _, err := Compile(newValidTest("k6"), newValidTestRun("run", "sample-k6"), Options{})
		require.ErrorIs(t, err, ErrMissingContentFetcherImage)
	})
}

func TestCompile_UnknownExecutor(t *testing.T) {
	// Webhook rejects unknown types at admission, but the compiler is a pure
	// function and must reject too — defense in depth for callers that skip
	// admission (tests, offline generation, etc.).
	test := newValidTest("k6")
	test.Spec.Type = "artillery"
	_, _, err := Compile(test, newValidTestRun("run", test.Name), defaultOpts())
	require.ErrorIs(t, err, ErrUnknownExecutor)
	assert.Contains(t, err.Error(), "artillery")
}

func TestCompile_StructuralInvariants(t *testing.T) {
	job, aux, err := Compile(newValidTest("k6"), newValidTestRun("myrun", "sample-k6"), defaultOpts())
	require.NoError(t, err)
	require.NotNil(t, job)

	t.Run("backoffLimit is 0", func(t *testing.T) {
		require.NotNil(t, job.Spec.BackoffLimit)
		assert.Equal(t, int32(0), *job.Spec.BackoffLimit)
	})
	t.Run("TTLSecondsAfterFinished is set (safety net per §15.3)", func(t *testing.T) {
		// TTL is a safety net: reconciler deletes Jobs explicitly after
		// persisting status, but if the controller is down / finalizer stripped
		// / bug in reconcile, TTL prevents the Job from squatting forever.
		require.NotNil(t, job.Spec.TTLSecondsAfterFinished)
		assert.Equal(t, TTLSecondsAfterFinished, *job.Spec.TTLSecondsAfterFinished)
	})
	t.Run("TerminationGracePeriodSeconds accommodates the scrape budget (§15.3)", func(t *testing.T) {
		// Without this, k8s SIGKILLs the pod at the default 30s TGPS — EXACTLY
		// the scrape budget — killing the flush hook the wrapper relies on for
		// partial-artifact upload. TGPS must be scrape budget + margin.
		require.NotNil(t, job.Spec.Template.Spec.TerminationGracePeriodSeconds)
		got := *job.Spec.Template.Spec.TerminationGracePeriodSeconds
		want := int64(executor.ScrapeGracePeriodSeconds) + int64(TerminationGracePeriodMarginSeconds)
		assert.Equal(t, want, got,
			"TGPS must be at least ScrapeGracePeriodSeconds+margin, got %d", got)
		assert.Greater(t, got, int64(executor.ScrapeGracePeriodSeconds),
			"TGPS MUST be strictly greater than scrape budget — no margin = SIGKILL mid-scrape")
	})
	t.Run("restartPolicy is Never", func(t *testing.T) {
		assert.Equal(t, corev1.RestartPolicyNever, job.Spec.Template.Spec.RestartPolicy)
	})
	t.Run("automountServiceAccountToken is false", func(t *testing.T) {
		require.NotNil(t, job.Spec.Template.Spec.AutomountServiceAccountToken)
		assert.False(t, *job.Spec.Template.Spec.AutomountServiceAccountToken)
	})
	t.Run("job name matches TestRun name", func(t *testing.T) {
		assert.Equal(t, "myrun", job.Name)
		assert.Equal(t, "default", job.Namespace)
	})
	t.Run("aux contains one ConfigMap for the request", func(t *testing.T) {
		require.Len(t, aux, 1)
		cm, ok := aux[0].(*corev1.ConfigMap)
		require.True(t, ok, "aux[0] must be *corev1.ConfigMap")
		assert.Equal(t, "myrun-request", cm.Name)
		assert.Contains(t, cm.Data, RequestFileName)
	})
}

func TestCompile_ExactlyOneOwnerRefToTestRun(t *testing.T) {
	// §15.5 forbids ownerRef Test→TestRun. This test asserts Job and ConfigMap
	// each carry exactly one ownerRef, both pointing at the TestRun and no other.
	test := newValidTest("k6")
	run := newValidTestRun("myrun", test.Name)
	job, aux, err := Compile(test, run, defaultOpts())
	require.NoError(t, err)

	assertSingleTestRunOwner := func(t *testing.T, refs []metav1.OwnerReference, kind string) {
		t.Helper()
		require.Len(t, refs, 1, "%s should have exactly one ownerRef", kind)
		ref := refs[0]
		assert.Equal(t, "TestRun", ref.Kind, "%s ownerRef kind", kind)
		assert.Equal(t, "myrun", ref.Name, "%s ownerRef name", kind)
		require.NotNil(t, ref.Controller, "%s ownerRef.Controller nil", kind)
		assert.True(t, *ref.Controller, "%s controller flag must be true", kind)
		require.NotNil(t, ref.BlockOwnerDeletion, "%s ownerRef.BlockOwnerDeletion nil", kind)
		assert.True(t, *ref.BlockOwnerDeletion)
		assert.Equal(t, run.UID, ref.UID)
	}

	assertSingleTestRunOwner(t, job.OwnerReferences, "Job")
	cm := aux[0].(*corev1.ConfigMap)
	assertSingleTestRunOwner(t, cm.OwnerReferences, "ConfigMap")
}

func TestCompile_ConfigMapOwnerIsTestRun_NotJob(t *testing.T) {
	// Per user's step-03 refinement: ConfigMap ownerRef → TestRun (NOT Job).
	// Rationale: the operator deletes the Job explicitly after status persistence
	// (§15.3), and we want the request ConfigMap tied to the TestRun's own
	// lifetime — not garbage-collected out from under a still-live run when the
	// Job is torn down early.
	test := newValidTest("k6")
	run := newValidTestRun("myrun", test.Name)
	_, aux, err := Compile(test, run, defaultOpts())
	require.NoError(t, err)
	cm := aux[0].(*corev1.ConfigMap)
	require.Len(t, cm.OwnerReferences, 1)
	assert.Equal(t, "TestRun", cm.OwnerReferences[0].Kind,
		"ConfigMap ownerRef must be TestRun, never Job")
}

func TestCompile_Deadline_DefaultApplied(t *testing.T) {
	// Timeout nil → DefaultTimeout (30m). ADS = 30m + 60s buffer.
	test := newValidTest("k6")
	test.Spec.Timeout = nil
	job, aux, err := Compile(test, newValidTestRun("myrun", test.Name), defaultOpts())
	require.NoError(t, err)

	require.NotNil(t, job.Spec.ActiveDeadlineSeconds)
	expectedADS := int64(DefaultTimeout.Seconds()) + ADSBufferSeconds
	assert.Equal(t, expectedADS, *job.Spec.ActiveDeadlineSeconds, "ADS = DefaultTimeout + buffer")

	// Wrapper's TimeoutSeconds inside request.json must equal DefaultTimeout,
	// strictly less than ADS.
	cm := aux[0].(*corev1.ConfigMap)
	req := unmarshalRequest(t, cm)
	assert.Equal(t, int64(DefaultTimeout.Seconds()), req.TimeoutSeconds)
	assert.Less(t, req.TimeoutSeconds, *job.Spec.ActiveDeadlineSeconds,
		"wrapper timeout must be < Job ADS so scraper runs before SIGKILL")
}

func TestCompile_Deadline_ExplicitTimeout(t *testing.T) {
	cases := []struct {
		name        string
		timeout     time.Duration
		expectedADS int64
	}{
		{"10m", 10 * time.Minute, 660}, // 10m*60 + 60
		{"1h", 1 * time.Hour, 3660},    // 3600 + 60
		{"5s (short)", 5 * time.Second, 65},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			test := newValidTest("k6")
			test.Spec.Timeout = &metav1.Duration{Duration: tc.timeout}
			job, aux, err := Compile(test, newValidTestRun("myrun", test.Name), defaultOpts())
			require.NoError(t, err)

			require.NotNil(t, job.Spec.ActiveDeadlineSeconds)
			assert.Equal(t, tc.expectedADS, *job.Spec.ActiveDeadlineSeconds)

			cm := aux[0].(*corev1.ConfigMap)
			req := unmarshalRequest(t, cm)
			assert.Equal(t, int64(tc.timeout.Seconds()), req.TimeoutSeconds)
			assert.Less(t, req.TimeoutSeconds, *job.Spec.ActiveDeadlineSeconds)
		})
	}
}

func TestCompile_DevShm_OnlyCypress(t *testing.T) {
	// Cypress needs memory-backed /dev/shm; other executors do not (§15.3).
	// This is a full matrix: assert presence for cypress, absence for others.
	cases := map[string]bool{
		"k6":      false,
		"cypress": true,
		"newman":  false,
		"locust":  false,
		"jmeter":  false,
	}
	for execType, wantDevShm := range cases {
		t.Run(execType, func(t *testing.T) {
			test := newValidTest(execType)
			job, _, err := Compile(test, newValidTestRun("myrun", test.Name), defaultOpts())
			require.NoError(t, err)

			hasVolume := findVolume(job.Spec.Template.Spec.Volumes, VolumeDevShm) != nil
			assert.Equal(t, wantDevShm, hasVolume, "volume presence mismatch for %s", execType)

			_, main := getJobContainers(t, job)
			hasMount := false
			for _, m := range main.VolumeMounts {
				if m.Name == VolumeDevShm {
					hasMount = true
					assert.Equal(t, "/dev/shm", m.MountPath)
				}
			}
			assert.Equal(t, wantDevShm, hasMount, "mount presence mismatch for %s", execType)

			if wantDevShm {
				vol := findVolume(job.Spec.Template.Spec.Volumes, VolumeDevShm)
				require.NotNil(t, vol.EmptyDir, "dev-shm must use emptyDir")
				assert.Equal(t, corev1.StorageMediumMemory, vol.EmptyDir.Medium,
					"dev-shm must be memory-backed")
			}
		})
	}
}

func TestCompile_ContentFetcherWiring(t *testing.T) {
	test := newValidTest("k6")
	test.Spec.Content.Git = &testsv1alpha1.GitContent{URI: "https://example.com/x.git", Revision: "main"}

	job, aux, err := Compile(test, newValidTestRun("myrun", test.Name), defaultOpts())
	require.NoError(t, err)

	init, _ := getJobContainers(t, job)
	assert.Equal(t, ContainerContentFetcher, init.Name)
	assert.Equal(t, "ghcr.io/hinskii/kubetest-alt/content-fetcher:v0.0.0", init.Image)
	// One binary, two subcommands — the init container invokes /entry fetch.
	assert.Equal(t, []string{executor.EntryCommand, "fetch"}, init.Command)

	// init container mounts /data AND the request ConfigMap so it can read content.json.
	require.Len(t, init.VolumeMounts, 2)
	names := []string{init.VolumeMounts[0].Name, init.VolumeMounts[1].Name}
	assert.ElementsMatch(t, []string{VolumeData, VolumeRequest}, names)

	// Regression guard for the step-03 NOTE: content spec MUST NOT be embedded
	// on the init container's env. Inline files near the 512KB webhook cap
	// would bloat the pod object in etcd if we did.
	for _, e := range init.Env {
		assert.NotEqual(t, EnvContentJSON, e.Name,
			"KUBETEST_CONTENT_JSON env var was removed in step 06 — content ships via ConfigMap now")
	}

	// Content lands in the aux ConfigMap under executor.ContentFileName.
	cm := aux[0].(*corev1.ConfigMap)
	require.Contains(t, cm.Data, executor.ContentFileName)
	var got testsv1alpha1.Content
	require.NoError(t, json.Unmarshal([]byte(cm.Data[executor.ContentFileName]), &got))
	require.NotNil(t, got.Git)
	assert.Equal(t, "https://example.com/x.git", got.Git.URI)
}

func TestCompile_WrapperEnvContract(t *testing.T) {
	test := newValidTest("k6")
	test.Spec.Container.Env = []corev1.EnvVar{{Name: "USER_VAR", Value: "hello"}}
	job, _, err := Compile(test, newValidTestRun("myrun", test.Name), defaultOpts())
	require.NoError(t, err)

	_, main := getJobContainers(t, job)
	// Operator-injected env vars in known positions.
	wantOperatorEnv := map[string]string{
		executor.EnvDataDir:   DataDirPath,
		executor.EnvResultDir: ResultDirPath,
		executor.EnvRunID:     "myrun",
		executor.EnvTestRef:   "sample-k6",
	}
	for _, e := range main.Env {
		if want, ok := wantOperatorEnv[e.Name]; ok {
			assert.Equal(t, want, e.Value, "env %s value mismatch", e.Name)
			delete(wantOperatorEnv, e.Name)
		}
	}
	assert.Empty(t, wantOperatorEnv, "missing operator env vars: %v", wantOperatorEnv)

	// User env appended after operator env — asserts append order.
	assert.Equal(t, "USER_VAR", main.Env[len(main.Env)-1].Name)
	assert.Equal(t, "hello", main.Env[len(main.Env)-1].Value)
}

func TestCompile_WrapperMountsContract(t *testing.T) {
	job, _, err := Compile(newValidTest("k6"), newValidTestRun("myrun", "sample-k6"), defaultOpts())
	require.NoError(t, err)

	_, main := getJobContainers(t, job)
	wantMounts := map[string]struct {
		path     string
		readOnly bool
	}{
		VolumeData:    {DataDirPath, false},
		VolumeRequest: {RequestMountDir, true},
		VolumeResult:  {ResultDirPath, false},
	}
	for _, m := range main.VolumeMounts {
		if want, ok := wantMounts[m.Name]; ok {
			assert.Equal(t, want.path, m.MountPath, "mountPath for %s", m.Name)
			assert.Equal(t, want.readOnly, m.ReadOnly, "readOnly for %s", m.Name)
			delete(wantMounts, m.Name)
		}
	}
	assert.Empty(t, wantMounts, "missing wrapper mounts: %v", wantMounts)
}

func TestCompile_DoesNotMutateInputs(t *testing.T) {
	// Guard against a sloppy edit that later mutates the input Test/TestRun.
	// The controller passes shared cache objects — mutation there is a data race.
	test := newValidTest("k6")
	test.Spec.Pod = &testsv1alpha1.PodConfig{Labels: map[string]string{"team": "sre"}}
	test.Spec.Container.Env = []corev1.EnvVar{{Name: "X", Value: "y"}}
	run := newValidTestRun("myrun", test.Name)

	testJSON := mustMarshal(t, test)
	runJSON := mustMarshal(t, run)

	_, _, err := Compile(test, run, defaultOpts())
	require.NoError(t, err)

	assert.JSONEq(t, testJSON, mustMarshal(t, test), "compiler mutated Test input")
	assert.JSONEq(t, runJSON, mustMarshal(t, run), "compiler mutated TestRun input")
}

func TestCompile_JobLabelsIncludeManagedByAndRunID(t *testing.T) {
	// Beyond the pod template, the Job object itself carries operator labels
	// so kubectl can select `Jobs owned by kubetest-alt`.
	job, _, err := Compile(newValidTest("k6"), newValidTestRun("myrun", "sample-k6"), defaultOpts())
	require.NoError(t, err)
	assert.Equal(t, "myrun", job.Labels[LabelRunID])
	assert.Equal(t, ManagedByValue, job.Labels[LabelManagedBy])
}

func TestCompile_RequestJSON_HasAllExpectedFields(t *testing.T) {
	test := newValidTest("k6")
	test.Spec.Timeout = &metav1.Duration{Duration: 5 * time.Minute}
	test.Spec.Artifacts = &testsv1alpha1.ArtifactSpec{Paths: []string{"a/**/*.xml"}}
	run := newValidTestRun("myrun", test.Name)
	run.Spec.Config = map[string]string{"vus": "10"}

	_, aux, err := Compile(test, run, defaultOpts())
	require.NoError(t, err)
	req := unmarshalRequest(t, aux[0].(*corev1.ConfigMap))

	assert.Equal(t, "k6", req.Type, "Type must be set from Test.spec.type for step-11 dispatch")
	assert.Equal(t, "myrun", req.RunID)
	assert.Equal(t, "sample-k6", req.TestRef)
	assert.Equal(t, DataDirPath, req.DataDir)
	assert.Equal(t, int64(300), req.TimeoutSeconds)
	assert.Equal(t, []string{"a/**/*.xml"}, req.Artifacts.Paths)
	assert.Equal(t, map[string]string{"vus": "10"}, req.Config)
	assert.NotEmpty(t, req.Args, "default k6 args should be present")
}

// TestCompile_RequestJSON_TypeMatchesEveryExecutor asserts the request.Type
// field is populated for every supported executor. This is the compiler side
// of the step-11 dispatch contract — the wrapper picks Runner by req.Type.
func TestCompile_RequestJSON_TypeMatchesEveryExecutor(t *testing.T) {
	for _, execType := range []string{"k6", "cypress", "newman", "locust", "jmeter"} {
		t.Run(execType, func(t *testing.T) {
			_, aux, err := Compile(
				newValidTest(execType),
				newValidTestRun("r", "sample-"+execType),
				defaultOpts(),
			)
			require.NoError(t, err)
			req := unmarshalRequest(t, aux[0].(*corev1.ConfigMap))
			assert.Equal(t, execType, req.Type)
		})
	}
}

func TestCompile_RequestJSON_ValueFromEnvExcluded(t *testing.T) {
	// envSliceToMap drops ValueFrom entries — the wrapper has no k8s API access
	// to resolve secretKeyRef/configMapKeyRef itself. Those refs stay on the pod.
	test := newValidTest("k6")
	test.Spec.Container.Env = []corev1.EnvVar{
		{Name: "LITERAL", Value: "v"},
		{Name: "FROM_SECRET", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "s"}, Key: "k",
			},
		}},
	}
	_, aux, err := Compile(test, newValidTestRun("myrun", test.Name), defaultOpts())
	require.NoError(t, err)
	req := unmarshalRequest(t, aux[0].(*corev1.ConfigMap))
	assert.Equal(t, "v", req.Env["LITERAL"])
	assert.NotContains(t, req.Env, "FROM_SECRET", "ValueFrom env must not leak into request.json")
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

// Ensure the compile-time interface {} vs any alias hasn't drifted (belt+suspenders).
func TestCompile_ErrorsAreSentinels(t *testing.T) {
	for _, err := range []error{ErrNilTest, ErrNilTestRun, ErrUnknownExecutor, ErrMissingContentFetcherImage} {
		assert.True(t, errors.Is(err, err))
	}
}
