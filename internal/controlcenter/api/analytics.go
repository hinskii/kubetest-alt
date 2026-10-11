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
	"cmp"
	"fmt"
	"maps"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// Analytics (step 18f): trends over a Test's recent runs and a comparison
// of selected runs, for every tool. Everything is computed from the run
// history the API returns — rows are whatever the runs carry (duration,
// JUnit counts, the metric keys of pkg/report), never a per-tool layout.

// MaxCompared bounds the runs in one comparison (one API call each).
const MaxCompared = 10

// Run phases as the API spells them.
const (
	phasePassed  = string(testsv1alpha1.PhasePassed)
	phaseQueued  = string(testsv1alpha1.PhaseQueued)
	phaseFailed  = string(testsv1alpha1.PhaseFailed)
	phaseError   = string(testsv1alpha1.PhaseError)
	phaseAborted = string(testsv1alpha1.PhaseAborted)
)

// Analytics is a Test's last Window finished runs, summarized.
type Analytics struct {
	Window  int   `json:"window" required:"true"`
	Windows []int `json:"windows" required:"true" nullable:"false"`
	// Runs are the finished runs, newest first.
	Runs    []RunRow `json:"runs" required:"true" nullable:"false"`
	Summary Summary  `json:"summary" required:"true"`
	// Trends are duration, JUnit counts (when the runs report them), then
	// every metric key, in natural order — over passed and failed runs,
	// oldest first.
	Trends []Trend `json:"trends" required:"true" nullable:"false"`
	// Cases is set when the runs reported JUnit.
	Cases *CasesSummary `json:"cases,omitempty"`
}

// Summary counts the window's runs.
type Summary struct {
	Finished int `json:"finished" required:"true"`
	Passed   int `json:"passed" required:"true"`
	Failed   int `json:"failed" required:"true"`
	Errored  int `json:"errored" required:"true"`
	Aborted  int `json:"aborted" required:"true"`
	// PassRate is passed of the runs with a verdict (passed, failed,
	// error), rounded; "" without any.
	PassRate string `json:"passRate,omitempty"`
	// AvgMs and P95Ms are over passed and failed runs (an error or abort
	// says nothing about the system under test).
	AvgMs       int64   `json:"avgMs,omitempty"`
	P95Ms       int64   `json:"p95Ms,omitempty"`
	LastFailure *RunRow `json:"lastFailure,omitempty"`
}

// Trend is one value per run, oldest first.
type Trend struct {
	Label string `json:"label" required:"true"`
	// Unit is "ms" for durations, "" otherwise.
	Unit   string    `json:"unit" required:"true"`
	Values []float64 `json:"values" required:"true" nullable:"false"`
	Latest float64   `json:"latest" required:"true"`
	Min    float64   `json:"min" required:"true"`
	Avg    float64   `json:"avg" required:"true"`
	Max    float64   `json:"max" required:"true"`
	// Delta is the latest against the average of the runs before it
	// ("+12%", "-3.5%"); "" without a meaningful base.
	Delta string `json:"delta,omitempty"`
}

// CasesSummary is the window's flaky and failing JUnit cases.
type CasesSummary struct {
	Flaky      int    `json:"flaky" required:"true"`
	FailingNow int    `json:"failingNow" required:"true"`
	Error      string `json:"error,omitempty"`
}

// Comparison is the selected runs side by side: one column per run
// (oldest first), one row per fact, metric or parameter.
type Comparison struct {
	Runs    []RunRow     `json:"runs" required:"true" nullable:"false"`
	Facts   []CompareRow `json:"facts" required:"true" nullable:"false"`
	Metrics []CompareRow `json:"metrics" required:"true" nullable:"false"`
	Params  []CompareRow `json:"params" required:"true" nullable:"false"`
	// Missing are requested runs that aren't runs of this Test.
	Missing []string `json:"missing" required:"true" nullable:"false"`
	// Markdown is the comparison for a report or a ticket.
	Markdown string `json:"markdown" required:"true"`
}

// CompareRow is one fact, metric or parameter across the runs.
type CompareRow struct {
	Label string        `json:"label" required:"true"`
	Cells []CompareCell `json:"cells" required:"true" nullable:"false"`
}

// CompareCell is one run's value; Delta (metrics only) is against the
// first column.
type CompareCell struct {
	Value string `json:"value,omitempty"`
	Delta string `json:"delta,omitempty"`
}

type compareRunsParams struct {
	Cluster   string `path:"cluster"`
	Namespace string `path:"ns"`
	Name      string `path:"name"`
	// Run names the runs to compare (repeated), at most 10.
	Run []string `query:"run" required:"true"`
}

func (s *Server) analytics(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	ns, name, n := r.PathValue("ns"), r.PathValue("name"), window(r)
	page, err := client.ListRuns(r.Context(), apiclient.ListRunsOptions{Namespace: ns, Test: name, Limit: n})
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	var finished []apiclient.Run
	for _, run := range page.Runs {
		if Finished(run.Phase) {
			finished = append(finished, run)
		}
	}
	out := Analytics{Window: n, Windows: Windows, Runs: make([]RunRow, 0, len(finished))}
	hasCases := false
	for _, run := range finished {
		out.Runs = append(out.Runs, runRow(run))
		hasCases = hasCases || run.TestCounts != nil
	}
	oldestFirst := slices.Clone(finished)
	slices.Reverse(oldestFirst)
	out.Summary = summarize(oldestFirst)
	out.Trends = trends(oldestFirst)
	if hasCases {
		cs := &CasesSummary{}
		stats, err := client.TestCaseStats(r.Context(), ns, name, n)
		if err != nil {
			cs.Error = messageOf(err)
		}
		for _, st := range stats {
			if st.Flaky {
				cs.Flaky++
			}
			if failing(st.LastStatus) {
				cs.FailingNow++
			}
		}
		out.Cases = cs
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) compareRuns(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	names := selectedRuns(r)
	if len(names) == 0 {
		writeError(w, http.StatusBadRequest, "Pick the runs to compare (?run= is missing).")
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	out, err := buildComparison(r, client, ns, name, names)
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	out.Markdown = comparisonMarkdown(c.DisplayNameOrName(), ns, name, out, time.Now())
	writeJSON(w, http.StatusOK, out)
}

// selectedRuns returns the distinct ?run= values, at most MaxCompared.
func selectedRuns(r *http.Request) []string {
	var out []string
	for _, n := range r.URL.Query()["run"] {
		if n = strings.TrimSpace(n); n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	if len(out) > MaxCompared {
		out = out[:MaxCompared]
	}
	return out
}

// summarize counts the phases of runs and the durations of the runs that
// produced a verdict (passed or failed — error and aborted runs' durations
// say nothing about the system under test).
func summarize(runs []apiclient.Run) Summary {
	var s Summary
	var durations []int64
	for i := range runs {
		run := runs[i]
		s.Finished++
		switch run.Phase {
		case phasePassed:
			s.Passed++
		case phaseFailed:
			s.Failed++
		case phaseError:
			s.Errored++
		case phaseAborted:
			s.Aborted++
		}
		if run.Phase == phaseFailed || run.Phase == phaseError {
			row := runRow(run)
			s.LastFailure = &row
		}
		if (run.Phase == phasePassed || run.Phase == phaseFailed) && run.DurationMs > 0 {
			durations = append(durations, run.DurationMs)
		}
	}
	if verdicts := s.Passed + s.Failed + s.Errored; verdicts > 0 {
		s.PassRate = fmt.Sprintf("%d%%", (s.Passed*100+verdicts/2)/verdicts)
	}
	if len(durations) > 0 {
		var sum int64
		for _, d := range durations {
			sum += d
		}
		s.AvgMs = sum / int64(len(durations))
		slices.Sort(durations)
		s.P95Ms = durations[(len(durations)*95+99)/100-1]
	}
	return s
}

// trends builds the trend rows from runs (oldest first): duration, JUnit
// counts when the runs report them, then every metric key, natural order.
func trends(runs []apiclient.Run) []Trend {
	out := []Trend{}
	verdict := func(run apiclient.Run) bool { return run.Phase == phasePassed || run.Phase == phaseFailed }
	add := func(label, unit string, value func(apiclient.Run) (float64, bool)) {
		var vals []float64
		for _, run := range runs {
			if v, ok := value(run); ok {
				vals = append(vals, v)
			}
		}
		if len(vals) > 0 {
			out = append(out, newTrend(label, unit, vals))
		}
	}
	add("Duration", "ms", func(run apiclient.Run) (float64, bool) {
		return float64(run.DurationMs), verdict(run) && run.DurationMs > 0
	})
	add("Tests failed", "", func(run apiclient.Run) (float64, bool) {
		if run.TestCounts == nil {
			return 0, false
		}
		return float64(run.TestCounts.Failed), true
	})
	add("Tests total", "", func(run apiclient.Run) (float64, bool) {
		if run.TestCounts == nil {
			return 0, false
		}
		return float64(run.TestCounts.Total), true
	})
	keys := map[string]bool{}
	for _, run := range runs {
		for k := range run.Metrics {
			keys[k] = true
		}
	}
	for _, k := range sortedNatural(keys) {
		add(k, "", func(run apiclient.Run) (float64, bool) {
			v, ok := run.Metrics[k]
			return v, ok && verdict(run) && !math.IsNaN(v) && !math.IsInf(v, 0)
		})
	}
	return out
}

func newTrend(label, unit string, vals []float64) Trend {
	t := Trend{Label: label, Unit: unit, Values: vals, Latest: vals[len(vals)-1], Min: slices.Min(vals), Max: slices.Max(vals)}
	var sum float64
	for _, v := range vals {
		sum += v
	}
	t.Avg = sum / float64(len(vals))
	if len(vals) > 1 {
		var prev float64
		for _, v := range vals[:len(vals)-1] {
			prev += v
		}
		t.Delta = relChange(t.Latest, prev/float64(len(vals)-1))
	}
	return t
}

// relChange renders v against base as "+12%" / "-3.5%"; "" when there is
// no meaningful base or no change.
func relChange(v, base float64) string {
	if base == 0 || v == base {
		return ""
	}
	p := (v - base) / math.Abs(base) * 100
	if math.Abs(p) < 0.05 {
		return ""
	}
	if math.Abs(p) >= 10 {
		return fmt.Sprintf("%+.0f%%", p)
	}
	return fmt.Sprintf("%+.1f%%", p)
}

func buildComparison(r *http.Request, client *apiclient.Client, ns, test string, names []string) (*Comparison, error) {
	var runs []apiclient.Run
	out := &Comparison{Missing: []string{}}
	for _, n := range names {
		run, err := client.GetRun(r.Context(), ns, n)
		switch {
		case apiclient.IsNotFound(err):
			out.Missing = append(out.Missing, n)
			continue
		case err != nil:
			return nil, err
		}
		if run.TestRef != test {
			out.Missing = append(out.Missing, n)
			continue
		}
		runs = append(runs, *run)
	}
	slices.SortStableFunc(runs, func(a, b apiclient.Run) int { return cmp.Compare(runTime(a), runTime(b)) })
	fillComparison(out, runs)
	return out, nil
}

// runTime orders runs: started, else queued, else finished.
func runTime(run apiclient.Run) int64 {
	for _, t := range []*time.Time{run.StartedAt, run.QueuedAt, run.FinishedAt} {
		if t != nil {
			return t.UnixNano()
		}
	}
	return 0
}

func fillComparison(c *Comparison, runs []apiclient.Run) {
	c.Runs, c.Facts, c.Metrics, c.Params = []RunRow{}, []CompareRow{}, []CompareRow{}, []CompareRow{}
	for _, run := range runs {
		c.Runs = append(c.Runs, runRow(run))
	}
	fact := func(label string, value func(apiclient.Run) string) {
		row := CompareRow{Label: label}
		seen := false
		for _, run := range runs {
			v := value(run)
			seen = seen || v != ""
			row.Cells = append(row.Cells, CompareCell{Value: v})
		}
		if seen {
			c.Facts = append(c.Facts, row)
		}
	}
	fact("Status", func(run apiclient.Run) string { return run.Phase })
	fact("Started", func(run apiclient.Run) string {
		if run.StartedAt == nil {
			return ""
		}
		return run.StartedAt.UTC().Format("2006-01-02 15:04 UTC")
	})
	fact("Duration", func(run apiclient.Run) string { return FormatDuration(run.DurationMs) })
	fact("Commit", func(run apiclient.Run) string {
		if run.Git == nil || run.Git.Commit == "" {
			return ""
		}
		v := ShortSHA(run.Git.Commit)
		if run.Git.Revision != "" {
			v += " (" + run.Git.Revision + ")"
		}
		return v
	})
	fact("Tests", func(run apiclient.Run) string {
		tc := run.TestCounts
		if tc == nil {
			return ""
		}
		return fmt.Sprintf("%d passed · %d failed · %d skipped", tc.Passed, tc.Failed, tc.Skipped)
	})
	fact("Started by", func(run apiclient.Run) string { return run.Tags[TagCreatedBy] })

	metricKeys := map[string]bool{}
	paramKeys := map[string]bool{}
	for _, run := range runs {
		for k := range run.Metrics {
			metricKeys[k] = true
		}
		for k := range run.Config {
			paramKeys[k] = true
		}
	}
	for _, k := range sortedNatural(metricKeys) {
		row := CompareRow{Label: k}
		base, hasBase := 0.0, false
		for i, run := range runs {
			v, ok := run.Metrics[k]
			if !ok {
				row.Cells = append(row.Cells, CompareCell{})
				continue
			}
			cell := CompareCell{Value: FormatNumber(v)}
			if i == 0 {
				base, hasBase = v, true
			} else if hasBase {
				cell.Delta = relChange(v, base)
			}
			row.Cells = append(row.Cells, cell)
		}
		c.Metrics = append(c.Metrics, row)
	}
	for _, k := range sortedNatural(paramKeys) {
		row := CompareRow{Label: k}
		for _, run := range runs {
			row.Cells = append(row.Cells, CompareCell{Value: run.Config[k]})
		}
		c.Params = append(c.Params, row)
	}
}

func sortedNatural(set map[string]bool) []string {
	keys := slices.Collect(maps.Keys(set))
	slices.SortFunc(keys, NaturalCompare)
	return keys
}

// comparisonMarkdown renders c as a Markdown section for a report or a
// ticket: one column per run, metrics and parameters as rows.
func comparisonMarkdown(cluster, ns, test string, c *Comparison, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Test comparison: %s\n\n", mdText(test))
	fmt.Fprintf(&b, "- Cluster: `%s`, namespace `%s`\n", mdCode(cluster), mdCode(ns))
	fmt.Fprintf(&b, "- Generated: %s\n\n", now.UTC().Format("2006-01-02 15:04 UTC"))
	if len(c.Runs) == 0 {
		b.WriteString("No runs to compare.\n")
		return b.String()
	}
	header := []string{"Run"}
	for _, run := range c.Runs {
		header = append(header, mdCell(run.Name))
	}
	row := func(cells []string) { b.WriteString("| " + strings.Join(cells, " | ") + " |\n") }
	row(header)
	sep := make([]string, len(header))
	for i := range sep {
		sep[i] = "---"
	}
	row(sep)
	section := func(title string, rows []CompareRow) {
		if len(rows) == 0 {
			return
		}
		cells := make([]string, len(header))
		cells[0] = "**" + title + "**"
		row(cells)
		for _, r := range rows {
			cells := []string{mdCell(r.Label)}
			for _, cell := range r.Cells {
				v := cell.Value
				if v == "" {
					v = "—"
				}
				if cell.Delta != "" {
					v += " (" + cell.Delta + ")"
				}
				cells = append(cells, mdCell(v))
			}
			row(cells)
		}
	}
	section("Run", c.Facts)
	section("Metrics", c.Metrics)
	section("Parameters", c.Params)
	if len(c.Metrics) > 0 && len(c.Runs) > 1 {
		b.WriteString("\nChanges in brackets are against the first run.\n")
	}
	return b.String()
}

// mdCell escapes a table cell: pipes and line breaks would break the row.
func mdCell(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ", "|", `\|`).Replace(s)
	return strings.TrimSpace(s)
}

func mdText(s string) string {
	return mdCell(strings.NewReplacer("*", `\*`, "_", `\_`, "`", "'").Replace(s))
}

func mdCode(s string) string { return strings.ReplaceAll(mdCell(s), "`", "'") }
