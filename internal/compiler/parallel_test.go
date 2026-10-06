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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

func i32(v int32) *int32 { return &v }

func TestParallelWorkers(t *testing.T) {
	assert.Nil(t, ParallelWorkers(nil))

	// count only
	ws := ParallelWorkers(&testsv1alpha1.ParallelSpec{Count: i32(3)})
	require.Len(t, ws, 3)
	assert.Equal(t, Worker{Index: 2, Count: 3, Matrix: map[string]string{}, Shard: map[string]string{}}, ws[2])

	// matrix × count, keys sorted
	ws = ParallelWorkers(&testsv1alpha1.ParallelSpec{Count: i32(2), Matrix: map[string][]string{
		"os": {"linux"}, "browser": {"chrome", "firefox"},
	}})
	require.Len(t, ws, 4)
	assert.Equal(t, map[string]string{"browser": "chrome", "os": "linux"}, ws[1].Matrix)
	assert.Equal(t, map[string]string{"browser": "firefox", "os": "linux"}, ws[2].Matrix)
	for i, w := range ws {
		assert.Equal(t, i, w.Index)
		assert.Equal(t, 4, w.Count)
	}

	// shards decide the count: one per value of the longest list
	ws = ParallelWorkers(&testsv1alpha1.ParallelSpec{Shards: map[string][]string{"spec": {"a", "b", "c"}}})
	require.Len(t, ws, 3)
	assert.Equal(t, "b", ws[1].Shard["spec"])

	// maxCount caps it; parts are contiguous and near-equal, nothing lost
	ws = ParallelWorkers(&testsv1alpha1.ParallelSpec{MaxCount: i32(2),
		Shards: map[string][]string{"spec": {"a", "b", "c", "d", "e"}}})
	require.Len(t, ws, 2)
	assert.Equal(t, "a,b", ws[0].Shard["spec"])
	assert.Equal(t, "c,d,e", ws[1].Shard["spec"])

	// count wins over the shard length
	ws = ParallelWorkers(&testsv1alpha1.ParallelSpec{Count: i32(2), Shards: map[string][]string{"spec": {"a", "b", "c", "d"}}})
	assert.Equal(t, []string{"a,b", "c,d"}, []string{ws[0].Shard["spec"], ws[1].Shard["spec"]})
}

func TestCompileWorker(t *testing.T) {
	test := canonicalTest()
	test.Spec.Container.Args = []string{"run", "--tag", "browser={{ matrix.browser }}", "--shard={{ shard.spec }}",
		"{{ worker.index }}/{{ worker.count }}"}
	test.Spec.Artifacts = &testsv1alpha1.ArtifactSpec{Paths: []string{"out/{{ worker.index }}/*.xml"}}
	original := test.DeepCopy()
	run := canonicalTestRun()
	w := Worker{Index: 1, Count: 2, Matrix: map[string]string{"browser": "firefox"}, Shard: map[string]string{"spec": "b,c"}}

	job, aux, err := CompileWorker(test, run, defaultOpts(), w)
	require.NoError(t, err)
	assert.Equal(t, "sample-run-w1", job.Name)
	assert.Equal(t, "1", job.Labels[LabelWorker])
	assert.Equal(t, "sample-run", job.Labels[LabelRunID], "the worker pod still maps to its run")
	assert.Equal(t, "1", job.Spec.Template.Labels[LabelWorker])

	c := job.Spec.Template.Spec.Containers[0]
	assert.Equal(t, []string{"run", "--tag", "browser=firefox", "--shard=b,c", "1/2"}, c.Args)
	env := envOf(c)
	assert.Equal(t, "1", env[EnvWorkerIndex])
	assert.Equal(t, "2", env[EnvWorkerCount])
	assert.Equal(t, "firefox", env["KUBETEST_MATRIX_BROWSER"])
	assert.Equal(t, "b,c", env["KUBETEST_SHARD_SPEC"])

	cm := aux[0].(*corev1.ConfigMap)
	assert.Equal(t, "sample-run-w1-request", cm.Name)
	req := unmarshalRequest(t, cm)
	assert.Equal(t, "runs/kubetest-samples/00000000-0000-0000-0000-0000cafe0001/artifacts/workers/1/", req.StoragePrefix)
	assert.Equal(t, []string{"out/1/*.xml"}, req.Artifacts.Paths)

	assert.Equal(t, original, test, "the input Test is not mutated")
}
