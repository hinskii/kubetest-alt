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

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// TestReconcile_NotFound_CleansUpPersistedRuns is the regression test for
// the persist-set leak: on CR NotFound the reconciler MUST drop the
// map entry so a long-lived operator doesn't grow unbounded when a
// TestRun disappears without our finalize path firing.
//
// Scenarios where NotFound-cleanup matters:
//   - Finalizer force-stripped externally (kubectl edit / patch).
//   - Reconcile fires on a delete event we missed the finalize for
//     (e.g. operator restart AFTER k8s already reaped the CR).
//   - Any bug that produces a NotFound-on-Get for a UID we previously
//     persisted.
//
// Not using envtest — this is pure controller behavior, faster + isolated
// via fake client. The countingReconciler wrapper is irrelevant here.
func TestReconcile_NotFound_CleansUpPersistedRuns(t *testing.T) {
	scheme := runtime.NewScheme()
	assert.NoError(t, clientgoscheme.AddToScheme(scheme))
	assert.NoError(t, testsv1alpha1.AddToScheme(scheme))

	// Empty fake client → any Get returns NotFound.
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &TestRunReconciler{
		Client: c,
		Scheme: scheme,
	}
	// Pre-populate the persist set as if we'd previously written this
	// TestRun to the DB during THIS operator lifetime.
	victim := types.NamespacedName{Namespace: "ns", Name: "leaked-run"}
	r.persistedRuns.Store(victim, struct{}{})

	// Sanity: entry is there.
	_, present := r.persistedRuns.Load(victim)
	assert.True(t, present, "precondition: entry should exist")

	// Reconcile the missing CR — should return quickly + clean up.
	res, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: victim})
	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, res, "no requeue expected on NotFound")

	// Post-condition: entry is gone (leak fix works).
	_, present = r.persistedRuns.Load(victim)
	assert.False(t, present, "NotFound must evict persistedRuns entry")
}

// TestReconcile_NotFound_IdempotentCleanup — calling twice must not
// panic and stays quiet (Delete on absent key is a no-op).
func TestReconcile_NotFound_IdempotentCleanup(t *testing.T) {
	scheme := runtime.NewScheme()
	assert.NoError(t, clientgoscheme.AddToScheme(scheme))
	assert.NoError(t, testsv1alpha1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &TestRunReconciler{Client: c, Scheme: scheme}
	key := types.NamespacedName{Namespace: "ns", Name: "never-existed"}

	for range 3 {
		res, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, res)
	}
}
