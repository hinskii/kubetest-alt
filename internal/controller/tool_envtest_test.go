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
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/compiler"
)

// fixes.md #3: the tool identity used to be dropped on the way to the
// compiler, so Job/Pod never carried kubetest.io/tool. A Test that gets
// its tool only from a template, run without any label, must still end up
// with status.tool and the label on Job + Pod template.
func TestReconcile_ToolFromTemplateReachesJobAndPod(t *testing.T) {
	fakeResults.Reset()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	tmpl := &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name: "jmeter", Namespace: ns,
			Labels: map[string]string{compiler.LabelKubetestTool: "jmeter"},
		},
		Spec: testsv1alpha1.TestTemplateSpec{
			Container: testsv1alpha1.ContainerConfig{Image: "justb4/jmeter:5.6.3", Command: []string{"jmeter"}},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, tmpl))
	test := newTestFixture(ns, "uses-template")
	test.Spec.Use = []string{"jmeter"}
	test.Spec.Container = testsv1alpha1.ContainerConfig{Args: []string{"-n", "-t", "plan.jmx"}}
	require.NoError(t, k8sClient.Create(ctx, test))

	run := newRunFixture(ns, "uses-template-run", "uses-template")
	require.NoError(t, k8sClient.Create(ctx, run))
	key := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForJob(t, ctx, key, 5*time.Second)

	var got testsv1alpha1.TestRun
	require.NoError(t, k8sClient.Get(ctx, key, &got))
	assert.Equal(t, "jmeter", got.Status.Tool)

	var job batchv1.Job
	require.NoError(t, k8sClient.Get(ctx, key, &job))
	assert.Equal(t, "jmeter", job.Labels[compiler.LabelKubetestTool], "Job label")
	assert.Equal(t, "jmeter", job.Spec.Template.Labels[compiler.LabelKubetestTool], "Pod template label")
}
