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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/executor"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

const testReaderBucket = "kubetest-artifacts"

func TestStorageResultReader_HappyPath(t *testing.T) {
	fake := storage.NewFake()
	// Plant a result.json the way the scraper writes it.
	er := executor.ExecutionResult{
		Phase:      executor.PhasePassed,
		Metrics:    map[string]float64{"p95_ms": 123.4, "rps": 5.6},
		TestCounts: &executor.TestCounts{Total: 10, Passed: 9, Failed: 1},
		Artifacts: []executor.ArtifactRef{
			{Path: "results/junit.xml", Key: readerKeys("run-42").Artifact("results/junit.xml"), SizeBytes: 512},
		},
	}
	b, _ := json.Marshal(er)
	require.NoError(t, fake.Put(context.Background(), testReaderBucket, readerKeys("run-42").Result(),
		strings.NewReader(string(b)), int64(len(b)), "application/json"))

	r := NewStorageResultReader(fake, testReaderBucket)
	got, err := r.Read(context.Background(), readerRun("run-42"))
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, testsv1alpha1.PhasePassed, got.Phase)
	assert.Equal(t, map[string]float64{"p95_ms": 123.4, "rps": 5.6}, got.Metrics)
	require.NotNil(t, got.TestCounts)
	assert.Equal(t, 10, got.TestCounts.Total)
	assert.Len(t, got.Artifacts, 1)
	assert.Equal(t, readerKeys("run-42").Artifact("results/junit.xml"), got.Artifacts[0].Key)
}

// TestStorageResultReader_MissingReturnsErrResultNotFound: crash/OOM path.
// Reconciler falls back to Pod terminated-state analysis (§15.2).
func TestStorageResultReader_MissingReturnsErrResultNotFound(t *testing.T) {
	r := NewStorageResultReader(storage.NewFake(), testReaderBucket)
	_, err := r.Read(context.Background(), readerRun("no-such-run"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrResultNotFound))
}

// TestStorageResultReader_TransientErrorBubbles: the reconciler treats this
// as retryable via FallbackRequeue.
func TestStorageResultReader_TransientErrorBubbles(t *testing.T) {
	fake := storage.NewFake()
	fake.GetErrors = []error{errors.New("network hiccup")}

	r := NewStorageResultReader(fake, testReaderBucket)
	_, err := r.Read(context.Background(), readerRun("run-tx"))
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrResultNotFound), "transient error must NOT collapse into ErrResultNotFound")
	assert.Contains(t, err.Error(), "network hiccup")
}

// TestStorageResultReader_MalformedJSONFails: garbage payload → error
// (permanent: the reconciler ends the run as error/MalformedResult).
func TestStorageResultReader_MalformedJSONFails(t *testing.T) {
	fake := storage.NewFake()
	require.NoError(t, fake.Put(context.Background(), testReaderBucket, readerKeys("run-bad").Result(),
		strings.NewReader("{not-json"), 9, "application/json"))
	r := NewStorageResultReader(fake, testReaderBucket)
	_, err := r.Read(context.Background(), readerRun("run-bad"))
	require.ErrorIs(t, err, ErrResultMalformed, "permanent — the reconciler must not requeue forever")
}

func TestStorageResultReader_NonFinalPhaseIsMalformed(t *testing.T) {
	fake := storage.NewFake()
	body := `{"phase":"running"}`
	require.NoError(t, fake.Put(context.Background(), testReaderBucket, readerKeys("run-odd").Result(),
		strings.NewReader(body), int64(len(body)), "application/json"))
	_, err := NewStorageResultReader(fake, testReaderBucket).Read(context.Background(), readerRun("run-odd"))
	require.ErrorIs(t, err, ErrResultMalformed)
	assert.Contains(t, err.Error(), `phase "running"`)
}

func TestStorageResultReader_GuardRails(t *testing.T) {
	t.Run("nil downloader", func(t *testing.T) {
		r := &StorageResultReader{Bucket: testReaderBucket}
		_, err := r.Read(context.Background(), readerRun("r"))
		require.Error(t, err)
	})
	t.Run("empty bucket", func(t *testing.T) {
		r := &StorageResultReader{Downloader: storage.NewFake()}
		_, err := r.Read(context.Background(), readerRun("r"))
		require.Error(t, err)
	})
}

// readerRun builds a TestRun whose UID is derived from name, so tests can
// plant objects at the exact keys the reader will look up.
func readerRun(name string) *testsv1alpha1.TestRun {
	run := &testsv1alpha1.TestRun{}
	run.Namespace = "ns"
	run.Name = name
	run.UID = types.UID("uid-" + name)
	return run
}

func readerKeys(name string) storage.RunKeys {
	run := readerRun(name)
	return storage.ForRun(run.Namespace, string(run.UID))
}

// A run without a UID has no safe key: the reader must refuse instead of
// falling back to a name-only path another run could own.
func TestStorageResultReader_RunWithoutUID(t *testing.T) {
	r := NewStorageResultReader(storage.NewFake(), testReaderBucket)
	run := readerRun("no-uid")
	run.UID = ""
	_, err := r.Read(context.Background(), run)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrResultNotFound)
}
