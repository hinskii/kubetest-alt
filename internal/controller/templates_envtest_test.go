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
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// TestReconcile_Step13_ResolveFailed_MissingTemplate: a Test with
// spec.use=[non-existent-template] passes the webhook (which relaxes
// image/command when spec.use is set) but the controller must
// transition the TestRun to phase=error with reason=ResolveFailed
// naming the missing template.
func TestReconcile_Step13_ResolveFailed_MissingTemplate(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	// Test that relies on a non-existent template. Container is empty —
	// the webhook allows this because spec.use is set.
	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "resolve-fail-test", Namespace: ns},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: PolicyAllow,
			Use:               []string{"does-not-exist"},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, test))

	run := newRunFixture(ns, "resolve-fail-run", "resolve-fail-test")
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	final := waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseError, 5*time.Second)
	assert.Contains(t, final.Status.Message, ReasonResolveFailed)
	assert.Contains(t, final.Status.Message, `"does-not-exist"`,
		"error must name the missing template so users can fix it quickly")
}

// TestReconcile_Step13_ResolveFailed_TemplateStillMissingImage: template
// resolves but neither Test nor template supplies container.image →
// ValidateResolved should fail with reason=ResolveFailed.
func TestReconcile_Step13_ResolveFailed_TemplateStillMissingImage(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	// Template supplies only Args — no image.
	tmpl := &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "no-image-tmpl", Namespace: ns},
		Spec: testsv1alpha1.TestTemplateSpec{
			Container: testsv1alpha1.ContainerConfig{Args: []string{"run"}},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, tmpl))

	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "no-image-test", Namespace: ns},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: PolicyAllow,
			Use:               []string{"no-image-tmpl"},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, test))

	run := newRunFixture(ns, "no-image-run", "no-image-test")
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	final := waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseError, 5*time.Second)
	assert.Contains(t, final.Status.Message, ReasonResolveFailed)
	assert.Contains(t, final.Status.Message, "container.image is empty")
}

// TestReconcile_Step13_ResolvedSpec_FullResolutionSnapshotted: a Test
// using a template with config defaults + `{{ config.x }}` interpolation
// produces a resolvedSpec whose Container.Args reflect the RESOLVED
// values, not the raw templates. Proves the compiler downstream reads
// the resolved snapshot rather than re-resolving.
func TestReconcile_Step13_ResolvedSpec_FullResolutionSnapshotted(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	tmpl := &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "k6-basic", Namespace: ns},
		Spec: testsv1alpha1.TestTemplateSpec{
			Container: testsv1alpha1.ContainerConfig{
				Image: "grafana/k6:2.2.0",
				// The template's Args reference config that resolves to a
				// concrete value AFTER Config resolution.
				Args: []string{"run", "--vus", "{{ config.vus }}", "s.js"},
			},
			Config: map[string]testsv1alpha1.Parameter{
				"vus": {Type: "integer", Default: "10"},
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, tmpl))

	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "k6-consumer", Namespace: ns},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: PolicyAllow,
			Use:               []string{"k6-basic"},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, test))

	run := newRunFixture(ns, "k6-consumer-run", "k6-consumer")
	run.Spec.Config = map[string]string{"vus": "42"}
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	// Reaches queued (resolvedSpec set).
	waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseQueued, 5*time.Second)

	var fresh testsv1alpha1.TestRun
	require.NoError(t, k8sClient.Get(ctx, runKey, &fresh))
	require.NotEmpty(t, fresh.Status.ResolvedSpec)

	var snap testsv1alpha1.TestSpec
	require.NoError(t, json.Unmarshal([]byte(fresh.Status.ResolvedSpec), &snap))
	assert.Equal(t, "grafana/k6:2.2.0", snap.Container.Image,
		"template's image survives into resolved snapshot")
	assert.Equal(t, []string{"run", "--vus", "42", "s.js"}, snap.Container.Args,
		"config override (vus=42) and expression eval applied in snapshot")
}

// TestReconcile_Step13_TemplateEditAfterStart_DoesNotChangeSnapshot is the
// §15.5 invariant applied to templates: once a TestRun has resolved,
// EDITING the referenced TestTemplate MUST NOT change the run's
// ResolvedSpec — the snapshot is frozen at start.
//
// Otherwise historical runs would correspond to no recorded definition
// (the current definition on-disk is a different template body).
//
// Assertion strategy: DO NOT sleep. A sleep-based test passes by
// accident if the reconciler simply doesn't wake up during the sleep,
// which would leave the invariant untested. Instead, after the template
// edit, we FORCE a TestRun reconcile by touching an annotation on the
// TestRun (a metadata change enqueues a reconcile), then wait for the
// reconcile count to actually increase past the pre-touch baseline
// before we assert the snapshot is unchanged. This proves the
// reconciler DID run and DID see the edited template — and still left
// ResolvedSpec alone.
func TestReconcile_Step13_TemplateEditAfterStart_DoesNotChangeSnapshot(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	tmpl := &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "editable-tmpl", Namespace: ns},
		Spec: testsv1alpha1.TestTemplateSpec{
			Container: testsv1alpha1.ContainerConfig{
				Image: "grafana/k6:2.2.0",
				Args:  []string{"run", "original.js"},
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, tmpl))

	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "snapshot-consumer", Namespace: ns},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: PolicyAllow,
			Use:               []string{"editable-tmpl"},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, test))

	run := newRunFixture(ns, "snapshot-consumer-run", "snapshot-consumer")
	require.NoError(t, k8sClient.Create(ctx, run))

	runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
	waitForPhase(t, ctx, runKey, testsv1alpha1.PhaseQueued, 5*time.Second)

	// Capture the snapshot BEFORE the template edit.
	var snap1 testsv1alpha1.TestRun
	require.NoError(t, k8sClient.Get(ctx, runKey, &snap1))
	firstSnap := snap1.Status.ResolvedSpec
	require.NotEmpty(t, firstSnap)

	// Edit the template — different Args. This alone does NOT enqueue
	// the TestRun (the TestRun reconciler doesn't watch TestTemplates);
	// the forced reconcile below is what proves the invariant.
	require.NoError(t, k8sClient.Get(ctx,
		client.ObjectKey{Namespace: ns, Name: tmpl.Name}, tmpl))
	tmpl.Spec.Container.Args = []string{"run", "changed.js"}
	require.NoError(t, k8sClient.Update(ctx, tmpl))

	// Force-reconcile path: capture baseline count, then patch a fresh
	// annotation onto the TestRun. controller-runtime enqueues a
	// reconcile for the object on any metadata change; we then wait
	// until the reconcile counter has advanced past baseline.
	baseline := reconcileCount(runKey)
	patch := client.MergeFrom(snap1.DeepCopy())
	if snap1.Annotations == nil {
		snap1.Annotations = map[string]string{}
	}
	snap1.Annotations["kubetest.io/test-force-reconcile"] = "template-edit-invariant"
	require.NoError(t, k8sClient.Patch(ctx, &snap1, patch))

	require.Eventually(t, func() bool {
		return reconcileCount(runKey) > baseline
	}, 3*time.Second, 20*time.Millisecond,
		"forced reconcile must fire after annotation patch — otherwise the invariant is untested")

	// The reconciler ran with the edited template visible in the cache.
	// ResolvedSpec MUST still equal the pre-edit snapshot.
	var snap2 testsv1alpha1.TestRun
	require.NoError(t, k8sClient.Get(ctx, runKey, &snap2))
	assert.Equal(t, firstSnap, snap2.Status.ResolvedSpec,
		"§15.5: template edit after run start MUST NOT change TestRun.status.resolvedSpec, even when a reconcile fires with the edited template visible")
}

// TestReconcile_Step13_WebhookAllowsMissingImage_WhenSpecUse: end-to-end
// that a Test with spec.use=[t] and NO container.image is accepted by the
// api server (webhook accept path — step 13 relaxation).
func TestReconcile_Step13_WebhookAllowsMissingImage_WhenSpecUse(t *testing.T) {
	ctx := context.Background()
	ns := uniqueNamespace(t)

	// The webhook isn't wired in this envtest suite (the shared main test
	// wires only the controllers), so this is really "the api server + our
	// CRD schema accept the Test object". Kept for symmetry: if we later
	// add webhook wiring here, this test doubles as a webhook admission
	// pin — a Test without image should NOT be rejected when spec.use is
	// non-empty.
	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "webhook-relax-test", Namespace: ns},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: PolicyAllow,
			Use:               []string{"any-template"},
			// no Container fields at all
		},
	}
	require.NoError(t, k8sClient.Create(ctx, test))
}
