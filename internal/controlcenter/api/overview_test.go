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

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

func TestOverviewOf_BucketsAndTiles(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 30, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	runs := []apiclient.Run{
		{Phase: "passed", DurationMs: 1000, FinishedAt: ago(10 * time.Minute)},
		{Phase: "failed", DurationMs: 3000, FinishedAt: ago(30 * time.Minute)},
		{Phase: "error", DurationMs: 9000, FinishedAt: ago(3 * time.Hour)},
		{Phase: "aborted", FinishedAt: ago(2 * time.Hour)},
		{Phase: "passed", DurationMs: 2000, FinishedAt: ago(5 * time.Hour)},
		{Phase: "passed", DurationMs: 5000, FinishedAt: ago(30 * time.Hour), Name: "too old"},
		{Phase: "running"},
	}
	o := overviewOf("24h", runs, now)
	assert.Equal(t, now.Add(-24*time.Hour), o.Since)
	assert.Len(t, o.Buckets, 25, "hours from the truncated start through now")
	assert.Equal(t, 5, o.Summary.Finished, "the range's finished runs only")
	assert.Equal(t, "50%", o.Summary.PassRate, "2 passed of 4 verdicts")
	assert.Equal(t, int64(2000), o.Summary.AvgMs, "error and aborted runs don't count")
	assert.Equal(t, int64(2000), o.P50Ms)
	assert.Nil(t, o.Summary.LastFailure)
	last := o.Buckets[len(o.Buckets)-1]
	assert.Equal(t, OverviewBucket{Start: now.Truncate(time.Hour), Total: 2, Passed: 1, Failed: 1, AvgMs: 2000}, last)

	week := overviewOf("7d", runs, now)
	assert.Len(t, week.Buckets, 8)
	assert.Equal(t, 6, week.Summary.Finished)
	empty := overviewOf("30d", nil, now)
	assert.Empty(t, empty.Summary.PassRate)
	assert.Len(t, empty.Buckets, 31)
}

func TestOverview_FromTheAPI(t *testing.T) {
	w := newWorld(t, smokeTest(),
		finishedAtNow("smoke-1", "uid-o1", testsv1alpha1.PhasePassed, time.Hour),
		finishedAtNow("smoke-2", "uid-o2", testsv1alpha1.PhaseFailed, 2*time.Hour))
	rec := w.do(t, http.MethodGet, smokePath+"/overview?range=24h", viewer, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	o := decodeAs[TestOverview](t, rec)
	assert.Equal(t, "24h", o.Range)
	assert.Equal(t, 2, o.Summary.Finished)
	assert.Equal(t, "50%", o.Summary.PassRate)
	assert.False(t, o.Capped)

	assert.Equal(t, "7d", decodeAs[TestOverview](t, w.do(t, http.MethodGet, smokePath+"/overview?range=1y", viewer, nil)).Range,
		"unknown ranges fall back to a week")
}

// finishedAtNow is a finished run of smoke that ended ago before now.
func finishedAtNow(name, uid string, phase testsv1alpha1.Phase, ago time.Duration) *testsv1alpha1.TestRun {
	run := finishedAt(name, uid, phase, 0, nil, nil)
	started := metav1.NewTime(time.Now().Add(-ago - 30*time.Second))
	done := metav1.NewTime(time.Now().Add(-ago))
	run.Status.StartedAt, run.Status.FinishedAt = &started, &done
	return run
}
