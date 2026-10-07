/*
Copyright 2026.
*/

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/compiler"
)

// TestReconcile_Composite_Sequential covers the plan-17 happy path:
// step1 passes → step2 children get created and run → parent passes.
func TestReconcile_Composite_Sequential(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	// Two leaf Tests + one composite Test referencing both, one per step.
	leafA := newTestFixture(ns, "leaf-a")
	require.NoError(t, k8sClient.Create(ctx, leafA))
	leafB := newTestFixture(ns, "leaf-b")
	require.NoError(t, k8sClient.Create(ctx, leafB))

	parent := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "composite-seq", Namespace: ns},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: PolicyAllow,
			Steps: []testsv1alpha1.Step{
				{Name: "smoke", Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "leaf-a"}}}},
				{Name: "load", Condition: "passed", Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "leaf-b"}}}},
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, parent))

	parentRun := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "composite-seq-run",
			Namespace: ns,
			Labels:    map[string]string{compiler.LabelKubetestTool: "composite"},
		},
		Spec: testsv1alpha1.TestRunSpec{TestRef: "composite-seq", Source: "api"},
	}
	require.NoError(t, k8sClient.Create(ctx, parentRun))
	parentKey := client.ObjectKey{Namespace: ns, Name: parentRun.Name}

	// Wait for parent to reach Running (step 1 children created).
	waitForPhase(t, ctx, parentKey, testsv1alpha1.PhaseRunning, 5*time.Second)

	// Assert exactly ONE child exists for step 0 (leaf-a) — step 1 must not
	// be materialized yet.
	assert.Eventually(t, func() bool {
		kids := listChildren(t, ctx, ns, parentRun.Name)
		if len(kids) != 1 {
			return false
		}
		return kids[0].Labels[compiler.LabelStep] == "0"
	}, 3*time.Second, 100*time.Millisecond, "expect exactly 1 step-0 child before step 1 runs")

	// Force step 0 child to Passed via a status write. In envtest there's
	// no Job controller so nothing else drives child phase.
	kids := listChildren(t, ctx, ns, parentRun.Name)
	require.Len(t, kids, 1)
	// Preload result reader for the child so its leaf reconciler transitions
	// cleanly instead of hitting MissingResult (that would surface as
	// phase=error and step 1 would be skipped).
	// Simpler path here: overwrite child status directly so aggregator sees passed.
	forceChildPhase(t, ctx, ns, kids[0].Name, testsv1alpha1.PhasePassed)

	// Step 1 child must appear.
	assert.Eventually(t, func() bool {
		kids := listChildren(t, ctx, ns, parentRun.Name)
		if len(kids) < 2 {
			return false
		}
		for _, k := range kids {
			if k.Labels[compiler.LabelStep] == "1" {
				return true
			}
		}
		return false
	}, 5*time.Second, 100*time.Millisecond, "step 1 child should be created after step 0 passes")

	// Force step 1 child to Passed.
	kids = listChildren(t, ctx, ns, parentRun.Name)
	for _, k := range kids {
		if k.Labels[compiler.LabelStep] == "1" {
			forceChildPhase(t, ctx, ns, k.Name, testsv1alpha1.PhasePassed)
		}
	}

	// Parent should reach passed.
	final := waitForPhase(t, ctx, parentKey, testsv1alpha1.PhasePassed, 5*time.Second)
	assert.Contains(t, final.Status.Message, "composite")
	// Per-step aggregate keys present.
	assert.Contains(t, final.Status.Steps, "s0")
	assert.Contains(t, final.Status.Steps, "s1")
	assert.Equal(t, testsv1alpha1.StepPhasePassed, final.Status.Steps["s0"].Phase)
	assert.Equal(t, testsv1alpha1.StepPhasePassed, final.Status.Steps["s1"].Phase)
}

// TestReconcile_Composite_SkipOnFail: step 1 fails → step 2 marked
// skipped (per-step) → parent failed, Phase enum unchanged.
func TestReconcile_Composite_SkipOnFail(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	leafA := newTestFixture(ns, "sk-leaf-a")
	require.NoError(t, k8sClient.Create(ctx, leafA))
	leafB := newTestFixture(ns, "sk-leaf-b")
	require.NoError(t, k8sClient.Create(ctx, leafB))

	parent := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "composite-skip", Namespace: ns},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: PolicyAllow,
			Steps: []testsv1alpha1.Step{
				{Name: "smoke", Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "sk-leaf-a"}}}},
				{Name: "load", Condition: "passed", Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "sk-leaf-b"}}}},
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, parent))

	parentRun := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "composite-skip-run",
			Namespace: ns,
			Labels:    map[string]string{compiler.LabelKubetestTool: "composite"},
		},
		Spec: testsv1alpha1.TestRunSpec{TestRef: "composite-skip", Source: "api"},
	}
	require.NoError(t, k8sClient.Create(ctx, parentRun))
	parentKey := client.ObjectKey{Namespace: ns, Name: parentRun.Name}
	waitForPhase(t, ctx, parentKey, testsv1alpha1.PhaseRunning, 5*time.Second)

	// Fail step 0's child.
	assert.Eventually(t, func() bool {
		return len(listChildren(t, ctx, ns, parentRun.Name)) == 1
	}, 3*time.Second, 50*time.Millisecond)
	kids := listChildren(t, ctx, ns, parentRun.Name)
	forceChildPhase(t, ctx, ns, kids[0].Name, testsv1alpha1.PhaseFailed)

	// Parent should go straight to failed (step 1 skipped). 15s covers
	// the worst-case cache-lag + fallback-requeue path.
	final := waitForPhase(t, ctx, parentKey, testsv1alpha1.PhaseFailed, 15*time.Second)
	// Step 1 was never created.
	kids = listChildren(t, ctx, ns, parentRun.Name)
	for _, k := range kids {
		assert.NotEqualf(t, "1", k.Labels[compiler.LabelStep],
			"step 1 child must not be created after step 0 failed (skip-on-fail)")
	}
	// s1 in Status.Steps map is "skipped".
	require.Contains(t, final.Status.Steps, "s1")
	assert.Equal(t, testsv1alpha1.StepPhaseSkipped, final.Status.Steps["s1"].Phase,
		"step 1 must be marked skipped in Status.Steps map (NOT a Phase-level state)")
	// Parent's own Phase is failed — the enum was NOT extended with skipped.
	assert.Equal(t, testsv1alpha1.PhaseFailed, final.Status.Phase)
}

// TestReconcile_Composite_CycleErrors: A → B → A composition graph
// errors at setup with a human-readable path.
func TestReconcile_Composite_CycleErrors(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	// a → b → a (cycle).
	a := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "cyc-a", Namespace: ns},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: PolicyAllow,
			Steps:             []testsv1alpha1.Step{{Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "cyc-b"}}}}},
		},
	}
	b := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "cyc-b", Namespace: ns},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: PolicyAllow,
			Steps:             []testsv1alpha1.Step{{Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "cyc-a"}}}}},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, a))
	require.NoError(t, k8sClient.Create(ctx, b))

	run := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "cyc-run", Namespace: ns},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "cyc-a", Source: "api"},
	}
	require.NoError(t, k8sClient.Create(ctx, run))
	final := waitForPhase(t, ctx, client.ObjectKey{Namespace: ns, Name: run.Name}, testsv1alpha1.PhaseError, 5*time.Second)
	assert.Contains(t, final.Status.Message, "composition")
	assert.Contains(t, final.Status.Message, "cycle")
}

// TestReconcile_Composite_Idempotent: reconciling twice creates no
// duplicate children (deterministic names).
func TestReconcile_Composite_Idempotent(t *testing.T) {
	fakeResults.Reset()
	resetReconcileCounts()
	ctx := context.Background()
	ns := uniqueNamespace(t)

	leaf := newTestFixture(ns, "idem-leaf")
	require.NoError(t, k8sClient.Create(ctx, leaf))
	parent := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "idem-comp", Namespace: ns},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: PolicyAllow,
			Steps:             []testsv1alpha1.Step{{Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "idem-leaf", Count: 3}}}}},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, parent))
	run := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "idem-run", Namespace: ns},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "idem-comp", Source: "api"},
	}
	require.NoError(t, k8sClient.Create(ctx, run))

	// Wait for children to appear.
	assert.Eventually(t, func() bool {
		return len(listChildren(t, ctx, ns, run.Name)) == 3
	}, 5*time.Second, 100*time.Millisecond)

	// Poke the parent to force a re-reconcile (annotate). Should not create dupes.
	var fresh testsv1alpha1.TestRun
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: run.Name}, &fresh))
	if fresh.Annotations == nil {
		fresh.Annotations = map[string]string{}
	}
	fresh.Annotations["poke"] = "1"
	require.NoError(t, k8sClient.Update(ctx, &fresh))

	// Give the reconciler a moment; count MUST remain 3.
	time.Sleep(500 * time.Millisecond)
	kids := listChildren(t, ctx, ns, run.Name)
	assert.Len(t, kids, 3, "deterministic naming must prevent duplicate child creation on re-reconcile")
}

// ---- helpers ----

func listChildren(t *testing.T, ctx context.Context, ns, parent string) []testsv1alpha1.TestRun {
	t.Helper()
	var runs testsv1alpha1.TestRunList
	require.NoError(t, k8sClient.List(ctx, &runs,
		client.InNamespace(ns),
		client.MatchingLabels{compiler.LabelParentRun: parent}))
	return runs.Items
}

// forceChildPhase writes a terminal phase into a child's status. In
// envtest there's no Job controller, so we bypass the leaf reconciler
// and set it directly — the parent's watch picks it up.
func forceChildPhase(t *testing.T, ctx context.Context, ns, name string, phase testsv1alpha1.Phase) {
	t.Helper()
	// The child's own reconciler writes its status too: retry on conflict
	// until our write lands on the latest version.
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var child testsv1alpha1.TestRun
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &child); err != nil {
			return err
		}
		// Snapshot a minimal resolvedSpec so the leaf reconciler won't
		// re-enter setup and race us.
		if child.Status.ResolvedSpec == "" {
			snap := testsv1alpha1.TestSpec{Container: testsv1alpha1.ContainerConfig{Image: "x", Args: []string{"y"}}}
			b, _ := json.Marshal(snap)
			child.Status.ResolvedSpec = string(b)
		}
		child.Status.Phase = phase
		now := metav1.Now()
		if child.Status.StartedAt == nil {
			child.Status.StartedAt = &now
		}
		child.Status.FinishedAt = &now
		return k8sClient.Status().Update(ctx, &child)
	})
	require.NoError(t, err, fmt.Sprintf("force phase %s on %s", phase, name))
}

// fixes.md #16: steps[].retry re-creates a failed child (<child>-r<N>);
// the step uses the latest try of each child.
func TestReconcile_Composite_StepRetry(t *testing.T) {
	fakeResults.Reset()
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, newTestFixture(ns, "rt-leaf")))
	parent := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "composite-retry", Namespace: ns},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: PolicyAllow,
			Steps: []testsv1alpha1.Step{{
				Name:    "flaky",
				Retry:   &testsv1alpha1.RetryPolicy{Count: 2},
				Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "rt-leaf"}}},
			}},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, parent))
	parentRun := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "retry-run", Namespace: ns},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "composite-retry", Source: "api"},
	}
	require.NoError(t, k8sClient.Create(ctx, parentRun))
	parentKey := client.ObjectKey{Namespace: ns, Name: parentRun.Name}

	waitChild := func(name string) {
		t.Helper()
		require.Eventually(t, func() bool {
			for _, k := range listChildren(t, ctx, ns, parentRun.Name) {
				if k.Name == name {
					return true
				}
			}
			return false
		}, 10*time.Second, 50*time.Millisecond, "child %s", name)
	}
	first := "retry-run-s0-rt-leaf-0"
	waitChild(first)
	forceChildPhase(t, ctx, ns, first, testsv1alpha1.PhaseFailed)
	waitChild(first + "-r1")
	forceChildPhase(t, ctx, ns, first+"-r1", testsv1alpha1.PhaseError)
	waitChild(first + "-r2")
	forceChildPhase(t, ctx, ns, first+"-r2", testsv1alpha1.PhasePassed)

	final := waitForPhase(t, ctx, parentKey, testsv1alpha1.PhasePassed, 15*time.Second)
	assert.Equal(t, "try 3 of 3 ("+first+"-r2)", final.Status.Steps["s0/rt-leaf[0]"].Message)
	kids := listChildren(t, ctx, ns, parentRun.Name)
	assert.Len(t, kids, 3, "no try beyond retry.count")
	for _, k := range kids {
		if k.Name == first+"-r1" {
			assert.Equal(t, "1", k.Labels[compiler.LabelAttempt])
		}
	}
}

// Retries run out: the step (and parent) fail with the last try.
func TestReconcile_Composite_StepRetryExhausted(t *testing.T) {
	fakeResults.Reset()
	ctx := context.Background()
	ns := uniqueNamespace(t)
	require.NoError(t, k8sClient.Create(ctx, newTestFixture(ns, "rx-leaf")))
	require.NoError(t, k8sClient.Create(ctx, &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "composite-rx", Namespace: ns},
		Spec: testsv1alpha1.TestSpec{
			ConcurrencyPolicy: PolicyAllow,
			Steps: []testsv1alpha1.Step{{
				Retry:   &testsv1alpha1.RetryPolicy{Count: 1},
				Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "rx-leaf"}}},
			}},
		},
	}))
	require.NoError(t, k8sClient.Create(ctx, &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "rx-run", Namespace: ns},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "composite-rx", Source: "api"},
	}))
	for _, name := range []string{"rx-run-s0-rx-leaf-0", "rx-run-s0-rx-leaf-0-r1"} {
		require.Eventually(t, func() bool {
			var c testsv1alpha1.TestRun
			return k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &c) == nil
		}, 10*time.Second, 50*time.Millisecond, name)
		forceChildPhase(t, ctx, ns, name, testsv1alpha1.PhaseFailed)
	}
	waitForPhase(t, ctx, client.ObjectKey{Namespace: ns, Name: "rx-run"}, testsv1alpha1.PhaseFailed, 15*time.Second)
	assert.Len(t, listChildren(t, ctx, ns, "rx-run"), 2)
}
