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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/resolver"
)

// pathTemplate is a template whose tool takes its files from
// /data/repo/{{ config.projectDir }}, without a default (step 20h).
func pathTemplate(ns string) *testsv1alpha1.TestTemplate {
	return &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "pw", Namespace: ns},
		Spec: testsv1alpha1.TestTemplateSpec{
			Container: testsv1alpha1.ContainerConfig{Image: "busybox", Command: []string{"sh", "-c"},
				Args: []string{"cd /data/repo/{{ config.projectDir }} && true"}},
			Config: map[string]testsv1alpha1.Parameter{
				"projectDir": {Type: "string", Path: "directory", Description: "The project, relative to the repository root."},
			},
		},
	}
}

// readyOf reads the Ready condition of the Test "web" in ns.
func readyOf(ns string) func() *metav1.Condition {
	return func() *metav1.Condition {
		var test testsv1alpha1.Test
		if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "web"}, &test); err != nil {
			return nil
		}
		return meta.FindStatusCondition(test.Status.Conditions, ConditionReady)
	}
}

func eventuallyReason(t *testing.T, cond func() *metav1.Condition, reason string) *metav1.Condition {
	t.Helper()
	var got *metav1.Condition
	require.Eventually(t, func() bool {
		got = cond()
		return got != nil && got.Reason == reason
	}, 10*time.Second, 100*time.Millisecond, "Ready reason %s (last: %+v)", reason, got)
	return got
}

func TestTestReady_PathMissingThenSet(t *testing.T) {
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, pathTemplate(ns)))
	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
		Spec: testsv1alpha1.TestSpec{Use: []string{"pw"},
			Content: testsv1alpha1.Content{Git: &testsv1alpha1.GitContent{URI: "https://example.com/r.git"}}},
	}
	require.NoError(t, k8sClient.Create(ctx, test))

	c := eventuallyReason(t, readyOf(ns), ReasonParameterMissing)
	assert.Equal(t, metav1.ConditionFalse, c.Status)
	assert.Equal(t, "set spec.config.projectDir: The project, relative to the repository root.", c.Message)

	// latestRun (the TestRun controller's) survives our status patches.
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(test), test))
	patch := client.MergeFrom(test.DeepCopy())
	test.Status.LatestRun = &testsv1alpha1.RunReference{Name: "web-1", Phase: testsv1alpha1.PhasePassed}
	require.NoError(t, k8sClient.Status().Patch(ctx, test, patch))

	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(test), test))
	test.Spec.Config = map[string]testsv1alpha1.Parameter{"projectDir": {Type: "string", Default: "e2e/web"}}
	require.NoError(t, k8sClient.Update(ctx, test))
	c = eventuallyReason(t, readyOf(ns), ReasonResolved)
	assert.Equal(t, metav1.ConditionTrue, c.Status)
	assert.Equal(t, test.Generation, c.ObservedGeneration)

	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(test), test))
	require.NotNil(t, test.Status.LatestRun)
	assert.Equal(t, "web-1", test.Status.LatestRun.Name, "latestRun untouched")
}

func TestTestReady_TemplateMissingThenCreated(t *testing.T) {
	ctx := context.Background()
	ns := uniqueNamespace(t)
	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
		Spec: testsv1alpha1.TestSpec{Use: []string{"pw"},
			Config: map[string]testsv1alpha1.Parameter{"projectDir": {Type: "string", Default: "."}}},
	}
	require.NoError(t, k8sClient.Create(ctx, test))
	c := eventuallyReason(t, readyOf(ns), ReasonTemplateMissing)
	assert.Contains(t, c.Message, `template "pw"`)

	// The template arriving (ArgoCD syncing it after the Test) re-evaluates.
	require.NoError(t, k8sClient.Create(ctx, pathTemplate(ns)))
	eventuallyReason(t, readyOf(ns), ReasonResolved)
}

func TestReadiness_NoMainPath(t *testing.T) {
	store := resolver.MapStore{}
	cases := map[string]*testsv1alpha1.Test{
		"own image": {Spec: testsv1alpha1.TestSpec{Container: testsv1alpha1.ContainerConfig{Image: "curlimages/curl", Args: []string{"https://x"}}}},
		"composite": {Spec: testsv1alpha1.TestSpec{Steps: []testsv1alpha1.Step{{Name: "a"}}}},
	}
	for name, test := range cases {
		c, err := readiness(test, store)
		require.NoError(t, err, name)
		assert.Equal(t, ReasonResolved, c.Reason, name)
	}
}

func TestTestReady_InlineProjectAndInlineFile(t *testing.T) {
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, pathTemplate(ns)))
	project := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
		Spec: testsv1alpha1.TestSpec{Use: []string{"pw"},
			Content: testsv1alpha1.Content{Files: []testsv1alpha1.FileContent{{Path: "package.json", Content: "{}"}}}},
	}
	require.NoError(t, k8sClient.Create(ctx, project))
	c := eventuallyReason(t, readyOf(ns), ReasonInlineNotSupported)
	assert.Contains(t, c.Message, "put it in git")
}

func TestTestReady_InlineSingleFileNeedsNoPath(t *testing.T) {
	ctx := context.Background()
	ns := uniqueNamespace(t)
	k6 := pathTemplate(ns)
	k6.Spec.Container.Args = []string{"run", "/data/repo/{{ config.script }}"}
	k6.Spec.Config = map[string]testsv1alpha1.Parameter{"script": {Type: "string", Path: "file"}}
	require.NoError(t, k8sClient.Create(ctx, k6))
	require.NoError(t, k8sClient.Create(ctx, &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
		Spec: testsv1alpha1.TestSpec{Use: []string{"pw"},
			Content: testsv1alpha1.Content{Files: []testsv1alpha1.FileContent{{Path: "load.js", Content: "x"}}}},
	}))
	eventuallyReason(t, readyOf(ns), ReasonResolved)
}

func TestReconcile_TestDataMissingFailsFast(t *testing.T) {
	ctx := context.Background()
	ns := uniqueNamespace(t)
	test := newTestFixture(ns, "with-data")
	test.Spec.Content.TestData = []testsv1alpha1.TestDataSource{
		{ConfigMap: "products"},
		{Secret: "account", Items: []testsv1alpha1.TestDataItem{{Key: "env", Path: "/data/repo/.env"}}},
	}
	require.NoError(t, k8sClient.Create(ctx, test))

	run := newRunFixture(ns, "with-data-1", "with-data")
	require.NoError(t, k8sClient.Create(ctx, run))
	got := waitForPhase(t, ctx, client.ObjectKeyFromObject(run), testsv1alpha1.PhaseError, 10*time.Second)
	assert.Contains(t, got.Status.Message, "no ConfigMap products in namespace "+ns)

	require.NoError(t, k8sClient.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "products", Namespace: ns},
		Data: map[string]string{"products.csv": "1"}}))
	require.NoError(t, k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "account", Namespace: ns},
		Data: map[string][]byte{"user": []byte("u")}}))
	run2 := newRunFixture(ns, "with-data-2", "with-data")
	require.NoError(t, k8sClient.Create(ctx, run2))
	got = waitForPhase(t, ctx, client.ObjectKeyFromObject(run2), testsv1alpha1.PhaseError, 10*time.Second)
	assert.Contains(t, got.Status.Message, `Secret account has no key "env"`)
}
