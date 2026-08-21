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

// Package metrics defines every Prometheus metric this operator emits.
// Centralized so the "labels must be a hard whitelist" invariant can be
// enforced by ONE test (TestAllMetricLabels_WhitelistOnly) rather than
// scattered across packages.
//
// Bounded labels only:
//   - tool     — from kubetest.io/tool, mapped to a catalog whitelist
//     (unknown → "other"). Never the free-text label value.
//   - phase    — from testsv1alpha1.Phase enum.
//   - source   — from TestRun.spec.source enum.
//   - outcome  — trigger evaluation result (fired/expired/skipped-*).
//   - code_class — "2xx"/"3xx"/"4xx"/"5xx"/"err" for webhook deliveries.
//
// NEVER labels: run name, test name, namespace, UID, endpoint URL, any
// user-supplied string. These have unbounded cardinality and would
// blow up any TSDB scraping /metrics.
package metrics

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// Namespace + subsystem — every metric name is namespaced under
// kubetest_ so a shared /metrics scrape stays unambiguous.
const (
	metricsNamespace = "kubetest"
)

// KnownTools is the catalog whitelist. Any label value not in this set
// is folded to "other" by NormalizeTool. Kept in sync with
// config/templates/ by convention — this list is checked at code-review
// time (add a new template → add its tool here).
var KnownTools = map[string]struct{}{
	// Session A
	toolK6:         {},
	toolCypress:    {},
	toolNewman:     {},
	toolJmeter:     {},
	toolLocust:     {},
	toolPlaywright: {},
	toolPytest:     {},
	// Session B
	toolGatling:     {},
	toolGradle:      {},
	toolMaven:       {},
	toolArtillery:   {},
	toolSoapui:      {},
	toolZapBaseline: {},
	toolCucumber:    {},
	toolKubepug:     {},
	// Raw-Test tools documented in docs/examples/.
	toolCurl: {},
	// Step 17: composite parent runs. A composite Test has no single
	// tool identity — its label is `kubetest.io/tool=composite` (set
	// by the user OR the reconciler when it detects spec.steps). This
	// keeps runs_total{tool=composite} distinguishable from bare
	// "other" (which is genuinely unknown, not "this is a scenario").
	toolComposite: {},
}

// ToolOther is the sink value for unknown tool labels. Kept as a const
// so tests and callers all read the same identifier.
const ToolOther = "other"

// Tool label constant strings — centralized so goconst stays quiet and
// a rename in one place propagates cleanly. Every entry MUST appear in
// KnownTools below (the two lists are enforced consistent by
// TestKnownToolsMatchConsts).
const (
	toolK6          = "k6"
	toolCypress     = "cypress"
	toolNewman      = "newman"
	toolJmeter      = "jmeter"
	toolLocust      = "locust"
	toolPlaywright  = "playwright"
	toolPytest      = "pytest"
	toolGatling     = "gatling"
	toolGradle      = "gradle"
	toolMaven       = "maven"
	toolArtillery   = "artillery"
	toolSoapui      = "soapui"
	toolZapBaseline = "zap-baseline"
	toolCucumber    = "cucumber"
	toolKubepug     = "kubepug"
	toolCurl        = "curl"
	toolComposite   = "composite"
)

// ToolComposite is the exported value for step-17 composite parents.
// Kept as a package-level const so the reconciler labels children +
// scrape assertions can reference the same string without hard-coding
// the literal.
const ToolComposite = "composite"

// Source label constants.
const (
	sourceAPI     = "api"
	sourceUI      = "ui"
	sourceCLI     = "cli"
	sourceCron    = "cron"
	sourceTrigger = "trigger"
	sourceGitops  = "gitops"
)

// CodeClass constants.
const (
	codeClassErr = "err"
	codeClass2xx = "2xx"
	codeClass3xx = "3xx"
	codeClass4xx = "4xx"
	codeClass5xx = "5xx"
)

// Label name constants.
const (
	labelTool      = "tool"
	labelPhase     = "phase"
	labelSource    = "source"
	labelOutcome   = "outcome"
	labelCodeClass = "code_class"
)

// NormalizeTool folds a raw tool label to either its catalog name or
// ToolOther. Whitespace trimmed + lowercased. Empty string is treated
// as unknown (folded to ToolOther) so a Test without the label doesn't
// blow up metric cardinality with an empty-string label value.
func NormalizeTool(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ToolOther
	}
	if _, ok := KnownTools[s]; ok {
		return s
	}
	return ToolOther
}

// SourceValues are the allowed TestRun source values (mirror the CRD enum).
// Any TestRun with an unexpected source folds to "other" via NormalizeSource.
var SourceValues = map[string]struct{}{
	sourceAPI:     {},
	sourceUI:      {},
	sourceCLI:     {},
	sourceCron:    {},
	sourceTrigger: {},
	sourceGitops:  {},
}

// NormalizeSource behaves like NormalizeTool for TestRun.spec.source.
func NormalizeSource(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ToolOther
	}
	if _, ok := SourceValues[s]; ok {
		return s
	}
	return ToolOther
}

// CodeClass turns an HTTP status code into the bounded "2xx"/"3xx"/"4xx"/
// "5xx"/"err" label. `code==0` means no HTTP response arrived (transport
// error / timeout) → "err".
func CodeClass(code int) string {
	switch {
	case code == 0:
		return codeClassErr
	case code >= 200 && code < 300:
		return codeClass2xx
	case code >= 300 && code < 400:
		return codeClass3xx
	case code >= 400 && code < 500:
		return codeClass4xx
	case code >= 500 && code < 600:
		return codeClass5xx
	default:
		return codeClassErr
	}
}

// The metrics themselves. Every counter/gauge/histogram is declared
// package-var so callers can .Inc()/.Observe() from anywhere and
// tests can gather() the shared registry.

var (
	// RunsTotal counts terminal transitions per (tool, phase, source).
	// Fires once per run (dedup upstream at the reconciler).
	RunsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "runs_total",
		Help:      "Total number of TestRuns reaching a terminal phase, labeled by tool, phase, and source.",
	}, []string{labelTool, labelPhase, labelSource})

	// RunDurationSeconds is a histogram of run wall-clock duration
	// (finished - started, in seconds) per tool.
	RunDurationSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricsNamespace,
		Name:      "run_duration_seconds",
		Help:      "TestRun wall-clock duration in seconds, per tool.",
		// Load-test bias: heavy tail up to 30min.
		Buckets: []float64{1, 5, 15, 30, 60, 120, 300, 600, 1200, 1800, 3600},
	}, []string{labelTool})

	// ActiveRuns is the gauge of currently-running TestRuns per tool.
	// Reconciler increments on queued→running, decrements on
	// running→terminal.
	ActiveRuns = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "active_runs",
		Help:      "Number of TestRuns currently in the running phase, per tool.",
	}, []string{labelTool})

	// SchedulerFiresTotal is a plain counter (no labels — the scheduler
	// fires the same shape every time; per-test cardinality is off-limits).
	SchedulerFiresTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "scheduler_fires_total",
		Help:      "Total number of cron scheduler fire attempts (regardless of outcome).",
	})

	// TriggerFiresTotal counts trigger outcomes by their bounded kind
	// (fired / expired / skipped-* / conditions-not-met).
	TriggerFiresTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "trigger_fires_total",
		Help:      "TestTrigger evaluation outcomes, labeled by outcome kind.",
	}, []string{labelOutcome})

	// WebhookDeliveriesTotal counts outbound webhook attempts by response
	// code class (2xx/3xx/4xx/5xx/err).
	WebhookDeliveriesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "webhook_deliveries_total",
		Help:      "Total webhook delivery attempts, labeled by response code class.",
	}, []string{labelCodeClass})

	// LogStreamBytesTotal counts the aggregate log bytes streamed from
	// pods into the log store (MinIO). Unlabeled — high-cardinality
	// per-run breakdown belongs in traces.
	LogStreamBytesTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "logstream_bytes_total",
		Help:      "Aggregate log bytes streamed from pods to the log store.",
	})

	// ScraperBytesTotal counts the aggregate artifact bytes uploaded
	// from the scraper to the artifact store (MinIO).
	ScraperBytesTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "scraper_bytes_total",
		Help:      "Aggregate artifact bytes uploaded from the scraper to the artifact store.",
	})
)

// All returns every metric this package exposes, in a stable order.
// Callers (cmd/operator, cmd/apiserver) MustRegister against the
// prometheus.Registry they own. Kept as a function so a new metric
// added at the top can't be forgotten from the register list — the
// slice IS the checklist.
func All() []prometheus.Collector {
	return []prometheus.Collector{
		RunsTotal,
		RunDurationSeconds,
		ActiveRuns,
		SchedulerFiresTotal,
		TriggerFiresTotal,
		WebhookDeliveriesTotal,
		LogStreamBytesTotal,
		ScraperBytesTotal,
	}
}

// AllowedLabelNames is the HARD whitelist of label names any Kubetest
// metric may declare. TestAllMetricLabels_WhitelistOnly walks the
// registry post-Register and refuses anything outside this set. Adding
// a label requires adding it here + reviewer sign-off — the point is
// that high-cardinality labels (namespace, run name, url) can never
// slip in by accident.
var AllowedLabelNames = map[string]struct{}{
	labelTool:      {},
	labelPhase:     {},
	labelSource:    {},
	labelOutcome:   {},
	labelCodeClass: {},
}
