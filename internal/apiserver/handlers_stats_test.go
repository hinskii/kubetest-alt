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
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

type fakeStats struct {
	got  store.StatsFilter
	rows []store.TestStats
}

func (f *fakeStats) TestStats(_ context.Context, sf store.StatsFilter) ([]store.TestStats, error) {
	f.got = sf
	return f.rows, nil
}

func TestListTestStats(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	fin := now.Add(-time.Hour)
	stats := &fakeStats{rows: []store.TestStats{
		{Namespace: "default", TestRef: "smoke", Total: 5, Passed: 3, Failed: 1, Errored: 0, Aborted: 1,
			P50Ms: 1200, P95Ms: 3400, Recent: []store.RecentRun{{UID: "u1", Name: "smoke-1", Phase: "passed",
				DurationMs: 1200, FinishedAt: fin}}},
		{Namespace: "default", TestRef: "idle", Recent: []store.RecentRun{}},
	}}
	s, _ := mkServer(t)
	s.Stats, s.Now = stats, func() time.Time { return now }
	h := s.Handler()

	rec := get(t, h, "/stats/tests?since=30d&recent=5")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out []apiclient.TestStats
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out, 2)
	assert.Equal(t, "smoke", out[0].Test)
	require.NotNil(t, out[0].PassRatio)
	assert.InDelta(t, 0.75, *out[0].PassRatio, 1e-9, "passed of passed+failed+error")
	assert.Equal(t, int64(3400), out[0].P95Ms)
	require.Len(t, out[0].Recent, 1)
	assert.Equal(t, "smoke-1", out[0].Recent[0].Name)
	assert.Nil(t, out[1].PassRatio, "no verdict, no ratio")
	assert.NotNil(t, out[1].Recent, "an empty list, not null")

	assert.Equal(t, "default", stats.got.Namespace, "the server's namespace")
	assert.Equal(t, now.Add(-30*24*time.Hour), stats.got.Since)
	assert.Equal(t, now.Add(-90*24*time.Hour), stats.got.RecentSince)
	assert.Equal(t, 5, stats.got.Recent)

	require.Equal(t, http.StatusOK, get(t, h, "/stats/tests").Code)
	assert.Equal(t, now.Add(-7*24*time.Hour), stats.got.Since, "default window 7d")
	assert.Equal(t, 10, stats.got.Recent, "default 10 recent runs")
	require.Equal(t, http.StatusOK, get(t, h, "/stats/tests?since=24h").Code)
	assert.Equal(t, now.Add(-24*time.Hour), stats.got.Since)

	for _, bad := range []string{"since=91d", "since=0d", "since=x", "since=-1h", "recent=0", "recent=31", "recent=x"} {
		assert.Equal(t, http.StatusBadRequest, get(t, h, "/stats/tests?"+bad).Code, bad)
	}
	assert.Equal(t, http.StatusBadRequest, get(t, h, "/stats/tests?namespace=other").Code, "a scoped server")
}

func TestListTestStats_NoStore(t *testing.T) {
	_, h := mkServer(t)
	assert.Equal(t, http.StatusServiceUnavailable, get(t, h, "/stats/tests").Code)
}
