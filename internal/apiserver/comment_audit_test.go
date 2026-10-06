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
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

func (f *fakeRunStore) SetComment(_ context.Context, uid string, c *store.Comment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[uid]
	if !ok {
		return store.ErrNotFound
	}
	r.Comment = c
	f.rows[uid] = r
	return nil
}

func (f *fakeRunStore) AppendAudit(_ context.Context, e store.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	e.ID = int64(len(f.audit) + 1)
	f.audit = append(f.audit, e)
	return nil
}

func (f *fakeRunStore) ListAudit(_ context.Context, flt store.AuditFilter, limit int) ([]store.AuditEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.AuditEntry{}
	for i := len(f.audit) - 1; i >= 0 && len(out) < limit; i-- {
		e := f.audit[i]
		if (flt.Namespace == "" || e.Namespace == flt.Namespace) && (flt.Actor == "" || e.Actor == flt.Actor) &&
			(flt.Action == "" || e.Action == flt.Action) && (flt.BeforeID == 0 || e.ID < flt.BeforeID) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeRunStore) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.audit))
	for _, e := range f.audit {
		out = append(out, e.Action+" "+e.Target+" by "+e.Actor)
	}
	return out
}

func TestComment_OnArchivedRun(t *testing.T) {
	s, _, rs := mkStorageServer(t, []store.Row{archivedRow("old")})
	s.Now = func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) }
	c := mkContractClient(t, s).AsUser("alice@example.com")
	ctx := t.Context()
	id := testUID("old")

	got, err := c.SetComment(ctx, "", id, "  flaky DNS  ")
	require.NoError(t, err)
	assert.Equal(t, &apiclient.Comment{Text: "flaky DNS", By: "alice@example.com", At: s.Now()}, got)

	run, err := c.GetRun(ctx, "", id)
	require.NoError(t, err)
	assert.Equal(t, got, run.Comment)
	page, err := c.ListRuns(ctx, apiclient.ListRunsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Runs, 1)
	assert.Equal(t, got, page.Runs[0].Comment)

	require.NoError(t, c.DeleteComment(ctx, "", id))
	require.NoError(t, c.DeleteComment(ctx, "", id), "idempotent")
	run, err = c.GetRun(ctx, "", id)
	require.NoError(t, err)
	assert.Nil(t, run.Comment)

	assert.Equal(t, []string{
		"run.comment old by alice@example.com",
		"run.uncomment old by alice@example.com",
		"run.uncomment old by alice@example.com",
	}, rs.actions())
}

// A finished run whose CR still exists: the envelope comes from the
// cluster, the comment from history — both GET /runs/{name} and GET /runs
// must show it.
func TestComment_OnTerminalCRShownFromHistory(t *testing.T) {
	cr := liveRun("done", testsv1alpha1.PhasePassed)
	fin := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	cr.Status.FinishedAt = &fin
	row := archivedRow("done")
	s, _, _ := mkStorageServer(t, []store.Row{row}, cr)
	c := mkContractClient(t, s)
	ctx := t.Context()

	_, err := c.SetComment(ctx, "", "done", "expected failure")
	require.NoError(t, err)
	run, err := c.GetRun(ctx, "", "done")
	require.NoError(t, err)
	assert.Equal(t, apiclient.OriginCluster, run.Origin)
	require.NotNil(t, run.Comment)
	assert.Equal(t, "expected failure", run.Comment.Text)

	page, err := c.ListRuns(ctx, apiclient.ListRunsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Runs, 1)
	require.NotNil(t, page.Runs[0].Comment)
	assert.Equal(t, apiclient.OriginCluster, page.Runs[0].Origin)
}

func TestComment_Rejections(t *testing.T) {
	s, _, _ := mkStorageServer(t, nil,
		liveRun("live", testsv1alpha1.PhaseRunning), liveRun("unsaved", testsv1alpha1.PhaseFailed))
	c := mkContractClient(t, s)
	ctx := t.Context()

	_, err := c.SetComment(ctx, "", "live", "x")
	assert.True(t, apiclient.IsConflict(err), err)
	assert.ErrorContains(t, err, "still running")
	assert.True(t, apiclient.IsConflict(c.DeleteComment(ctx, "", "live")))

	_, err = c.SetComment(ctx, "", "unsaved", "x")
	assert.True(t, apiclient.IsConflict(err), err)
	assert.ErrorContains(t, err, "not in run history yet")

	_, err = c.SetComment(ctx, "", "unsaved", "   ")
	assert.ErrorContains(t, err, "text is required")
	_, err = c.SetComment(ctx, "", "unsaved", strings.Repeat("ż", apiclient.MaxCommentLen+1))
	assert.ErrorContains(t, err, "longer than 500")
	_, err = c.SetComment(ctx, "", "nope", "x")
	assert.True(t, apiclient.IsNotFound(err), err)

	s.Commenter = nil
	_, err = c.SetComment(ctx, "", "unsaved", "x")
	var apiErr *apiclient.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusServiceUnavailable, apiErr.Status)
}

// Every mutating action is audited with the caller from X-Kubetest-User.
func TestAudit_RecordsActionsAndPages(t *testing.T) {
	s, _, rs := mkStorageServer(t, []store.Row{archivedRow("old")}, liveRun("r1", testsv1alpha1.PhaseRunning))
	c := mkContractClient(t, s).AsUser("bob@example.com")
	ctx := t.Context()

	at := metav1.NewTime(time.Date(2026, 10, 7, 6, 0, 0, 0, time.UTC))
	_, err := c.CreateRun(ctx, "", &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly"},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "smoke", NotBefore: &at},
	})
	require.NoError(t, err)
	_, err = c.AbortRun(ctx, "", "r1", "wrong env")
	require.NoError(t, err)
	_, err = c.AbortRun(ctx, "", "r1", "again")
	require.NoError(t, err, "repeat abort is a no-op and not audited again")
	require.NoError(t, c.DeleteRun(ctx, "", testUID("old")))
	_, err = c.CreateRun(ctx, "", &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "bad"}, Spec: testsv1alpha1.TestRunSpec{TestRef: "x", Source: "cron"},
	})
	require.Error(t, err, "failed actions are not audited")

	assert.Equal(t, []string{
		"run.create nightly by bob@example.com",
		"run.abort r1 by bob@example.com",
		"run.delete old by bob@example.com",
	}, rs.actions())
	assert.Equal(t, map[string]string{"test": "smoke", "uid": rs.audit[0].Details["uid"], "notBefore": "2026-10-07T06:00:00Z"},
		rs.audit[0].Details)
	assert.Equal(t, "wrong env", rs.audit[1].Details["message"])

	page, err := c.ListAudit(ctx, apiclient.ListAuditOptions{Limit: 2})
	require.NoError(t, err)
	require.Len(t, page.Entries, 2)
	assert.Equal(t, apiclient.ActionRunDelete, page.Entries[0].Action, "newest first")
	require.NotEmpty(t, page.NextCursor)
	page, err = c.ListAudit(ctx, apiclient.ListAuditOptions{Limit: 2, Before: page.NextCursor})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, apiclient.ActionRunCreate, page.Entries[0].Action)
	assert.Empty(t, page.NextCursor)

	page, err = c.ListAudit(ctx, apiclient.ListAuditOptions{Action: apiclient.ActionRunAbort})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, "r1", page.Entries[0].Target)

	_, err = c.ListAudit(ctx, apiclient.ListAuditOptions{Before: "x"})
	assert.ErrorContains(t, err, "before")
}

func TestAudit_TestDefinitionChanges(t *testing.T) {
	s, _, rs := mkStorageServer(t, nil)
	h := s.Handler()
	req := func(method, target, body string) int {
		t.Helper()
		r, rec := httptest.NewRequest(method, target, strings.NewReader(body)), httptest.NewRecorder()
		r.Header.Set(HeaderUser, "carol@example.com")
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	require.Equal(t, http.StatusCreated, req(http.MethodPost, "/tests",
		`{"metadata":{"name":"t1"},"spec":{"container":{"image":"busybox","command":["true"]}}}`))
	require.Equal(t, http.StatusOK, req(http.MethodPatch, "/tests/t1", `{"metadata":{"labels":{"team":"sre"}}}`))
	require.Equal(t, http.StatusNoContent, req(http.MethodDelete, "/tests/t1", ""))
	assert.Equal(t, []string{
		"test.create t1 by carol@example.com",
		"test.update t1 by carol@example.com",
		"test.delete t1 by carol@example.com",
	}, rs.actions())
}

func TestAudit_NoStore(t *testing.T) {
	s, _ := mkServer(t)
	c := mkContractClient(t, s)
	_, err := c.ListAudit(t.Context(), apiclient.ListAuditOptions{})
	var apiErr *apiclient.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusServiceUnavailable, apiErr.Status)
	// Actions still work without an audit log.
	_, err = c.CreateRun(t.Context(), "", &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r"}, Spec: testsv1alpha1.TestRunSpec{TestRef: "t"},
	})
	require.NoError(t, err)
}

// GET /runs shows a scheduled run's start time.
func TestRuns_NotBeforeInEnvelope(t *testing.T) {
	cr := liveRun("later", testsv1alpha1.PhaseQueued)
	at := metav1.NewTime(time.Date(2026, 10, 7, 6, 0, 0, 0, time.UTC))
	cr.Spec.NotBefore = &at
	s, _ := mkServer(t, cr)
	run, err := mkContractClient(t, s).GetRun(t.Context(), "", "later")
	require.NoError(t, err)
	require.NotNil(t, run.NotBefore)
	assert.Equal(t, at.UTC(), *run.NotBefore)
}
