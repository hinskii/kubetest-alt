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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/podpolicy"
	"github.com/hinskii/kubetest-alt/internal/resolver"
)

// The admission webhook sees a Test as written; a template can still bring
// the node's disk in. The operator checks the resolved spec: the run ends
// with PolicyDenied and no Job is ever created.
func TestSetup_PodPolicyOnTheResolvedSpec(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, testsv1alpha1.AddToScheme(scheme))
	test := newTestFixture("ns", "smoke")
	test.Spec.Use = []string{"sneaky"}
	run := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns", Finalizers: []string{FinalizerName}},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "smoke"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(test, run).
		WithStatusSubresource(&testsv1alpha1.TestRun{}).Build()
	tmpl := &testsv1alpha1.TestTemplate{ObjectMeta: metav1.ObjectMeta{Name: "sneaky", Namespace: "ns"},
		Spec: testsv1alpha1.TestTemplateSpec{Pod: &testsv1alpha1.PodConfig{Volumes: []corev1.Volume{{
			Name: "node", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}}}}}}}
	r := &TestRunReconciler{Client: c, Scheme: scheme, Results: NoResultReader{}, Now: metav1.Now,
		TemplateStore: resolver.MapStore{"ns/sneaky": tmpl}, PodPolicy: podpolicy.Policy{}}

	key := types.NamespacedName{Namespace: "ns", Name: "r"}
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	var got testsv1alpha1.TestRun
	require.NoError(t, c.Get(t.Context(), key, &got))
	assert.Equal(t, testsv1alpha1.PhaseError, got.Status.Phase)
	assert.Contains(t, got.Status.Message, ReasonPolicyDenied)
	assert.Contains(t, got.Status.Message, "hostPath volumes are not allowed")
	assert.True(t, apierrors.IsNotFound(c.Get(t.Context(), key, &batchv1.Job{})), "no pod was ever asked for")

	r.PodPolicy = podpolicy.Policy{AllowHostPath: true}
	run2 := run.DeepCopy()
	run2.Name, run2.ResourceVersion = "r2", ""
	require.NoError(t, c.Create(t.Context(), run2))
	_, err = r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "r2"}})
	require.NoError(t, err)
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "ns", Name: "r2"}, &got))
	assert.NotEqual(t, testsv1alpha1.PhaseError, got.Status.Phase, "allowed by the platform: the run goes on")
}
