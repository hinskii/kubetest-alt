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

// Package controller hosts the TestRun reconciler (step 04). The reconciler
// is event-driven: Owns(Job) fires on Job status transitions; Watches(Pod)
// filtered by the kubetest.io/run-id label fires on pod events (compiler
// puts that label on the pod template in step 03). A short fallback requeue
// covers lost events; there is no polling loop.
package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/compiler"
	"github.com/hinskii/kubetest-alt/internal/metrics"
	"github.com/hinskii/kubetest-alt/internal/resolver"
	"github.com/hinskii/kubetest-alt/internal/webhookdelivery"
)

// TestRunReconciler owns the TestRun lifecycle: setup → snapshot + concurrency
// check → compile + create Job → observe → terminal → cleanup.
type TestRunReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// CompilerOpts feeds into every compiler.Compile call. Populated by
	// cmd/operator from flags (--content-fetcher-image, --image-registry,
	// --executor-images) so the controller stays free of hard-coded images.
	CompilerOpts compiler.Options

	// Results reads the wrapper's result.json for terminal Jobs. Defaults to
	// NoResultReader (always ErrResultNotFound) — step 07 wires the real
	// MinIO-backed implementation.
	Results ResultReader

	// LogRegistry starts/stops per-run log tailers as pods transition to
	// Running and then to a terminal phase. Nil disables the log-streaming
	// path entirely (backwards-compatible: existing tests don't need it and
	// cmd/operator only wires it when --logs-enabled is set).
	//
	// Interface, not the concrete *logstream.Registry, so envtest can inject
	// a lightweight recorder without a real k8s log source.
	LogRegistry LogRegistry

	// RunStore persists finished TestRuns to Postgres for the API server
	// (step 10) and retention (§9). Called from transitionTerminal AFTER
	// the Status Update succeeds — so the DB row always matches the CR's
	// terminal state. Nil disables the write path (unit tests without a
	// Postgres, cmd/operator without --postgres-dsn).
	//
	// Failure semantics: SaveFinished errors are LOGGED, not returned. Run
	// correctness > run history. The next reconcile retries the write
	// (transitionTerminal is a no-op once the CR phase is already terminal,
	// but the reconciler enters this code path again after fallback requeue
	// so we still get a retry — the RunStore's UID upsert keeps it idempotent).
	RunStore RunStorePersister

	// APIReader is a cache-bypassing client used ONLY to disambiguate the
	// informer-lag race on orphan-job detection: after createJob() sets
	// jobName in Status, the very next reconcile may see the Job as
	// NotFound because the informer hasn't observed it yet. Direct API
	// reads avoid classifying a healthy run as OrphanJobMissing. Nil is
	// tolerated — cmd/operator wires it from mgr.GetAPIReader(); envtest
	// wires it from a direct client. See observeOrCreateJob.
	APIReader client.Reader

	// TemplateStore resolves spec.use[] references to TestTemplate CRs.
	// Nil disables template resolution (spec.use is treated as an error);
	// production wires a client-backed store (see NewClientTemplateStore).
	//
	// Kept as an interface so tests can inject an in-memory MapStore.
	TemplateStore resolver.TemplateStore

	// ResolverEnv is the map projected to `{{ env.X }}` during expression
	// evaluation. Nil is equivalent to "empty env" — no OS env leaks in.
	// cmd/operator wires the operator-curated subset it wants to expose.
	ResolverEnv map[string]string

	// WebhookDispatcher fires outbound webhooks on terminal transitions
	// (step 14). Nil disables the outbound webhook path — reconcile
	// otherwise unchanged. Dispatcher.Enqueue is documented non-blocking,
	// so calling it in-line from the reconciler is safe.
	WebhookDispatcher *webhookdelivery.Dispatcher

	// dispatchedRuns dedupes webhook fires per (namespace, name, phase).
	// Populated on successful Enqueue; consulted on every reconcile-of-
	// terminal to avoid re-firing when Job GC / status update triggers
	// another reconcile pass. Same shape/rationale as persistedRuns.
	dispatchedRuns sync.Map // types.NamespacedName+phase → struct{}

	// FallbackRequeue is a safety-net requeue for lost Job/Pod events; the
	// primary trigger is Owns(Job) + Watches(Pod). Default 30s.
	FallbackRequeue time.Duration

	// ConcurrencyWaitInterval is how often to re-check when a Forbid-policy
	// run is queued behind an active prior. Default 10s. Cheap event-driven
	// alternative (map terminal-transitions of siblings back to queued runs)
	// can land later; 10s is fine for MVP.
	ConcurrencyWaitInterval time.Duration

	// Now returns the current time. Overridable so tests get deterministic
	// timestamps. Defaults to metav1.Now.
	Now func() metav1.Time

	// persistedRuns dedupes SaveFinished calls per TestRun. Populated on
	// successful save; consulted on every reconcile-of-terminal to skip
	// unnecessary DB traffic (Job GC / status subresource updates fire
	// reconciles even after the CR is terminal). Cleared implicitly on
	// operator restart — subsequent re-persist is a no-op idempotent upsert.
	//
	// Key is types.NamespacedName, NOT UID, for two reasons:
	//  1. On CR NotFound the reconciler only knows name+namespace, not UID
	//     — so a UID-keyed map couldn't self-clean when a CR is deleted
	//     without our finalize path firing (finalizer force-stripped, or
	//     bug elsewhere). Name-keyed map cleans up on every NotFound seen.
	//  2. Recreation collisions are semantically fine: a new TestRun with
	//     the same name gets a new UID; when it reaches terminal, its own
	//     persistFinished call overwrites the entry via .Store. The map's
	//     job is "have we written THIS lifetime's current row?" — DB row
	//     PK is still UID, so no cross-lifetime confusion in Postgres.
	persistedRuns sync.Map // types.NamespacedName → struct{}
}

// LogRegistry is the reconciler-facing surface of logstream.Registry. Kept
// as an interface so envtest can inject a recorder — a real registry needs
// a k8s LogSource that envtest doesn't provide.
type LogRegistry interface {
	// EnsureTailer starts a tailer for runID + pod, or no-ops if one already
	// exists. Called on pod Running (may fire multiple times).
	EnsureTailer(ctx context.Context, runID, namespace, podName string) error

	// StopTailer stops and removes the tailer for runID. No-op if absent.
	// Called on terminal transitions and finalize.
	StopTailer(runID string)
}

// RunStorePersister is the reconciler-facing surface of store.Postgres.
// Interface so envtest injects a fake without a real database. Matches
// store.RunStore's SaveFinished — controller doesn't need Get/List.
type RunStorePersister interface {
	SaveFinished(ctx context.Context, run *testsv1alpha1.TestRun) error
}

// SetupWithManager registers the reconciler with the controller manager and
// wires event sources: Job (via ownerRef on Job → TestRun set by compiler)
// and Pod (via label kubetest.io/run-id set by compiler on the pod template).
func (r *TestRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.FallbackRequeue == 0 {
		r.FallbackRequeue = 30 * time.Second
	}
	if r.ConcurrencyWaitInterval == 0 {
		r.ConcurrencyWaitInterval = 10 * time.Second
	}
	if r.Results == nil {
		r.Results = NoResultReader{}
	}
	if r.Now == nil {
		r.Now = metav1.Now
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&testsv1alpha1.TestRun{}).
		Owns(&batchv1.Job{}).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.mapPodToTestRun),
			builder.WithPredicates(hasRunIDLabelPredicate),
		).
		// Step 17: composite parent runs need to reconcile whenever a
		// CHILD TestRun changes phase. Children carry ownerRef to the
		// parent TestRun (set by ensureStepChildren). Owns(TestRun) is
		// the standard controller-runtime pattern for this — for the
		// primary type this happens to also be the owned type, but
		// controller-runtime handles that fine (the OwnerReference
		// filter separates child events from primary events, and the
		// mapper enqueues the parent by name).
		//
		// Kept mapChildRunToParent + LabelParentRun / LabelStep /
		// LabelExecIndex for GUI queries (`kubectl -l parent-run=X`)
		// and for the reconciler's own listChildRuns — the label is
		// a stable, human-readable index, ownerRef is the mechanism.
		Owns(&testsv1alpha1.TestRun{}).
		Named("testrun").
		Complete(r)
}

// hasRunIDLabelPredicate lets only pods carrying the compiler-set run-id
// label into our event stream — every other pod on the cluster gets filtered
// out here (cheap) instead of in the mapper.
var hasRunIDLabelPredicate = predicate.NewPredicateFuncs(func(obj client.Object) bool {
	_, ok := obj.GetLabels()[compiler.LabelRunID]
	return ok
})

// mapPodToTestRun turns a Pod event into a Reconcile request for the TestRun
// named by the run-id label. Namespaces match by construction (compiler puts
// the pod in the same namespace as the TestRun).
func (r *TestRunReconciler) mapPodToTestRun(_ context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	runID := pod.Labels[compiler.LabelRunID]
	if runID == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Namespace: pod.Namespace, Name: runID},
	}}
}

// +kubebuilder:rbac:groups=tests.kubetest.io,resources=testruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=tests.kubetest.io,resources=testruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=tests.kubetest.io,resources=testruns/finalizers,verbs=update
// +kubebuilder:rbac:groups=tests.kubetest.io,resources=tests,verbs=get;list;watch
// +kubebuilder:rbac:groups=tests.kubetest.io,resources=webhooks,verbs=get;list;watch
// +kubebuilder:rbac:groups=tests.kubetest.io,resources=webhooks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get;list
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives a TestRun through its state machine. Idempotent by design:
// running the same reconcile twice on the same state produces no additional
// writes.
func (r *TestRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("testrun", req.NamespacedName)

	var run testsv1alpha1.TestRun
	if err := r.Get(ctx, req.NamespacedName, &run); err != nil {
		if apierrors.IsNotFound(err) {
			// CR is gone. Drop any persistedRuns entry so the map stays
			// bounded on a long-lived operator. Covers the edge cases
			// where finalize() didn't run (finalizer force-stripped,
			// external eviction) — the finalizer normally guarantees
			// cleanup, but this belt-and-suspenders keeps growth bounded
			// against operator lifetime instead of finalizer discipline.
			r.persistedRuns.Delete(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Deletion path — always runs, terminal or not.
	if !run.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &run)
	}

	// Ensure our finalizer is present before doing any real work; that way
	// deletion during running is always intercepted (§15.5).
	if !controllerutil.ContainsFinalizer(&run, FinalizerName) {
		controllerutil.AddFinalizer(&run, FinalizerName)
		if err := r.Update(ctx, &run); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Terminal phases are frozen — no writes, no requeue. This is what keeps
	// the event-driven design from hot-looping on its own status updates.
	// Still run the persist path: if the very first save failed (DB down,
	// network flake) we get a retry on every subsequent reconcile-of-
	// terminal (Job GC, status subresource update, fallback requeue).
	// persistFinished dedupes via persistedRuns so a successful save
	// short-circuits every subsequent call cheaply.
	if IsTerminalPhase(run.Status.Phase) {
		r.persistFinished(ctx, &run)
		return ctrl.Result{}, nil
	}

	// Setup path: first time we see this run, snapshot the Test spec, check
	// concurrency, transition to queued.
	if run.Status.ResolvedSpec == "" {
		return r.setup(ctx, logger, &run)
	}

	// Step 17: composite runs have no Job — dispatch to the composite
	// reconciler which owns child TestRuns and step aggregation.
	// Detection is via the resolvedSpec snapshot (the source of truth
	// for what this run WAS at setup time — §15.5 invariant), NOT the
	// live Test spec (which may have been edited mid-run).
	if isCompositeRun(&run) {
		return r.reconcileComposite(ctx, logger, &run)
	}

	// From here we expect either an existing Job or a not-yet-created one.
	return r.observeOrCreateJob(ctx, logger, &run)
}

// isCompositeRun decodes the resolvedSpec snapshot and reports whether
// this run is a composite (spec.steps non-empty). Kept as a small
// helper so composite detection is a single call site with a clear
// name — the alternative (inlining a JSON-unmarshal every reconcile)
// would obscure the branch.
func isCompositeRun(run *testsv1alpha1.TestRun) bool {
	if run.Status.ResolvedSpec == "" {
		return false
	}
	var snap testsv1alpha1.TestSpec
	if err := json.Unmarshal([]byte(run.Status.ResolvedSpec), &snap); err != nil {
		return false
	}
	return len(snap.Steps) > 0
}

// setup handles the very first reconcile for a TestRun: resolve Test, validate
// config keys, check concurrency, snapshot Test.Spec into Status.ResolvedSpec,
// then either wait (Forbid), abort priors (Replace), or fall through by
// setting Status.Phase=queued and letting the next reconcile create the Job.
func (r *TestRunReconciler) setup(ctx context.Context, logger interface{ Info(string, ...any) }, run *testsv1alpha1.TestRun) (ctrl.Result, error) {
	var test testsv1alpha1.Test
	testKey := types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.TestRef}
	if err := r.Get(ctx, testKey, &test); err != nil {
		if apierrors.IsNotFound(err) {
			return r.transitionTerminal(ctx, run, testsv1alpha1.PhaseError,
				ReasonTestNotFound,
				fmt.Sprintf("Test %q not found in namespace %q", run.Spec.TestRef, run.Namespace))
		}
		return ctrl.Result{}, err
	}

	// Config-keys validation runs AFTER template resolution because a
	// template may declare `spec.config` entries the Test itself doesn't
	// mention (that's the whole point of shared templates). But we still
	// want to surface an unknown-key error with reason=InvalidConfig
	// rather than ResolveFailed — those are two different classes of
	// bug (user typo vs. missing template). To keep the reasons distinct
	// we validate keys against the RESOLVED spec.config right after
	// resolveSpec succeeds.

	// Resolve templates + config + expressions BEFORE concurrency checks:
	// a resolve failure should surface immediately, not sit behind a
	// prior-run gate. §15.5: the snapshot in status.resolvedSpec is the
	// FULLY resolved spec so historical runs remain interpretable after
	// templates are edited.
	//
	// Reason mapping: ErrUnknownConfigKey → InvalidConfig (user typo);
	// every other resolve error → ResolveFailed (template missing,
	// coercion failure, expression eval error, ValidateResolved failure).
	resolvedSpec, resolveErr := r.resolveSpec(&test, run)
	if resolveErr != nil {
		reason := ReasonResolveFailed
		if errors.Is(resolveErr, resolver.ErrUnknownConfigKey) {
			reason = ReasonInvalidConfig
		}
		return r.transitionTerminal(ctx, run, testsv1alpha1.PhaseError,
			reason, resolveErr.Error())
	}

	// Step 17: composite cycle detection at setup — resolves the whole
	// execute graph ONCE using live Test specs, then fails fast if a
	// cycle exists. The graph itself is not persisted anywhere; only
	// this run's resolved spec (which already includes spec.steps) is
	// snapshotted below. That upholds §15.5: editing a child Test
	// mid-run does not change THIS run's step plan, but the child's
	// resolvedSpec at its OWN setup time may differ (documented).
	if len(resolvedSpec.Steps) > 0 {
		if err := r.validateCompositeGraph(ctx, &test, resolvedSpec); err != nil {
			return r.transitionTerminal(ctx, run, testsv1alpha1.PhaseError,
				ReasonResolveFailed, err.Error())
		}
	}

	priors, err := r.listPriorRuns(ctx, run)
	if err != nil {
		return ctrl.Result{}, err
	}
	action := DecideConcurrency(priors, run, resolvedSpec.ConcurrencyPolicy)

	switch action {
	case ConcurrencyWait:
		if run.Status.Phase != testsv1alpha1.PhaseQueued {
			run.Status.Phase = testsv1alpha1.PhaseQueued
			run.Status.Message = "waiting: prior active TestRun for this Test (concurrencyPolicy=Forbid)"
			if run.Status.QueuedAt == nil {
				now := r.Now()
				run.Status.QueuedAt = &now
			}
			if err := r.Status().Update(ctx, run); err != nil {
				return ctrl.Result{}, err
			}
			logger.Info("TestRun waiting on prior active runs", "count", len(priors))
		}
		return ctrl.Result{RequeueAfter: r.ConcurrencyWaitInterval}, nil

	case ConcurrencyReplacePrior:
		for _, prior := range SelectPriorsToAbort(priors, run) {
			if err := r.abortRun(ctx, prior); err != nil {
				return ctrl.Result{}, err
			}
		}
		// Fall through to snapshot + queue.

	case ConcurrencyProceed:
		// Fall through.
	}

	snap, err := json.Marshal(resolvedSpec)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("snapshot resolved spec: %w", err)
	}
	run.Status.ResolvedSpec = string(snap)
	run.Status.Phase = testsv1alpha1.PhaseQueued
	run.Status.Message = ""
	if run.Status.QueuedAt == nil {
		now := r.Now()
		run.Status.QueuedAt = &now
	}
	if err := r.Status().Update(ctx, run); err != nil {
		return ctrl.Result{}, err
	}
	// Don't requeue explicitly — the Update above triggers a Reconcile that
	// enters observeOrCreateJob to create the Job.
	return ctrl.Result{}, nil
}

// resolveSpec runs the step-13 pipeline: templates → config coercion →
// `{{ }}` expression eval → post-resolution validation (image + command
// present, since the webhook allows them to be empty when spec.use is set).
//
// Returns the fully-resolved spec on success; a human-readable error on
// failure (surfaced as TestRun.status.message with ReasonResolveFailed).
func (r *TestRunReconciler) resolveSpec(test *testsv1alpha1.Test, run *testsv1alpha1.TestRun) (*testsv1alpha1.TestSpec, error) {
	store := r.TemplateStore
	if store == nil {
		// No template store wired — treat as "no templates available".
		// A Test with spec.use will error via ErrTemplateNotFound; a Test
		// without spec.use passes through unchanged.
		store = resolver.MapStore{}
	}
	spec, err := resolver.Resolve(test, run, store, resolver.Options{
		Env: r.ResolverEnv,
	})
	if err != nil {
		return nil, err
	}
	if err := resolver.ValidateResolved(spec); err != nil {
		return nil, err
	}
	return spec, nil
}

// observeOrCreateJob is entered when ResolvedSpec is set. Either the Job
// exists (inspect + transition) or it doesn't (compile + create, or orphan).
func (r *TestRunReconciler) observeOrCreateJob(ctx context.Context, logger interface{ Info(string, ...any) }, run *testsv1alpha1.TestRun) (ctrl.Result, error) {
	jobKey := types.NamespacedName{Namespace: run.Namespace, Name: run.Name}
	var job batchv1.Job
	err := r.Get(ctx, jobKey, &job)

	if apierrors.IsNotFound(err) {
		if run.Status.JobName != "" {
			// Cache-vs-live disambiguation: the informer may not have
			// observed a just-created Job on the reconcile immediately
			// after createJob wrote jobName to Status. Confirm the Job
			// is genuinely gone via a cache-bypassing read before
			// declaring OrphanJobMissing (§15.3). Under envtest load
			// this race manifested as a false-positive orphan on the
			// TestCatalog_ApplyAllTemplatesAndSamples run for one of
			// the samples.
			if r.APIReader != nil {
				var live batchv1.Job
				if lerr := r.APIReader.Get(ctx, jobKey, &live); lerr == nil {
					// Job exists live — cache is stale, requeue and let
					// the informer catch up.
					return ctrl.Result{RequeueAfter: 500 * time.Millisecond}, nil
				} else if !apierrors.IsNotFound(lerr) {
					return ctrl.Result{}, lerr
				}
			}
			// Genuinely gone.
			return r.transitionTerminal(ctx, run, testsv1alpha1.PhaseError,
				ReasonOrphanJobMissing,
				"Job was deleted before status persisted; run cannot continue")
		}
		return r.createJob(ctx, logger, run)
	} else if err != nil {
		return ctrl.Result{}, err
	}

	return r.inspectJob(ctx, run, &job)
}

// createJob compiles the resolved spec and creates aux ConfigMap + Job.
func (r *TestRunReconciler) createJob(ctx context.Context, logger interface{ Info(string, ...any) }, run *testsv1alpha1.TestRun) (ctrl.Result, error) {
	var testSpec testsv1alpha1.TestSpec
	if err := json.Unmarshal([]byte(run.Status.ResolvedSpec), &testSpec); err != nil {
		return r.transitionTerminal(ctx, run, testsv1alpha1.PhaseError,
			ReasonCompileError, fmt.Sprintf("resolvedSpec unmarshal: %v", err))
	}

	// Reconstruct a minimal *Test for the compiler — only Spec is read from it.
	testForCompile := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{
			Name:      run.Spec.TestRef,
			Namespace: run.Namespace,
		},
		Spec: testSpec,
	}
	job, aux, cerr := compiler.Compile(testForCompile, run, r.CompilerOpts)
	if cerr != nil {
		return r.transitionTerminal(ctx, run, testsv1alpha1.PhaseError,
			ReasonCompileError, cerr.Error())
	}

	// Create aux objects (ConfigMap holding request.json) first; the Job
	// references them by name. AlreadyExists is fine — idempotency.
	for _, obj := range aux {
		if err := r.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, fmt.Errorf("create aux %T: %w", obj, err)
		}
	}
	if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, fmt.Errorf("create Job: %w", err)
	}

	// Persist JobName so the next reconcile knows the Job SHOULD exist
	// (orphan detection depends on this).
	if run.Status.JobName != job.Name {
		run.Status.JobName = job.Name
		if err := r.Status().Update(ctx, run); err != nil {
			return ctrl.Result{}, err
		}
	}
	logger.Info("created Job for TestRun", "job", job.Name)
	// Owns(Job) fires on Job create → next reconcile enters inspectJob.
	return ctrl.Result{}, nil
}

// inspectJob reads Job status + associated Pod status and drives the phase
// transition. Terminal transitions persist status THEN delete the Job (§15.3).
func (r *TestRunReconciler) inspectJob(ctx context.Context, run *testsv1alpha1.TestRun, job *batchv1.Job) (ctrl.Result, error) {
	conclusion := InspectJob(job)

	// Fetch the pod once (best-effort). Even for still-running Jobs we look
	// for ImagePullBackOff / OOMKilled since Job status alone doesn't
	// surface those.
	pod, _ := r.findPodForJob(ctx, job)
	if infra := AnalyzePod(pod); infra.Reason != "" {
		return r.terminalAndDeleteJob(ctx, run, testsv1alpha1.PhaseError,
			infra.Reason, infra.Message, job)
	}

	switch conclusion {
	case JobSucceeded, JobFailedConclusion:
		result, err := r.Results.Read(ctx, run.Name)
		if err == nil && result != nil {
			// Fold scraper output (metrics, JUnit counts, artifact refs)
			// into TestRun.Status before the terminal transition writes it.
			applyRunResultToStatus(run, result)
			return r.terminalAndDeleteJob(ctx, run, result.Phase, "", result.ErrorMessage, job)
		}
		if !errors.Is(err, ErrResultNotFound) && err != nil {
			// Transient reader failure — requeue.
			return ctrl.Result{RequeueAfter: r.FallbackRequeue}, err
		}
		// Missing result.json — §15.2 fallback via pod terminated state.
		phase, reason, msg := FallbackPhaseFromJobFailure(job)
		return r.terminalAndDeleteJob(ctx, run, phase, reason, msg, job)

	case JobStillRunning:
		if IsPodRunning(pod) {
			// Start log tail on first Running observation. EnsureTailer is
			// idempotent, so subsequent reconciles on the same running pod
			// are cheap map-lookups. Failures here are logged but do NOT
			// block the reconcile — losing live logs is preferable to
			// stalling the run's lifecycle (§15.4 durability trade-off).
			if r.LogRegistry != nil && pod != nil {
				if err := r.LogRegistry.EnsureTailer(ctx, run.Name, pod.Namespace, pod.Name); err != nil {
					log.FromContext(ctx).Error(err, "EnsureTailer failed",
						"run", run.Name, "pod", pod.Name)
				}
			}
			if run.Status.Phase != testsv1alpha1.PhaseRunning {
				run.Status.Phase = testsv1alpha1.PhaseRunning
				if run.Status.StartedAt == nil {
					now := r.Now()
					run.Status.StartedAt = &now
				}
				if err := r.Status().Update(ctx, run); err != nil {
					return ctrl.Result{}, err
				}
				// ActiveRuns++ on first queued→running transition. The
				// matching decrement fires in transitionTerminal for
				// runs that went through this path.
				metrics.ActiveRuns.
					WithLabelValues(metrics.NormalizeTool(run.Labels[compiler.LabelKubetestTool])).
					Inc()
			}
		}
		// Safety-net requeue in case we miss a Job/Pod event; the primary
		// path is event-driven via Owns(Job) + Watches(Pod).
		return ctrl.Result{RequeueAfter: r.FallbackRequeue}, nil
	}
	return ctrl.Result{}, nil
}

// terminalAndDeleteJob persists the terminal phase, THEN deletes the Job.
// The order matters: §15.3 requires persistence before cleanup so a mid-crash
// leaves us in "terminal + orphan Job" (recoverable) rather than "still
// running + Job gone" (which the orphan detector treats as an error).
//
// Also stops the log tailer BEFORE deleting the Job: StopTailer blocks on
// final flush, so we guarantee the last chunk lands in MinIO before the
// pod's logs disappear along with the Job.
func (r *TestRunReconciler) terminalAndDeleteJob(ctx context.Context, run *testsv1alpha1.TestRun,
	phase testsv1alpha1.Phase, reason, message string, job *batchv1.Job) (ctrl.Result, error) {

	if _, err := r.transitionTerminal(ctx, run, phase, reason, message); err != nil {
		return ctrl.Result{}, err
	}
	// Persist to run-history DB AFTER the CR reaches terminal state so the
	// row reflects the same finishedAt/durationMs/message the CR shows.
	// Error is logged, NOT returned — run history < run correctness.
	r.persistFinished(ctx, run)
	// Fire outbound webhooks (step 14). Enqueue is async and non-blocking
	// — endpoint latency cannot delay this reconcile. Deduped per
	// (run, phase) via dispatchedRuns.
	r.dispatchWebhooks(ctx, run)
	if r.LogRegistry != nil {
		r.LogRegistry.StopTailer(run.Name)
	}
	if job != nil {
		if err := deleteJobBackground(ctx, r.Client, job); err != nil {
			// Best-effort: TTL will eventually reap the Job if we fail.
			log.FromContext(ctx).Error(err, "delete Job after terminal phase (TTL safety net will retry)")
		}
	}
	return ctrl.Result{}, nil
}

// persistFinished writes the (terminal) TestRun to the run-history store,
// once per NamespacedName per operator lifetime. Errors are logged but never
// returned: the plan (§step-09) makes run history strictly secondary to run
// correctness. Retry on subsequent reconciles until success, then dedupe
// via persistedRuns. See the field docstring for why the key isn't UID.
func (r *TestRunReconciler) persistFinished(ctx context.Context, run *testsv1alpha1.TestRun) {
	if r.RunStore == nil || run == nil || run.UID == "" {
		return
	}
	key := types.NamespacedName{Namespace: run.Namespace, Name: run.Name}
	if _, done := r.persistedRuns.Load(key); done {
		return
	}
	if err := r.RunStore.SaveFinished(ctx, run); err != nil {
		log.FromContext(ctx).Error(err, "SaveFinished to run store failed (will retry on next reconcile)",
			"run", run.Name, "uid", run.UID)
		return
	}
	r.persistedRuns.Store(key, struct{}{})
}

// dispatchWebhooks lists Webhook CRs in the run's namespace, filters
// by event, and Enqueues one Job per matching subscriber. Enqueue is
// documented non-blocking. All errors are LOGGED, never returned —
// webhook delivery MUST NOT delay or fail a reconcile (§14 async).
//
// Dedup: fires at most once per (run, phase). A subsequent reconcile
// on the terminal CR (Job GC, status update ripple) sees the entry
// and skips.
func (r *TestRunReconciler) dispatchWebhooks(ctx context.Context, run *testsv1alpha1.TestRun) {
	if r.WebhookDispatcher == nil || run == nil {
		return
	}
	dedupKey := webhookDedupKey(run)
	if _, done := r.dispatchedRuns.Load(dedupKey); done {
		return
	}
	var hooks testsv1alpha1.WebhookList
	if err := r.List(ctx, &hooks, client.InNamespace(run.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "list Webhooks for delivery — skipping this cycle", "run", run.Name)
		return
	}
	event := string(run.Status.Phase)
	// Tool label propagates from resolved spec's labels via the compiler
	// onto the Job/Pod; but the CR itself doesn't carry it. Best-effort
	// lookup from run.Labels + resolvedSpec's implied tool for the
	// payload. Empty string is fine — payload just doesn't include it.
	tool := run.Labels[compiler.LabelKubetestTool]
	payload := webhookdelivery.BuildPayload(event, run, tool, r.Now())
	for i := range hooks.Items {
		hook := &hooks.Items[i]
		if !webhookdelivery.EventMatches(hook.Spec.Events, event) {
			continue
		}
		r.WebhookDispatcher.Enqueue(webhookdelivery.Job{
			Key: webhookdelivery.NamespacedKey{
				Namespace: hook.Namespace,
				Name:      hook.Name,
			},
			Spec:      *hook.Spec.DeepCopy(),
			Namespace: hook.Namespace,
			Payload:   payload,
			QueuedAt:  r.Now().Time,
		})
	}
	r.dispatchedRuns.Store(dedupKey, struct{}{})
}

// webhookDedupKey is (namespace/name#phase). Different phase for the
// same run gets a distinct entry, which is what we want — a queued→
// running→passed sequence fires three separate deliveries per
// subscriber, dedup only against duplicate fires of the SAME phase.
func webhookDedupKey(run *testsv1alpha1.TestRun) string {
	return run.Namespace + "/" + run.Name + "#" + string(run.Status.Phase)
}

// deleteJobBackground deletes a Job with Background propagation so its pods
// are garbage-collected too.
//
// IMPORTANT: batch/v1 Job's DEFAULT propagation policy at the API server is
// Orphan (legacy behavior of the batch API — kubectl hardcodes Background,
// but bare controller-runtime client.Delete does NOT). Without an explicit
// Background policy the Job vanishes but its pods keep running — for load-
// generating tests (k6, JMeter) that means the workload keeps hitting the
// target after the TestRun is marked terminal.
//
// envtest CANNOT exercise this invariant: it has no garbage collector, so
// orphan pods never get reaped either way. The regression guard is a
// fake-client interceptor test in testrun_controller_test.go — see
// TestDeleteJobBackground_UsesExplicitPropagation.
func deleteJobBackground(ctx context.Context, c client.Client, job *batchv1.Job) error {
	return client.IgnoreNotFound(c.Delete(ctx, job,
		client.PropagationPolicy(metav1.DeletePropagationBackground)))
}

// transitionTerminal sets Status.Phase to a terminal value plus timestamps
// and message. Idempotent: if the phase is already the target terminal, no
// write happens. Emits Prometheus counters/histograms on the FIRST
// transition to terminal (guarded by the "phase already terminal" no-op).
func (r *TestRunReconciler) transitionTerminal(ctx context.Context, run *testsv1alpha1.TestRun,
	phase testsv1alpha1.Phase, reason, message string) (ctrl.Result, error) {

	if !IsTerminalPhase(phase) {
		return ctrl.Result{}, fmt.Errorf("transitionTerminal called with non-terminal phase %q", phase)
	}
	if run.Status.Phase == phase {
		return ctrl.Result{}, nil
	}
	// Metrics fire ONCE per run on the first terminal transition.
	// If the run was Running before (StartedAt set) the ActiveRuns
	// gauge decrements to match — otherwise it's a straight-to-error
	// transition and the gauge was never incremented for this run.
	tool := metrics.NormalizeTool(run.Labels[compiler.LabelKubetestTool])
	source := metrics.NormalizeSource(run.Spec.Source)
	metrics.RunsTotal.WithLabelValues(tool, string(phase), source).Inc()
	if run.Status.StartedAt != nil && run.Status.Phase == testsv1alpha1.PhaseRunning {
		metrics.ActiveRuns.WithLabelValues(tool).Dec()
	}
	run.Status.Phase = phase
	if reason != "" && message != "" {
		run.Status.Message = fmt.Sprintf("%s: %s", reason, message)
	} else if message != "" {
		run.Status.Message = message
	} else if reason != "" {
		run.Status.Message = reason
	}
	now := r.Now()
	if run.Status.StartedAt == nil {
		run.Status.StartedAt = &now
	}
	run.Status.FinishedAt = &now
	if run.Status.StartedAt != nil && run.Status.FinishedAt != nil {
		run.Status.DurationMs = run.Status.FinishedAt.Time.Sub(run.Status.StartedAt.Time).Milliseconds()
		// Observe the run's wall-clock duration once per terminal
		// transition (guarded by the "phase already terminal" no-op
		// above so retries don't double-observe).
		metrics.RunDurationSeconds.
			WithLabelValues(tool).
			Observe(float64(run.Status.DurationMs) / 1000.0)
	}
	if err := r.Status().Update(ctx, run); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// abortRun marks a prior TestRun as aborted (for Replace concurrency) and
// deletes its Job. Used by setup() when the new run's policy is Replace.
func (r *TestRunReconciler) abortRun(ctx context.Context, prior *testsv1alpha1.TestRun) error {
	patch := client.MergeFrom(prior.DeepCopy())
	prior.Status.Phase = testsv1alpha1.PhaseAborted
	prior.Status.Message = ReasonAborted + ": superseded by a Replace-policy run"
	now := r.Now()
	prior.Status.FinishedAt = &now
	if err := r.Status().Patch(ctx, prior, patch); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	// Best-effort Job delete (Background propagation — see deleteJobBackground).
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: prior.Namespace, Name: prior.Name}, &job); err == nil {
		_ = deleteJobBackground(ctx, r.Client, &job)
	}
	return nil
}

// listPriorRuns fetches all TestRuns in the same namespace referencing the
// same Test. Used for concurrency decisions.
func (r *TestRunReconciler) listPriorRuns(ctx context.Context, run *testsv1alpha1.TestRun) ([]testsv1alpha1.TestRun, error) {
	var list testsv1alpha1.TestRunList
	if err := r.List(ctx, &list, client.InNamespace(run.Namespace)); err != nil {
		return nil, err
	}
	out := list.Items[:0]
	for _, item := range list.Items {
		if item.Spec.TestRef == run.Spec.TestRef {
			out = append(out, item)
		}
	}
	return out, nil
}

// findPodForJob returns the (typically single) pod owned by the given Job.
// Returns nil, nil when no pod is found yet — that's normal for a Job we
// just created.
func (r *TestRunReconciler) findPodForJob(ctx context.Context, job *batchv1.Job) (*corev1.Pod, error) {
	// Prefer the run-id label because our label-based mapper already
	// guarantees compiler puts it on the pod template.
	runID, ok := job.Labels[compiler.LabelRunID]
	if !ok {
		return nil, nil
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(job.Namespace),
		client.MatchingLabels{compiler.LabelRunID: runID},
	); err != nil {
		return nil, err
	}
	if len(pods.Items) == 0 {
		return nil, nil
	}
	// If there are multiple (a re-created pod, for instance), take the most
	// recently created — its status is the freshest.
	newest := &pods.Items[0]
	for i := range pods.Items {
		if pods.Items[i].CreationTimestamp.After(newest.CreationTimestamp.Time) {
			newest = &pods.Items[i]
		}
	}
	return newest, nil
}

// finalize handles the "TestRun is being deleted" path: stop the tailer,
// delete owned Job, then remove the finalizer so k8s can reap the TestRun.
// Stopping the tailer before deleting the Job flushes the final log chunk;
// the finalizer path is the correct hook for "user deleted this while
// running" per §15.5.
func (r *TestRunReconciler) finalize(ctx context.Context, run *testsv1alpha1.TestRun) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(run, FinalizerName) {
		return ctrl.Result{}, nil
	}
	if r.LogRegistry != nil {
		r.LogRegistry.StopTailer(run.Name)
	}
	// Drop the persisted-set entry so a future TestRun with the same name
	// starts fresh, and the map stays bounded as runs are deleted. Reconcile's
	// NotFound handler is the primary bound-guarantee; this covers the
	// normal delete path so we don't wait for an eventual NotFound observation.
	r.persistedRuns.Delete(types.NamespacedName{Namespace: run.Namespace, Name: run.Name})
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Name}, &job); err == nil {
		if err := deleteJobBackground(ctx, r.Client, &job); err != nil {
			return ctrl.Result{}, err
		}
	} else if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	controllerutil.RemoveFinalizer(run, FinalizerName)
	if err := r.Update(ctx, run); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}
