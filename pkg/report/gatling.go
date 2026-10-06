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

// parseGatling — reads the global block of Gatling's HTML-report data,
// <results>/<simulation>-<epoch>/js/stats.json (Gatling 3.9). Times are
// milliseconds. percentiles1..4 follow gatling.conf's
// charting.indicators.percentile1..4, defaults 50/75/95/99 — the mapping
// below assumes the defaults; a custom gatling.conf shifts them.

package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type stat struct {
	Total *float64 `json:"total"`
	KO    *float64 `json:"ko"`
}

type stats struct {
	Stats map[string]json.RawMessage `json:"stats"`
}

var fields = map[string]string{
	"meanNumberOfRequestsPerSecond": RPS,
	"meanResponseTime":              LatencyAvgMs,
	"minResponseTime":               LatencyMinMs,
	"percentiles1":                  LatencyMedMs, // p50
	"percentiles3":                  LatencyP95Ms, // p95
	"percentiles4":                  LatencyP99Ms, // p99
	"maxResponseTime":               LatencyMaxMs,
}

// Parse maps the global "stats" block. Gatling has no p90 by default.
func parseGatling(r io.Reader) (map[string]float64, error) {
	var s stats
	if err := json.NewDecoder(r).Decode(&s); err != nil {
		return nil, fmt.Errorf("gatling stats: %w", err)
	}
	if len(s.Stats) == 0 {
		return nil, errors.New("gatling stats: no global stats block")
	}
	out := map[string]float64{}
	decode := func(name string) *stat {
		raw, ok := s.Stats[name]
		if !ok {
			return nil
		}
		var st stat
		if json.Unmarshal(raw, &st) != nil {
			return nil
		}
		return &st
	}
	if n := decode("numberOfRequests"); n != nil {
		if n.Total != nil {
			out[Requests] = *n.Total
		}
		if n.KO != nil {
			out[Errors] = *n.KO
		}
	}
	for field, key := range fields {
		if st := decode(field); st != nil && st.Total != nil {
			out[key] = *st.Total
		}
	}
	SetErrorRate(out)
	return out, nil
}
