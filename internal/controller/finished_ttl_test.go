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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/compiler"
)

var ttlNow = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func finishedFixture(name string, finishedAgo time.Duration) *testsv1alpha1.TestRun {
	done := metav1.NewTime(ttlNow.Add(-finishedAgo))
	return &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", UID: types.UID("uid-" + name),
			Finalizers: []string{FinalizerName}},
		Spec:   testsv1alpha1.TestRunSpec{TestRef: "smoke"},
		Status: testsv1alpha1.TestRunStatus{Phase: testsv1alpha1.PhasePassed, StartedAt: &done, FinishedAt: &done},
	}
}

func ttlReconciler(t *testing.T, st RunStorePersister, objs ...client.Object) *TestRunReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, testsv1alpha1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&testsv1alpha1.TestRun{}).Build()
	return &TestRunReconciler{Client: c, Scheme: scheme, RunStore: st, FinishedRunTTL: time.Hour, Results: NoResultReader{},
		Now: func() metav1.Time { return metav1.NewTime(ttlNow) }}
}

func reconcileRun(t *testing.T, r *TestRunReconciler, name string) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: name}})
	require.NoError(t, err)
	return res
}

// deleted: the fake client marks a CR with a finalizer for deletion.
func deleted(t *testing.T, r *TestRunReconciler, name string) bool {
	t.Helper()
	var run testsv1alpha1.TestRun
	err := r.Get(t.Context(), types.NamespacedName{Namespace: "ns", Name: name}, &run)
	if apierrors.IsNotFound(err) {
		return true
	}
	require.NoError(t, err)
	return !run.DeletionTimestamp.IsZero()
}

func TestFinishedRunTTL(t *testing.T) {
	t.Run("deleted an hour after it finished, once in run history", func(t *testing.T) {
		st := NewRecordingRunStore()
		r := ttlReconciler(t, st, finishedFixture("old", 2*time.Hour))
		reconcileRun(t, r, "old")
		assert.Len(t, st.SavesForUID("uid-old"), 1, "saved to run history first")
		assert.True(t, deleted(t, r, "old"))
	})
	t.Run("kept until the TTL, then looked at again", func(t *testing.T) {
		r := ttlReconciler(t, NewRecordingRunStore(), finishedFixture("fresh", 10*time.Minute))
		res := reconcileRun(t, r, "fresh")
		assert.Equal(t, 50*time.Minute, res.RequeueAfter)
		assert.False(t, deleted(t, r, "fresh"))
	})
	t.Run("never without run history", func(t *testing.T) {
		r := ttlReconciler(t, nil, finishedFixture("only-record", 48*time.Hour))
		r.RunStore = nil
		assert.Zero(t, reconcileRun(t, r, "only-record"))
		assert.False(t, deleted(t, r, "only-record"), "the CR is the run's only record")
	})
	t.Run("not while the history save fails", func(t *testing.T) {
		st := NewRecordingRunStore()
		st.QueueErr("uid-unsaved", errors.New("db down"))
		r := ttlReconciler(t, st, finishedFixture("unsaved", 2*time.Hour))
		assert.Equal(t, expireRetry, reconcileRun(t, r, "unsaved").RequeueAfter)
		assert.False(t, deleted(t, r, "unsaved"))
		reconcileRun(t, r, "unsaved") // the store is back
		assert.True(t, deleted(t, r, "unsaved"))
	})
	t.Run("TTL 0 keeps finished runs", func(t *testing.T) {
		r := ttlReconciler(t, NewRecordingRunStore(), finishedFixture("keep", 48*time.Hour))
		r.FinishedRunTTL = 0
		assert.Zero(t, reconcileRun(t, r, "keep"))
		assert.False(t, deleted(t, r, "keep"))
	})
	t.Run("composite children go with their parent, after all are in history", func(t *testing.T) {
		parent := finishedFixture("suite", 2*time.Hour)
		parent.Status.ResolvedSpec = `{"steps":[{"name":"a","execute":{"tests":[{"name":"leaf"}]}}]}`
		child := finishedFixture("suite-s0-leaf-0", 2*time.Hour)
		child.Labels = map[string]string{compiler.LabelParentRun: "suite"}
		st := NewRecordingRunStore()
		r := ttlReconciler(t, st, parent, child)

		assert.Equal(t, expireRetry, reconcileRun(t, r, "suite").RequeueAfter, "the child isn't in history yet")
		assert.False(t, deleted(t, r, "suite"))

		assert.Zero(t, reconcileRun(t, r, "suite-s0-leaf-0"))
		assert.False(t, deleted(t, r, "suite-s0-leaf-0"), "a child is never deleted on its own")

		reconcileRun(t, r, "suite")
		assert.True(t, deleted(t, r, "suite"), "deleting the parent cascades to the child (owner reference)")
	})
}
