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

package server

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/views"
	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

func TestParseCleanup(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	parse := func(kv ...string) (cleanupCriteria, string, error) {
		v := url.Values{}
		for i := 0; i+1 < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return parseCleanup(v.Get, now)
	}

	c, _, err := parse("mode", "older")
	require.NoError(t, err)
	assert.Equal(t, 30, c.Days, "default age")
	assert.Equal(t, now.Add(-30*24*time.Hour), c.to)
	assert.True(t, c.from.IsZero())

	c, _, err = parse("mode", "older", "days", "7")
	require.NoError(t, err)
	assert.Equal(t, now.Add(-7*24*time.Hour), c.to)
	for _, bad := range []string{"0", "-1", "x", "3651"} {
		_, _, err = parse("mode", "older", "days", bad)
		require.Error(t, err, bad)
	}

	_, _, err = parse("mode", "range")
	require.ErrorIs(t, err, errNoBound, "the no-bound guard")
	_, _, err = parse("mode", "range", "from", "", "to", " ")
	require.ErrorIs(t, err, errNoBound)
	_, _, err = parse("mode", "range", "from", "07.10.2026")
	require.ErrorContains(t, err, "not a date")

	c, note, err := parse("mode", "range", "from", "2026-10-05", "to", "2026-10-01")
	require.NoError(t, err)
	assert.Equal(t, "From was after To — swapped.", note)
	assert.Equal(t, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), c.from)
	assert.Equal(t, time.Date(2026, 10, 5, 23, 59, 59, 999999999, time.UTC), c.to, "To covers the whole day")

	c, _, err = parse("mode", "range", "to", "2026-09-30")
	require.NoError(t, err)
	assert.True(t, c.from.IsZero(), "one bound is enough")
}

// agedRun is a finished smoke run that finished age ago.
func agedRun(name, uid string, age time.Duration) *testsv1alpha1.TestRun {
	done := metav1.NewTime(time.Now().Add(-age).Truncate(time.Second))
	started := metav1.NewTime(done.Add(-time.Minute))
	return &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team-a", UID: types.UID(uid)},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "smoke", Source: "ui"},
		Status: testsv1alpha1.TestRunStatus{Phase: testsv1alpha1.PhasePassed, StartedAt: &started, FinishedAt: &done,
			DurationMs: 60000},
	}
}

func TestCleanup_PreviewAndDelete(t *testing.T) {
	day := 24 * time.Hour
	other := agedRun("other-old", "00000000-0000-0000-0000-0000000000f9", 90*day)
	other.Spec.TestRef = "other"
	w := newWorld(t, smokeTest(), other,
		agedRun("old-2", "00000000-0000-0000-0000-0000000000b2", 40*day),
		agedRun("old-1", "00000000-0000-0000-0000-0000000000b1", 60*day),
		agedRun("recent", "00000000-0000-0000-0000-0000000000b3", 5*day),
		runOf("smoke-live", testsv1alpha1.PhaseRunning))
	page := "/clusters/dev/tests/team-a/smoke/cleanup"

	assert.Equal(t, http.StatusForbidden, w.get(t, page, "").Code, "viewers can't even preview")
	form := w.get(t, page, developer).Body.String()
	assert.Contains(t, form, `name="days"`)
	assert.NotContains(t, form, "Matching runs", "no preview before the form is sent")

	body := w.get(t, page+"?mode=older&days=30", developer).Body.String()
	assert.Contains(t, body, `Matching runs <span class="muted">2</span>`)
	assert.Less(t, strings.Index(body, ">old-1<"), strings.Index(body, ">old-2<"), "oldest first")
	assert.NotContains(t, body, ">recent<")
	assert.NotContains(t, body, ">smoke-live<", "running runs never match")
	assert.Contains(t, body, "Only admins can delete runs.")
	assert.Contains(t, body, `value="old-1" checked aria-label="delete old-1" disabled`)

	guard := w.get(t, page+"?mode=range", developer).Body.String()
	assert.Contains(t, guard, "would match every run of this Test")
	assert.NotContains(t, guard, "Matching runs")

	adminView := w.get(t, page+"?mode=older&days=30", admin).Body.String()
	assert.Contains(t, adminView, "Delete selected runs")

	assert.Equal(t, http.StatusForbidden, w.post(t, page, developer, url.Values{"run": {"old-1"}}).Code, "only admins delete")

	rec := w.post(t, page, admin, url.Values{"run": {"old-1", "old-2", "old-1", "other-old", "smoke-live"}})
	require.Equal(t, http.StatusSeeOther, rec.Code)
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, page, loc.Path)
	notice := loc.Query().Get("notice")
	assert.Contains(t, notice, "Deleted 2 of 4 runs.")
	assert.Contains(t, notice, "other-old: not a finished run of this Test")
	assert.Contains(t, notice, "smoke-live: not a finished run of this Test")

	gone := func(name string) bool {
		var run testsv1alpha1.TestRun
		err := w.k8s.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: name}, &run)
		return apierrors.IsNotFound(err)
	}
	assert.True(t, gone("old-1"))
	assert.True(t, gone("old-2"))
	assert.False(t, gone("recent"))
	assert.False(t, gone("other-old"), "another Test's run is never deleted")
	assert.False(t, gone("smoke-live"))

	none := w.post(t, page, admin, url.Values{})
	assert.Contains(t, none.Header().Get("Location"), "nothing+deleted")
}

// archiveStore is run history for the real API server: rows whose TestRun
// is gone. Paging is not modelled (every row fits on one page).
type archiveStore struct {
	mu   sync.Mutex
	rows map[string]store.Row
}

func (a *archiveStore) Get(_ context.Context, uid string) (*store.Row, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.rows[uid]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &r, nil
}

func (a *archiveStore) GetByName(_ context.Context, namespace, name string) (*store.Row, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, r := range a.rows {
		if r.Namespace == namespace && r.Name == name {
			return &r, nil
		}
	}
	return nil, store.ErrNotFound
}

func (a *archiveStore) List(_ context.Context, f store.Filter, p store.Page) ([]store.Row, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p.After != "" {
		return nil, nil
	}
	var out []store.Row
	for _, r := range a.rows {
		if (f.TestRef == "" || r.TestRef == f.TestRef) && (f.Namespace == "" || r.Namespace == f.Namespace) &&
			(f.Phase == "" || r.Phase == f.Phase) {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(x, y store.Row) int { return y.FinishedAt.Compare(x.FinishedAt) })
	return out, nil
}

func (a *archiveStore) Delete(_ context.Context, uid string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.rows[uid]; !ok {
		return store.ErrNotFound
	}
	delete(a.rows, uid)
	return nil
}

// The main case: old runs are usually only in run history. The API finds
// those by UID, so the preview, the links and the delete must use it.
func TestCleanup_ArchivedRuns(t *testing.T) {
	day := 24 * time.Hour
	archived := func(name, uid string, age time.Duration) store.Row {
		return store.Row{UID: uid, Name: name, Namespace: "team-a", TestRef: "smoke", Phase: "failed",
			Source: "ui", FinishedAt: time.Now().Add(-age).UTC().Truncate(time.Second), DurationMs: 1000}
	}
	const uid = "00000000-0000-0000-0000-0000000000a9"
	archive := &archiveStore{rows: map[string]store.Row{uid: archived("gone-1", uid, 50*day)}}
	w := newWorldWithArchive(t, archive, smokeTest())
	page := "/clusters/dev/tests/team-a/smoke/cleanup"

	body := w.get(t, page+"?mode=older&days=30", admin).Body.String()
	assert.Contains(t, body, `href="/clusters/dev/runs/team-a/`+uid+`">gone-1</a>`, "archived runs link by UID")
	assert.Contains(t, body, `value="`+uid+`" checked`)

	rec := w.post(t, page, admin, url.Values{"run": {uid}})
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "Deleted 1 of 1 runs.", loc.Query().Get("notice"))
	assert.Empty(t, archive.rows, "the history row is gone")
}

func TestRunID(t *testing.T) {
	assert.Equal(t, "smoke-1", views.RunID(apiclient.Run{Name: "smoke-1", UID: "u", Origin: "cluster"}))
	assert.Equal(t, "u", views.RunID(apiclient.Run{Name: "smoke-1", UID: "u", Origin: "archive"}))
}
