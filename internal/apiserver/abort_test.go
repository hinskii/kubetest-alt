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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/store"
)

func liveRun(name string, phase testsv1alpha1.Phase) *testsv1alpha1.TestRun {
	return &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(testUID(name))},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "t"},
		Status:     testsv1alpha1.TestRunStatus{Phase: phase},
	}
}

func postAbort(t *testing.T, h http.Handler, target, user, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		req.Header.Set(HeaderUser, user)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAbort_SetsSpecAbortWithRequester(t *testing.T) {
	s, h := mkServer(t, liveRun("r1", testsv1alpha1.PhaseRunning))

	rec := postAbort(t, h, "/runs/r1/abort", "alice@example.com", `{"message":"wrong params"}`)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	var got testsv1alpha1.TestRun
	require.NoError(t, s.K8sClient.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "r1"}, &got))
	require.NotNil(t, got.Spec.Abort)
	assert.Equal(t, testsv1alpha1.AbortReasonUser, got.Spec.Abort.Reason)
	assert.Equal(t, "alice@example.com", got.Spec.Abort.RequestedBy)
	assert.Equal(t, "wrong params", got.Spec.Abort.Message)
}

func TestAbort_NoBodyIsFine(t *testing.T) {
	_, h := mkServer(t, liveRun("r1", testsv1alpha1.PhaseQueued))
	rec := postAbort(t, h, "/runs/r1/abort", "", "")
	assert.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
}

// Second request must not rewrite the first one's requester — spec.abort
// is one-way and the webhook would reject a rewrite anyway.
func TestAbort_IsIdempotent(t *testing.T) {
	s, h := mkServer(t, liveRun("r1", testsv1alpha1.PhaseRunning))
	require.Equal(t, http.StatusAccepted, postAbort(t, h, "/runs/r1/abort", "first@x", "").Code)
	require.Equal(t, http.StatusAccepted, postAbort(t, h, "/runs/r1/abort", "second@x", "").Code)

	var got testsv1alpha1.TestRun
	require.NoError(t, s.K8sClient.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "r1"}, &got))
	assert.Equal(t, "first@x", got.Spec.Abort.RequestedBy)
}

func TestAbort_TerminalRunIsConflict(t *testing.T) {
	_, h := mkServer(t, liveRun("done", testsv1alpha1.PhasePassed))
	rec := postAbort(t, h, "/runs/done/abort", "", "")
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "already finished")
}

func TestAbort_ArchivedRunIsConflict(t *testing.T) {
	s, _ := mkServer(t)
	s.Store = newFakeRunStore(store.Row{
		UID: testUID("old"), Name: "old", Namespace: "default", Phase: "passed",
		FinishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	rec := postAbort(t, s.Handler(), "/runs/"+testUID("old")+"/abort", "", "")
	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestAbort_UnknownRunIs404(t *testing.T) {
	_, h := mkServer(t)
	assert.Equal(t, http.StatusNotFound, postAbort(t, h, "/runs/ghost/abort", "", "").Code)
}

func TestAbort_ClusterWideNeedsNamespace(t *testing.T) {
	r := liveRun("r1", testsv1alpha1.PhaseRunning)
	r.Namespace = "team-a"
	_, h := mkClusterWideServer(t, r)
	assert.Equal(t, http.StatusBadRequest, postAbort(t, h, "/runs/r1/abort", "", "").Code)
	assert.Equal(t, http.StatusAccepted, postAbort(t, h, "/runs/r1/abort?namespace=team-a", "", "").Code)
}

func TestAbort_RejectsOversizedInput(t *testing.T) {
	_, h := mkServer(t, liveRun("r1", testsv1alpha1.PhaseRunning))
	long := `{"message":"` + strings.Repeat("x", maxAbortMessageLen+1) + `"}`
	assert.Equal(t, http.StatusBadRequest, postAbort(t, h, "/runs/r1/abort", "", long).Code)
	assert.Equal(t, http.StatusBadRequest, postAbort(t, h, "/runs/r1/abort", "", `{not json`).Code)
}

func TestRequestUser_TrimsAndCaps(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderUser, "  bob@example.com  ")
	assert.Equal(t, "bob@example.com", requestUser(req))
	req.Header.Set(HeaderUser, strings.Repeat("a", maxUserLen+10))
	assert.Len(t, requestUser(req), maxUserLen)
}
