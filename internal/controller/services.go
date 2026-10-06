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
	"fmt"
	"slices"
	"strings"
	"time"

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

// DefaultServiceTimeout bounds the wait for a service to become ready
// when spec.services.<name>.timeout is unset.
const DefaultServiceTimeout = 5 * time.Minute

// servicePoll is how often a run waiting for its services re-checks, on
// top of the pod events that wake it.
const servicePoll = 2 * time.Second

// +kubebuilder:rbac:groups="",resources=pods,verbs=create;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=create;delete

// ensureServices starts spec.services (headless Services + replica Pods)
// and reports whether every replica is ready, so the test Job can be
// created. Fails the run when a replica can't run (image pull, crash,
// unschedulable) or isn't ready within the service's timeout.
//
// Returns done=true when the caller must return res/err as is (waiting,
// or the run just ended); done=false means "all ready, go on".
func (r *TestRunReconciler) ensureServices(ctx context.Context, run *testsv1alpha1.TestRun,
	test *testsv1alpha1.Test) (res ctrl.Result, done bool, err error) {
	compiled, err := compiler.CompileServices(test, run)
	if err != nil {
		res, err = r.transitionTerminal(ctx, run, testsv1alpha1.PhaseError, ReasonCompileError, err.Error())
		return res, true, err
	}
	for _, svc := range compiled.Services {
		if err := r.Create(ctx, svc); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, true, fmt.Errorf("create service %s: %w", svc.Name, err)
		}
	}
	now := r.Now().Time
	var waiting []string
	pending := map[string][2]int{} // service → ready, total
	for _, rep := range compiled.Replicas {
		var pod corev1.Pod
		err := r.Get(ctx, client.ObjectKeyFromObject(rep.Pod), &pod)
		switch {
		case apierrors.IsNotFound(err):
			if err := r.Create(ctx, rep.Pod); err != nil && !apierrors.IsAlreadyExists(err) {
				return ctrl.Result{}, true, fmt.Errorf("create service pod %s: %w", rep.Pod.Name, err)
			}
			waiting = append(waiting, rep.Service)
			continue
		case err != nil:
			return ctrl.Result{}, true, err
		}
		if msg := serviceFailure(&pod, now, r.unschedulableTimeout()); msg != "" {
			res, err = r.transitionTerminal(ctx, run, testsv1alpha1.PhaseError, ReasonServiceNotReady,
				fmt.Sprintf("service %s (%s): %s", rep.Service, pod.Name, msg))
			return res, true, err
		}
		r.tailServiceLogs(ctx, run, test, rep.Service, &pod)
		c := pending[rep.Service]
		c[1]++
		if podReady(&pod) {
			c[0]++
		} else {
			waiting = append(waiting, rep.Service)
			if timeout := serviceTimeout(test, rep.Service); now.Sub(pod.CreationTimestamp.Time) > timeout {
				res, err = r.transitionTerminal(ctx, run, testsv1alpha1.PhaseError, ReasonServiceNotReady,
					fmt.Sprintf("service %s: replica %s not ready after %s", rep.Service, pod.Name, timeout))
				return res, true, err
			}
		}
		pending[rep.Service] = c
	}
	if len(waiting) == 0 {
		return ctrl.Result{}, false, nil
	}
	msg := "waiting for services: " + strings.Join(slices.Compact(slices.Sorted(slices.Values(waiting))), ", ")
	if run.Status.Message != msg {
		run.Status.Message = msg
		if err := r.Status().Update(ctx, run); err != nil {
			return ctrl.Result{}, true, err
		}
	}
	return ctrl.Result{RequeueAfter: servicePoll}, true, nil
}

// serviceFailure says why a replica will never become ready, or "".
func serviceFailure(pod *corev1.Pod, now time.Time, unschedulableFor time.Duration) string {
	if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
		return fmt.Sprintf("replica exited (pod %s)", strings.ToLower(string(pod.Status.Phase)))
	}
	if infra := AnalyzePod(pod, now, unschedulableFor); infra.Reason != "" {
		return infra.Message
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil && w.Reason == "CrashLoopBackOff" {
			return "replica keeps crashing (CrashLoopBackOff)"
		}
	}
	return ""
}

func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func serviceTimeout(test *testsv1alpha1.Test, name string) time.Duration {
	if t := test.Spec.Services[name].Timeout; t != nil && t.Duration > 0 {
		return t.Duration
	}
	return DefaultServiceTimeout
}

// serviceTailerID keys a replica's log tailer in the registry.
func serviceTailerID(run *testsv1alpha1.TestRun, pod string) string {
	return tailerID(run) + "/services/" + pod
}

// tailServiceLogs streams a running replica's log when the service asked
// for it (spec.services.<name>.logs).
func (r *TestRunReconciler) tailServiceLogs(ctx context.Context, run *testsv1alpha1.TestRun,
	test *testsv1alpha1.Test, service string, pod *corev1.Pod) {
	if r.LogRegistry == nil || !test.Spec.Services[service].Logs || pod.Status.Phase != corev1.PodRunning {
		return
	}
	keys := storage.ForRun(run.Namespace, string(run.UID)).Service(pod.Name)
	if err := r.LogRegistry.EnsureTailer(ctx, serviceTailerID(run, pod.Name), keys, pod.Namespace, pod.Name); err != nil {
		log.FromContext(ctx).Error(err, "EnsureTailer failed for service replica", "run", run.Name, "pod", pod.Name)
	}
}

// teardownServices removes a finished run's services (their log tailers
// first, so the last lines are flushed). Best effort: the owner reference
// to the TestRun removes them anyway when the run is deleted.
func (r *TestRunReconciler) teardownServices(ctx context.Context, run *testsv1alpha1.TestRun) {
	if run.Status.ResolvedSpec == "" {
		return
	}
	var spec testsv1alpha1.TestSpec
	if err := json.Unmarshal([]byte(run.Status.ResolvedSpec), &spec); err != nil || len(spec.Services) == 0 {
		return
	}
	compiled, err := compiler.CompileServices(&testsv1alpha1.Test{Spec: spec}, run)
	if err != nil {
		return
	}
	logger := log.FromContext(ctx)
	for _, rep := range compiled.Replicas {
		if r.LogRegistry != nil {
			r.LogRegistry.StopTailer(serviceTailerID(run, rep.Pod.Name))
		}
		if err := r.Delete(ctx, rep.Pod); client.IgnoreNotFound(err) != nil {
			logger.Error(err, "delete service pod", "pod", rep.Pod.Name)
		}
	}
	for _, svc := range compiled.Services {
		obj := &corev1.Service{}
		obj.Name, obj.Namespace = svc.Name, svc.Namespace
		if err := r.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
			logger.Error(err, "delete service", "service", types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name})
		}
	}
}
