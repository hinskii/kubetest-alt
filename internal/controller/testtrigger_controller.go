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
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/scheduler"
)

// SourceTrigger is TestRun.spec.source on runs the TestTrigger controller
// creates. Matches the CRD enum on TestRunSpec.Source.
const SourceTrigger = "trigger"

// TestTrigger concurrency policy strings (mirror CRD enum, lowercase form
// per api/v1alpha1.TestTriggerSpec.ConcurrencyPolicy).
const (
	TriggerConcurrencyAllow   = "allow"
	TriggerConcurrencyForbid  = "forbid"
	TriggerConcurrencyReplace = "replace"
)

// Labels + annotations stamped on TestRuns the trigger creates. UX sugar for
// kubectl selectors; the semantic source of truth is spec.source.
const (
	LabelTriggerName        = "kubetest.io/trigger-name"
	LabelTriggerFor         = "kubetest.io/triggered-for-test"
	AnnotationTriggerTarget = "kubetest.io/trigger-target"
	AnnotationTriggerEvent  = "kubetest.io/trigger-event"
)

// TestTriggerReconciler watches TestTrigger CRs and drives the k8s-event →
// TestRun pipeline: dynamic informer per unique GVK, resourceSelector
// filtering, conditionSpec gating, concurrencyPolicy enforcement.
//
// Two loops:
//   - controller-runtime Reconcile: register/unregister informers when a
//     TestTrigger CR is created / updated / deleted.
//   - triggerRunnable: pumps informer events and periodically evaluates
//     pending condition gates.
//
// Both run behind the manager's leader election so exactly one replica
// dispatches events (and creates TestRuns).
type TestTriggerReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Clock is shared with the scheduler — same discipline: no wall-clock
	// dependencies in tests. Real clock wired by cmd/operator.
	Clock scheduler.Clock

	// Dyn + Mapper resolve spec.resource → GVK and drive the dynamic
	// informers. Wired by cmd/operator; unit tests inject fakes.
	Dyn    dynamic.Interface
	Mapper meta.RESTMapper

	// Recorder emits k8s Events on the TestTrigger CR (invalid resource,
	// timeout, fire). Wired by cmd/operator via mgr.GetEventRecorderFor.
	Recorder record.EventRecorder

	// GateEvalInterval controls how often the runnable calls
	// gateManager.Evaluate. Default 1s. Set higher for tests that want to
	// step manually.
	GateEvalInterval time.Duration

	// Populated by SetupWithManager.
	informers *informerManager
	gates     *gateManager
}

// NeedLeaderElection is asserted on the runnable side; the reconciler is
// controller-runtime managed and always leader-elected.
var _ = (&TestTriggerReconciler{}).SetupWithManager

// SetupWithManager wires the reconciler + the events/gates runnable into
// the manager. Both are leader-election-gated by controller-runtime.
func (r *TestTriggerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Clock == nil {
		r.Clock = scheduler.RealClock{}
	}
	if r.GateEvalInterval == 0 {
		r.GateEvalInterval = 1 * time.Second
	}
	if r.Dyn == nil {
		return errors.New("TestTriggerReconciler.Dyn is required")
	}
	if r.Mapper == nil {
		return errors.New("TestTriggerReconciler.Mapper is required")
	}
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorderFor("testtrigger-controller")
	}
	r.informers = newInformerManager(r.Dyn, r.Mapper)
	r.gates = newGateManager(r.Clock, r.fireTestRun)

	if err := mgr.Add(&triggerRunnable{r: r}); err != nil {
		return fmt.Errorf("add trigger runnable: %w", err)
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&testsv1alpha1.TestTrigger{}).
		Named("testtrigger").
		Complete(r)
}

// +kubebuilder:rbac:groups=tests.kubetest.io,resources=testtriggers,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=tests.kubetest.io,resources=testtriggers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
//
// NOTE: additional dynamic GVKs beyond the ones marked above require extra
// RBAC. `deployment` is the first-supported GVK for step 12; extending to
// e.g. Argo Rollouts is a matter of adding the corresponding marker and
// re-running `make manifests`. The controller itself is GVK-generic — no
// per-GVK code path.

// Reconcile registers or unregisters the trigger's GVK with the informer
// manager. This method does NOT dispatch events — that happens in the
// runnable's event loop.
func (r *TestTriggerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("testtrigger", req.NamespacedName)

	var t testsv1alpha1.TestTrigger
	if err := r.Get(ctx, req.NamespacedName, &t); err != nil {
		if apierrors.IsNotFound(err) {
			r.informers.Unregister(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !t.DeletionTimestamp.IsZero() {
		r.informers.Unregister(req.NamespacedName)
		return ctrl.Result{}, nil
	}

	gvk, err := r.resolveGVK(t.Spec.Resource)
	if err != nil {
		r.Recorder.Eventf(&t, "Warning", "InvalidResource",
			"cannot resolve spec.resource %q: %v", t.Spec.Resource, err)
		logger.Info("cannot resolve spec.resource", "resource", t.Spec.Resource, "err", err)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	if err := r.informers.Register(req.NamespacedName, gvk); err != nil {
		r.Recorder.Eventf(&t, "Warning", "InformerRegistration",
			"cannot start informer for %s: %v", gvk, err)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}
	logger.V(1).Info("registered TestTrigger", "gvk", gvk)
	return ctrl.Result{}, nil
}

// Well-known GVK / spec.resource shortcut string constants used by both the
// resolver and unit tests. Centralized so an accidental rename in one place
// can't drift out of sync with the other.
const (
	groupApps          = "apps"
	kindDeployment     = "Deployment"
	kindPod            = "Pod"
	resourceDeployment = "deployment"
	resourcesDeploys   = "deployments"
	resourcePod        = "pod"
	resourcesPods      = "pods"
)

// deploymentGVK / podGVK are the first-class step 12 shortcuts spec.resource
// accepts as single-word forms. Kept as package consts so the resolver, the
// tests, and any future controller code all speak of them by one name.
var (
	shortcutDeploymentGVK = schema.GroupVersionKind{Group: groupApps, Version: "v1", Kind: kindDeployment}
	shortcutPodGVK        = schema.GroupVersionKind{Group: "", Version: "v1", Kind: kindPod}
)

// resolveGVK maps spec.resource ("deployment", "pods", "widgets.example.com")
// to a GroupVersionKind via the RESTMapper. Short lower-cased shortcuts for
// the first-class step 12 GVK land here so operators don't need to write
// full GVR strings for the common case.
func (r *TestTriggerReconciler) resolveGVK(resource string) (schema.GroupVersionKind, error) {
	s := strings.ToLower(strings.TrimSpace(resource))
	if s == "" {
		return schema.GroupVersionKind{}, errors.New("spec.resource is empty")
	}
	// Convenience: single-word forms map to well-known core GVKs. Extending
	// this list is CHEAP and safe — the resolver still round-trips via the
	// RESTMapper on the general path below.
	switch s {
	case resourceDeployment, resourcesDeploys:
		return shortcutDeploymentGVK, nil
	case resourcePod, resourcesPods:
		return shortcutPodGVK, nil
	}
	// General form: "kind.group[/version]" or "resource.group".
	gvr, gk := schema.ParseResourceArg(s)
	var query schema.GroupVersionResource
	if gvr != nil {
		query = *gvr
	} else {
		query = schema.GroupVersionResource{Group: gk.Group, Resource: gk.Resource}
	}
	kinds, err := r.Mapper.KindsFor(query)
	if err != nil {
		return schema.GroupVersionKind{}, fmt.Errorf("KindsFor(%s): %w", query, err)
	}
	if len(kinds) == 0 {
		return schema.GroupVersionKind{}, fmt.Errorf("no GVK for %q", resource)
	}
	return kinds[0], nil
}

// handleEvent is the informer → gate manager path. Called from the
// runnable's event loop for every incoming resource event. Filters against
// every registered trigger for that GVK+eventKind, applies resourceSelector,
// and enqueues a gate for each matching (trigger, target).
func (r *TestTriggerReconciler) handleEvent(ctx context.Context, ev informerEvent) {
	triggers, err := r.listMatchingTriggers(ctx, ev.GVK, ev.Kind)
	if err != nil {
		log.FromContext(ctx).Error(err, "listMatchingTriggers", "gvk", ev.GVK.String())
		return
	}
	now := r.Clock.Now()
	for i := range triggers {
		t := &triggers[i]
		matched, err := SelectorMatches(t.Spec.ResourceSelector, ev.Obj)
		if err != nil {
			r.Recorder.Eventf(t, "Warning", "SelectorError", "%s", err.Error())
			continue
		}
		if !matched {
			continue
		}
		r.gates.Enqueue(&gate{Trigger: t.DeepCopy(), Target: ev.Obj, EventTime: now})
	}
}

// listMatchingTriggers returns every TestTrigger whose (resolved GVK, event)
// matches (gvk, event). Cluster-scoped list — TestTriggers can live in any
// namespace but a trigger in ns-A can target a deployment in ns-B via
// resourceSelector.namespace.
func (r *TestTriggerReconciler) listMatchingTriggers(ctx context.Context, gvk schema.GroupVersionKind, event string) ([]testsv1alpha1.TestTrigger, error) {
	var list testsv1alpha1.TestTriggerList
	if err := r.List(ctx, &list); err != nil {
		return nil, err
	}
	out := list.Items[:0]
	for _, t := range list.Items {
		if t.Spec.Event != event {
			continue
		}
		tgvk, err := r.resolveGVK(t.Spec.Resource)
		if err != nil {
			continue
		}
		if tgvk == gvk {
			out = append(out, t)
		}
	}
	return out, nil
}

// fireTestRun is the gate manager's firer callback. Resolves the target
// Test, enforces concurrencyPolicy, creates a TestRun with source=trigger,
// updates the trigger's LastFiredAt status.
func (r *TestTriggerReconciler) fireTestRun(ctx context.Context, g *gate) error {
	testName, testNS, err := r.resolveTestTarget(ctx, g.Trigger)
	if err != nil {
		r.Recorder.Eventf(g.Trigger, "Warning", "TestSelectorError", "%s", err.Error())
		return err
	}

	// Concurrency policy check. Replace / Allow are treated as Allow for
	// MVP (the TestRun-side controller already implements Replace via
	// Test.spec.concurrencyPolicy — a triggered run inherits that).
	if strings.ToLower(g.Trigger.Spec.ConcurrencyPolicy) == TriggerConcurrencyForbid {
		active, err := r.hasActiveRunForTest(ctx, testNS, testName)
		if err != nil {
			return err
		}
		if active {
			key := gateKeyFor(g)
			r.gates.FireSkipped(key, g.Trigger, g.EventTime,
				fmt.Sprintf("concurrencyPolicy=forbid: prior active run for Test %q", testName))
			r.Recorder.Eventf(g.Trigger, "Normal", "Skipped",
				"trigger fire skipped: prior active TestRun for Test %q", testName)
			// Returning nil signals "handled" — gate is removed by
			// FireSkipped; gateManager will not fall through into its own
			// remove path.
			return errGateAlreadyHandled
		}
	}

	run := r.buildTestRun(g, testName, testNS)
	if err := r.Create(ctx, run); err != nil {
		return fmt.Errorf("create TestRun for trigger %s/%s: %w",
			g.Trigger.Namespace, g.Trigger.Name, err)
	}
	r.Recorder.Eventf(g.Trigger, "Normal", "Fired",
		"TestTrigger fired: created TestRun %s/%s for Test %q",
		run.Namespace, run.Name, testName)

	// Best-effort status update — never fail the fire on a status write.
	r.updateTriggerLastFiredAt(ctx, g.Trigger)
	return nil
}

// errGateAlreadyHandled is a sentinel returned from fireTestRun when the
// gate manager should NOT record a fired outcome — because the concurrency
// short-circuit already recorded a "skipped" outcome and removed the gate.
// The gate manager sees this error and does nothing further with the gate.
var errGateAlreadyHandled = errors.New("gate already handled by firer")

// buildTestRun assembles the TestRun the trigger creates. GenerateName so
// multiple fires of the same trigger against the same target don't collide.
func (r *TestTriggerReconciler) buildTestRun(g *gate, testName, testNS string) *testsv1alpha1.TestRun {
	run := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    testNS,
			GenerateName: fmt.Sprintf("%s-trigger-", testName),
			Labels: map[string]string{
				LabelTriggerName: g.Trigger.Name,
				LabelTriggerFor:  testName,
			},
			Annotations: map[string]string{
				AnnotationTriggerTarget: fmt.Sprintf("%s/%s", g.Target.GetNamespace(), g.Target.GetName()),
				AnnotationTriggerEvent:  g.Trigger.Spec.Event,
			},
		},
		Spec: testsv1alpha1.TestRunSpec{
			TestRef: testName,
			Source:  SourceTrigger,
		},
	}
	if p := g.Trigger.Spec.ActionParameters; p != nil {
		run.Spec.Config = p.Config
		run.Spec.Tags = p.Tags
	}
	return run
}

// resolveTestTarget picks the Test the trigger should fire. Precedence:
// TestSelector.Name (exact) → TestSelector.LabelSelector (first match).
// Namespace defaults to the trigger's namespace when unset — matches
// Testkube's default and keeps single-namespace deployments simple.
func (r *TestTriggerReconciler) resolveTestTarget(ctx context.Context, t *testsv1alpha1.TestTrigger) (name, namespace string, err error) {
	sel := t.Spec.TestSelector
	if sel == nil {
		return "", "", errors.New("spec.testSelector is required to fire a TestRun")
	}
	ns := sel.Namespace
	if ns == "" {
		ns = t.Namespace
	}
	if sel.Name != "" {
		return sel.Name, ns, nil
	}
	if sel.LabelSelector != nil {
		selector, err := metav1.LabelSelectorAsSelector(sel.LabelSelector)
		if err != nil {
			return "", "", fmt.Errorf("testSelector.labelSelector: %w", err)
		}
		var list testsv1alpha1.TestList
		if err := r.List(ctx, &list, client.InNamespace(ns),
			client.MatchingLabelsSelector{Selector: selector}); err != nil {
			return "", "", fmt.Errorf("list Tests: %w", err)
		}
		if len(list.Items) == 0 {
			return "", "", fmt.Errorf("no Tests match testSelector.labelSelector in namespace %q", ns)
		}
		return list.Items[0].Name, ns, nil
	}
	return "", "", errors.New("spec.testSelector must set name or labelSelector")
}

// hasActiveRunForTest returns true if any TestRun in ns for testName is not
// in a terminal phase. Reuses IsTerminalPhase from phases.go.
func (r *TestTriggerReconciler) hasActiveRunForTest(ctx context.Context, ns, testName string) (bool, error) {
	var list testsv1alpha1.TestRunList
	if err := r.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return false, err
	}
	for _, run := range list.Items {
		if run.Spec.TestRef != testName {
			continue
		}
		if !IsTerminalPhase(run.Status.Phase) {
			return true, nil
		}
	}
	return false, nil
}

func (r *TestTriggerReconciler) updateTriggerLastFiredAt(ctx context.Context, t *testsv1alpha1.TestTrigger) {
	// Fetch fresh copy — the g.Trigger is a snapshot from event time and
	// may be stale w.r.t. ResourceVersion.
	var fresh testsv1alpha1.TestTrigger
	if err := r.Get(ctx, types.NamespacedName{Namespace: t.Namespace, Name: t.Name}, &fresh); err != nil {
		if !apierrors.IsNotFound(err) {
			log.FromContext(ctx).V(1).Info("updateTriggerLastFiredAt: get failed", "err", err.Error())
		}
		return
	}
	now := metav1.NewTime(r.Clock.Now())
	fresh.Status.LastFiredAt = &now
	if err := r.Status().Update(ctx, &fresh); err != nil {
		log.FromContext(ctx).V(1).Info("updateTriggerLastFiredAt: status update failed", "err", err.Error())
	}
}

// triggerRunnable owns the informer event loop + periodic gate evaluation.
// Leader-elected so exactly one replica dispatches events and creates
// TestRuns. Separate from TestRunReconciler for clean separation of
// concerns (trigger doesn't touch Jobs).
type triggerRunnable struct {
	r *TestTriggerReconciler
}

// NeedLeaderElection makes the manager schedule this Runnable only on the
// leader — non-leader replicas neither watch resources nor create TestRuns.
func (t *triggerRunnable) NeedLeaderElection() bool { return true }

// Start pumps informer events and periodically evaluates the gate manager.
// Returns nil when ctx is canceled.
func (t *triggerRunnable) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("testtrigger-runnable")
	t.r.informers.Start(ctx)
	logger.Info("testtrigger runnable starting", "gateEvalInterval", t.r.GateEvalInterval)

	// #nosec G115 -- GateEvalInterval bounded above by reconciler wire-up.
	ticker := time.NewTicker(t.r.GateEvalInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.r.informers.Stop()
			logger.Info("testtrigger runnable stopping")
			return nil
		case ev := <-t.r.informers.Events():
			t.r.handleEvent(ctx, ev)
		case <-ticker.C:
			t.r.gates.Evaluate(ctx)
		}
	}
}

// _ compile-time hint that we import the reconcile package (kept so a future
// factoring that returns reconcile.Result directly doesn't need to reintroduce
// the import).
var _ = reconcile.Result{}
