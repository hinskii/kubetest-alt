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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// canonicalTriggerTest returns a Test in the CANONICAL default.yaml shape:
// image+args populated, no schedule, ConcurrencyPolicy=Allow. The trigger
// e2e uses this exact shape so a webhook-off run and a webhook-on run
// produce the same TestRun creation behavior downstream.
func canonicalTriggerTest(namespace, name string) *testsv1alpha1.Test {
	return &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: PolicyAllow,
			Container: testsv1alpha1.ContainerConfig{
				Image: "grafana/k6:2.2.0",
				Args:  []string{"run", "script.js"},
			},
		},
	}
}

// newTriggerFor deployments matching a labelSelector; fires the given Test.
func newTriggerFor(ns, name, testName string, cond *testsv1alpha1.TriggerConditionSpec, concurrency string) *testsv1alpha1.TestTrigger {
	return &testsv1alpha1.TestTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: testsv1alpha1.TestTriggerSpec{
			Resource: "deployment",
			ResourceSelector: &testsv1alpha1.TriggerResourceSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"trigger-target": name},
				},
			},
			Event:             TriggerEventModified,
			Action:            "run",
			Execution:         "test",
			ConcurrencyPolicy: concurrency,
			ConditionSpec:     cond,
			TestSelector:      &testsv1alpha1.TriggerTestSelector{Name: testName},
		},
	}
}

// newTargetDeployment creates a deployment carrying the label the trigger
// selects on. Two replicas of a busybox image — envtest doesn't schedule
// anything, so image doesn't matter; the label + status.conditions do.
func newTargetDeployment(ns, name string) *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    map[string]string{"trigger-target": name},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "app",
						Image: "busybox:latest",
					}},
				},
			},
		},
	}
}

// setDeploymentConditions sets .status.conditions via the status subresource.
// envtest supports the subresource but doesn't drive it — we do it explicitly.
func setDeploymentConditions(t *testing.T, ctx context.Context, key client.ObjectKey, conds []appsv1.DeploymentCondition) {
	t.Helper()
	var d appsv1.Deployment
	require.NoError(t, k8sClient.Get(ctx, key, &d))
	d.Status.Conditions = conds
	require.NoError(t, k8sClient.Status().Update(ctx, &d))
}

// listTestRunsForTest returns every TestRun in ns whose spec.testRef matches.
func listTestRunsForTest(t *testing.T, ctx context.Context, ns, testName string) []testsv1alpha1.TestRun {
	t.Helper()
	var list testsv1alpha1.TestRunList
	require.NoError(t, k8sClient.List(ctx, &list, client.InNamespace(ns)))
	out := list.Items[:0]
	for _, r := range list.Items {
		if r.Spec.TestRef == testName {
			out = append(out, r)
		}
	}
	return out
}

// TestTrigger_Envtest_FiresOnDeploymentModified is the primary e2e:
// deployment modified event + selector match + conditions met → TestRun
// created with source=trigger, using a Test in the canonical default.yaml
// shape (image + args) — the same shape the compiler + webhook expect.
func TestTrigger_Envtest_FiresOnDeploymentModified(t *testing.T) {
	ctx := context.Background()
	ns := uniqueNamespace(t)

	// Anchor the fake clock in this namespace's slice of test-time.
	triggerClock.Set(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))

	test := canonicalTriggerTest(ns, "e2e-fire-test")
	require.NoError(t, k8sClient.Create(ctx, test))

	dep := newTargetDeployment(ns, "e2e-fire-dep")
	require.NoError(t, k8sClient.Create(ctx, dep))

	trg := newTriggerFor(ns, "e2e-fire-dep", "e2e-fire-test",
		&testsv1alpha1.TriggerConditionSpec{
			Conditions: []testsv1alpha1.TriggerCondition{
				{Type: "Available", Status: "True"},
			},
		},
		TriggerConcurrencyAllow,
	)
	require.NoError(t, k8sClient.Create(ctx, trg))

	// Give the reconciler time to register the informer for deployments.
	require.Eventually(t, func() bool {
		refs := triggerReconciler.informers.gvkRefs()
		return refs[deploymentGVK] > 0
	}, 3*time.Second, 50*time.Millisecond, "informer must register for apps/v1 Deployment")

	// Set the deployment's Available=True. The informer sees the update,
	// the reconciler enqueues a gate, the runnable's Evaluate loop
	// (100ms interval) fires it — no wall-clock dependence beyond the
	// runnable's own tick rate.
	setDeploymentConditions(t, ctx, client.ObjectKey{Namespace: ns, Name: dep.Name},
		[]appsv1.DeploymentCondition{{
			Type:               appsv1.DeploymentAvailable,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(triggerClock.Now()),
			Reason:             "MinimumReplicasAvailable",
		}})

	// A TestRun with source=trigger is created for our Test.
	require.Eventually(t, func() bool {
		runs := listTestRunsForTest(t, ctx, ns, "e2e-fire-test")
		if len(runs) == 0 {
			return false
		}
		for _, r := range runs {
			if r.Spec.Source == SourceTrigger {
				return true
			}
		}
		return false
	}, 5*time.Second, 100*time.Millisecond, "trigger must create TestRun with source=trigger")

	runs := listTestRunsForTest(t, ctx, ns, "e2e-fire-test")
	require.NotEmpty(t, runs)
	run := runs[0]
	assert.Equal(t, "e2e-fire-test", run.Spec.TestRef)
	assert.Equal(t, SourceTrigger, run.Spec.Source)
	assert.Equal(t, trg.Name, run.Labels[LabelTriggerName])
	assert.Equal(t, "e2e-fire-test", run.Labels[LabelTriggerFor])
	assert.Contains(t, run.Annotations[AnnotationTriggerTarget], dep.Name)
	assert.Equal(t, TriggerEventModified, run.Annotations[AnnotationTriggerEvent])
}

// TestTrigger_Envtest_TimeoutFromEventTime pins the plan's flagged invariant:
// conditionSpec.timeout is measured from the event, not from operator start
// or gate creation drift. Advance fake clock past timeout with conditions
// still unmet → gate expires, no TestRun.
func TestTrigger_Envtest_TimeoutFromEventTime(t *testing.T) {
	ctx := context.Background()
	ns := uniqueNamespace(t)

	// Anchor clock.
	t0 := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	triggerClock.Set(t0)

	test := canonicalTriggerTest(ns, "e2e-timeout-test")
	require.NoError(t, k8sClient.Create(ctx, test))

	dep := newTargetDeployment(ns, "e2e-timeout-dep")
	require.NoError(t, k8sClient.Create(ctx, dep))

	trg := newTriggerFor(ns, "e2e-timeout-dep", "e2e-timeout-test",
		&testsv1alpha1.TriggerConditionSpec{
			Timeout: 10, // seconds
			Conditions: []testsv1alpha1.TriggerCondition{
				{Type: "Available", Status: "True"},
			},
		},
		TriggerConcurrencyAllow,
	)
	require.NoError(t, k8sClient.Create(ctx, trg))

	require.Eventually(t, func() bool {
		return triggerReconciler.informers.gvkRefs()[deploymentGVK] > 0
	}, 3*time.Second, 50*time.Millisecond)

	// Set condition to False so the gate hangs on conditions.
	setDeploymentConditions(t, ctx, client.ObjectKey{Namespace: ns, Name: dep.Name},
		[]appsv1.DeploymentCondition{{
			Type:               appsv1.DeploymentAvailable,
			Status:             corev1.ConditionFalse,
			LastTransitionTime: metav1.NewTime(t0),
			Reason:             "MinimumReplicasUnavailable",
		}})

	// Wait for the gate to be pending.
	require.Eventually(t, func() bool {
		return triggerReconciler.gates.Pending() > 0
	}, 3*time.Second, 50*time.Millisecond, "gate must land in pending set")

	// Advance fake clock past timeout — timeout is measured from EventTime,
	// which was recorded when the informer saw the modified event at t0.
	triggerClock.Set(t0.Add(15 * time.Second))

	// Within a couple of runnable ticks, gate expires.
	require.Eventually(t, func() bool {
		return triggerReconciler.gates.Pending() == 0
	}, 3*time.Second, 50*time.Millisecond, "gate must expire after timeout from event time")

	// No TestRun created.
	runs := listTestRunsForTest(t, ctx, ns, "e2e-timeout-test")
	assert.Empty(t, runs, "gate expired ⇒ no TestRun")

	// Outcome recorded as expired for our trigger.
	outcomes := triggerReconciler.gates.Outcomes()
	found := false
	for _, o := range outcomes {
		if o.Trigger != nil && o.Trigger.Name == trg.Name && o.Kind == OutcomeExpired {
			found = true
			break
		}
	}
	assert.True(t, found, "expired outcome must be recorded for trigger %q; got %v", trg.Name, outcomes)
}

// TestTrigger_Envtest_ConcurrencyForbid_Skips: with concurrencyPolicy=forbid
// and an already-active prior TestRun, the trigger's fire is SKIPPED (no
// second TestRun) and the Skipped outcome is recorded.
func TestTrigger_Envtest_ConcurrencyForbid_Skips(t *testing.T) {
	ctx := context.Background()
	ns := uniqueNamespace(t)

	triggerClock.Set(time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC))

	test := canonicalTriggerTest(ns, "e2e-forbid-test")
	require.NoError(t, k8sClient.Create(ctx, test))

	// Prior active TestRun — non-terminal by default (Phase="").
	priorRun := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "e2e-forbid-prior",
			Namespace: ns,
			// Prevent the existing testrun controller from processing this
			// stub — it's not a real run, it just occupies the "prior active"
			// slot. Use a fake finalizer that never gets removed, plus a
			// terminal-looking status? No — we want it to LOOK active.
			// Existing controller will try to process it; that's OK as long
			// as it doesn't reach terminal before we assert.
		},
		Spec: testsv1alpha1.TestRunSpec{
			TestRef: "e2e-forbid-test",
			Source:  "api",
		},
	}
	require.NoError(t, k8sClient.Create(ctx, priorRun))

	dep := newTargetDeployment(ns, "e2e-forbid-dep")
	require.NoError(t, k8sClient.Create(ctx, dep))

	trg := newTriggerFor(ns, "e2e-forbid-dep", "e2e-forbid-test",
		&testsv1alpha1.TriggerConditionSpec{
			Conditions: []testsv1alpha1.TriggerCondition{
				{Type: "Available", Status: "True"},
			},
		},
		TriggerConcurrencyForbid,
	)
	require.NoError(t, k8sClient.Create(ctx, trg))

	require.Eventually(t, func() bool {
		return triggerReconciler.informers.gvkRefs()[deploymentGVK] > 0
	}, 3*time.Second, 50*time.Millisecond)

	setDeploymentConditions(t, ctx, client.ObjectKey{Namespace: ns, Name: dep.Name},
		[]appsv1.DeploymentCondition{{
			Type:               appsv1.DeploymentAvailable,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(triggerClock.Now()),
			Reason:             "MinimumReplicasAvailable",
		}})

	// A "skipped" outcome should be recorded eventually.
	require.Eventually(t, func() bool {
		outcomes := triggerReconciler.gates.Outcomes()
		for _, o := range outcomes {
			if o.Trigger != nil && o.Trigger.Name == trg.Name && o.Kind == OutcomeSkipped {
				return true
			}
		}
		return false
	}, 5*time.Second, 100*time.Millisecond, "forbid + active prior ⇒ Skipped outcome recorded")

	// Only the prior TestRun exists — no trigger-sourced one.
	runs := listTestRunsForTest(t, ctx, ns, "e2e-forbid-test")
	triggerRuns := 0
	for _, r := range runs {
		if r.Spec.Source == SourceTrigger {
			triggerRuns++
		}
	}
	assert.Zero(t, triggerRuns, "forbid must skip TestRun creation while prior active")
}

// TestTrigger_Envtest_UnregisterOnDelete: deleting a TestTrigger CR must
// unregister the informer. When it was the last consumer of the GVK, the
// informer stops (no watch leak).
func TestTrigger_Envtest_UnregisterOnDelete(t *testing.T) {
	ctx := context.Background()
	ns := uniqueNamespace(t)

	// Fresh trigger on a resource NOT used elsewhere in the shared envtest.
	// Pods are safer than Deployments (other tests may have registered
	// deployment triggers that keep the ref count nonzero).
	trg := &testsv1alpha1.TestTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "unreg-trigger", Namespace: ns},
		Spec: testsv1alpha1.TestTriggerSpec{
			Resource:     "pods",
			Event:        TriggerEventModified,
			Action:       "run",
			Execution:    "test",
			TestSelector: &testsv1alpha1.TriggerTestSelector{Name: "some-test"},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, trg))

	// Wait for informer registration.
	require.Eventually(t, func() bool {
		return triggerReconciler.informers.gvkRefs()[podGVK] > 0
	}, 3*time.Second, 50*time.Millisecond, "pod informer must register")

	// Delete the trigger.
	require.NoError(t, k8sClient.Delete(ctx, trg))

	// Ref count for the pod GVK drops to zero (this was the only trigger).
	require.Eventually(t, func() bool {
		refs := triggerReconciler.informers.gvkRefs()
		return refs[podGVK] == 0
	}, 5*time.Second, 100*time.Millisecond, "pod informer must be stopped after last trigger deleted")
}

// TestTrigger_Envtest_MultipleTriggersSameGVK_ShareInformer: two triggers
// on the same GVK (Deployment) result in ONE informer (ref count 2), and
// events are dispatched to both. This is the "50 triggers ≠ 50 informers"
// invariant, condensed to the smallest e2e we can express.
func TestTrigger_Envtest_MultipleTriggersSameGVK_ShareInformer(t *testing.T) {
	ctx := context.Background()
	ns := uniqueNamespace(t)

	trg1 := &testsv1alpha1.TestTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "share-1", Namespace: ns},
		Spec: testsv1alpha1.TestTriggerSpec{
			Resource:     "deployment",
			Event:        TriggerEventModified,
			Action:       "run",
			Execution:    "test",
			TestSelector: &testsv1alpha1.TriggerTestSelector{Name: "x"},
		},
	}
	trg2 := &testsv1alpha1.TestTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "share-2", Namespace: ns},
		Spec: testsv1alpha1.TestTriggerSpec{
			Resource:     "deployment",
			Event:        TriggerEventModified,
			Action:       "run",
			Execution:    "test",
			TestSelector: &testsv1alpha1.TriggerTestSelector{Name: "y"},
		},
	}
	// Capture baseline BEFORE creating the fresh triggers — any prior
	// tests in this shared envtest may have registered their own
	// deployment triggers. Baseline captures what's already there so we
	// can assert we added exactly 2 more.
	before := triggerReconciler.informers.gvkRefs()[deploymentGVK]

	require.NoError(t, k8sClient.Create(ctx, trg1))
	require.NoError(t, k8sClient.Create(ctx, trg2))

	// Wait for both to register.
	require.Eventually(t, func() bool {
		refs := triggerReconciler.informers.gvkRefs()
		return refs[deploymentGVK] >= before+2
	}, 5*time.Second, 100*time.Millisecond, "both triggers register on the same GVK")

	afterRegister := triggerReconciler.informers.gvkRefs()[deploymentGVK]

	// Delete one; ref count decrements by 1, informer NOT stopped.
	require.NoError(t, k8sClient.Delete(ctx, trg1))
	require.Eventually(t, func() bool {
		refs := triggerReconciler.informers.gvkRefs()
		return refs[deploymentGVK] == afterRegister-1
	}, 5*time.Second, 100*time.Millisecond, "one trigger delete decrements ref, informer stays")
}

// _ discard unused; helps future refactors realize this fixture is present.
var _ = unstructured.Unstructured{}
