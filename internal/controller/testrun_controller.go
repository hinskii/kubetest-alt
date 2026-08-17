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
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives a TestRun through its state machine. Idempotent by design:
// running the same reconcile twice on the same state produces no additional
// writes.
func (r *TestRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("testrun", req.NamespacedName)

	var run testsv1alpha1.TestRun
	if err := r.Get(ctx, req.NamespacedName, &run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
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
	if IsTerminalPhase(run.Status.Phase) {
		return ctrl.Result{}, nil
	}

	// Setup path: first time we see this run, snapshot the Test spec, check
	// concurrency, transition to queued.
	if run.Status.ResolvedSpec == "" {
		return r.setup(ctx, logger, &run)
	}

	// From here we expect either an existing Job or a not-yet-created one.
	return r.observeOrCreateJob(ctx, logger, &run)
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

	if err := ValidateConfigKeys(run.Spec.Config, test.Spec.Config); err != nil {
		return r.transitionTerminal(ctx, run, testsv1alpha1.PhaseError,
			ReasonInvalidConfig, err.Error())
	}

	priors, err := r.listPriorRuns(ctx, run)
	if err != nil {
		return ctrl.Result{}, err
	}
	action := DecideConcurrency(priors, run, test.Spec.ConcurrencyPolicy)

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

	snap, err := json.Marshal(test.Spec)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("snapshot Test spec: %w", err)
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

// observeOrCreateJob is entered when ResolvedSpec is set. Either the Job
// exists (inspect + transition) or it doesn't (compile + create, or orphan).
func (r *TestRunReconciler) observeOrCreateJob(ctx context.Context, logger interface{ Info(string, ...any) }, run *testsv1alpha1.TestRun) (ctrl.Result, error) {
	jobKey := types.NamespacedName{Namespace: run.Namespace, Name: run.Name}
	var job batchv1.Job
	err := r.Get(ctx, jobKey, &job)

	if apierrors.IsNotFound(err) {
		if run.Status.JobName != "" {
			// We've created (or observed) the Job before; now it's gone.
			// §15.3 orphan detection.
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
		if IsPodRunning(pod) && run.Status.Phase != testsv1alpha1.PhaseRunning {
			run.Status.Phase = testsv1alpha1.PhaseRunning
			if run.Status.StartedAt == nil {
				now := r.Now()
				run.Status.StartedAt = &now
			}
			if err := r.Status().Update(ctx, run); err != nil {
				return ctrl.Result{}, err
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
func (r *TestRunReconciler) terminalAndDeleteJob(ctx context.Context, run *testsv1alpha1.TestRun,
	phase testsv1alpha1.Phase, reason, message string, job *batchv1.Job) (ctrl.Result, error) {

	if _, err := r.transitionTerminal(ctx, run, phase, reason, message); err != nil {
		return ctrl.Result{}, err
	}
	if job != nil {
		if err := deleteJobBackground(ctx, r.Client, job); err != nil {
			// Best-effort: TTL will eventually reap the Job if we fail.
			log.FromContext(ctx).Error(err, "delete Job after terminal phase (TTL safety net will retry)")
		}
	}
	return ctrl.Result{}, nil
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
// write happens.
func (r *TestRunReconciler) transitionTerminal(ctx context.Context, run *testsv1alpha1.TestRun,
	phase testsv1alpha1.Phase, reason, message string) (ctrl.Result, error) {

	if !IsTerminalPhase(phase) {
		return ctrl.Result{}, fmt.Errorf("transitionTerminal called with non-terminal phase %q", phase)
	}
	if run.Status.Phase == phase {
		return ctrl.Result{}, nil
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

// finalize handles the "TestRun is being deleted" path: delete owned Job then
// remove the finalizer so k8s can reap the TestRun.
func (r *TestRunReconciler) finalize(ctx context.Context, run *testsv1alpha1.TestRun) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(run, FinalizerName) {
		return ctrl.Result{}, nil
	}
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
