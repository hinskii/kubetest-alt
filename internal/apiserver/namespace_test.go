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
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

// mkClusterWideServer is mkServer with Namespace "" — the chart's default.
func mkClusterWideServer(t *testing.T, seed ...client.Object) (*Server, http.Handler) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(seed...).Build()
	s := &Server{K8sClient: c, Bucket: testBucket}
	return s, s.Handler()
}

func testIn(ns, name string) *testsv1alpha1.Test {
	t := mkTest(name, ManagedByUI)
	t.Namespace = ns
	return t
}

// fixes.md #2: with no --namespace, name lookups used Namespace "" and
// 404'd for every existing object. They now need ?namespace=.
func TestClusterWide_GetTestRequiresNamespace(t *testing.T) {
	_, h := mkClusterWideServer(t, testIn("team-a", "smoke"))

	rec, body := doRequest(t, h, "GET", "/tests/smoke", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, body["message"], "namespace is required")

	rec, body = doRequest(t, h, "GET", "/tests/smoke?namespace=team-a", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "smoke", body["metadata"].(map[string]any)["name"])

	rec, _ = doRequest(t, h, "GET", "/tests/smoke?namespace=team-b", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestClusterWide_PatchAndDeleteUseNamespace(t *testing.T) {
	_, h := mkClusterWideServer(t, testIn("team-a", "smoke"))

	rec, _ := doRequest(t, h, "PATCH", "/tests/smoke?namespace=team-a",
		map[string]any{"metadata": map[string]any{"labels": map[string]string{"x": "y"}}})
	assert.Equal(t, http.StatusOK, rec.Code)

	rec, _ = doRequest(t, h, "DELETE", "/tests/smoke?namespace=team-a", nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestClusterWide_ListTestsAllOrFiltered(t *testing.T) {
	_, h := mkClusterWideServer(t, testIn("team-a", "a1"), testIn("team-b", "b1"))

	rec, _ := doRequest(t, h, "GET", "/tests", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var all []testsv1alpha1.Test
	require.NoError(t, jsonDecode(rec, &all))
	assert.Len(t, all, 2, "no ?namespace on a cluster-wide server lists everything")

	rec, _ = doRequest(t, h, "GET", "/tests?namespace=team-b", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var onlyB []testsv1alpha1.Test
	require.NoError(t, jsonDecode(rec, &onlyB))
	require.Len(t, onlyB, 1)
	assert.Equal(t, "b1", onlyB[0].Name)
}

func TestClusterWide_CreateRunNeedsNamespace(t *testing.T) {
	s, h := mkClusterWideServer(t)

	rec, _ := doRequest(t, h, "POST", "/runs", &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r1"},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "smoke"},
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec, _ = doRequest(t, h, "POST", "/runs?namespace=team-a", &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r1"},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "smoke"},
	})
	require.Equal(t, http.StatusCreated, rec.Code)
	var got testsv1alpha1.TestRun
	require.NoError(t, s.K8sClient.Get(t.Context(), types.NamespacedName{Namespace: "team-a", Name: "r1"}, &got))
}

func TestClusterWide_QueryAndPayloadNamespaceMustAgree(t *testing.T) {
	_, h := mkClusterWideServer(t)
	rec, body := doRequest(t, h, "POST", "/tests?namespace=team-a", &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "team-b"},
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, body["message"], "disagrees")
}

// A scoped server used to let metadata.namespace in the payload win, i.e.
// write into any namespace its RBAC reached (fixes.md #1).
func TestScoped_RejectsOtherNamespace(t *testing.T) {
	s, h := mkServer(t, mkTest("smoke", ManagedByUI))

	rec, body := doRequest(t, h, "POST", "/tests", &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "evil", Namespace: "kube-system"},
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, body["message"], "scoped to namespace")
	var leaked testsv1alpha1.Test
	err := s.K8sClient.Get(t.Context(), types.NamespacedName{Namespace: "kube-system", Name: "evil"}, &leaked)
	assert.Error(t, err, "nothing may be created outside the server's namespace")

	rec, _ = doRequest(t, h, "POST", "/runs", &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "kube-system"},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "smoke"},
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec, _ = doRequest(t, h, "GET", "/tests/smoke?namespace=other", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Naming the server's own namespace explicitly is fine.
	rec, _ = doRequest(t, h, "GET", "/tests/smoke?namespace=default", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// The store is cluster-wide; an archived row from another namespace must
// not be served through a lookup scoped to this one.
func TestGetRun_ArchivedRowFromOtherNamespaceIsHidden(t *testing.T) {
	s, _ := mkServer(t)
	s.Store = newFakeRunStore(store.Row{
		UID: testUID("foreign"), Name: "foreign", Namespace: "team-b",
		Phase: "passed", FinishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	rec, _ := doRequest(t, s.Handler(), "GET", "/runs/"+testUID("foreign"), nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// Same run name in two namespaces: each log stream must only see its own
// chunks. Before RunKeys, both runs wrote to kubetest-logs/<name>/.
func TestLogs_SameRunNameInTwoNamespacesAreIsolated(t *testing.T) {
	mk := func(ns string) *testsv1alpha1.TestRun {
		return &testsv1alpha1.TestRun{
			ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: ns, UID: types.UID(testUID(ns + "/nightly"))},
			Status:     testsv1alpha1.TestRunStatus{Phase: testsv1alpha1.PhasePassed},
		}
	}
	a, b := mk("team-a"), mk("team-b")
	c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(a, b).Build()
	up := storageFake()
	up.put(testBucket, storage.ForRun("team-a", string(a.UID)).LogChunk(0), []byte("from-a"))
	up.put(testBucket, storage.ForRun("team-b", string(b.UID)).LogChunk(0), []byte("from-b"))

	s := &Server{
		K8sClient: c, Downloader: up, Lister: up, Bucket: testBucket,
		LogPollInterval: 10 * time.Millisecond, LogPollDeadline: 50 * time.Millisecond,
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	read := func(ns string) string {
		url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/runs/nightly/logs?namespace=" + ns
		conn, _, err := websocket.Dial(t.Context(), url, nil)
		require.NoError(t, err)
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
		var got bytes.Buffer
		for {
			_, data, err := conn.Read(t.Context())
			if err != nil {
				return got.String()
			}
			got.Write(data)
		}
	}
	assert.Equal(t, "from-a", read("team-a"))
	assert.Equal(t, "from-b", read("team-b"))
}

// Unknown run → 404 before any WS upgrade; previously an unknown name
// silently streamed an empty log.
func TestLogs_UnknownRunIs404(t *testing.T) {
	s, _ := mkServer(t)
	s.Downloader, s.Lister = storageFake(), storageFake()
	rec, _ := doRequest(t, s.Handler(), "GET", "/runs/ghost/logs", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func jsonDecode(rec *httptest.ResponseRecorder, out any) error {
	return json.Unmarshal(rec.Body.Bytes(), out)
}
