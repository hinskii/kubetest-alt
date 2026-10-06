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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// recordLatestRun keeps Test.status.latestRun (fixes.md #18; the LastRun
// print column) pointing at the most recently started run of the Test.
//
//   - started=true (setup done): this run becomes the latest.
//   - started=false (terminal): the phase is updated only while this run
//     is still the latest, so an older run finishing late can't overwrite
//     a newer one.
//
// Best effort: Test status is a convenience view; run history is the
// record. A failure is logged, never returned.
func (r *TestRunReconciler) recordLatestRun(ctx context.Context, run *testsv1alpha1.TestRun, started bool) {
	var test testsv1alpha1.Test
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.TestRef}, &test); err != nil {
		if !apierrors.IsNotFound(err) {
			log.FromContext(ctx).Error(err, "read Test for latestRun", "test", run.Spec.TestRef)
		}
		return
	}
	cur := test.Status.LatestRun
	if !started && cur != nil && cur.Name != run.Name {
		return
	}
	next := &testsv1alpha1.RunReference{Name: run.Name, Phase: run.Status.Phase, FinishedAt: run.Status.FinishedAt}
	if cur != nil && *cur == *next {
		return
	}
	patch := client.MergeFrom(test.DeepCopy())
	test.Status.LatestRun = next
	if err := r.Status().Patch(ctx, &test, patch); err != nil && !apierrors.IsNotFound(err) {
		log.FromContext(ctx).Error(err, "update Test latestRun", "test", test.Name, "run", run.Name)
	}
}
