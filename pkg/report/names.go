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

package report

// Metric names — the shared vocabulary (docs/metrics.md). Consumers render
// by key, never by tool, so every parser maps onto these.

// Request-level metrics, common to every load tool. An "error" is a
// request that got no response or a status >= 400 — whatever the tool
// itself calls a failure (Artillery, for one, doesn't count 404s).
const (
	Requests     = "requests"   // total requests sent
	Errors       = "errors"     // failed requests
	ErrorRate    = "error_rate" // errors / requests, in [0,1]
	RPS          = "rps"        // throughput, requests per second
	LatencyAvgMs = "latency_avg_ms"
	LatencyMinMs = "latency_min_ms"
	LatencyMedMs = "latency_med_ms"
	LatencyP90Ms = "latency_p90_ms"
	LatencyP95Ms = "latency_p95_ms"
	LatencyP99Ms = "latency_p99_ms"
	LatencyMaxMs = "latency_max_ms"
)

// k6-only.
const (
	ChecksPassed      = "checks_passed"
	ChecksFailed      = "checks_failed"
	Iterations        = "iterations"
	DataReceivedBytes = "data_received_bytes"
	DataSentBytes     = "data_sent_bytes"
	VUsMax            = "vus_max"
)

// ZAP: alert types found, by risk (all four always present, 0 when none).
const (
	AlertsHigh   = "alerts_high"
	AlertsMedium = "alerts_medium"
	AlertsLow    = "alerts_low"
	AlertsInfo   = "alerts_info"
	AlertsTotal  = "alerts_total"
)

// kubepug: API versions the manifests use that the target Kubernetes
// version deprecates or no longer serves, and the objects using them.
const (
	DeprecatedAPIs  = "deprecated_apis"
	DeletedAPIs     = "deleted_apis"
	AffectedObjects = "affected_objects"
)

// SetErrorRate fills ErrorRate from Requests/Errors when both are known
// and Requests > 0.
func SetErrorRate(m map[string]float64) {
	req, ok1 := m[Requests]
	errs, ok2 := m[Errors]
	if ok1 && ok2 && req > 0 {
		m[ErrorRate] = errs / req
	}
}
