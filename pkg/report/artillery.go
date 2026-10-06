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

// parseArtillery — reads the "aggregate" block of
// `artillery run --output report.json` (Artillery 2.x). Times are
// milliseconds.
//
// Artillery's own errors.* counters only cover transport failures (no
// response); an HTTP 404 is just http.codes.404. To match the shared
// definition — a failed request is no response OR status >= 400 — errors
// here = sum(errors.*) + sum(http.codes.4xx|5xx).

package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type report struct {
	Aggregate *struct {
		Counters  map[string]float64            `json:"counters"`
		Rates     map[string]float64            `json:"rates"`
		Summaries map[string]map[string]float64 `json:"summaries"`
	} `json:"aggregate"`
}

// Parse maps the aggregate block.
func parseArtillery(r io.Reader) (map[string]float64, error) {
	var rep report
	if err := json.NewDecoder(r).Decode(&rep); err != nil {
		return nil, fmt.Errorf("artillery report: %w", err)
	}
	if rep.Aggregate == nil {
		return nil, errors.New("artillery report: no aggregate block")
	}
	a := rep.Aggregate
	out := map[string]float64{}
	if v, ok := a.Counters["http.requests"]; ok {
		out[Requests] = v
	}
	errs, sawErrors := 0.0, false
	for k, v := range a.Counters {
		switch {
		case strings.HasPrefix(k, "errors."):
			errs += v
			sawErrors = true
		case strings.HasPrefix(k, "http.codes."):
			sawErrors = true
			if code, err := strconv.Atoi(strings.TrimPrefix(k, "http.codes.")); err == nil && code >= 400 {
				errs += v
			}
		}
	}
	if sawErrors {
		out[Errors] = errs
	}
	if v, ok := a.Rates["http.request_rate"]; ok {
		out[RPS] = v
	}
	rt := a.Summaries["http.response_time"]
	for field, key := range map[string]string{
		"mean": LatencyAvgMs, "min": LatencyMinMs, "median": LatencyMedMs,
		"p90": LatencyP90Ms, "p95": LatencyP95Ms, "p99": LatencyP99Ms, "max": LatencyMaxMs,
	} {
		if v, ok := rt[field]; ok {
			out[key] = v
		}
	}
	SetErrorRate(out)
	return out, nil
}
