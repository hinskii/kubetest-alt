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

package k6

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
)

// Summary is the (partial) shape of k6's --summary-export=summary.json output.
// We only decode the fields we surface — unknown fields are ignored, which
// keeps us forward-compatible across k6 versions.
//
// Real k6 output structure (abridged):
//
//	{
//	  "metrics": {
//	    "http_req_duration": {
//	      "avg": 200.3, "min": 10, "max": 900,
//	      "p(95)": 500,
//	      "thresholds": {"p(95)<500": {"ok": true}}
//	    },
//	    "iterations":  {"count": 100, "rate": 3.4},
//	    "http_reqs":   {"count": 500, "rate": 16.7},
//	    "vus":         {"max": 10},
//	    "checks":      {"passes": 100, "fails": 2}
//	  }
//	}
type Summary struct {
	Metrics map[string]Metric `json:"metrics"`
}

// Metric collapses the union of numeric fields k6 emits per metric. Most
// fields are only present on a subset of metrics; JSON's zero-value behavior
// leaves absent fields at 0, which is what our summarizer expects.
type Metric struct {
	Avg        float64              `json:"avg,omitempty"`
	Rate       float64              `json:"rate,omitempty"`
	Count      float64              `json:"count,omitempty"`
	Max        float64              `json:"max,omitempty"`
	P95        float64              `json:"p(95),omitempty"`
	Passes     float64              `json:"passes,omitempty"`
	Fails      float64              `json:"fails,omitempty"`
	Thresholds map[string]Threshold `json:"thresholds,omitempty"`
}

// Threshold reflects one threshold expression's post-run status. `ok=false`
// on any threshold is what triggers k6 exit code 99.
type Threshold struct {
	OK bool `json:"ok"`
}

// LoadSummary reads and parses the summary file. Returns a distinct
// notFoundError sentinel behavior so the Runner can differentiate "k6
// crashed before writing" from "we can't parse what k6 wrote".
func LoadSummary(path string) (*Summary, error) {
	// #nosec G304 -- path is Runner-computed from req.WorkingDir + fixed name.
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseSummary(b)
}

// ParseSummary decodes summary JSON. Malformed input returns an error rather
// than panicking — critical because the wrapper reaches this code path after
// the tool has already finished (possibly crashed mid-write).
func ParseSummary(b []byte) (*Summary, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("empty summary")
	}
	var s Summary
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse k6 summary: %w", err)
	}
	return &s, nil
}

// ExtractMetrics maps k6's summary into the flat plan-§11 metric names the
// operator UI charts on. Missing metrics are omitted rather than reported as
// zero — a real zero and "k6 didn't report" mean different things.
func ExtractMetrics(s *Summary) map[string]float64 {
	if s == nil {
		return nil
	}
	out := map[string]float64{}
	if v, ok := s.Metrics["http_req_duration"]; ok && v.P95 != 0 {
		out["p95_ms"] = v.P95
	}
	if v, ok := s.Metrics["http_reqs"]; ok && v.Rate != 0 {
		out["rps"] = v.Rate
	}
	if v, ok := s.Metrics["checks"]; ok {
		if v.Passes != 0 || v.Fails != 0 {
			out["checks_passed"] = v.Passes
			out["checks_total"] = v.Passes + v.Fails
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// FailedThresholds returns a sorted list of "metric: threshold" for every
// threshold that ended `ok=false`. Deterministic order because message text
// gets asserted in tests and shown to humans in the operator UI.
func FailedThresholds(s *Summary) []string {
	if s == nil {
		return nil
	}
	var out []string
	for metricName, metric := range s.Metrics {
		for tName, t := range metric.Thresholds {
			if !t.OK {
				out = append(out, metricName+": "+tName)
			}
		}
	}
	slices.Sort(out)
	return out
}
