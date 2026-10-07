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
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// A Test created just before its run can be missing from the informer
// cache when the run's setup reads it. The run must wait for the cache,
// not end with TestNotFound — a composite child failing this way made
// TestReconcile_Composite_StepTimeoutAbortsChildrenOnce flake in CI.
func TestSetup_TestMissingFromCacheOnly_Requeues(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, testsv1alpha1.AddToScheme(scheme))
	newRun := func() *testsv1alpha1.TestRun {
		return &testsv1alpha1.TestRun{
			ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns", Finalizers: []string{FinalizerName}},
			Spec:       testsv1alpha1.TestRunSpec{TestRef: "smoke"},
		}
	}
	test := newTestFixture("ns", "smoke")
	key := types.NamespacedName{Namespace: "ns", Name: "r"}
	reconcilerWith := func(live ...*testsv1alpha1.Test) *TestRunReconciler {
		cache := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(newRun()).WithStatusSubresource(&testsv1alpha1.TestRun{}).Build()
		api := fake.NewClientBuilder().WithScheme(scheme)
		for _, o := range live {
			api = api.WithObjects(o)
		}
		return &TestRunReconciler{Client: cache, APIReader: api.Build(), Scheme: scheme, Now: metav1.Now}
	}

	r := reconcilerWith(test)
	res, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	assert.Equal(t, cacheLagRequeue, res.RequeueAfter, "the Test exists — wait for the cache")
	var got testsv1alpha1.TestRun
	require.NoError(t, r.Get(t.Context(), key, &got))
	assert.Empty(t, got.Status.Phase, "no verdict while the cache catches up")

	r = reconcilerWith() // gone from the API server too
	_, err = r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	require.NoError(t, r.Get(t.Context(), key, &got))
	assert.Equal(t, testsv1alpha1.PhaseError, got.Status.Phase)
	assert.Contains(t, got.Status.Message, ReasonTestNotFound)
}
