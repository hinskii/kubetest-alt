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
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// StatsReader is the store's per-Test stats query (GET /stats/tests).
type StatsReader interface {
	TestStats(ctx context.Context, f store.StatsFilter) ([]store.TestStats, error)
}

const (
	// defaultStatsWindow and maxStatsWindow bound ?since=.
	defaultStatsWindow = 7 * 24 * time.Hour
	maxStatsWindow     = 90 * 24 * time.Hour
	// defaultRecentRuns is ?recent='s default.
	defaultRecentRuns = 10
)

// parseWindow reads a window like "24h", "7d" or "90m" (days on top of
// time.ParseDuration).
func parseWindow(v string) (time.Duration, bool) {
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.Atoi(days)
		return time.Duration(n) * 24 * time.Hour, err == nil && n > 0
	}
	d, err := time.ParseDuration(v)
	return d, err == nil && d > 0
}

// listTestStats serves GET /stats/tests: per Test, its finished runs over
// ?since= (default 7d, max 90d) — counts by phase, pass ratio, p50/p95 —
// and its ?recent= (default 10, max 30) newest runs.
func (s *Server) listTestStats(w http.ResponseWriter, r *http.Request) {
	if s.Stats == nil {
		writeError(w, http.StatusServiceUnavailable, ReasonServiceUnavail, "run history store is not configured")
		return
	}
	ns, err := s.listNamespace(r)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	window := defaultStatsWindow
	if v := r.URL.Query().Get("since"); v != "" {
		var ok bool
		if window, ok = parseWindow(v); !ok || window > maxStatsWindow {
			writeError(w, http.StatusBadRequest, ReasonBadRequest, "since: a duration up to 90d (24h, 7d, …)")
			return
		}
	}
	recent := defaultRecentRuns
	if v := r.URL.Query().Get("recent"); v != "" {
		if recent, err = strconv.Atoi(v); err != nil || recent < 1 || recent > store.MaxRecentRuns {
			writeError(w, http.StatusBadRequest, ReasonBadRequest, "recent: want 1-30")
			return
		}
	}
	now := s.Now()
	stats, err := s.Stats.TestStats(r.Context(), store.StatsFilter{Namespace: ns,
		Since: now.Add(-window), RecentSince: now.Add(-maxStatsWindow), Recent: recent})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	out := make([]apiclient.TestStats, 0, len(stats))
	for _, st := range stats {
		t := apiclient.TestStats{Namespace: st.Namespace, Test: st.TestRef,
			Total: st.Total, Passed: st.Passed, Failed: st.Failed, Errored: st.Errored, Aborted: st.Aborted,
			P50Ms: st.P50Ms, P95Ms: st.P95Ms, Recent: make([]apiclient.RecentRun, 0, len(st.Recent))}
		if verdicts := st.Passed + st.Failed + st.Errored; verdicts > 0 {
			ratio := float64(st.Passed) / float64(verdicts)
			t.PassRatio = &ratio
		}
		for _, rr := range st.Recent {
			t.Recent = append(t.Recent, apiclient.RecentRun{UID: rr.UID, Name: rr.Name, Phase: rr.Phase,
				DurationMs: rr.DurationMs, FinishedAt: rr.FinishedAt})
		}
		out = append(out, t)
	}
	writeJSON(w, http.StatusOK, out)
}
