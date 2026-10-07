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

package server

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/views"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// Analytics (step 18f): trends over a Test's recent runs and a comparison
// of selected runs, for every tool. Everything is computed from the run
// history the API returns — rows are whatever the runs carry (duration,
// JUnit counts, the metric keys of pkg/report), never a per-tool layout.

// analyticsWindows are the run windows offered on the analytics page.
var analyticsWindows = []int{10, 30, 100}

// maxCompared bounds the runs in one comparison (one API call each).
const maxCompared = 10

// Run phases as the API spells them.
const (
	phasePassed  = string(testsv1alpha1.PhasePassed)
	phaseFailed  = string(testsv1alpha1.PhaseFailed)
	phaseError   = string(testsv1alpha1.PhaseError)
	phaseAborted = string(testsv1alpha1.PhaseAborted)
)

func analyticsPath(c, ns, test string) string { return testPath(c, ns, test) + "/analytics" }

// series is one trend row: a value per run that has it, oldest first.
type series struct {
	Label, Unit          string
	Values               []float64
	Latest, Min, Avg     float64
	Max                  float64
	Delta                string // latest vs the average of the runs before it
	Points               string // SVG polyline points
	LastX, LastY         float64
	SparkWidth, SparkTop int
}

type analyticsSummary struct {
	Finished, Passed, Failed, Errored, Aborted int
	PassRate                                   string
	AvgMs, P95Ms                               int64
	LastFailure                                *apiclient.Run
}

type analyticsData struct {
	Cluster, Namespace, Test string
	Window                   int
	Windows                  []int
	Runs                     []apiclient.Run // finished, newest first
	Strip                    []apiclient.Run // finished, oldest first
	Summary                  analyticsSummary
	Trends                   []series
	HasCases                 bool
	FlakyCount, FailingNow   int
	CasesErr                 string
	Selected                 map[string]bool
	Compare                  *comparison
	Markdown                 string
	MarkdownHref             string
}

// testAnalyticsPage shows trends over the last ?runs finished runs and,
// with ?run=<name> (repeated), the comparison of those runs.
func (s *Server) testAnalyticsPage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	window, _ := strconv.Atoi(r.URL.Query().Get("runs"))
	if !slices.Contains(analyticsWindows, window) {
		window = 30
	}
	client := api(r, c)
	page, err := client.ListRuns(r.Context(), apiclient.ListRunsOptions{Namespace: ns, Test: name, Limit: window})
	if err != nil {
		s.apiError(w, r, c, err)
		return
	}
	data := analyticsData{Cluster: c.Name, Namespace: ns, Test: name, Window: window, Windows: analyticsWindows,
		Selected: map[string]bool{}}
	for _, run := range page.Runs {
		if finished(run.Phase) {
			data.Runs = append(data.Runs, run)
		}
	}
	data.Strip = slices.Clone(data.Runs)
	slices.Reverse(data.Strip)
	data.Summary = summarize(data.Strip)
	data.Trends = trends(data.Strip)

	for _, run := range data.Runs {
		if run.TestCounts != nil {
			data.HasCases = true
			break
		}
	}
	if data.HasCases {
		stats, err := client.TestCaseStats(r.Context(), ns, name, window)
		if err != nil {
			data.CasesErr = messageOf(err)
		}
		for _, st := range stats {
			if st.Flaky {
				data.FlakyCount++
			}
			if failing(st.LastStatus) {
				data.FailingNow++
			}
		}
	}

	if names := selectedRuns(r); len(names) > 0 {
		cmpData, err := s.buildComparison(r, client, ns, name, names)
		if err != nil {
			s.apiError(w, r, c, err)
			return
		}
		for _, run := range cmpData.Runs {
			data.Selected[views.RunID(run)] = true
		}
		data.Compare = cmpData
		data.Markdown = comparisonMarkdown(c.DisplayNameOrName(), ns, name, cmpData, time.Now())
		data.MarkdownHref = analyticsPath(c.Name, ns, name) + "/compare.md?" + r.URL.RawQuery
	}
	crumbs := append(clusterCrumbs(c), views.Crumb{Label: name, Href: testPath(c.Name, ns, name)})
	s.page(w, r, "analytics", name+" · analytics", crumbs, data)
}

// comparisonMarkdownDownload serves the comparison of ?run=… as Markdown.
func (s *Server) comparisonMarkdownDownload(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	names := selectedRuns(r)
	if len(names) == 0 {
		s.renderError(w, r, http.StatusBadRequest, "Pick the runs to compare (?run= is missing).")
		return
	}
	cmpData, err := s.buildComparison(r, api(r, c), ns, name, names)
	if err != nil {
		s.apiError(w, r, c, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-comparison.md"`, safeFilename(name)))
	_, _ = w.Write([]byte(comparisonMarkdown(c.DisplayNameOrName(), ns, name, cmpData, time.Now())))
}

// selectedRuns returns the distinct ?run= values, at most maxCompared.
func selectedRuns(r *http.Request) []string {
	var out []string
	for _, n := range r.URL.Query()["run"] {
		if n = strings.TrimSpace(n); n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	if len(out) > maxCompared {
		out = out[:maxCompared]
	}
	return out
}

func safeFilename(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9') {
			return r
		}
		return '_'
	}, s)
}

// summarize counts the phases of runs and the durations of the runs that
// produced a verdict (passed or failed — error and aborted runs' durations
// say nothing about the system under test).
func summarize(runs []apiclient.Run) analyticsSummary {
	var s analyticsSummary
	var durations []int64
	for i := range runs {
		run := &runs[i]
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
			s.LastFailure = run
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
func trends(runs []apiclient.Run) []series {
	var out []series
	verdict := func(run apiclient.Run) bool { return run.Phase == phasePassed || run.Phase == phaseFailed }
	add := func(label, unit string, value func(apiclient.Run) (float64, bool)) {
		var vals []float64
		for _, run := range runs {
			if v, ok := value(run); ok {
				vals = append(vals, v)
			}
		}
		if len(vals) > 0 {
			out = append(out, newSeries(label, unit, vals))
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
	sorted := slices.Collect(maps.Keys(keys))
	slices.SortFunc(sorted, views.NaturalCompare)
	for _, k := range sorted {
		add(k, "", func(run apiclient.Run) (float64, bool) {
			v, ok := run.Metrics[k]
			return v, ok && verdict(run) && !math.IsNaN(v) && !math.IsInf(v, 0)
		})
	}
	return out
}

const (
	sparkWidth  = 140
	sparkHeight = 28
)

func newSeries(label, unit string, vals []float64) series {
	s := series{Label: label, Unit: unit, Values: vals, Latest: vals[len(vals)-1],
		Min: slices.Min(vals), Max: slices.Max(vals), SparkWidth: sparkWidth, SparkTop: sparkHeight}
	var sum float64
	for _, v := range vals {
		sum += v
	}
	s.Avg = sum / float64(len(vals))
	if len(vals) > 1 {
		var prev float64
		for _, v := range vals[:len(vals)-1] {
			prev += v
		}
		s.Delta = relChange(s.Latest, prev/float64(len(vals)-1))
	}
	pts := make([]string, len(vals))
	for i, v := range vals {
		x := float64(sparkWidth) / 2
		if len(vals) > 1 {
			x = float64(i) * float64(sparkWidth) / float64(len(vals)-1)
		}
		y := float64(sparkHeight) / 2
		if s.Max > s.Min {
			y = float64(sparkHeight-2) - (v-s.Min)/(s.Max-s.Min)*float64(sparkHeight-4)
		}
		pts[i] = fmt.Sprintf("%.1f,%.1f", x, y)
		s.LastX, s.LastY = x, y
	}
	s.Points = strings.Join(pts, " ")
	return s
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

// comparison is the selected runs side by side: one column per run
// (oldest first), one row per fact, metric or parameter.
type comparison struct {
	Runs    []apiclient.Run
	Facts   []compareRow
	Metrics []compareRow
	Params  []compareRow
	Missing []string // requested runs that aren't runs of this Test
}

type compareRow struct {
	Label string
	Cells []compareCell
}

type compareCell struct {
	Value, Delta string // Delta: against the first column, metrics only
}

func (s *Server) buildComparison(r *http.Request, client *apiclient.Client, ns, test string, names []string) (*comparison, error) {
	out := &comparison{}
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
		out.Runs = append(out.Runs, *run)
	}
	slices.SortStableFunc(out.Runs, func(a, b apiclient.Run) int { return cmp.Compare(runTime(a), runTime(b)) })
	fillComparison(out)
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

func fillComparison(c *comparison) {
	fact := func(label string, value func(apiclient.Run) string) {
		row := compareRow{Label: label}
		seen := false
		for _, run := range c.Runs {
			v := value(run)
			seen = seen || v != ""
			row.Cells = append(row.Cells, compareCell{Value: v})
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
	fact("Duration", func(run apiclient.Run) string { return views.Duration(run.DurationMs) })
	fact("Commit", func(run apiclient.Run) string {
		if run.Git == nil || run.Git.Commit == "" {
			return ""
		}
		v := views.ShortSHA(run.Git.Commit)
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
	fact("Started by", func(run apiclient.Run) string { return run.Tags["kubetest.io/created-by"] })

	metricKeys := map[string]bool{}
	paramKeys := map[string]bool{}
	for _, run := range c.Runs {
		for k := range run.Metrics {
			metricKeys[k] = true
		}
		for k := range run.Config {
			paramKeys[k] = true
		}
	}
	for _, k := range sortedNatural(metricKeys) {
		row := compareRow{Label: k}
		base, hasBase := 0.0, false
		for i, run := range c.Runs {
			v, ok := run.Metrics[k]
			if !ok {
				row.Cells = append(row.Cells, compareCell{})
				continue
			}
			cell := compareCell{Value: views.FormatNumber(v)}
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
		row := compareRow{Label: k}
		for _, run := range c.Runs {
			row.Cells = append(row.Cells, compareCell{Value: run.Config[k]})
		}
		c.Params = append(c.Params, row)
	}
}

func sortedNatural(set map[string]bool) []string {
	keys := slices.Collect(maps.Keys(set))
	slices.SortFunc(keys, views.NaturalCompare)
	return keys
}

// comparisonMarkdown renders c as a Markdown section for a report or a
// ticket: one column per run, metrics and parameters as rows.
func comparisonMarkdown(cluster, ns, test string, c *comparison, now time.Time) string {
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
	section := func(title string, rows []compareRow) {
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
