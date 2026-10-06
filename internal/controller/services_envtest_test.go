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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/compiler"
)

func testWithService(ns, name string, svc testsv1alpha1.ServiceSpec) *testsv1alpha1.Test {
	t := newTestFixture(ns, name)
	t.Spec.Services = map[string]testsv1alpha1.ServiceSpec{"db": svc}
	return t
}

func waitForObject(t *testing.T, ctx context.Context, key client.ObjectKey, obj client.Object) {
	t.Helper()
	require.Eventually(t, func() bool { return k8sClient.Get(ctx, key, obj) == nil },
		5*time.Second, 50*time.Millisecond, "%T %s", obj, key)
}

func setPodStatus(t *testing.T, ctx context.Context, key client.ObjectKey, mutate func(*corev1.Pod)) {
	t.Helper()
	require.Eventually(t, func() bool {
		var pod corev1.Pod
		if k8sClient.Get(ctx, key, &pod) != nil {
			return false
		}
		mutate(&pod)
		return k8sClient.Status().Update(ctx, &pod) == nil
	}, 5*time.Second, 50*time.Millisecond)
}

func markReady(p *corev1.Pod) {
	p.Status.Phase = corev1.PodRunning
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
}

// fixes.md #17: spec.services was accepted and ignored. The test Job now
// waits until every replica is ready; services go away when the run ends.
func TestReconcile_Services_StartBeforeTestAndStopAfter(t *testing.T) {
	fakeResults.Reset()
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, testWithService(ns, "with-db", testsv1alpha1.ServiceSpec{Image: "postgres:17"})))
	run := newRunFixture(ns, "db-run", "with-db")
	require.NoError(t, k8sClient.Create(ctx, run))
	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}

	podKey := client.ObjectKey{Namespace: ns, Name: "db-run-db-0"}
	var pod corev1.Pod
	waitForObject(t, ctx, podKey, &pod)
	assert.Equal(t, "db-run", pod.Labels[compiler.LabelServiceOf])
	var svc corev1.Service
	waitForObject(t, ctx, client.ObjectKey{Namespace: ns, Name: "db-run-db"}, &svc)
	assert.Equal(t, corev1.ClusterIPNone, svc.Spec.ClusterIP)

	assert.Eventually(t, func() bool {
		var r testsv1alpha1.TestRun
		return k8sClient.Get(ctx, runKey, &r) == nil && r.Status.Message == "waiting for services: db"
	}, 5*time.Second, 50*time.Millisecond)
	assert.Never(t, func() bool { return hasJob(ctx, runKey) }, time.Second, 100*time.Millisecond,
		"no test Job before the services are ready")

	setPodStatus(t, ctx, podKey, markReady)
	waitForJob(t, ctx, runKey, 5*time.Second)
	var job batchv1.Job
	require.NoError(t, k8sClient.Get(ctx, runKey, &job))
	env := map[string]string{}
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	assert.Equal(t, "db-run-db."+ns+".svc", env["KUBETEST_SERVICE_DB_HOST"])

	fakeResults.Set(run.Name, &RunResult{Phase: testsv1alpha1.PhasePassed})
	patchJobConditions(t, ctx, runKey, []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}})
	waitForPhase(t, ctx, runKey, testsv1alpha1.PhasePassed, 5*time.Second)
	assert.Eventually(t, func() bool {
		var p corev1.Pod
		var s corev1.Service
		gone := func(err error) bool { return apierrors.IsNotFound(err) }
		pErr := k8sClient.Get(ctx, podKey, &p)
		sErr := k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: "db-run-db"}, &s)
		return (gone(pErr) || p.DeletionTimestamp != nil) && gone(sErr)
	}, 5*time.Second, 50*time.Millisecond, "services are removed when the run ends")
}

func TestReconcile_Services_NotReadyInTimeIsError(t *testing.T) {
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, testWithService(ns, "slow-db", testsv1alpha1.ServiceSpec{
		Image: "postgres:17", Timeout: &metav1.Duration{Duration: time.Second},
	})))
	run := newRunFixture(ns, "slow-run", "slow-db")
	require.NoError(t, k8sClient.Create(ctx, run))
	final := waitForPhase(t, ctx, client.ObjectKey{Namespace: ns, Name: run.Name}, testsv1alpha1.PhaseError, 10*time.Second)
	assert.Contains(t, final.Status.Message, ReasonServiceNotReady)
	assert.Contains(t, final.Status.Message, "not ready after 1s")
	assert.False(t, hasJob(ctx, client.ObjectKey{Namespace: ns, Name: run.Name}))
}

func TestReconcile_Services_ImagePullFailsTheRun(t *testing.T) {
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, testWithService(ns, "bad-db", testsv1alpha1.ServiceSpec{Image: "nope:404"})))
	run := newRunFixture(ns, "bad-run", "bad-db")
	require.NoError(t, k8sClient.Create(ctx, run))
	podKey := client.ObjectKey{Namespace: ns, Name: "bad-run-db-0"}
	var pod corev1.Pod
	waitForObject(t, ctx, podKey, &pod)
	setPodStatus(t, ctx, podKey, func(p *corev1.Pod) {
		p.Status.Phase = corev1.PodPending
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name: compiler.ContainerService, Image: "nope:404",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
		}}
	})
	final := waitForPhase(t, ctx, client.ObjectKey{Namespace: ns, Name: run.Name}, testsv1alpha1.PhaseError, 10*time.Second)
	assert.Contains(t, final.Status.Message, "service db (bad-run-db-0)")
	assert.Contains(t, final.Status.Message, "ImagePullBackOff nope:404")
}
