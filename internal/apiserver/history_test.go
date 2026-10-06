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

package apiserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

// mkStorageServer wires a scoped server with one shared storage fake.
func mkStorageServer(t *testing.T, rows []store.Row, seed ...*testsv1alpha1.TestRun) (*Server, *fakeUploaderDownloader, *fakeRunStore) {
	t.Helper()
	objs := make([]client.Object, 0, len(seed))
	for _, r := range seed {
		objs = append(objs, r)
	}
	s, _ := mkServer(t, objs...)
	up := storageFake()
	rs := newFakeRunStore(rows...)
	s.Downloader, s.Lister, s.Presigner, s.Remover = up, up, up, up
	s.Store, s.Deleter = rs, rs
	return s, up, rs
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func archivedRow(name string) store.Row {
	return store.Row{
		UID: testUID(name), Name: name, Namespace: "default", Phase: "passed",
		FinishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		ArtifactRefs: []store.ArtifactRef{
			{Path: "report/index.html", SizeBytes: 5, ContentType: "text/html; charset=utf-8"},
		},
	}
}

// --- artifacts ---------------------------------------------------------

func TestArtifacts_ListFromRunRefs(t *testing.T) {
	run := liveRun("r1", testsv1alpha1.PhasePassed)
	run.Status.ArtifactRefs = []testsv1alpha1.ArtifactRef{
		{Path: "results/junit.xml", SizeBytes: 42, ContentType: "application/xml"},
	}
	s, _, _ := mkStorageServer(t, nil, run)

	rec := get(t, s.Handler(), "/runs/r1/artifacts")
	require.Equal(t, http.StatusOK, rec.Code)
	var got []artifactEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, []artifactEntry{{Path: "results/junit.xml", SizeBytes: 42, ContentType: "application/xml"}}, got)
}

// No refs recorded (e.g. wrapper OOM-killed before result.json): fall back
// to listing the artifacts/ prefix so partial output is still reachable.
func TestArtifacts_ListFallsBackToPrefix(t *testing.T) {
	s, up, _ := mkStorageServer(t, nil, liveRun("r1", testsv1alpha1.PhaseError))
	up.put(runKeys("r1").Artifact("partial/a.txt"), []byte("a"))
	up.put(runKeys("r1").Result(), []byte("{}")) // not an artifact

	rec := get(t, s.Handler(), "/runs/r1/artifacts")
	require.Equal(t, http.StatusOK, rec.Code)
	var got []artifactEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, []artifactEntry{{Path: "partial/a.txt"}}, got)
}

func TestArtifacts_ListEmptyIsArrayNotNull(t *testing.T) {
	s, _, _ := mkStorageServer(t, nil, liveRun("r1", testsv1alpha1.PhasePassed))
	rec := get(t, s.Handler(), "/runs/r1/artifacts")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, "[]", rec.Body.String())
}

func TestArtifacts_StreamsBytesWithSafeHeaders(t *testing.T) {
	s, up, _ := mkStorageServer(t, []store.Row{archivedRow("old")})
	up.put(storageKeysFor("old").Artifact("report/index.html"), []byte("<h1>"))

	rec := get(t, s.Handler(), "/runs/"+testUID("old")+"/artifacts/report/index.html")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "<h1>", rec.Body.String())
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"), "recorded type wins")
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "sandbox", rec.Header().Get("Content-Security-Policy"),
		"workload-produced HTML must not run with the server's origin")
	assert.Equal(t, `inline; filename="index.html"`, rec.Header().Get("Content-Disposition"))

	rec = get(t, s.Handler(), "/runs/"+testUID("old")+"/artifacts/report/index.html?download=1")
	assert.Equal(t, `attachment; filename="index.html"`, rec.Header().Get("Content-Disposition"))
}

func TestArtifacts_StreamUnknownObjectIs404(t *testing.T) {
	s, _, _ := mkStorageServer(t, nil, liveRun("r1", testsv1alpha1.PhasePassed))
	rec := get(t, s.Handler(), "/runs/r1/artifacts/missing.txt")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestArtifactContentType_FallsBackToExtension(t *testing.T) {
	ref := runRef{}
	assert.Equal(t, "application/json", artifactContentType(ref, "a/summary.json"))
	assert.Equal(t, "application/octet-stream", artifactContentType(ref, "a/blob.unknownext"))
}

func TestSafeFilename_StripsHeaderBreakers(t *testing.T) {
	assert.Equal(t, "evilname.txt", safeFilename("dir/evil\"name\r\n.txt"))
}

// --- logs.txt ----------------------------------------------------------

func TestLogsText_ConcatenatesChunks(t *testing.T) {
	s, up, _ := mkStorageServer(t, []store.Row{archivedRow("old")})
	up.put(storageKeysFor("old").LogChunk(0), []byte("line 1\n"))
	up.put(storageKeysFor("old").LogChunk(1), []byte("line 2\n"))

	rec := get(t, s.Handler(), "/runs/"+testUID("old")+"/logs.txt?download=1")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "line 1\nline 2\n", rec.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, `attachment; filename="old.log"`, rec.Header().Get("Content-Disposition"))
}

func TestLogsText_NoChunksIsEmpty200(t *testing.T) {
	s, _, _ := mkStorageServer(t, nil, liveRun("r1", testsv1alpha1.PhaseQueued))
	rec := get(t, s.Handler(), "/runs/r1/logs.txt")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Body.String())
}

// --- DELETE /runs/{id} -------------------------------------------------

func del(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, target, nil))
	return rec
}

func TestDeleteRun_ArchivedRemovesRowAndObjects(t *testing.T) {
	s, up, rs := mkStorageServer(t, []store.Row{archivedRow("old"), archivedRow("keep")})
	up.put(storageKeysFor("old").LogChunk(0), []byte("x"))
	up.put(storageKeysFor("old").Artifact("a.txt"), []byte("x"))
	up.put(storageKeysFor("keep").LogChunk(0), []byte("x"))

	rec := del(t, s.Handler(), "/runs/"+testUID("old"))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	_, err := rs.Get(t.Context(), testUID("old"))
	assert.ErrorIs(t, err, store.ErrNotFound)
	assert.False(t, up.has(storageKeysFor("old").LogChunk(0)))
	assert.False(t, up.has(storageKeysFor("old").Artifact("a.txt")))
	assert.True(t, up.has(storageKeysFor("keep").LogChunk(0)), "other runs untouched")

	assert.Equal(t, http.StatusNotFound, del(t, s.Handler(), "/runs/"+testUID("old")).Code, "now gone")
}

func TestDeleteRun_TerminalCRRemovesCRAndRow(t *testing.T) {
	run := liveRun("done", testsv1alpha1.PhaseFailed)
	row := archivedRow("done")
	s, _, rs := mkStorageServer(t, []store.Row{row}, run)

	require.Equal(t, http.StatusNoContent, del(t, s.Handler(), "/runs/done").Code)
	var cr testsv1alpha1.TestRun
	assert.Error(t, s.K8sClient.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "done"}, &cr))
	_, err := rs.Get(t.Context(), testUID("done"))
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestDeleteRun_LiveRunIsConflict(t *testing.T) {
	s, _, _ := mkStorageServer(t, nil, liveRun("busy", testsv1alpha1.PhaseRunning))
	rec := del(t, s.Handler(), "/runs/busy")
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "abort it")
}

func TestDeleteRun_ArchivedWithoutDeleterIs503(t *testing.T) {
	s, _, _ := mkStorageServer(t, []store.Row{archivedRow("old")})
	s.Deleter = nil
	assert.Equal(t, http.StatusServiceUnavailable, del(t, s.Handler(), "/runs/"+testUID("old")).Code)
}

// storageKeysFor is runKeys for archived rows (same derivation: namespace
// "default" + testUID(name)); separate name keeps intent readable.
func storageKeysFor(name string) storage.RunKeys { return runKeys(name) }
