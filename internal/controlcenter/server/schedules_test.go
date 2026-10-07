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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

func TestNextFires(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 30, 0, 0, time.UTC)
	got, err := nextFires("0 2 * * *", now, 3)
	require.NoError(t, err)
	assert.Equal(t, []time.Time{
		time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 9, 2, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC),
	}, got, "UTC without a zone")

	warsaw, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)
	got, err = nextFires("0 2 * * *", now.In(warsaw), 1)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC), got[0], "UTC whatever zone Control Center runs in")

	got, err = nextFires("CRON_TZ=Europe/Warsaw 0 2 * * *", now, 1)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC), got[0], "02:00 in Warsaw (CEST) is 00:00 UTC")

	_, err = nextFires("every night", now, 1)
	require.Error(t, err)
	_, err = nextFires("0 0 0 2 * *", now, 1)
	require.Error(t, err, "six fields (seconds) are not the operator's format")
}

func TestScheduledRuns_FutureQueuedOnly(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	later, sooner, past := now.Add(2*time.Hour), now.Add(time.Hour), now.Add(-time.Minute)
	got := scheduledRuns([]apiclient.Run{
		{Name: "later", Phase: "queued", NotBefore: &later},
		{Name: "running", Phase: "running", NotBefore: &sooner},
		{Name: "due", Phase: "queued", NotBefore: &past},
		{Name: "plain", Phase: "queued"},
		{Name: "cancelled", Phase: "queued", NotBefore: &sooner, Abort: &testsv1alpha1.AbortRequest{}},
		{Name: "sooner", Phase: "queued", NotBefore: &sooner},
	}, now)
	names := make([]string, 0, len(got))
	for _, r := range got {
		names = append(names, r.Name)
	}
	assert.Equal(t, []string{"sooner", "later"}, names)
}

func scheduledTest(name, ns, schedule string, managedByUI bool) *testsv1alpha1.Test {
	labels := map[string]string{"kubetest.io/tool": "k6"}
	if managedByUI {
		labels["app.kubernetes.io/managed-by"] = "ui"
	}
	return &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec: testsv1alpha1.TestSpec{Schedule: schedule,
			Container: testsv1alpha1.ContainerConfig{Image: "grafana/k6:1.4.0", Args: []string{"run", "s.js"}}},
	}
}

func waitingRun(name string, in time.Duration) *testsv1alpha1.TestRun {
	at := metav1.NewTime(time.Now().Add(in).Truncate(time.Second))
	return &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team-a", UID: types.UID("00000000-0000-0000-0000-00000000000" + name[len(name)-1:])},
		Spec: testsv1alpha1.TestRunSpec{TestRef: "smoke", Source: "ui", NotBefore: &at,
			Tags: map[string]string{"kubetest.io/created-by": developer}},
		Status: testsv1alpha1.TestRunStatus{Phase: testsv1alpha1.PhaseQueued},
	}
}

func TestSchedulesPage(t *testing.T) {
	w := newWorld(t, smokeTest(),
		scheduledTest("nightly", "team-a", "0 2 * * *", true),
		scheduledTest("weekly", "team-b", "CRON_TZ=Europe/Warsaw 0 6 * * 1", false),
		scheduledTest("broken", "team-b", "61 * * * *", false),
		waitingRun("later-2", 2*time.Hour), waitingRun("sooner-1", time.Hour))

	rec := w.get(t, "/clusters/dev/schedules", developer)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, "Recurring <span class=\"muted\">3</span>")
	assert.Contains(t, body, `>0 2 * * *</td>`)
	assert.Contains(t, body, "CRON_TZ=Europe/Warsaw 0 6 * * 1")
	assert.Contains(t, body, "invalid", "a schedule that can't fire is flagged")
	assert.Less(t, strings.Index(body, ">nightly<"), strings.Index(body, ">broken<"), "broken schedules go last")
	assert.Contains(t, body, "One-time <span class=\"muted\">2</span>")
	assert.Less(t, strings.Index(body, ">sooner-1<"), strings.Index(body, ">later-2<"), "soonest first")
	assert.Contains(t, body, `action="/clusters/dev/runs/team-a/sooner-1/abort"`)
	assert.Contains(t, body, `name="back" value="schedules"`)

	viewer := w.get(t, "/clusters/dev/schedules", "").Body.String()
	assert.NotContains(t, viewer, ">Cancel</button>", "viewers can't cancel")

	// Cancelling returns to the schedules page, nowhere else.
	rec = w.post(t, "/clusters/dev/runs/team-a/sooner-1/abort", developer,
		url.Values{"back": {"schedules"}, "message": {"cancelled before its start time"}})
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.True(t, strings.HasPrefix(rec.Header().Get("Location"), "/clusters/dev/schedules?notice="), rec.Header().Get("Location"))
	var got testsv1alpha1.TestRun
	require.NoError(t, w.k8s.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "sooner-1"}, &got))
	require.NotNil(t, got.Spec.Abort)
	assert.Equal(t, "cancelled before its start time", got.Spec.Abort.Message)
	rec = w.post(t, "/clusters/dev/runs/team-a/later-2/abort", developer, url.Values{"back": {"https://evil.example"}})
	assert.True(t, strings.HasPrefix(rec.Header().Get("Location"), "/clusters/dev/runs/team-a/later-2?"), "unknown back → the run")
}

func TestSetSchedule(t *testing.T) {
	w := newWorld(t, smokeTest(), gitopsTest())
	page := "/clusters/dev/tests/team-a/smoke"
	post := func(user, expr string) *http.Response {
		return w.post(t, page+"/schedule", user, url.Values{"schedule": {expr}}).Result()
	}
	stored := func() string {
		var got testsv1alpha1.Test
		require.NoError(t, w.k8s.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "smoke"}, &got))
		return got.Spec.Schedule
	}
	notice := func(resp *http.Response) string {
		u, err := url.Parse(resp.Header.Get("Location"))
		require.NoError(t, err)
		return u.Query().Get("notice")
	}

	assert.Equal(t, http.StatusForbidden, post("", "0 2 * * *").StatusCode, "viewers can't schedule")

	resp := post(developer, "  0   2 * * 1-5 ")
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Contains(t, notice(resp), "Schedule saved. Next run: ")
	assert.Equal(t, "0 2 * * 1-5", stored(), "whitespace normalized")

	body := w.get(t, page, developer).Body.String()
	assert.Contains(t, body, `Runs on <span class="mono">0 2 * * 1-5</span>`)
	assert.Contains(t, body, `value="0 2 * * 1-5"`)
	assert.Contains(t, body, "Remove schedule")

	resp = post(developer, "every night")
	assert.Contains(t, notice(resp), "Not a valid cron expression")
	assert.Equal(t, "0 2 * * 1-5", stored(), "an invalid expression changes nothing")

	resp = w.post(t, page+"/schedule", developer, url.Values{"schedule": {"0 2 * * 1-5"}, "remove": {"1"}}).Result()
	assert.Equal(t, "Schedule removed.", notice(resp), "the Remove button wins over the field")
	assert.Empty(t, stored())
	require.Equal(t, http.StatusSeeOther, post(developer, "0 3 * * *").StatusCode)
	assert.Equal(t, "Schedule removed.", notice(post(developer, "")), "an emptied field removes it too")
	assert.Empty(t, stored())

	locked := w.post(t, "/clusters/dev/tests/team-b/checkout/schedule", developer, url.Values{"schedule": {"0 2 * * *"}}).Result()
	assert.Equal(t, "This Test is managed in Git: change spec.schedule there.", notice(locked))
	gitops := w.get(t, "/clusters/dev/tests/team-b/checkout", developer).Body.String()
	assert.NotContains(t, gitops, "Save schedule", "Git-managed Tests have no schedule form")
}
