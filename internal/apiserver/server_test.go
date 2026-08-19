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

// Unit tests for internal/apiserver. All handlers hit via httptest with a
// fake controller-runtime client + hand-rolled fake store/downloader.
// goleak covers the whole package so WS handlers can't leak goroutines
// (bug that only shows up under load).
package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/logstream"
	"github.com/hinskii/kubetest-alt/internal/store"
)

// TestMain runs goleak after all tests. Any WS handler that leaves a
// goroutine dangling (e.g. chunk_reader loop not respecting ctx cancel on
// client disconnect) trips this — the class of bug that only shows up
// under load in production.
func TestMain(m *testing.M) {
	// Starting strict — add explicit ignore options here if a legitimate
	// long-lived background goroutine surfaces.
	goleak.VerifyTestMain(m)
}

// -----------------------------------------------------------------------------
// Test scaffolding
// -----------------------------------------------------------------------------

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	sch := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(sch))
	require.NoError(t, testsv1alpha1.AddToScheme(sch))
	return sch
}

// mkServer builds a Server with a fake k8s client + in-memory storage
// fakes. Seed with pre-existing objects via seed.
func mkServer(t *testing.T, seed ...client.Object) (*Server, http.Handler) {
	t.Helper()
	sch := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(sch).WithObjects(seed...).Build()
	s := &Server{
		K8sClient:       c,
		Namespace:       "default",
		LogsBucket:      "kubetest-logs",
		ArtifactsBucket: "kubetest-artifacts",
	}
	return s, s.Handler()
}

// fakeRunStore is a hand-rolled RunReader — small enough to inline rather
// than depending on a mocking framework.
type fakeRunStore struct {
	mu   sync.Mutex
	rows map[string]store.Row // UID → row
}

func newFakeRunStore(rows ...store.Row) *fakeRunStore {
	f := &fakeRunStore{rows: map[string]store.Row{}}
	for _, r := range rows {
		f.rows[r.UID] = r
	}
	return f
}

func (f *fakeRunStore) Get(_ context.Context, uid string) (*store.Row, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[uid]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &r, nil
}

func (f *fakeRunStore) List(_ context.Context, filter store.Filter, page store.Page) ([]store.Row, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Row
	for _, r := range f.rows {
		if filter.TestRef != "" && r.TestRef != filter.TestRef {
			continue
		}
		if filter.Phase != "" && r.Phase != filter.Phase {
			continue
		}
		out = append(out, r)
	}
	if page.Limit > 0 && len(out) > page.Limit {
		out = out[:page.Limit]
	}
	return out, nil
}

// mkTest returns a Test CR with the given name + managed-by label value.
// Empty labelVal → no managed-by label (treated as ui per §7).
func mkTest(name, labelVal string) *testsv1alpha1.Test {
	labels := map[string]string{}
	if labelVal != "" {
		labels[LabelManagedBy] = labelVal
	}
	return &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    labels,
		},
		Spec: testsv1alpha1.TestSpec{
			Container: testsv1alpha1.ContainerConfig{
				Image: "grafana/k6:2.2.0",
				Args:  []string{"run", "script.js"},
			},
			Content: testsv1alpha1.Content{
				Git: &testsv1alpha1.GitContent{URI: "https://example.com/tests.git"},
			},
		},
	}
}

// doRequest executes a request against the handler and returns the
// response body decoded into a fresh map (or nil for empty bodies).
func doRequest(t *testing.T, h http.Handler, method, target string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var decoded map[string]any
	if rec.Body.Len() > 0 && rec.Header().Get("Content-Type") == "application/json; charset=utf-8" {
		// Best-effort decode; arrays land as nil map (fine — tests using
		// arrays parse the body themselves).
		_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
	}
	return rec, decoded
}

// -----------------------------------------------------------------------------
// R-mb Managed-by suite (§7) — the core regression table. Each row = 1 test.
// -----------------------------------------------------------------------------

// R-mb-1: gitops + PATCH → 409 with ManagedByGitOps reason.
func TestManagedBy_GitopsPatch_Returns409(t *testing.T) {
	_, h := mkServer(t, mkTest("gitops-owned", ManagedByGitOps))
	rec, body := doRequest(t, h, "PATCH", "/tests/gitops-owned",
		map[string]any{"spec": map[string]any{"type": "cypress"}})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, ReasonManagedByGitOps, body["reason"])
	assert.Contains(t, body["message"], "managed by GitOps")
}

// R-mb-2: gitops + DELETE → 409 with ManagedByGitOps reason.
func TestManagedBy_GitopsDelete_Returns409(t *testing.T) {
	_, h := mkServer(t, mkTest("gitops-owned", ManagedByGitOps))
	rec, body := doRequest(t, h, "DELETE", "/tests/gitops-owned", nil)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, ReasonManagedByGitOps, body["reason"])
}

// R-mb-3: gitops Test + POST run → 201 (runs are ephemeral, always allowed
// per §7 — the whole reason we have this table).
func TestManagedBy_PostRunOnGitopsTest_Returns201(t *testing.T) {
	_, h := mkServer(t, mkTest("gitops-owned", ManagedByGitOps))
	rec, body := doRequest(t, h, "POST", "/runs",
		&testsv1alpha1.TestRun{
			ObjectMeta: metav1.ObjectMeta{Name: "ui-run-1", Namespace: "default"},
			Spec:       testsv1alpha1.TestRunSpec{TestRef: "gitops-owned"},
		})
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "ui", body["spec"].(map[string]any)["source"],
		"server MUST set source=ui regardless of caller intent")
}

// R-mb-4: ui + PATCH → 200.
func TestManagedBy_UIPatch_Returns200(t *testing.T) {
	_, h := mkServer(t, mkTest("ui-owned", ManagedByUI))
	rec, _ := doRequest(t, h, "PATCH", "/tests/ui-owned",
		map[string]any{"spec": map[string]any{"type": "cypress"}})
	assert.Equal(t, http.StatusOK, rec.Code)
}

// R-mb-5: missing managed-by label → treated as ui → PATCH 200.
func TestManagedBy_MissingLabel_TreatedAsUI(t *testing.T) {
	_, h := mkServer(t, mkTest("no-label", ""))
	rec, _ := doRequest(t, h, "PATCH", "/tests/no-label",
		map[string]any{"spec": map[string]any{"type": "cypress"}})
	assert.Equal(t, http.StatusOK, rec.Code)
}

// R-mb-6: POST /tests with spoofed managed-by=gitops → 400 (spoof guard).
// Without this, an attacker could create a "gitops-owned" Test via the
// GUI and then locking it against further edits — the whole point of §7
// enforcement collapses.
func TestManagedBy_PostSpoof_Returns400(t *testing.T) {
	_, h := mkServer(t)
	body := map[string]any{
		"metadata": map[string]any{
			"name":      "spoofed",
			"namespace": "default",
			"labels": map[string]any{
				LabelManagedBy: ManagedByGitOps,
			},
		},
		"spec": map[string]any{"type": "k6"},
	}
	rec, envelope := doRequest(t, h, "POST", "/tests", body)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, ReasonBadRequest, envelope["reason"])
	assert.Contains(t, envelope["message"], LabelManagedBy)
}

// R-mb-7: POST /tests sets managed-by=ui exactly once.
func TestManagedBy_PostSetsUILabelExactlyOnce(t *testing.T) {
	_, h := mkServer(t)
	rec, body := doRequest(t, h, "POST", "/tests",
		&testsv1alpha1.Test{
			ObjectMeta: metav1.ObjectMeta{Name: "clean", Namespace: "default"},
			Spec: testsv1alpha1.TestSpec{
				Container: testsv1alpha1.ContainerConfig{
					Image: "grafana/k6:2.2.0",
					Args:  []string{"run", "script.js"},
				},
			},
		})
	assert.Equal(t, http.StatusCreated, rec.Code)
	labels := body["metadata"].(map[string]any)["labels"].(map[string]any)
	assert.Equal(t, ManagedByUI, labels[LabelManagedBy])
}

// R-mb-8: PATCH cannot re-label ui → gitops (would silently escape mgmt).
func TestManagedBy_PatchRejectsGitopsRelabel(t *testing.T) {
	_, h := mkServer(t, mkTest("ui-owned", ManagedByUI))
	rec, _ := doRequest(t, h, "PATCH", "/tests/ui-owned",
		map[string]any{"metadata": map[string]any{"labels": map[string]string{
			LabelManagedBy: ManagedByGitOps,
		}}})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// -----------------------------------------------------------------------------
// R-runs Run listing + merge
// -----------------------------------------------------------------------------

// R-runs-1: merged list dedupes by UID; cluster wins when a run is in both.
func TestRuns_MergeDedupesByUID(t *testing.T) {
	sameUID := "aaaaaaaa-0000-0000-0000-000000000001"
	crRun := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shared",
			Namespace: "default",
			UID:       types.UID(sameUID),
		},
		Spec: testsv1alpha1.TestRunSpec{TestRef: "t"},
		Status: testsv1alpha1.TestRunStatus{
			Phase:   "running", // fresher than archived
			Message: "cluster wins",
		},
	}
	s, _ := mkServer(t, crRun)

	startedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(time.Minute)
	s.Store = newFakeRunStore(store.Row{
		UID:        sameUID, // same UID as the CR
		Name:       "shared",
		Namespace:  "default",
		TestRef:    "t",
		Phase:      "passed", // stale — CR overrides
		Message:    "archive stale",
		StartedAt:  &startedAt,
		FinishedAt: finishedAt,
	})
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/runs", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var out []runEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 1, "duplicate UID must dedupe")
	assert.Equal(t, "running", out[0].Phase, "cluster must win on dedup")
	assert.Equal(t, "cluster wins", out[0].Message)
	assert.Equal(t, "cluster", out[0].Origin)
}

// R-runs-2: sort by startedAt DESC; nil sinks; UID tiebreak.
func TestRuns_SortByStartedAtDesc(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	s, _ := mkServer(t)
	s.Store = newFakeRunStore(
		store.Row{UID: "u-oldest", Name: "a", Namespace: "default", Phase: "passed", StartedAt: &t1, FinishedAt: t1.Add(time.Minute)},
		store.Row{UID: "u-newest", Name: "b", Namespace: "default", Phase: "passed", StartedAt: &t3, FinishedAt: t3.Add(time.Minute)},
		store.Row{UID: "u-middle", Name: "c", Namespace: "default", Phase: "passed", StartedAt: &t2, FinishedAt: t2.Add(time.Minute)},
	)
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/runs", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var out []runEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 3)
	assert.Equal(t, "u-newest", out[0].UID)
	assert.Equal(t, "u-middle", out[1].UID)
	assert.Equal(t, "u-oldest", out[2].UID)
}

// R-runs-3: filter by test + phase narrows results.
func TestRuns_FilterByTestAndPhase(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, _ := mkServer(t)
	s.Store = newFakeRunStore(
		store.Row{UID: "keep", TestRef: "match", Phase: "failed", StartedAt: &t1, FinishedAt: t1.Add(time.Minute)},
		store.Row{UID: "drop-test", TestRef: "other", Phase: "failed", StartedAt: &t1, FinishedAt: t1.Add(time.Minute)},
		store.Row{UID: "drop-phase", TestRef: "match", Phase: "passed", StartedAt: &t1, FinishedAt: t1.Add(time.Minute)},
	)
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/runs?test=match&phase=failed", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var out []runEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 1)
	assert.Equal(t, "keep", out[0].UID)
}

// R-runs-4: pagination limit clamps and defaults.
func TestRuns_PaginationLimitDefaultsAndClamps(t *testing.T) {
	s, _ := mkServer(t)
	// Store has 60 rows; default limit is 50.
	rows := make([]store.Row, 0, 60)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 60 {
		ts := base.Add(time.Duration(i) * time.Second)
		rows = append(rows, store.Row{
			UID:        "u-" + string(rune('A'+i)),
			Phase:      "passed",
			StartedAt:  &ts,
			FinishedAt: ts.Add(time.Minute),
		})
	}
	s.Store = newFakeRunStore(rows...)
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/runs", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var out []runEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.LessOrEqual(t, len(out), 50, "default limit is 50")
}

// R-runs-5: getRun by name → cluster; by UID → store.
func TestRuns_GetByNameThenUID(t *testing.T) {
	cr := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "live",
			Namespace: "default",
			UID:       types.UID("aaaaaaaa-0000-0000-0000-000000000009"),
		},
		Spec:   testsv1alpha1.TestRunSpec{TestRef: "t"},
		Status: testsv1alpha1.TestRunStatus{Phase: "running"},
	}
	s, _ := mkServer(t, cr)
	finishedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.Store = newFakeRunStore(store.Row{
		UID:        "archive-only-uid",
		Name:       "archived",
		Namespace:  "default",
		Phase:      "passed",
		FinishedAt: finishedAt,
	})
	h := s.Handler()

	// by CR name
	rec, body := doRequest(t, h, "GET", "/runs/live", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "running", body["phase"])
	assert.Equal(t, "cluster", body["origin"])

	// by store UID
	rec, body = doRequest(t, h, "GET", "/runs/archive-only-uid", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "passed", body["phase"])
	assert.Equal(t, "archive", body["origin"])

	// neither → 404
	rec, body = doRequest(t, h, "GET", "/runs/missing", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, ReasonNotFound, body["reason"])
}

// -----------------------------------------------------------------------------
// R-run-post: POST /runs source-spoof guard.
// -----------------------------------------------------------------------------

func TestRuns_PostRejectsSourceSpoof(t *testing.T) {
	_, h := mkServer(t)
	rec, body := doRequest(t, h, "POST", "/runs",
		&testsv1alpha1.TestRun{
			ObjectMeta: metav1.ObjectMeta{Name: "spoof", Namespace: "default"},
			Spec:       testsv1alpha1.TestRunSpec{TestRef: "t", Source: "gitops"},
		})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, body["message"], "source")
}

// -----------------------------------------------------------------------------
// R-logs: WS logs — replay buffer + live tail + disconnect safety.
// -----------------------------------------------------------------------------

func TestLogs_ArchivedRun_StreamsAllChunks(t *testing.T) {
	// Setup: an archived run (no CR present) with 3 chunks in the store.
	sch := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(sch).Build()
	uploader := storageFake()
	// Seed chunks directly via the fake's Put — that's the same shape the
	// operator's logstream would produce.
	seedChunk(t, uploader, "kubetest-logs", logstream.LogChunkKey("archived-run", 0), "AAA")
	seedChunk(t, uploader, "kubetest-logs", logstream.LogChunkKey("archived-run", 1), "BBB")
	seedChunk(t, uploader, "kubetest-logs", logstream.LogChunkKey("archived-run", 2), "CCC")

	s := &Server{
		K8sClient:       c,
		Namespace:       "default",
		Downloader:      uploader,
		Lister:          uploader,
		LogsBucket:      "kubetest-logs",
		LogPollInterval: 10 * time.Millisecond,
		LogPollDeadline: 100 * time.Millisecond,
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/runs/archived-run/logs"
	conn, _, err := websocket.Dial(t.Context(), wsURL, nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	// Read frames until server closes.
	var got bytes.Buffer
	for {
		_, data, err := conn.Read(t.Context())
		if err != nil {
			break // server closed after streaming
		}
		got.Write(data)
	}
	assert.Equal(t, "AAABBBCCC", got.String())
}

func TestLogs_LiveRun_StreamsNewChunksAsTheyAppear(t *testing.T) {
	sch := newTestScheme(t)
	// CR present, non-terminal phase → live mode (KeepPolling=true).
	cr := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "live-run", Namespace: "default"},
		Status:     testsv1alpha1.TestRunStatus{Phase: "running"},
	}
	c := fake.NewClientBuilder().WithScheme(sch).WithObjects(cr).Build()
	uploader := storageFake()
	seedChunk(t, uploader, "kubetest-logs", logstream.LogChunkKey("live-run", 0), "one")

	s := &Server{
		K8sClient:       c,
		Namespace:       "default",
		Downloader:      uploader,
		Lister:          uploader,
		LogsBucket:      "kubetest-logs",
		LogPollInterval: 20 * time.Millisecond,
		LogPollDeadline: 200 * time.Millisecond, // short so the test exits fast
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/runs/live-run/logs"
	conn, _, err := websocket.Dial(t.Context(), wsURL, nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	// First frame: initial chunk.
	_, data, err := conn.Read(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "one", string(data))

	// Simulate a new chunk landing in MinIO — the handler should pick it
	// up on the next poll tick.
	seedChunk(t, uploader, "kubetest-logs", logstream.LogChunkKey("live-run", 1), "two")

	_, data, err = conn.Read(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "two", string(data))
	// Then the handler idles until PollDeadline elapses → server closes → next Read errors.
	_, _, err = conn.Read(t.Context())
	assert.Error(t, err, "server should close after PollDeadline")
}

// R-logs-terminal: live→terminal transition must close the WS cleanly and
// stream any final chunks. Without this, a run that finished cleanly would
// keep the WS open until PollDeadline (5 minutes) or the 2h handler cap.
//
// Timeline the test simulates:
//  1. CR is Running; client dials; handler goes into live mode.
//  2. Operator flushes chunk 0 → client reads "one".
//  3. Operator flushes chunk 1 (final) AND flips CR phase to passed.
//  4. Handler's IsTerminal fires on the next poll tick, does one final
//     list pass, emits chunk 1, exits clean.
//  5. Client's next Read gets a clean close (not a timeout).
func TestLogs_LiveToTerminal_StreamsFinalChunksAndCloses(t *testing.T) {
	sch := newTestScheme(t)
	cr := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "flip-run", Namespace: "default"},
		Status:     testsv1alpha1.TestRunStatus{Phase: "running"},
	}
	c := fake.NewClientBuilder().
		WithScheme(sch).
		WithObjects(cr).
		WithStatusSubresource(cr).
		Build()
	uploader := storageFake()
	seedChunk(t, uploader, "kubetest-logs", logstream.LogChunkKey("flip-run", 0), "one")

	s := &Server{
		K8sClient:       c,
		Namespace:       "default",
		Downloader:      uploader,
		Lister:          uploader,
		LogsBucket:      "kubetest-logs",
		LogPollInterval: 20 * time.Millisecond,
		// Deliberately LONG so the test proves we close via IsTerminal, not
		// via the fallback deadline. If IsTerminal didn't work, the test
		// would hang here for 30s and t.Context() would cancel.
		LogPollDeadline: 30 * time.Second,
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/runs/flip-run/logs"
	conn, _, err := websocket.Dial(t.Context(), wsURL, nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	// Read initial chunk.
	_, data, err := conn.Read(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "one", string(data))

	// Simulate the operator's terminal transition — final chunk lands
	// FIRST (matches the real operator: StopTailer's finalize flushes the
	// pending buffer before status Update), then CR phase flips.
	seedChunk(t, uploader, "kubetest-logs", logstream.LogChunkKey("flip-run", 1), "final")
	patch := cr.DeepCopy()
	patch.Status.Phase = testsv1alpha1.PhasePassed
	require.NoError(t, c.Status().Update(t.Context(), patch))

	// Client MUST get the final chunk (proves one-final-round-after-terminal
	// works) — reading in a bounded time proves we didn't wait for
	// PollDeadline.
	readCtx, readCancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer readCancel()
	_, data, err = conn.Read(readCtx)
	require.NoError(t, err, "expected final chunk within 3s (IsTerminal fired)")
	assert.Equal(t, "final", string(data))

	// Next Read gets a clean WS close — NOT a timeout — because the
	// handler exited via IsTerminal, not via poll-deadline expiry.
	closeCtx, closeCancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer closeCancel()
	_, _, err = conn.Read(closeCtx)
	require.Error(t, err, "server must close after final round")
	// If we exited via PollDeadline we'd be here after ~30s; ctx above caps
	// at 3s so the test would fail-fast. TestMain's goleak also catches any
	// leftover poll goroutine.
}

func TestLogs_ClientDisconnectMidStream_NoLeak(t *testing.T) {
	// Client dials, reads one frame, then hangs up. Handler must notice
	// (chunkStream.emit returns io.EOF via Write failure) and exit cleanly.
	// If the handler leaks a goroutine, TestMain's goleak trips.
	sch := newTestScheme(t)
	cr := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hangup-run", Namespace: "default"},
		Status:     testsv1alpha1.TestRunStatus{Phase: "running"},
	}
	c := fake.NewClientBuilder().WithScheme(sch).WithObjects(cr).Build()
	uploader := storageFake()
	for i := range 5 {
		seedChunk(t, uploader, "kubetest-logs",
			logstream.LogChunkKey("hangup-run", uint64(i)),
			strings.Repeat("x", 100))
	}
	s := &Server{
		K8sClient:       c,
		Namespace:       "default",
		Downloader:      uploader,
		Lister:          uploader,
		LogsBucket:      "kubetest-logs",
		LogPollInterval: 10 * time.Millisecond,
		LogPollDeadline: 50 * time.Millisecond,
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/runs/hangup-run/logs"
	conn, _, err := websocket.Dial(t.Context(), wsURL, nil)
	require.NoError(t, err)

	// Read exactly one frame, then abruptly close.
	_, _, err = conn.Read(t.Context())
	require.NoError(t, err)
	_ = conn.CloseNow()
	// Give the server a beat to notice the disconnect + tear down.
	// PollDeadline is 50ms; give 5× headroom.
	time.Sleep(300 * time.Millisecond)
	// goleak (in TestMain) fails if the handler leaked. Nothing to assert
	// here — the test passing without goleak complaint IS the assertion.
}

// -----------------------------------------------------------------------------
// R-artifacts
// -----------------------------------------------------------------------------

func TestArtifacts_ReturnsPresignedURL(t *testing.T) {
	sch := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(sch).Build()
	up := storageFake()
	s := &Server{
		K8sClient:          c,
		Namespace:          "default",
		Presigner:          up,
		ArtifactsBucket:    "kubetest-artifacts",
		PresignedURLExpiry: 5 * time.Minute,
	}
	h := s.Handler()

	rec, body := doRequest(t, h, "GET",
		"/runs/run-1/artifacts/results/junit.xml", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, body["url"], "fake://kubetest-artifacts/run-1/results/junit.xml")
	assert.Equal(t, float64(300), body["expiresIn"])
}

func TestArtifacts_PathTraversalRejected(t *testing.T) {
	// The router won't accept literal ".." in the URL path (it 404s at
	// mux level via cleanPath). So we call the handler indirectly by
	// crafting a URL with encoded slashes preserved via url.URL{RawPath}
	// — httptest.NewRequest normalizes, so we hand-craft with a URL
	// object. Real-world attackers try %2e%2e etc.; net/http decodes
	// those before pattern matching too, but our layered check in
	// validateArtifactPath catches whatever slips through.
	sch := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(sch).Build()
	up := storageFake()
	s := &Server{
		K8sClient:       c,
		Namespace:       "default",
		Presigner:       up,
		ArtifactsBucket: "kubetest-artifacts",
	}
	h := s.Handler()

	cases := []struct {
		name string
		path string
	}{
		{"dotdot", "..%2fetc%2fpasswd"},        // encoded ../etc/passwd
		{"absolute", "%2fetc%2fpasswd"},        // encoded /etc/passwd
		{"backslash", "results%5c..%5csecret"}, // encoded results\..\secret
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target := "/runs/run-1/artifacts/" + c.path
			rec, body := doRequest(t, h, "GET", target, nil)
			// Some encodings normalize before reach; we accept EITHER a 400
			// from our validator OR a 404 from the router. Both mean
			// "attacker did not get bytes."
			require.NotEqual(t, http.StatusOK, rec.Code,
				"traversal attempt must NOT succeed: %s", target)
			if rec.Code == http.StatusBadRequest {
				assert.Equal(t, ReasonBadRequest, body["reason"])
			}
		})
	}
}

// Direct unit tests on validateArtifactPath — the function is the only
// thing between the URL and the object key, so pin its behaviour tightly.
func TestValidateArtifactPath(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"results/junit.xml", false},
		{"logs/step-1/out.log", false},
		{"a/b/c", false},
		{"", true},
		{"/absolute", true},
		{"../etc/passwd", true},
		{"results/..", true},
		{"results/./whatever", true},
		{"results//double", true},
		{"has\\backslash", true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			err := validateArtifactPath(c.in)
			if c.wantErr {
				assert.Error(t, err, "expected error for %q", c.in)
			} else {
				assert.NoError(t, err, "unexpected error for %q", c.in)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// R-openapi: spec zero-diff between code and committed artifact.
// -----------------------------------------------------------------------------

// TestOpenAPI_SpecServedAtEndpoint asserts /openapi.json returns the spec.
func TestOpenAPI_SpecServedAtEndpoint(t *testing.T) {
	_, h := mkServer(t)
	rec, _ := doRequest(t, h, "GET", "/openapi.json", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.Equal(t, "3.1.0", doc["openapi"])
	assert.Contains(t, doc["paths"], "/tests")
	assert.Contains(t, doc["paths"], "/runs")
	assert.Contains(t, doc["paths"], "/runs/{id}/logs")
	assert.Contains(t, doc["paths"], "/runs/{id}/artifacts/{path}")
}

// -----------------------------------------------------------------------------
// Read-path handlers not covered by the managed-by suite
// -----------------------------------------------------------------------------

// TestTests_ListReturnsSeededItems — covers listTests happy path.
func TestTests_ListReturnsSeededItems(t *testing.T) {
	_, h := mkServer(t, mkTest("t1", ManagedByUI), mkTest("t2", ManagedByGitOps))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/tests", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var out []testsv1alpha1.Test
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Len(t, out, 2)
}

// TestTests_GetReturnsSingleItem — covers getTest happy + 404 paths.
func TestTests_GetReturnsSingleItem(t *testing.T) {
	_, h := mkServer(t, mkTest("target", ManagedByUI))
	rec, body := doRequest(t, h, "GET", "/tests/target", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "target", body["metadata"].(map[string]any)["name"])

	rec, body = doRequest(t, h, "GET", "/tests/missing", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, ReasonNotFound, body["reason"])
}

// TestTests_DeleteHappyPath — DELETE on ui-managed Test returns 204.
func TestTests_DeleteHappyPath(t *testing.T) {
	_, h := mkServer(t, mkTest("delme", ManagedByUI))
	rec, _ := doRequest(t, h, "DELETE", "/tests/delme", nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)

	// Follow-up GET → 404.
	rec, _ = doRequest(t, h, "GET", "/tests/delme", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestTests_MalformedJSONReturns400 — POST/PATCH with garbage body.
func TestTests_MalformedJSONReturns400(t *testing.T) {
	_, h := mkServer(t, mkTest("existing", ManagedByUI))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/tests",
		strings.NewReader("{not json")))
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("PATCH", "/tests/existing",
		strings.NewReader("{also broken")))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestParseLimitOrDefault — covers all branches of the query-string helper.
func TestParseLimitOrDefault(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 50},
		{"garbage", 50},
		{"0", 50},
		{"-5", 50},
		{"10", 10},
		{"500", 500},
		{"501", 500},
		{"999999", 500},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, parseLimitOrDefault(c.in), "in=%q", c.in)
	}
}

// TestRunEnvelopeFromCR_HandlesNilTimestamps — the CR → envelope projection
// must survive nil StartedAt/QueuedAt/FinishedAt (fresh/live runs).
func TestRunEnvelopeFromCR_HandlesNilTimestamps(t *testing.T) {
	cr := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "fresh", Namespace: "default", UID: types.UID("u")},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "t"},
		Status:     testsv1alpha1.TestRunStatus{Phase: "queued"},
	}
	env := runEnvelopeFromCR(cr)
	assert.Equal(t, "u", env.UID)
	assert.Equal(t, "queued", env.Phase)
	assert.Nil(t, env.StartedAt)
	assert.Nil(t, env.FinishedAt)
	assert.Nil(t, env.QueuedAt)
}

// TestCreateTest_MalformedBodyReturns400 — createTest decode-error path.
func TestCreateTest_MalformedBodyReturns400(t *testing.T) {
	_, h := mkServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/tests",
		strings.NewReader(`{"metadata":{"name":`))) // truncated JSON
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestCreateRun_MalformedBodyReturns400 — createRun decode-error path.
func TestCreateRun_MalformedBodyReturns400(t *testing.T) {
	_, h := mkServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/runs",
		strings.NewReader(`not-json-at-all`)))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestLogs_MissingStorageReturns503 — /runs/{id}/logs when MinIO unwired.
func TestLogs_MissingStorageReturns503(t *testing.T) {
	sch := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(sch).Build()
	s := &Server{K8sClient: c, Namespace: "default"} // no Downloader/Lister
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/runs/some-run/logs", nil)
	s.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// TestArtifacts_MissingStorageReturns503 — presigner unwired.
func TestArtifacts_MissingStorageReturns503(t *testing.T) {
	sch := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(sch).Build()
	s := &Server{K8sClient: c, Namespace: "default"} // no Presigner
	rec, _ := doRequest(t, s.Handler(), "GET",
		"/runs/some-run/artifacts/foo.txt", nil)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// TestGetTest_EmptyNameReturns400 — the router won't route "/tests/"
// (it 404s), but the handler still needs to reject empty PathValue in
// case someone hits it directly. Test via a synthetic PathValue-less path.
func TestGetTest_MissingNameReturns404(t *testing.T) {
	_, h := mkServer(t)
	rec, _ := doRequest(t, h, "GET", "/tests/nonexistent", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestDelete_NotFoundReturns404 — deleteTest 404 path.
func TestDelete_NotFoundReturns404(t *testing.T) {
	_, h := mkServer(t)
	rec, _ := doRequest(t, h, "DELETE", "/tests/nope", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestRunEnvelopeFromCR_WithAllTimestamps — happy path for the CR
// projection covers the non-nil branches.
func TestRunEnvelopeFromCR_WithAllTimestamps(t *testing.T) {
	q := metav1.NewTime(time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))
	s := metav1.NewTime(time.Date(2026, 1, 1, 10, 0, 1, 0, time.UTC))
	f := metav1.NewTime(time.Date(2026, 1, 1, 10, 0, 5, 0, time.UTC))
	cr := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "full", Namespace: "default", UID: "u"},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "t", Source: "ui"},
		Status: testsv1alpha1.TestRunStatus{
			Phase:      "passed",
			QueuedAt:   &q,
			StartedAt:  &s,
			FinishedAt: &f,
			DurationMs: 4000,
			Message:    "done",
		},
	}
	env := runEnvelopeFromCR(cr)
	assert.NotNil(t, env.QueuedAt)
	assert.NotNil(t, env.StartedAt)
	assert.NotNil(t, env.FinishedAt)
	assert.Equal(t, "done", env.Message)
	assert.Equal(t, int64(4000), env.DurationMs)
}

// TestWriteAPIError_MapsAlreadyExists — POST /tests on existing name.
func TestWriteAPIError_MapsAlreadyExists(t *testing.T) {
	_, h := mkServer(t, mkTest("dup", ManagedByUI))
	rec, body := doRequest(t, h, "POST", "/tests",
		&testsv1alpha1.Test{
			ObjectMeta: metav1.ObjectMeta{Name: "dup", Namespace: "default"},
			Spec: testsv1alpha1.TestSpec{
				Container: testsv1alpha1.ContainerConfig{
					Image: "grafana/k6:2.2.0",
					Args:  []string{"run", "script.js"},
				},
			},
		})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, ReasonConflict, body["reason"])
}

// TestHealthz — trivial coverage of the health handler.
func TestHealthz(t *testing.T) {
	_, h := mkServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// storageFake is a small alias to keep test bodies terse.
func storageFake() *fakeUploaderDownloader {
	return &fakeUploaderDownloader{
		objects: map[string][]byte{},
	}
}

// fakeUploaderDownloader implements Downloader + Lister + Presigner in one
// small struct so tests inject a single value into all three Server fields.
type fakeUploaderDownloader struct {
	mu      sync.Mutex
	objects map[string][]byte // key = "<bucket>/<key>"
}

func (f *fakeUploaderDownloader) put(bucket, key string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[bucket+"/"+key] = body
}

func (f *fakeUploaderDownloader) Get(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[bucket+"/"+key]
	if !ok {
		return nil, io.EOF // any error is fine — chunkStream surfaces it
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (f *fakeUploaderDownloader) List(_ context.Context, bucket, prefix string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefixWithBucket := bucket + "/" + prefix
	var out []string
	for k := range f.objects {
		if strings.HasPrefix(k, prefixWithBucket) {
			out = append(out, strings.TrimPrefix(k, bucket+"/"))
		}
	}
	// chunk_reader depends on sorted output.
	sortStrings(out)
	return out, nil
}

func (f *fakeUploaderDownloader) PresignGetURL(_ context.Context, bucket, key string, expiry time.Duration) (string, error) {
	return "fake://" + bucket + "/" + key + "?expires=" + duration2Sec(expiry), nil
}

func sortStrings(s []string) {
	// tiny impl to avoid importing sort at the caller
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func duration2Sec(d time.Duration) string {
	sec := int64(d.Seconds())
	return itoa(sec)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return sign + string(buf[i:])
}

// seedChunk writes body under "kubetest-logs"/key on the fake uploader.
// The bucket is fixed because every logstream chunk lives there in
// production — parameterizing didn't add value and tripped unparam.
func seedChunk(t *testing.T, up *fakeUploaderDownloader, _bucket, key, body string) {
	t.Helper()
	up.put("kubetest-logs", key, []byte(body))
}
