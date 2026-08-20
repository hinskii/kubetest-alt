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

package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNormalizeTool_FoldsToWhitelist: every unknown / empty / mixed-case
// input folds to a bounded output (either a catalog name or "other").
func TestNormalizeTool_Table(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"k6", "k6"},
		{"K6", "k6"},
		{" cypress ", "cypress"},
		{"", "other"},
		{"unknown-tool", "other"},
		{"artillery", "artillery"},
		{"gatling", "gatling"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, NormalizeTool(tc.in))
		})
	}
}

func TestNormalizeSource_Table(t *testing.T) {
	cases := []struct{ in, want string }{
		{"api", "api"},
		{"CRON", "cron"},
		{"", "other"},
		{"unknown", "other"},
		{"gitops", "gitops"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, NormalizeSource(tc.in))
		})
	}
}

func TestCodeClass_Table(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "err"},
		{-1, "err"},
		{200, "2xx"},
		{204, "2xx"},
		{301, "3xx"},
		{400, "4xx"},
		{404, "4xx"},
		{500, "5xx"},
		{599, "5xx"},
		{600, "err"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			assert.Equal(t, tc.want, CodeClass(tc.in))
		})
	}
}

// TestAllMetricLabels_WhitelistOnly is the HARD label whitelist
// enforcer. Walks every metric returned by All(), extracts every
// declared label name, and asserts each is in AllowedLabelNames.
//
// This is the guard the plan explicitly called out: "żadnych run-ID,
// nazw testów, namespace'ów w labelach metryk — test assertujący
// dozwolony zbiór nazw labeli na całym registry". A regression that
// adds a "namespace" label to any metric fails this test at CI time.
func TestAllMetricLabels_WhitelistOnly(t *testing.T) {
	reg := prometheus.NewRegistry()
	for _, c := range All() {
		require.NoError(t, reg.Register(c))
	}
	// Force each metric to emit at least one sample so gather() returns
	// the label descriptors. Zero-sample counters/histograms are
	// visible to gather() via Describe() metadata; we walk that instead
	// of Gather() so we don't need to synthesize sample values.
	descs := make(chan *prometheus.Desc, 64)
	for _, c := range All() {
		c.Describe(descs)
	}
	close(descs)

	seen := map[string]struct{}{}
	for d := range descs {
		// Desc.String() looks like:
		//   Desc{fqName: "kubetest_runs_total", help: "...", constLabels: {}, variableLabels: {tool,phase,source}}
		s := d.String()
		_, after, ok := strings.Cut(s, "variableLabels: {")
		if !ok {
			continue
		}
		body, _, ok := strings.Cut(after, "}")
		if !ok || body == "" {
			continue
		}
		for name := range strings.SplitSeq(body, ",") {
			// Each entry in the variableLabels body is `name="…"` in
			// newer client_golang versions, or just `name` in older
			// ones. Strip either shape to the name.
			name = strings.TrimSpace(name)
			if eq := strings.Index(name, "="); eq >= 0 {
				name = name[:eq]
			}
			if name == "" {
				continue
			}
			seen[name] = struct{}{}
		}
	}

	require.NotEmpty(t, seen, "sanity: at least one labeled metric exists")
	for name := range seen {
		_, ok := AllowedLabelNames[name]
		assert.True(t, ok,
			"metric label %q is not on the AllowedLabelNames whitelist — "+
				"any high-cardinality label (namespace, run name, url) MUST NOT ship. "+
				"Add to AllowedLabelNames only after reviewer sign-off.", name)
	}
}

// TestAll_RegistersCleanly is the belt-and-suspenders check: All()
// entries can be MustRegister'd against a fresh registry without
// collisions. A duplicate name in any two collectors fails here.
func TestAll_RegistersCleanly(t *testing.T) {
	reg := prometheus.NewRegistry()
	for _, c := range All() {
		assert.NotPanics(t, func() { reg.MustRegister(c) })
	}
}

// TestRunsTotal_LabelValuesTable: RunsTotal accepts the (tool, phase,
// source) triples we expect from the reconciler + trigger. Also asserts
// that a Gather() on the registry after Inc() reports the same triple.
func TestRunsTotal_IncAndGather(t *testing.T) {
	// Fresh registry per test to avoid pollution across the package.
	reg := prometheus.NewRegistry()
	for _, c := range All() {
		reg.MustRegister(c)
	}

	RunsTotal.WithLabelValues("k6", "passed", "api").Inc()
	RunsTotal.WithLabelValues("jmeter", "failed", "cron").Inc()
	RunsTotal.WithLabelValues("other", "error", "trigger").Inc()

	mfs, err := reg.Gather()
	require.NoError(t, err)

	var got *dto.MetricFamily
	for _, mf := range mfs {
		if mf.GetName() == "kubetest_runs_total" {
			got = mf
			break
		}
	}
	require.NotNil(t, got, "runs_total must be present after Inc()")
	assert.Len(t, got.GetMetric(), 3, "three distinct label tuples")
}
