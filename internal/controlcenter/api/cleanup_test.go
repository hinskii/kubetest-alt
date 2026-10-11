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

package api

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/cctest"
	"github.com/hinskii/kubetest-alt/internal/store"
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
	_, _, err = parse("mode", "range", "to", "tomorrow")
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

const cleanupAPI = smokePath + "/cleanup"

func TestCleanup_PreviewAndDelete(t *testing.T) {
	day := 24 * time.Hour
	other := agedRun("other-old", "uid-f9", 90*day)
	other.Spec.TestRef = "other"
	running := runOf("smoke-live", time.Now())
	running.Status.Phase, running.Status.FinishedAt = testsv1alpha1.PhaseRunning, nil
	w := newWorld(t, smokeTest(), other, running,
		agedRun("old-2", "uid-b2", 40*day), agedRun("old-1", "uid-b1", 60*day), agedRun("recent", "uid-b3", 5*day))

	assert.Equal(t, http.StatusForbidden, w.do(t, http.MethodGet, cleanupAPI+"?mode=older", viewer, nil).Code,
		"viewers can't even preview")

	rec := w.do(t, http.MethodGet, cleanupAPI+"?mode=older&days=30", developer, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeAs[CleanupPreview](t, rec)
	assert.Equal(t, "older", got.Mode)
	assert.Equal(t, 30, got.Days)
	require.Len(t, got.Matches, 2)
	assert.Equal(t, "old-1", got.Matches[0].Name, "oldest first")
	assert.Equal(t, "old-2", got.Matches[1].Name)
	assert.NotNil(t, got.Matches[0].FinishedAt)
	assert.Equal(t, MaxCleanup, got.Max)
	assert.Positive(t, got.Scanned)

	dflt := decodeAs[CleanupPreview](t, w.do(t, http.MethodGet, cleanupAPI, developer, nil))
	assert.Equal(t, DefaultOlderThanDays, dflt.Days, "older than 30 days by default")

	guard := w.do(t, http.MethodGet, cleanupAPI+"?mode=range", developer, nil)
	assert.Equal(t, http.StatusBadRequest, guard.Code)
	assert.Contains(t, errorOf(t, guard), "would match every run of this Test")
	swapped := decodeAs[CleanupPreview](t,
		w.do(t, http.MethodGet, cleanupAPI+"?mode=range&from=2099-01-02&to=2000-01-01", developer, nil))
	assert.Equal(t, "From was after To — swapped.", swapped.Note)
	assert.Equal(t, "2000-01-01", swapped.From)

	assert.Equal(t, http.StatusForbidden,
		w.do(t, http.MethodPost, cleanupAPI, developer, CleanupRequest{Runs: []string{"old-1"}}).Code, "only admins delete")

	rec = w.do(t, http.MethodPost, cleanupAPI, admin,
		CleanupRequest{Runs: []string{"old-1", "old-2", "old-1", "other-old", "smoke-live", " "}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	res := decodeAs[CleanupResult](t, rec)
	assert.Equal(t, CleanupResult{Deleted: 2, Requested: 4, Failures: []string{
		"other-old: not a finished run of this Test", "smoke-live: not a finished run of this Test",
	}}, res)

	gone := func(name string) bool {
		err := w.k8s.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: name}, &testsv1alpha1.TestRun{})
		return apierrors.IsNotFound(err)
	}
	assert.True(t, gone("old-1"))
	assert.True(t, gone("old-2"))
	assert.False(t, gone("recent"))
	assert.False(t, gone("other-old"), "another Test's run is never deleted")
	assert.False(t, gone("smoke-live"))

	none := w.do(t, http.MethodPost, cleanupAPI, admin, CleanupRequest{Runs: []string{}})
	assert.Equal(t, http.StatusBadRequest, none.Code)
	assert.Equal(t, "No runs selected — nothing deleted.", errorOf(t, none))
	tooMany := make([]string, MaxCleanup+1)
	for i := range tooMany {
		tooMany[i] = "r" + time.Duration(i).String()
	}
	assert.Equal(t, http.StatusBadRequest, w.do(t, http.MethodPost, cleanupAPI, admin, CleanupRequest{Runs: tooMany}).Code)
}

// The main case: old runs are usually only in run history. The API finds
// those by UID, so the preview and the delete must use it.
func TestCleanup_ArchivedRuns(t *testing.T) {
	const uid = "00000000-0000-0000-0000-0000000000a9"
	archive := &cctest.Archive{Rows: map[string]store.Row{uid: {UID: uid, Name: "gone-1", Namespace: "team-a",
		TestRef: "smoke", Phase: "failed", Source: "ui",
		FinishedAt: time.Now().Add(-50 * 24 * time.Hour).UTC().Truncate(time.Second), DurationMs: 1000}}}
	w := newRunWorld(t, archive, smokeTest())

	got := decodeAs[CleanupPreview](t, w.do(t, http.MethodGet, cleanupAPI+"?mode=older&days=30", admin, nil))
	require.Len(t, got.Matches, 1)
	assert.Equal(t, uid, got.Matches[0].ID, "archived runs go by UID")
	assert.Equal(t, "gone-1", got.Matches[0].Name)

	res := decodeAs[CleanupResult](t, w.do(t, http.MethodPost, cleanupAPI, admin, CleanupRequest{Runs: []string{uid}}))
	assert.Equal(t, CleanupResult{Deleted: 1, Requested: 1, Failures: []string{}}, res)
	assert.Empty(t, archive.Rows, "the history row is gone")
}
