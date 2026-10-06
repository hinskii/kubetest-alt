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

package compiler

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/names"
	"github.com/hinskii/kubetest-alt/pkg/expr"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

// LabelWorker is the spec.parallel worker number on a worker's Job and
// pod (they also carry the run's run-id).
const LabelWorker = "kubetest.io/worker"

// Worker env, alongside the expressions of the same values.
const (
	EnvWorkerIndex = "KUBETEST_WORKER_INDEX"
	EnvWorkerCount = "KUBETEST_WORKER_COUNT"
	EnvShardPrefix = "KUBETEST_SHARD_"
)

// Worker is one spec.parallel worker.
type Worker = expr.WorkerVars

// ParallelWorkers expands spec.parallel into its workers, in a stable
// order: matrix combinations (keys sorted, values in order), and within
// each, the per-combination workers. Per combination: Count; else with
// Shards one per value of the longest list, capped by MaxCount; else 1.
// Shards split each list into contiguous, near-equal parts, comma-joined.
func ParallelWorkers(p *testsv1alpha1.ParallelSpec) []Worker {
	if p == nil {
		return nil
	}
	combos := []map[string]string{{}}
	for _, key := range slices.Sorted(maps.Keys(p.Matrix)) {
		var next []map[string]string
		for _, c := range combos {
			for _, v := range p.Matrix[key] {
				n := maps.Clone(c)
				n[key] = v
				next = append(next, n)
			}
		}
		combos = next
	}
	per := 1
	switch {
	case p.Count != nil:
		per = int(*p.Count)
	case len(p.Shards) > 0:
		per = 0
		for _, values := range p.Shards {
			per = max(per, len(values))
		}
		if p.MaxCount != nil {
			per = min(per, int(*p.MaxCount))
		}
		per = max(per, 1)
	}
	total := len(combos) * per
	out := make([]Worker, 0, total)
	for _, c := range combos {
		for j := range per {
			shard := map[string]string{}
			for key, values := range p.Shards {
				lo, hi := j*len(values)/per, (j+1)*len(values)/per
				shard[key] = strings.Join(values[lo:hi], ",")
			}
			out = append(out, Worker{Index: len(out), Count: total, Matrix: c, Shard: shard})
		}
	}
	return out
}

// WorkerJobName is worker i's Job name.
func WorkerJobName(run *testsv1alpha1.TestRun, i int) string {
	return names.Bounded(fmt.Sprintf("%s-w%d", run.Name, i))
}

// CompileWorker builds the Job of one parallel worker: the run's Test with
// the worker's values filled into its expressions and env, writing to
// RunKeys.Worker(i).
func CompileWorker(test *testsv1alpha1.Test, run *testsv1alpha1.TestRun, opts Options, w Worker) (
	*batchv1.Job, []client.Object, error) {
	if test == nil {
		return nil, nil, ErrNilTest
	}
	if run == nil {
		return nil, nil, ErrNilTestRun
	}
	t := test.DeepCopy()
	substituteWorker(&t.Spec, w)

	env := []corev1.EnvVar{
		{Name: EnvWorkerIndex, Value: fmt.Sprintf("%d", w.Index)},
		{Name: EnvWorkerCount, Value: fmt.Sprintf("%d", w.Count)},
	}
	for _, k := range slices.Sorted(maps.Keys(w.Matrix)) {
		env = append(env, corev1.EnvVar{Name: EnvMatrixPrefix + strings.ToUpper(k), Value: w.Matrix[k]})
	}
	for _, k := range slices.Sorted(maps.Keys(w.Shard)) {
		env = append(env, corev1.EnvVar{Name: EnvShardPrefix + strings.ToUpper(k), Value: w.Shard[k]})
	}
	return compile(t, run, opts, jobTarget{
		Name:   WorkerJobName(run, w.Index),
		Keys:   storage.ForRun(run.Namespace, string(run.UID)).Worker(w.Index),
		Labels: map[string]string{LabelWorker: fmt.Sprintf("%d", w.Index)},
		Env:    env,
	})
}

// substituteWorker fills the worker refs the resolver deferred, in the
// fields it templated.
func substituteWorker(spec *testsv1alpha1.TestSpec, w Worker) {
	sub := func(s string) string { return expr.SubstituteWorker(s, w) }
	subAll := func(xs []string) {
		for i := range xs {
			xs[i] = sub(xs[i])
		}
	}
	subAll(spec.Container.Command)
	subAll(spec.Container.Args)
	spec.Container.WorkingDir = sub(spec.Container.WorkingDir)
	for i := range spec.Container.Env {
		spec.Container.Env[i].Value = sub(spec.Container.Env[i].Value)
	}
	if spec.Pod != nil {
		for k, v := range spec.Pod.Labels {
			spec.Pod.Labels[k] = sub(v)
		}
		for k, v := range spec.Pod.Annotations {
			spec.Pod.Annotations[k] = sub(v)
		}
	}
	if spec.Artifacts != nil {
		subAll(spec.Artifacts.Paths)
	}
}
