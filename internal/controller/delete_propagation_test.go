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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// TestDeleteJobBackground_UsesExplicitPropagation is the regression guard for
// the "orphaned pods" bug: batch/v1 Job's default propagation is Orphan, so
// omitting an explicit policy leaves pods running after we delete the Job.
// This test uses a fake client with an interceptor to observe the DeleteOptions
// passed to the API (envtest can't verify this — no garbage collector there).
func TestDeleteJobBackground_UsesExplicitPropagation(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, batchv1.AddToScheme(scheme))

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "j", Namespace: "ns"},
	}

	var captured client.DeleteOption
	// Interceptor captures the delete options the reconciler passes through.
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(job).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if len(opts) > 0 {
					captured = opts[0]
				}
				return c.Delete(ctx, obj, opts...)
			},
		}).
		Build()

	require.NoError(t, deleteJobBackground(context.Background(), fakeClient, job))
	require.NotNil(t, captured, "Delete must be called with at least one option")

	// Apply the option onto a fresh DeleteOptions to read out the effective
	// propagation policy (this is how client.PropagationPolicy exposes itself).
	var effective client.DeleteOptions
	captured.ApplyToDelete(&effective)
	require.NotNil(t, effective.PropagationPolicy, "PropagationPolicy must be set explicitly")
	assert.Equal(t, metav1.DeletePropagationBackground, *effective.PropagationPolicy,
		"orphaned-pods bug: Job delete MUST use Background propagation; batch/v1 defaults to Orphan")
}

// TestDeleteJobBackground_IgnoresNotFound is idempotency: a Job that's already
// gone should not surface an error (finalizer/terminal paths retry).
func TestDeleteJobBackground_IgnoresNotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, batchv1.AddToScheme(scheme))
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	// Delete a Job that doesn't exist — must return nil.
	err := deleteJobBackground(context.Background(), fakeClient,
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "gone", Namespace: "ns"}})
	assert.NoError(t, err)
}
