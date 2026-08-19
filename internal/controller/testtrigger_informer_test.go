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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/restmapper"
)

// dynamicScheme registers just the types the fake dynamic client needs to
// list/watch. Deployments live under apps/v1; add List kinds for the same.
func dynamicScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	// The fake dynamic client's scheme requires the *List kind for anything
	// you plan to List. Register generic unstructured shells for apps/v1.
	s.AddKnownTypeWithName(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		&unstructured.Unstructured{})
	s.AddKnownTypeWithName(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "DeploymentList"},
		&unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"},
		&unstructured.Unstructured{})
	s.AddKnownTypeWithName(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "PodList"},
		&unstructured.UnstructuredList{})
	return s
}

// newIM constructs an informerManager with the fake dynamic client + a
// working RESTMapper for Deployments and Pods.
func newIM(t *testing.T) *informerManager {
	t.Helper()
	// The fake dynamic client is enough for ref-counting tests — they don't
	// exercise event delivery, just Register/Unregister mechanics.
	dyn := fake.NewSimpleDynamicClient(dynamicScheme())

	groups := []*restmapper.APIGroupResources{
		{
			Group: metav1.APIGroup{
				Name: "",
				Versions: []metav1.GroupVersionForDiscovery{
					{GroupVersion: "v1", Version: "v1"},
				},
				PreferredVersion: metav1.GroupVersionForDiscovery{GroupVersion: "v1", Version: "v1"},
			},
			VersionedResources: map[string][]metav1.APIResource{
				"v1": {
					{Name: "pods", SingularName: "pod", Namespaced: true, Kind: "Pod", Verbs: []string{"list", "watch"}},
				},
			},
		},
		{
			Group: metav1.APIGroup{
				Name: "apps",
				Versions: []metav1.GroupVersionForDiscovery{
					{GroupVersion: "apps/v1", Version: "v1"},
				},
				PreferredVersion: metav1.GroupVersionForDiscovery{GroupVersion: "apps/v1", Version: "v1"},
			},
			VersionedResources: map[string][]metav1.APIResource{
				"v1": {
					{Name: "deployments", SingularName: "deployment", Namespaced: true, Kind: "Deployment", Verbs: []string{"list", "watch"}},
				},
			},
		},
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groups)
	return newInformerManager(dyn, mapper)
}

var podGVK = schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}

// TestInformerManager_PerGVKRefCount is the plan's flagged invariant:
// 50 triggers on the same GVK = ONE informer, not 50. Removing all 50
// triggers stops the informer (ref count → 0). Removing 49 leaves it up.
//
// If this test fails, the operator burns apiserver watch quota per-trigger
// instead of per-GVK — an incident-shaped bug at scale.
func TestInformerManager_PerGVKRefCount(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	im := newIM(t)
	im.Start(ctx)
	defer im.Stop()

	const numTriggers = 50
	keys := make([]types.NamespacedName, 0, numTriggers)
	for i := range numTriggers {
		key := types.NamespacedName{Namespace: "default", Name: fmt.Sprintf("t-%d", i)}
		require.NoError(t, im.Register(key, deploymentGVK))
		keys = append(keys, key)
	}

	refs := im.gvkRefs()
	require.Len(t, refs, 1, "50 triggers on deployment = 1 informer, got %v", refs)
	assert.Equal(t, numTriggers, refs[deploymentGVK])

	// Remove 49 — informer still up.
	for i := range numTriggers - 1 {
		im.Unregister(keys[i])
	}
	refs = im.gvkRefs()
	assert.Equal(t, 1, refs[deploymentGVK], "49 removed = 1 remains")

	// Remove the last one — informer stops, ref map empty.
	im.Unregister(keys[numTriggers-1])
	refs = im.gvkRefs()
	assert.Empty(t, refs, "removing last trigger of GVK stops informer (no watch leak)")
}

// TestInformerManager_MultipleGVKsIndependent verifies each GVK owns its own
// informer + ref counter. Unregistering all Pod triggers does not touch the
// Deployment informer.
func TestInformerManager_MultipleGVKsIndependent(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	im := newIM(t)
	im.Start(ctx)
	defer im.Stop()

	depKey := types.NamespacedName{Namespace: "default", Name: "dep-trigger"}
	podKey := types.NamespacedName{Namespace: "default", Name: "pod-trigger"}
	require.NoError(t, im.Register(depKey, deploymentGVK))
	require.NoError(t, im.Register(podKey, podGVK))

	refs := im.gvkRefs()
	require.Len(t, refs, 2)
	assert.Equal(t, 1, refs[deploymentGVK])
	assert.Equal(t, 1, refs[podGVK])

	// Unregister Pod → Deployment untouched.
	im.Unregister(podKey)
	refs = im.gvkRefs()
	assert.Len(t, refs, 1)
	assert.Equal(t, 1, refs[deploymentGVK])
	_, hasPod := refs[podGVK]
	assert.False(t, hasPod, "pod informer must be stopped when its last trigger goes away")
}

// TestInformerManager_ReRegisterSameGVKIsIdempotent: registering the same
// (triggerKey, GVK) twice does NOT double-count the ref.
func TestInformerManager_ReRegisterSameGVKIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	im := newIM(t)
	im.Start(ctx)
	defer im.Stop()

	key := types.NamespacedName{Namespace: "default", Name: "t"}
	require.NoError(t, im.Register(key, deploymentGVK))
	require.NoError(t, im.Register(key, deploymentGVK))
	require.NoError(t, im.Register(key, deploymentGVK))

	assert.Equal(t, 1, im.gvkRefs()[deploymentGVK], "re-Register same key is idempotent")
	im.Unregister(key)
	assert.Empty(t, im.gvkRefs())
}

// TestInformerManager_SwitchGVK: a trigger's spec.resource is edited from
// deployment → pod. Re-Register with the new GVK must release the old one
// (informer stops if last consumer) and start the new one.
func TestInformerManager_SwitchGVK(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	im := newIM(t)
	im.Start(ctx)
	defer im.Stop()

	key := types.NamespacedName{Namespace: "default", Name: "t"}
	require.NoError(t, im.Register(key, deploymentGVK))
	require.Equal(t, 1, im.gvkRefs()[deploymentGVK])

	// Switch to pod — deployment informer stops (this was its only ref),
	// pod informer starts.
	require.NoError(t, im.Register(key, podGVK))
	refs := im.gvkRefs()
	assert.Len(t, refs, 1)
	assert.Equal(t, 1, refs[podGVK])
	_, hasDep := refs[deploymentGVK]
	assert.False(t, hasDep, "switching GVK must stop the old informer")
}

// TestInformerManager_RegisterWithoutStart_Errors: Register before Start is
// a bug — must not silently do nothing.
func TestInformerManager_RegisterWithoutStart_Errors(t *testing.T) {
	im := newIM(t)
	err := im.Register(types.NamespacedName{Namespace: "default", Name: "t"}, deploymentGVK)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Start not called")
}

// TestInformerManager_UnknownGVK_Errors: registering a GVK the mapper
// doesn't know errors out cleanly.
func TestInformerManager_UnknownGVK_Errors(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	im := newIM(t)
	im.Start(ctx)
	defer im.Stop()

	unknown := schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Widget"}
	err := im.Register(types.NamespacedName{Namespace: "default", Name: "t"}, unknown)
	assert.Error(t, err)
}

// TestInformerManager_StopIsSafeMultipleTimes: Stop() twice must not panic.
func TestInformerManager_StopIsSafeMultipleTimes(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	im := newIM(t)
	im.Start(ctx)
	require.NoError(t, im.Register(types.NamespacedName{Namespace: "default", Name: "t"}, deploymentGVK))
	im.Stop()
	// Second stop — no panic, no crash.
	im.Stop()
	// Give any goroutine time to exit — but no sleep dependence: just
	// prove state is empty.
	_ = time.Millisecond
	assert.Empty(t, im.gvkRefs())
}

// TestInformerManager_UnregisterUnknownTriggerIsNoOp: Unregister on a key
// that was never Registered must not panic.
func TestInformerManager_UnregisterUnknownTriggerIsNoOp(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	im := newIM(t)
	im.Start(ctx)
	defer im.Stop()
	im.Unregister(types.NamespacedName{Namespace: "default", Name: "never-registered"})
	assert.Empty(t, im.gvkRefs())
}
