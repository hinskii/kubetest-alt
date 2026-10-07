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
	"errors"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/compiler"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

// parallelPoll is the safety-net requeue of a running parallel run; Job
// and pod events are the primary trigger.
const parallelPoll = 5 * time.Second

// WorkerStepKey is the status.steps key of parallel worker i.
func WorkerStepKey(i int) string { return fmt.Sprintf("worker-%d", i) }

// isParallelRun reports a run whose resolved spec has spec.parallel.
func isParallelRun(run *testsv1alpha1.TestRun) bool {
	spec, err := resolvedSpecOf(run)
	return err == nil && spec.Parallel != nil
}

func resolvedSpecOf(run *testsv1alpha1.TestRun) (*testsv1alpha1.TestSpec, error) {
	var spec testsv1alpha1.TestSpec
	if err := json.Unmarshal([]byte(run.Status.ResolvedSpec), &spec); err != nil {
		return nil, err
	}
	return &spec, nil
}

// testForCompile rebuilds the Test the compiler needs from the run's
// resolved spec, with the tool label it propagates to Jobs and pods.
func testForCompile(run *testsv1alpha1.TestRun, spec *testsv1alpha1.TestSpec) *testsv1alpha1.Test {
	t := &testsv1alpha1.Test{Spec: *spec}
	t.Name, t.Namespace = run.Spec.TestRef, run.Namespace
	if tool := runTool(run); tool != "" {
		t.Labels = map[string]string{compiler.LabelKubetestTool: tool}
	}
	return t
}

// reconcileParallel drives a spec.parallel run: services first (if any),
// then one Job per worker; each finished worker's verdict lands in
// status.steps["worker-<i>"] (and its test counts and artifacts in the
// run's status); when every worker has finished the run ends with the
// aggregate: any error → error, else any failure → failed, else passed.
func (r *TestRunReconciler) reconcileParallel(ctx context.Context, run *testsv1alpha1.TestRun) (ctrl.Result, error) {
	spec, err := resolvedSpecOf(run)
	if err != nil {
		return r.transitionTerminal(ctx, run, testsv1alpha1.PhaseError, ReasonCompileError,
			fmt.Sprintf("resolvedSpec unmarshal: %v", err))
	}
	test := testForCompile(run, spec)
	if len(spec.Services) > 0 {
		if res, wait, err := r.ensureServices(ctx, run, test); wait || err != nil {
			return res, err
		}
	}
	if run.Status.Steps == nil {
		run.Status.Steps = map[string]testsv1alpha1.StepResult{}
	}
	before := run.Status.DeepCopy()

	workers := compiler.ParallelWorkers(spec.Parallel)
	done := 0
	// Jobs of workers that finished in this pass. Deleted only after their
	// verdicts are persisted: a lost status write (conflict) must find the
	// Job again, not take its absence for a deleted, unfinished worker.
	var finishedJobs []*batchv1.Job
	for _, w := range workers {
		finished, job, err := r.reconcileWorker(ctx, run, test, w)
		if err != nil {
			return ctrl.Result{}, err
		}
		if finished {
			done++
		}
		if job != nil {
			finishedJobs = append(finishedJobs, job)
		}
	}
	defer func() { r.deleteWorkerJobs(ctx, run, finishedJobs) }()
	if run.Status.Phase != testsv1alpha1.PhaseRunning {
		run.Status.Phase = testsv1alpha1.PhaseRunning
		run.Status.Message = ""
		if run.Status.StartedAt == nil {
			now := r.Now()
			run.Status.StartedAt = &now
		}
	}
	if done < len(workers) {
		if !statusEqual(before, &run.Status) {
			if err := r.Status().Update(ctx, run); err != nil {
				finishedJobs = nil
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: parallelPoll}, nil
	}
	phase, msg := aggregateWorkers(run, len(workers))
	res, err := r.terminalAndDeleteJob(ctx, run, phase, "", msg, nil)
	if err != nil {
		finishedJobs = nil
	}
	return res, err
}

// deleteWorkerJobs removes the Jobs of finished workers (their verdicts
// are already in the persisted status).
func (r *TestRunReconciler) deleteWorkerJobs(ctx context.Context, run *testsv1alpha1.TestRun, jobs []*batchv1.Job) {
	for _, job := range jobs {
		if err := deleteJobBackground(ctx, r.Client, job); err != nil {
			log.FromContext(ctx).Error(err, "delete worker Job", "run", run.Name, "job", job.Name)
		}
	}
}

// reconcileWorker creates or inspects worker w's Job and records its
// verdict once it has finished. Returns whether the worker has finished
// and, when it finished in this pass, its Job (to delete once the verdict
// is persisted).
func (r *TestRunReconciler) reconcileWorker(ctx context.Context, run *testsv1alpha1.TestRun,
	test *testsv1alpha1.Test, w compiler.Worker) (bool, *batchv1.Job, error) {
	key := WorkerStepKey(w.Index)
	step := run.Status.Steps[key]
	if IsTerminalPhase(testsv1alpha1.Phase(step.Phase)) {
		return true, nil, nil
	}
	jobName := compiler.WorkerJobName(run, w.Index)
	var job batchv1.Job
	err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: jobName}, &job)
	switch {
	case apierrors.IsNotFound(err) && step.Phase != "":
		// Created earlier, gone now (and not because it finished).
		if r.APIReader != nil {
			if lerr := r.APIReader.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: jobName}, &job); lerr == nil {
				return false, nil, nil // cache lag
			}
		}
		r.finishWorker(run, w.Index, &RunResult{Phase: testsv1alpha1.PhaseError,
			ErrorMessage: ReasonOrphanJobMissing + ": worker Job was deleted before it finished"})
		return true, nil, nil
	case apierrors.IsNotFound(err):
		return false, nil, r.createWorker(ctx, run, test, w)
	case err != nil:
		return false, nil, err
	}

	pod, _ := r.findPodForJob(ctx, &job)
	if infra := AnalyzePod(pod, r.Now().Time, r.unschedulableTimeout()); infra.Reason != "" {
		r.endWorker(run, w.Index, &RunResult{Phase: testsv1alpha1.PhaseError,
			ErrorMessage: infra.Reason + ": " + infra.Message})
		return true, &job, nil
	}
	switch InspectJob(&job) {
	case JobSucceeded, JobFailedConclusion:
		result, err := r.workerResult(ctx, run, w.Index, &job, pod)
		if err != nil {
			return false, nil, err
		}
		r.endWorker(run, w.Index, result)
		return true, &job, nil
	}
	if IsPodRunning(pod) {
		if r.LogRegistry != nil {
			keys := storage.ForRun(run.Namespace, string(run.UID)).Worker(w.Index)
			if err := r.LogRegistry.EnsureTailer(ctx, workerTailerID(run, w.Index), keys, pod.Namespace, pod.Name); err != nil {
				log.FromContext(ctx).Error(err, "EnsureTailer failed for worker", "run", run.Name, "worker", w.Index)
			}
		}
		if step.Phase != testsv1alpha1.StepPhase(testsv1alpha1.PhaseRunning) {
			now := r.Now()
			step.Phase = testsv1alpha1.StepPhase(testsv1alpha1.PhaseRunning)
			step.StartedAt = &now
			run.Status.Steps[key] = step
		}
	}
	return false, nil, nil
}

func (r *TestRunReconciler) createWorker(ctx context.Context, run *testsv1alpha1.TestRun,
	test *testsv1alpha1.Test, w compiler.Worker) error {
	job, aux, err := compiler.CompileWorker(test, run, r.CompilerOpts, w)
	if err != nil {
		r.finishWorker(run, w.Index, &RunResult{Phase: testsv1alpha1.PhaseError, ErrorMessage: ReasonCompileError + ": " + err.Error()})
		return nil
	}
	for _, obj := range aux {
		if err := r.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create worker %d aux %T: %w", w.Index, obj, err)
		}
	}
	if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		if apierrors.IsInvalid(err) {
			r.finishWorker(run, w.Index, &RunResult{Phase: testsv1alpha1.PhaseError,
				ErrorMessage: fmt.Sprintf("%s: Job rejected by the API server: %v", ReasonCompileError, err)})
			return nil
		}
		return fmt.Errorf("create worker %d Job: %w", w.Index, err)
	}
	now := r.Now()
	run.Status.Steps[WorkerStepKey(w.Index)] = testsv1alpha1.StepResult{
		Phase: testsv1alpha1.StepPhase(testsv1alpha1.PhaseQueued), QueuedAt: &now,
	}
	return nil
}

// workerResult is a finished worker's verdict: its result.json, else the
// summary its wrapper left in the pod status, else the §15.2 fallback.
func (r *TestRunReconciler) workerResult(ctx context.Context, run *testsv1alpha1.TestRun, i int,
	job *batchv1.Job, pod *corev1.Pod) (*RunResult, error) {
	err := ErrResultNotFound
	var result *RunResult
	if wr, ok := r.Results.(WorkerResultReader); ok {
		result, err = wr.ReadWorker(ctx, run, i)
	}
	switch {
	case err == nil && result != nil:
		return result, nil
	case errors.Is(err, ErrResultMalformed):
		return &RunResult{Phase: testsv1alpha1.PhaseError, ErrorMessage: ReasonMalformedResult + ": " + err.Error()}, nil
	case err == nil || errors.Is(err, ErrResultNotFound):
		if res := ResultFromTermination(pod); res != nil {
			return res, nil
		}
		phase, reason, msg := FallbackPhaseFromJobFailure(job, pod)
		return &RunResult{Phase: phase, ErrorMessage: reason + ": " + msg}, nil
	}
	return nil, err // transient: retry on the next reconcile
}

// endWorker records a finished worker and stops its log tailer (final
// flush) — the caller deletes its Job once the verdict is persisted.
func (r *TestRunReconciler) endWorker(run *testsv1alpha1.TestRun, i int, result *RunResult) {
	r.finishWorker(run, i, result)
	if r.LogRegistry != nil {
		r.LogRegistry.StopTailer(workerTailerID(run, i))
	}
}

// finishWorker writes a worker's verdict into the run's status: its step,
// and its test counts and artifacts folded into the run's.
func (r *TestRunReconciler) finishWorker(run *testsv1alpha1.TestRun, i int, res *RunResult) {
	key := WorkerStepKey(i)
	step := run.Status.Steps[key]
	now := r.Now()
	step.Phase = testsv1alpha1.StepPhaseFromPhase(res.Phase)
	step.Message = res.ErrorMessage
	step.FinishedAt = &now
	if step.StartedAt == nil {
		step.StartedAt = &now
	}
	run.Status.Steps[key] = step
	// Workers check out the same revision; the first one's commit stands.
	if run.Status.Content == nil {
		run.Status.Content = res.Content
	}
	if tc := res.TestCounts; tc != nil {
		if run.Status.TestCounts == nil {
			run.Status.TestCounts = &testsv1alpha1.TestCounts{}
		}
		run.Status.TestCounts.Total += tc.Total
		run.Status.TestCounts.Passed += tc.Passed
		run.Status.TestCounts.Failed += tc.Failed
		run.Status.TestCounts.Skipped += tc.Skipped
	}
	// The run's artifact paths are relative to its artifacts/ prefix, where
	// worker i's live under workers/<i>/artifacts/.
	for _, a := range res.Artifacts {
		a.Path = fmt.Sprintf("workers/%d/artifacts/%s", i, a.Path)
		run.Status.ArtifactRefs = append(run.Status.ArtifactRefs, a)
	}
}

// aggregateWorkers: any error → error, else any failure (or abort) →
// failed, else passed.
func aggregateWorkers(run *testsv1alpha1.TestRun, n int) (testsv1alpha1.Phase, string) {
	var errs, fails int
	for i := range n {
		switch testsv1alpha1.Phase(run.Status.Steps[WorkerStepKey(i)].Phase) {
		case testsv1alpha1.PhaseError:
			errs++
		case testsv1alpha1.PhaseFailed, testsv1alpha1.PhaseAborted:
			fails++
		}
	}
	switch {
	case errs > 0:
		return testsv1alpha1.PhaseError, fmt.Sprintf("%d of %d workers errored, %d failed", errs, n, fails)
	case fails > 0:
		return testsv1alpha1.PhaseFailed, fmt.Sprintf("%d of %d workers failed", fails, n)
	}
	return testsv1alpha1.PhasePassed, fmt.Sprintf("all %d workers passed", n)
}

func workerTailerID(run *testsv1alpha1.TestRun, i int) string {
	return fmt.Sprintf("%s/workers/%d", tailerID(run), i)
}

// stopWorkers deletes a parallel run's worker Jobs and stops their log
// tailers (abort, deletion). Best effort.
func (r *TestRunReconciler) stopWorkers(ctx context.Context, run *testsv1alpha1.TestRun) {
	spec, err := resolvedSpecOf(run)
	if err != nil || spec.Parallel == nil {
		return
	}
	for _, w := range compiler.ParallelWorkers(spec.Parallel) {
		if r.LogRegistry != nil {
			r.LogRegistry.StopTailer(workerTailerID(run, w.Index))
		}
		var job batchv1.Job
		err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: compiler.WorkerJobName(run, w.Index)}, &job)
		if err == nil {
			if err := deleteJobBackground(ctx, r.Client, &job); err != nil {
				log.FromContext(ctx).Error(err, "delete worker Job", "run", run.Name, "worker", w.Index)
			}
		} else if client.IgnoreNotFound(err) != nil {
			log.FromContext(ctx).Error(err, "get worker Job", "run", run.Name, "worker", w.Index)
		}
	}
}

func statusEqual(a, b *testsv1alpha1.TestRunStatus) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}
