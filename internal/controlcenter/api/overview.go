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
	"slices"
	"time"

	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// OverviewLimit is how many of a range's newest runs the overview reads
// (one page of the kubetest API); more, and it says it is capped.
const OverviewLimit = 500

// Ranges are the overview's timeframes.
var Ranges = map[string]struct {
	Span, Bucket time.Duration
}{
	"24h": {24 * time.Hour, time.Hour},
	"7d":  {7 * 24 * time.Hour, 24 * time.Hour},
	"30d": {30 * 24 * time.Hour, 24 * time.Hour},
}

// TestOverview is a Test's finished runs over a timeframe (step 20c): the
// tiles on its Overview tab, with buckets for their charts.
type TestOverview struct {
	Range   string           `json:"range" required:"true" enum:"24h,7d,30d"`
	Since   time.Time        `json:"since" required:"true"`
	Summary Summary          `json:"summary" required:"true"`
	P50Ms   int64            `json:"p50Ms,omitempty"`
	Buckets []OverviewBucket `json:"buckets" required:"true" nullable:"false"`
	// Capped: more runs finished in the range than were read; the numbers
	// are over the newest OverviewLimit.
	Capped bool `json:"capped,omitempty"`
}

// OverviewBucket is one hour or day of the range, oldest first.
type OverviewBucket struct {
	Start  time.Time `json:"start" required:"true"`
	Total  int       `json:"total" required:"true"`
	Passed int       `json:"passed" required:"true"`
	// Failed counts failed and error runs.
	Failed int   `json:"failed" required:"true"`
	AvgMs  int64 `json:"avgMs,omitempty"`
}

type overviewParams struct {
	Cluster   string `path:"cluster"`
	Namespace string `path:"ns"`
	Name      string `path:"name"`
	Range     string `query:"range" enum:"24h,7d,30d" description:"Default 7d."`
}

// overviewOf sums up runs (finished, any order) that ended in [since, now].
func overviewOf(rangeName string, runs []apiclient.Run, now time.Time) TestOverview {
	r := Ranges[rangeName]
	since := now.Add(-r.Span)
	start := since.Truncate(r.Bucket)
	out := TestOverview{Range: rangeName, Since: since, Buckets: []OverviewBucket{}}
	for t := start; !t.After(now); t = t.Add(r.Bucket) {
		out.Buckets = append(out.Buckets, OverviewBucket{Start: t})
	}
	var in []apiclient.Run
	sums := make([]int64, len(out.Buckets))
	counts := make([]int64, len(out.Buckets))
	var durations []int64
	for _, run := range runs {
		if run.FinishedAt == nil || run.FinishedAt.Before(since) || !Finished(run.Phase) {
			continue
		}
		in = append(in, run)
		i := int(run.FinishedAt.Sub(start) / r.Bucket)
		if i < 0 || i >= len(out.Buckets) {
			continue
		}
		b := &out.Buckets[i]
		b.Total++
		switch run.Phase {
		case phasePassed:
			b.Passed++
		case phaseFailed, phaseError:
			b.Failed++
		}
		if (run.Phase == phasePassed || run.Phase == phaseFailed) && run.DurationMs > 0 {
			sums[i] += run.DurationMs
			counts[i]++
			durations = append(durations, run.DurationMs)
		}
	}
	for i := range out.Buckets {
		if counts[i] > 0 {
			out.Buckets[i].AvgMs = sums[i] / counts[i]
		}
	}
	out.Summary = summarize(in)
	out.Summary.LastFailure = nil
	if len(durations) > 0 {
		slices.Sort(durations)
		out.P50Ms = durations[(len(durations)+1)/2-1]
	}
	return out
}

// overview serves a Test's overview for ?range= (default 7d).
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	rangeName := r.URL.Query().Get("range")
	if _, ok := Ranges[rangeName]; !ok {
		rangeName = "7d"
	}
	now := time.Now().UTC()
	since := now.Add(-Ranges[rangeName].Span)
	page, err := client.ListRuns(r.Context(), apiclient.ListRunsOptions{Namespace: r.PathValue("ns"),
		Test: r.PathValue("name"), Limit: OverviewLimit, FinishedAfter: &since})
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	out := overviewOf(rangeName, page.Runs, now)
	out.Capped = page.NextCursor != ""
	writeJSON(w, http.StatusOK, out)
}
