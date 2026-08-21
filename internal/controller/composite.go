/*
Copyright 2026.

Step-17 composite reconciler. This file is the ONLY place composite
runs are handled — the leaf path (observeOrCreateJob, inspectJob, …)
is untouched. Split into its own file to keep testrun_controller.go
tight.

Approach:
  - Composite runs never create Jobs — they create CHILD TestRuns and
    let the existing leaf path handle everything below (Job compile,
    logs, artifacts, verdict, metrics).
  - The parent watches its children via the LabelParentRun label on
    the CR (SetupWithManager adds the Watches).
  - Step aggregation + sequencing lives in internal/composer (pure).
    This file wires the reconciler-side glue: fetch child specs,
    render config, name children deterministically, list, aggregate,
    advance / skip.
*/

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrl "sigs.k8s.io/controller-runtime/pkg/reconcile"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/compiler"
	"github.com/hinskii/kubetest-alt/internal/composer"
)

// compositeRequeue is the fallback re-poll interval while children
// are in flight. Owns(TestRun) is the primary event source; this
// only fires when the informer cache lags on a child status write
// (envtest race, ownerRef stripped, or a coalesced event burst) or
// when a step's timeout wall-clock may expire without any child
// event to trigger a check.
var compositeRequeue = 3 * time.Second

// validateCompositeGraph resolves the composite Test's execute graph
// via composer.ResolveGraph and returns a human-readable error on
// cycle / depth overrun. Missing children are NOT an error here (see
// composer.ResolveGraph doc); those surface at step exec time so
// per-step optional flags can honor them.
func (r *TestRunReconciler) validateCompositeGraph(ctx context.Context, root *testsv1alpha1.Test, spec *testsv1alpha1.TestSpec) error {
	// Build a lookup closure that reads Tests from the API on demand;
	// composer.ResolveGraph calls it once per named ref (cycles
	// short-circuit before a re-fetch).
	lookup := func(name string) (*testsv1alpha1.TestSpec, bool) {
		if name == root.Name {
			return spec, true
		}
		var t testsv1alpha1.Test
		if err := r.Get(ctx, types.NamespacedName{Namespace: root.Namespace, Name: name}, &t); err != nil {
			return nil, false
		}
		return &t.Spec, true
	}
	if err := composer.ResolveGraph(root.Name, spec, lookup); err != nil {
		return fmt.Errorf("composition graph: %w", err)
	}
	return nil
}

// reconcileComposite is the composite entry point: figure out which
// step is active, ensure its children exist, aggregate when they've
// all landed, advance / skip / terminate.
func (r *TestRunReconciler) reconcileComposite(ctx context.Context, logger interface{ Info(string, ...any) }, run *testsv1alpha1.TestRun) (ctrl.Result, error) {
	var snap testsv1alpha1.TestSpec
	if err := json.Unmarshal([]byte(run.Status.ResolvedSpec), &snap); err != nil {
		return r.transitionTerminal(ctx, run, testsv1alpha1.PhaseError,
			ReasonCompileError, fmt.Sprintf("composite resolvedSpec unmarshal: %v", err))
	}
	if run.Status.Steps == nil {
		run.Status.Steps = map[string]testsv1alpha1.StepResult{}
	}
	// Promote to Running on first pass (matches leaf-run semantics).
	if run.Status.Phase != testsv1alpha1.PhaseRunning {
		run.Status.Phase = testsv1alpha1.PhaseRunning
		if run.Status.StartedAt == nil {
			now := r.Now()
			run.Status.StartedAt = &now
		}
		if err := r.Status().Update(ctx, run); err != nil {
			return ctrl.Result{}, err
		}
		// Fall through — we've written status; the informer will
		// re-enqueue, but we can also continue processing now.
	}

	// Enumerate child TestRuns owned by this parent so we can group by step.
	kids, err := r.listChildRuns(ctx, run)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Step-by-step walk.
	stepVerdicts := make([]*composer.StepVerdict, len(snap.Steps))
	optional := make([]bool, len(snap.Steps))
	for i := range snap.Steps {
		optional[i] = snap.Steps[i].Optional
	}
	for i, step := range snap.Steps {
		stepKey := fmt.Sprintf("s%d", i)
		existing := run.Status.Steps[stepKey]

		// Skip-on-fail check: if any prior non-optional step failed AND
		// this step's condition isn't "always", mark skipped and skip.
		if shouldSkip(stepVerdicts, snap.Steps, i) {
			if existing.Phase != testsv1alpha1.StepPhaseSkipped {
				now := r.Now()
				run.Status.Steps[stepKey] = testsv1alpha1.StepResult{
					Phase:      testsv1alpha1.StepPhaseSkipped,
					StartedAt:  &now,
					FinishedAt: &now,
				}
			}
			stepVerdicts[i] = nil // nil = skipped in ParentPhase
			continue
		}

		// Look up (or create) children for this step.
		stepKids := kidsForStep(kids, i)
		expected := expectedChildren(step, i)
		if err := r.ensureStepChildren(ctx, logger, run, i, step, stepKids, expected); err != nil {
			return ctrl.Result{}, err
		}
		// Refresh after any creates.
		if len(stepKids) < len(expected) {
			kids, err = r.listChildRuns(ctx, run)
			if err != nil {
				return ctrl.Result{}, err
			}
			stepKids = kidsForStep(kids, i)
		}

		// Are all expected children present AND terminal?
		outcomes, allDone := gatherOutcomes(stepKids, expected)
		if !allDone {
			// Step still in flight — mark the step-aggregate as running
			// so the GUI can show progress, then requeue softly.
			if existing.Phase == "" {
				now := r.Now()
				run.Status.Steps[stepKey] = testsv1alpha1.StepResult{
					Phase:     testsv1alpha1.StepPhaseRunning,
					StartedAt: &now,
				}
			}
			// Persist per-child StepResult entries (nice-to-have; the
			// child TestRuns are the source of truth, but this keeps
			// GUI-only clients from needing a second list call).
			r.updatePerChildStepResults(run, 0, stepKids, expected)
			if err := r.Status().Update(ctx, run); err != nil {
				return ctrl.Result{}, err
			}
			// Step timeout: if wall-clock since first child creation
			// exceeded step.timeout, abort remaining children and
			// mark the step failed.
			if step.Timeout != nil && step.Timeout.Duration > 0 {
				if existing.StartedAt != nil && r.Now().Sub(existing.StartedAt.Time) > step.Timeout.Duration {
					if aerr := r.abortStepChildren(ctx, stepKids); aerr != nil {
						return ctrl.Result{}, aerr
					}
					v := composer.StepVerdict{Phase: testsv1alpha1.PhaseFailed, Message: "step timeout exceeded"}
					r.recordStepAggregate(run, i, v)
					stepVerdicts[i] = &v
					// Continue: subsequent steps decide via shouldSkip.
					continue
				}
			}
			return ctrl.Result{RequeueAfter: compositeRequeue}, nil
		}

		// All done — aggregate.
		v := composer.Aggregate(outcomes, composer.AggregateOpts{Negative: step.Negative})
		r.recordStepAggregate(run, i, v)
		r.updatePerChildStepResults(run, 0, stepKids, expected)
		stepVerdicts[i] = &v
	}

	// Every step either ran or was skipped — terminal.
	parent := composer.ParentPhase(stepVerdicts, optional)
	msg := parentMessage(stepVerdicts, optional)
	return r.transitionTerminal(ctx, run, parent, "", msg)
}

// expectedChild names one anticipated child TestRun for a step.
type expectedChild struct {
	Key     string // "s{step}/{testRef}[{i}]" — matches StepResult map key
	Name    string // "{parent}-s{step}-{testRef}-{i}"
	StepIdx int
	TestRef string
	Index   int32
	Count   int32
	Config  map[string]string
}

// expectedChildren enumerates every child (across all refs in the step,
// with count replicas each) that this step SHOULD have.
func expectedChildren(step testsv1alpha1.Step, stepIdx int) []expectedChild {
	if step.Execute == nil {
		return nil
	}
	var out []expectedChild
	for _, ref := range step.Execute.Tests {
		count := ref.Count
		if count == 0 {
			count = 1
		}
		for i := int32(0); i < count; i++ {
			out = append(out, expectedChild{
				Key:     fmt.Sprintf("s%d/%s[%d]", stepIdx, ref.Name, i),
				StepIdx: stepIdx,
				TestRef: ref.Name,
				Index:   i,
				Count:   count,
				Config:  composer.RenderChildConfig(ref.Config, i, count),
			})
		}
	}
	return out
}

// listChildRuns returns every TestRun labeled with parent-run = <parent>.
// Uses APIReader (cache-bypass) when wired — the informer cache lags
// behind child status writes badly enough under envtest load to leave
// gatherOutcomes reading empty Phase values on children that ARE
// terminal at the API server. Same class of race as OrphanJobMissing;
// same fix.
func (r *TestRunReconciler) listChildRuns(ctx context.Context, run *testsv1alpha1.TestRun) ([]testsv1alpha1.TestRun, error) {
	var reader client.Reader = r.Client
	if r.APIReader != nil {
		reader = r.APIReader
	}
	var runs testsv1alpha1.TestRunList
	if err := reader.List(ctx, &runs,
		client.InNamespace(run.Namespace),
		client.MatchingLabels{compiler.LabelParentRun: run.Name}); err != nil {
		return nil, err
	}
	// Sort by step/exec-index for deterministic aggregation.
	slices.SortStableFunc(runs.Items, func(a, b testsv1alpha1.TestRun) int {
		return strings.Compare(a.Name, b.Name)
	})
	return runs.Items, nil
}

// kidsForStep filters child runs to those labeled with the given step.
func kidsForStep(all []testsv1alpha1.TestRun, stepIdx int) []testsv1alpha1.TestRun {
	want := fmt.Sprintf("%d", stepIdx)
	var out []testsv1alpha1.TestRun
	for _, r := range all {
		if r.Labels[compiler.LabelStep] == want {
			out = append(out, r)
		}
	}
	return out
}

// findChild returns the child matching one expectedChild by name-suffix.
// Deterministic child names + label triple make this O(N) with tiny N.
func findChildByExecIndex(kids []testsv1alpha1.TestRun, testRef string, execIdx string) *testsv1alpha1.TestRun {
	for i := range kids {
		k := &kids[i]
		if k.Labels[compiler.LabelExecIndex] == execIdx && strings.Contains(k.Name, testRef) {
			return k
		}
	}
	return nil
}

// gatherOutcomes returns per-expected-child phase + a done flag.
// done=true only when every expected child exists AND is terminal.
func gatherOutcomes(kids []testsv1alpha1.TestRun, expected []expectedChild) ([]composer.ChildOutcome, bool) {
	outcomes := make([]composer.ChildOutcome, 0, len(expected))
	for _, e := range expected {
		child := findChildByExecIndex(kids, e.TestRef, fmt.Sprintf("%d", e.Index))
		if child == nil {
			return nil, false
		}
		if !IsTerminalPhase(child.Status.Phase) {
			return nil, false
		}
		outcomes = append(outcomes, composer.ChildOutcome{Phase: child.Status.Phase})
	}
	return outcomes, true
}

// ensureStepChildren creates any expected children that don't yet
// exist. AlreadyExists is treated as success (idempotent — deterministic
// naming means a duplicate reconcile hits the same key and no-ops).
func (r *TestRunReconciler) ensureStepChildren(
	ctx context.Context,
	logger interface{ Info(string, ...any) },
	parent *testsv1alpha1.TestRun,
	stepIdx int,
	step testsv1alpha1.Step,
	kids []testsv1alpha1.TestRun,
	expected []expectedChild,
) error {
	// Delay honored on the FIRST child creation for the step.
	// Simpler than tracking a "when did we first see this step" state
	// — we just skip creation entirely until the delay has passed
	// since the parent's StartedAt (composite steps run sequentially
	// so parent.StartedAt is a good proxy for "when the composite began").
	// Refinement to per-step delay basis would require another status
	// field; punting until a real user wants it.
	if step.Delay != nil && step.Delay.Duration > 0 && parent.Status.StartedAt != nil {
		if r.Now().Sub(parent.Status.StartedAt.Time) < step.Delay.Duration {
			return nil
		}
	}
	// Parallelism cap: count in-flight (non-terminal) kids in this step,
	// only create more up to the cap.
	inFlight := 0
	for i := range kids {
		if !IsTerminalPhase(kids[i].Status.Phase) {
			inFlight++
		}
	}
	cap := int(step.Execute.Parallelism)
	if cap == 0 {
		cap = len(expected) // unlimited within step
	}
	for _, e := range expected {
		if inFlight >= cap {
			return nil
		}
		name := fmt.Sprintf("%s-s%d-%s-%d", parent.Name, stepIdx, e.TestRef, e.Index)
		// Already exists?
		if findChildByExecIndex(kids, e.TestRef, fmt.Sprintf("%d", e.Index)) != nil {
			continue
		}
		child := &testsv1alpha1.TestRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: parent.Namespace,
				Labels: map[string]string{
					compiler.LabelParentRun:    parent.Name,
					compiler.LabelStep:         fmt.Sprintf("%d", stepIdx),
					compiler.LabelExecIndex:    fmt.Sprintf("%d", e.Index),
					compiler.LabelKubetestTool: parent.Labels[compiler.LabelKubetestTool],
				},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: testsv1alpha1.GroupVersion.String(),
					Kind:       "TestRun",
					Name:       parent.Name,
					UID:        parent.UID,
					// Controller=true → parent owns child; Background cascade
					// deletes on parent Delete (envtest-safe).
					Controller:         boolPtr(true),
					BlockOwnerDeletion: boolPtr(true),
				}},
			},
			Spec: testsv1alpha1.TestRunSpec{
				TestRef: e.TestRef,
				Source:  parent.Spec.Source, // provenance inherited
				Config:  e.Config,
			},
		}
		if err := r.Create(ctx, child); err != nil {
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			return fmt.Errorf("create child %s: %w", name, err)
		}
		logger.Info("composite: created child",
			"parent", parent.Name, "step", stepIdx, "child", name, "testRef", e.TestRef)
		inFlight++
	}
	return nil
}

// abortStepChildren deletes any non-terminal child in the given slice.
// Used by step-timeout to force termination before advancing.
func (r *TestRunReconciler) abortStepChildren(ctx context.Context, kids []testsv1alpha1.TestRun) error {
	for i := range kids {
		if IsTerminalPhase(kids[i].Status.Phase) {
			continue
		}
		if err := r.Delete(ctx, &kids[i]); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("abort child %s: %w", kids[i].Name, err)
		}
	}
	return nil
}

// recordStepAggregate persists the per-step aggregate under the "s{i}"
// key so callers (GUI) can query one map instead of walking children.
func (r *TestRunReconciler) recordStepAggregate(run *testsv1alpha1.TestRun, stepIdx int, v composer.StepVerdict) {
	key := fmt.Sprintf("s%d", stepIdx)
	sr := run.Status.Steps[key]
	sr.Phase = testsv1alpha1.StepPhaseFromPhase(v.Phase)
	if sr.StartedAt == nil {
		now := r.Now()
		sr.StartedAt = &now
	}
	now := r.Now()
	sr.FinishedAt = &now
	run.Status.Steps[key] = sr
}

// updatePerChildStepResults writes one StepResult per child so the GUI
// can render a hierarchy without a second List. Idempotent — no-op
// when nothing has changed.
func (r *TestRunReconciler) updatePerChildStepResults(run *testsv1alpha1.TestRun, _ int, kids []testsv1alpha1.TestRun, expected []expectedChild) {
	for _, e := range expected {
		child := findChildByExecIndex(kids, e.TestRef, fmt.Sprintf("%d", e.Index))
		if child == nil {
			continue
		}
		sr := testsv1alpha1.StepResult{
			Phase:      testsv1alpha1.StepPhaseFromPhase(child.Status.Phase),
			QueuedAt:   child.Status.QueuedAt,
			StartedAt:  child.Status.StartedAt,
			FinishedAt: child.Status.FinishedAt,
		}
		run.Status.Steps[e.Key] = sr
	}
}

// shouldSkip returns true when a prior non-optional step failed AND the
// current step's condition is not "always". Aborted/error prior steps
// also skip subsequent condition:passed steps.
func shouldSkip(prior []*composer.StepVerdict, steps []testsv1alpha1.Step, currIdx int) bool {
	curr := steps[currIdx]
	if curr.Condition == composer.ConditionAlways {
		return false
	}
	for i := range currIdx {
		v := prior[i]
		if v == nil {
			continue
		}
		if steps[i].Optional && v.Phase == testsv1alpha1.PhaseFailed {
			continue
		}
		switch v.Phase {
		case testsv1alpha1.PhaseFailed, testsv1alpha1.PhaseAborted, testsv1alpha1.PhaseError:
			return true
		}
	}
	return false
}

// parentMessage returns a short "N/M steps passed" summary for the
// parent's TestRunStatus.Message. Skipped steps count as neither pass
// nor fail.
func parentMessage(verdicts []*composer.StepVerdict, optional []bool) string {
	var passed, failed, skipped int
	for i, v := range verdicts {
		if v == nil {
			skipped++
			continue
		}
		if v.Phase == testsv1alpha1.PhasePassed {
			passed++
			continue
		}
		if i < len(optional) && optional[i] && v.Phase == testsv1alpha1.PhaseFailed {
			passed++
			continue
		}
		failed++
	}
	return fmt.Sprintf("composite: %d passed, %d failed, %d skipped", passed, failed, skipped)
}

func boolPtr(b bool) *bool { return &b }

// _ interface{ error } — errors imports live in composer; keep here in
// case future callers need to compare errors from composer directly.
var _ = errors.New
