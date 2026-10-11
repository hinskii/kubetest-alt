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
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/cctest"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

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
	labels := map[string]string{LabelTool: "k6"}
	if managedByUI {
		labels[LabelManagedBy] = ManagedByUI
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
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team-a", UID: types.UID("uid-" + name)},
		Spec: testsv1alpha1.TestRunSpec{TestRef: "smoke", Source: "ui", NotBefore: &at,
			Tags: map[string]string{TagCreatedBy: cctest.Developer}},
		Status: testsv1alpha1.TestRunStatus{Phase: testsv1alpha1.PhaseQueued},
	}
}

func TestListSchedules(t *testing.T) {
	w := newWorld(t, smokeTest(),
		scheduledTest("nightly", "team-a", "0 2 * * *", true),
		scheduledTest("weekly", "team-b", "CRON_TZ=Europe/Warsaw 0 6 * * 1", false),
		scheduledTest("broken", "team-b", "61 * * * *", false),
		waitingRun("later-2", 2*time.Hour), waitingRun("sooner-1", time.Hour))

	rec := w.do(t, http.MethodGet, "/api/v1/clusters/dev/schedules", viewer, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeAs[ScheduleList](t, rec)

	require.Len(t, got.Recurring, 3)
	assert.Equal(t, "nightly", got.Recurring[0].Name, "soonest next fire first")
	assert.False(t, got.Recurring[0].Locked)
	assert.Len(t, got.Recurring[0].Schedule.Next, NextFiresShown)
	assert.Equal(t, "CRON_TZ=Europe/Warsaw 0 6 * * 1", got.Recurring[1].Schedule.Expr)
	assert.True(t, got.Recurring[1].Locked)
	assert.Equal(t, "broken", got.Recurring[2].Name, "a schedule that never fires goes last")
	assert.NotEmpty(t, got.Recurring[2].Schedule.Error)

	require.Len(t, got.OneTime, 2)
	assert.Equal(t, "sooner-1", got.OneTime[0].Name, "soonest first")
	assert.Equal(t, "smoke", got.OneTime[0].TestRef)
	assert.Equal(t, cctest.Developer, got.OneTime[0].CreatedBy)
	assert.Empty(t, got.OneTimeError)
}
