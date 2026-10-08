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
	"slices"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/resolver"
)

// Test readiness (step 20h): status.conditions[type=Ready] says whether
// the Test, merged with its templates, can run — in particular whether it
// says where its files are. Templates never default that path, so a Test
// that doesn't set it would otherwise find out only when a run fails.
const (
	ConditionReady         = "Ready"
	ReasonResolved         = "Resolved"
	ReasonParameterMissing = "ParameterMissing"
	ReasonTemplateMissing  = "TemplateMissing"
)

// TestReconciler keeps a Test's Ready condition. It only reads Tests and
// TestTemplates and patches status.conditions — latestRun stays the
// TestRun controller's.
type TestReconciler struct {
	client.Client
	TemplateStore resolver.TemplateStore
}

// Reconcile evaluates one Test.
func (r *TestReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var test testsv1alpha1.Test
	if err := r.Get(ctx, req.NamespacedName, &test); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	cond, err := readiness(&test, r.TemplateStore)
	if err != nil {
		return ctrl.Result{}, err // the template store failed: retry
	}
	cond.ObservedGeneration = test.Generation
	if cur := meta.FindStatusCondition(test.Status.Conditions, ConditionReady); cur != nil &&
		cur.Status == cond.Status && cur.Reason == cond.Reason && cur.Message == cond.Message &&
		cur.ObservedGeneration == cond.ObservedGeneration {
		return ctrl.Result{}, nil
	}
	patch := client.MergeFrom(test.DeepCopy())
	meta.SetStatusCondition(&test.Status.Conditions, cond)
	return ctrl.Result{}, client.IgnoreNotFound(r.Status().Patch(ctx, &test, patch))
}

// readiness is the Ready condition of test. An error means the answer is
// unknown (a store failure), not that the Test is broken.
func readiness(test *testsv1alpha1.Test, store resolver.TemplateStore) (metav1.Condition, error) {
	for _, name := range test.Spec.Use {
		if _, err := store.Get(test.Namespace, name); err != nil {
			if errors.Is(err, resolver.ErrTemplateNotFound) {
				return notReady(ReasonTemplateMissing,
					fmt.Sprintf("template %q in spec.use doesn't exist in namespace %s", name, test.Namespace)), nil
			}
			return metav1.Condition{}, err
		}
	}
	merged, _, err := resolver.MergeTemplates(test, store)
	if err != nil {
		return metav1.Condition{}, err
	}
	if p := resolver.MainPathParam(merged.Container, merged.Config); p != "" && merged.Config[p].Default == "" {
		msg := fmt.Sprintf("set spec.config.%s — where the Test's files are", p)
		if d := merged.Config[p].Description; d != "" {
			msg = fmt.Sprintf("set spec.config.%s: %s", p, d)
		}
		return notReady(ReasonParameterMissing, msg), nil
	}
	return metav1.Condition{Type: ConditionReady, Status: metav1.ConditionTrue, Reason: ReasonResolved}, nil
}

func notReady(reason, msg string) metav1.Condition {
	return metav1.Condition{Type: ConditionReady, Status: metav1.ConditionFalse, Reason: reason, Message: msg}
}

// SetupWithManager watches Tests (spec changes only: our own status
// patches and latestRun updates don't trigger a pass) and TestTemplates
// (a template change re-evaluates the Tests that use it).
func (r *TestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("test").
		For(&testsv1alpha1.Test{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&testsv1alpha1.TestTemplate{}, handler.EnqueueRequestsFromMapFunc(r.testsUsing)).
		Complete(r)
}

// testsUsing maps a TestTemplate to the Tests of its namespace that use it.
func (r *TestReconciler) testsUsing(ctx context.Context, obj client.Object) []reconcile.Request {
	var tests testsv1alpha1.TestList
	if err := r.List(ctx, &tests, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var out []reconcile.Request
	for _, t := range tests.Items {
		if slices.Contains(t.Spec.Use, obj.GetName()) {
			out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: t.Namespace, Name: t.Name}})
		}
	}
	return out
}
