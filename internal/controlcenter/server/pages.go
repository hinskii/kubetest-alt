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
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	pathpkg "path"
	"slices"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/auth"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/clusters"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/views"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// Labels Control Center reads from Tests.
const (
	labelTool      = "kubetest.io/tool"
	labelManagedBy = "app.kubernetes.io/managed-by"
	// managedByUI marks a Test the GUI may edit (CLAUDE.md §7).
	managedByUI = "ui"
)

// HTML input types of parameter fields (inputNumber is also the
// parameter type).
const (
	inputNumber = "number"
	inputText   = "text"
)

// formComment is the comment form's text field.
const formComment = "text"

// artifactCSP is the Content-Security-Policy of a served artifact.
const artifactCSP = "sandbox allow-scripts"

// finalPhases are the phases a run never leaves.
var finalPhases = map[string]bool{
	string(testsv1alpha1.PhasePassed): true, string(testsv1alpha1.PhaseFailed): true,
	string(testsv1alpha1.PhaseError): true, string(testsv1alpha1.PhaseAborted): true,
}

// runsPerPage is the history page size on a Test's page.
const runsPerPage = 25

// maxLogPoll bounds one live-log poll response; the page asks again at
// once while X-More says there is more.
const maxLogPoll = 1 << 20

// routes registers the cluster pages on mux. Mutations need a role.
func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /clusters/{cluster}", s.testsPage)
	mux.HandleFunc("GET /clusters/{cluster}/tests/{ns}/{name}", s.testPage)
	mux.Handle("POST /clusters/{cluster}/tests/{ns}/{name}/run", auth.Require(auth.RoleDeveloper, http.HandlerFunc(s.startRun)))
	mux.HandleFunc("GET /clusters/{cluster}/tests/{ns}/{name}/cases", s.testCasesPage)
	mux.HandleFunc("GET /clusters/{cluster}/tests/{ns}/{name}/cases/history", s.caseHistoryPage)
	mux.HandleFunc("GET /clusters/{cluster}/schedules", s.schedulesPage)
	mux.Handle("POST /clusters/{cluster}/tests/{ns}/{name}/schedule", auth.Require(auth.RoleDeveloper, http.HandlerFunc(s.setSchedule)))
	mux.HandleFunc("GET /clusters/{cluster}/tests/{ns}/{name}/analytics", s.testAnalyticsPage)
	mux.HandleFunc("GET /clusters/{cluster}/tests/{ns}/{name}/analytics/compare.md", s.comparisonMarkdownDownload)
	mux.HandleFunc("GET /clusters/{cluster}/runs/{ns}/{id}", s.runPage)
	mux.HandleFunc("GET /clusters/{cluster}/runs/{ns}/{id}/compare", s.compareRuns)
	mux.HandleFunc("GET /clusters/{cluster}/runs/{ns}/{id}/log", s.runLogPoll)
	mux.HandleFunc("GET /clusters/{cluster}/runs/{ns}/{id}/logs.txt", s.runLogDownload)
	mux.HandleFunc("GET /clusters/{cluster}/runs/{ns}/{id}/artifacts/{path...}", s.runArtifact)
	mux.Handle("POST /clusters/{cluster}/runs/{ns}/{id}/abort", auth.Require(auth.RoleDeveloper, http.HandlerFunc(s.abortRun)))
	mux.Handle("POST /clusters/{cluster}/runs/{ns}/{id}/comment", auth.Require(auth.RoleDeveloper, http.HandlerFunc(s.commentRun)))
	mux.Handle("POST /clusters/{cluster}/runs/{ns}/{id}/delete", auth.Require(auth.RoleAdmin, http.HandlerFunc(s.deleteRun)))
}

// --- paths -------------------------------------------------------------------

func clusterPath(c string) string { return "/clusters/" + url.PathEscape(c) }

func testPath(c, ns, name string) string {
	return clusterPath(c) + "/tests/" + url.PathEscape(ns) + "/" + url.PathEscape(name)
}

func runPath(c, ns, id string) string {
	return clusterPath(c) + "/runs/" + url.PathEscape(ns) + "/" + url.PathEscape(id)
}

// --- plumbing ------------------------------------------------------------------

// cluster resolves {cluster}, rendering a 404 when it isn't configured.
func (s *Server) cluster(w http.ResponseWriter, r *http.Request) (*clusters.Cluster, bool) {
	c := s.Clusters.Get(r.PathValue("cluster"))
	if c == nil {
		s.renderError(w, r, http.StatusNotFound, fmt.Sprintf("No cluster named %q is configured.", r.PathValue("cluster")))
		return nil, false
	}
	return c, true
}

// api is the cluster's kubetest API, acting for the signed-in user.
func api(r *http.Request, c *clusters.Cluster) *apiclient.Client {
	return c.API.AsUser(auth.FromContext(r.Context()).Email)
}

// apiError renders a failed kubetest API call: its 404 as ours, anything
// else as a bad gateway with the API's own message.
func (s *Server) apiError(w http.ResponseWriter, r *http.Request, c *clusters.Cluster, err error) {
	var apiErr *apiclient.APIError
	switch {
	case apiclient.IsNotFound(err):
		s.renderError(w, r, http.StatusNotFound, "Not found in "+c.DisplayNameOrName()+": "+messageOf(err))
	case errors.As(err, &apiErr):
		s.renderError(w, r, http.StatusBadGateway, "The kubetest API of "+c.DisplayNameOrName()+" answered: "+messageOf(err))
	default:
		s.Log.Warn("kubetest API unreachable", "cluster", c.Name, "err", err)
		s.renderError(w, r, http.StatusBadGateway, "Cannot reach the kubetest API of "+c.DisplayNameOrName()+".")
	}
}

func messageOf(err error) string {
	var apiErr *apiclient.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Message
	}
	return err.Error()
}

// redirect finishes a POST with a GET of target, carrying a notice.
// Targets come from clusterPath/testPath/runPath (escaped segments under
// /clusters/); anything that isn't a local path goes home instead.
func redirect(w http.ResponseWriter, r *http.Request, target, notice string) {
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.HasPrefix(target, "/\\") {
		target = "/"
	}
	if notice != "" {
		target += "?notice=" + url.QueryEscape(notice)
	}
	http.Redirect(w, r, target, http.StatusSeeOther) //nolint:gosec // local path only, checked above
}

// page renders a page with the notice of the previous action.
func (s *Server) page(w http.ResponseWriter, r *http.Request, name, title string, crumbs []views.Crumb, data any) {
	notice := r.URL.Query().Get("notice")
	if len(notice) > 300 {
		notice = notice[:300]
	}
	p := views.Page{Title: title, User: auth.FromContext(r.Context()), Notice: notice, Breadcrumbs: crumbs, Data: data}
	if err := s.Views.Render(w, http.StatusOK, name, p); err != nil {
		s.Log.Error("render failed", "page", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func clusterCrumbs(c *clusters.Cluster) []views.Crumb {
	return []views.Crumb{{Label: "Clusters", Href: "/"}, {Label: c.DisplayNameOrName(), Href: clusterPath(c.Name)}}
}

// --- tests list -------------------------------------------------------------------

type testRow struct {
	Name, Namespace, Tool string
	Locked                bool
	LatestRun             *testsv1alpha1.RunReference
}

type testGroup struct {
	Tool  string
	Tests []testRow
}

type testsData struct {
	Cluster            string
	Namespace, Tool    string // filters
	Namespaces, Tools  []string
	Groups             []testGroup
	Total, Shown       int
	ClusterDisplayName string
}

func (s *Server) testsPage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	nsFilter, toolFilter := r.URL.Query().Get("namespace"), r.URL.Query().Get("tool")
	tests, err := api(r, c).ListTests(r.Context(), "")
	if err != nil {
		s.apiError(w, r, c, err)
		return
	}
	data := testsData{Cluster: c.Name, ClusterDisplayName: c.DisplayNameOrName(), Namespace: nsFilter, Tool: toolFilter, Total: len(tests)}
	namespaces, tools := map[string]bool{}, map[string]bool{}
	groups := map[string][]testRow{}
	for _, t := range tests {
		tool := t.Labels[labelTool]
		if tool == "" {
			tool = "other"
		}
		namespaces[t.Namespace], tools[tool] = true, true
		if (nsFilter != "" && t.Namespace != nsFilter) || (toolFilter != "" && tool != toolFilter) {
			continue
		}
		groups[tool] = append(groups[tool], testRow{
			Name: t.Name, Namespace: t.Namespace, Tool: tool,
			Locked:    t.Labels[labelManagedBy] != managedByUI,
			LatestRun: t.Status.LatestRun,
		})
		data.Shown++
	}
	data.Namespaces = slices.Sorted(maps.Keys(namespaces))
	data.Tools = slices.Sorted(maps.Keys(tools))
	for _, tool := range slices.Sorted(maps.Keys(groups)) {
		rows := groups[tool]
		slices.SortFunc(rows, func(a, b testRow) int {
			return strings.Compare(a.Namespace+"/"+a.Name, b.Namespace+"/"+b.Name)
		})
		data.Groups = append(data.Groups, testGroup{Tool: tool, Tests: rows})
	}
	s.page(w, r, "tests", c.DisplayNameOrName(), clusterCrumbs(c)[:1], data)
}

// --- test page ----------------------------------------------------------------------

// param is one spec.config parameter as a form field.
type param struct {
	Name, Type, Default, Pattern, Description string
	Enum                                      []string
	Required                                  bool
	InputType, Step                           string
}

type testData struct {
	Cluster    string
	Test       *apiclient.ResolvedTest
	Params     []param
	Runs       []apiclient.Run
	NextCursor string
	PageCursor string
	// ShowCommit: some run on the page checked out a git commit.
	ShowCommit bool
	// Schedule is the Test's recurring schedule (spec.schedule), if any.
	Schedule *scheduleView
}

func paramsOf(spec *testsv1alpha1.TestSpec) []param {
	if spec == nil {
		return nil
	}
	out := make([]param, 0, len(spec.Config))
	for _, name := range slices.Sorted(maps.Keys(spec.Config)) {
		p := spec.Config[name]
		f := param{Name: name, Type: p.Type, Default: p.Default, Pattern: p.Pattern,
			Enum: p.Enum, Required: p.Default == "", InputType: inputText}
		switch p.Type {
		case "integer":
			f.InputType, f.Step = inputNumber, "1"
		case inputNumber:
			f.InputType, f.Step = inputNumber, "any"
		}
		out = append(out, f)
	}
	return out
}

func (s *Server) testPage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	client := api(r, c)
	t, err := client.GetResolvedTest(r.Context(), ns, name)
	if err != nil {
		s.apiError(w, r, c, err)
		return
	}
	cursor := r.URL.Query().Get("after")
	runs, err := client.ListRuns(r.Context(), apiclient.ListRunsOptions{
		Namespace: ns, Test: name, Limit: runsPerPage, After: cursor,
	})
	if err != nil {
		s.apiError(w, r, c, err)
		return
	}
	data := testData{Cluster: c.Name, Test: t, Params: paramsOf(t.Spec), Runs: runs.Runs,
		NextCursor: runs.NextCursor, PageCursor: cursor}
	if t.Spec != nil {
		data.Schedule = viewSchedule(t.Spec.Schedule, time.Now())
	}
	data.ShowCommit = slices.ContainsFunc(runs.Runs, func(r apiclient.Run) bool { return r.Git != nil && r.Git.Commit != "" })
	s.page(w, r, "test", name, clusterCrumbs(c), data)
}

// startRun creates a TestRun from the run form: parameters that differ
// from their defaults, and an optional start time (UTC).
func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	back := testPath(c.Name, ns, name)
	client := api(r, c)
	t, err := client.GetResolvedTest(r.Context(), ns, name)
	if err != nil {
		s.apiError(w, r, c, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		redirect(w, r, back, "Could not read the form: "+err.Error())
		return
	}
	overrides := map[string]string{}
	for _, p := range paramsOf(t.Spec) {
		v := strings.TrimSpace(r.PostForm.Get("config." + p.Name))
		if v != "" && v != p.Default {
			overrides[p.Name] = v
		}
		if v == "" && p.Required {
			redirect(w, r, back, fmt.Sprintf("Parameter %s is required.", p.Name))
			return
		}
	}
	run := &testsv1alpha1.TestRun{Spec: testsv1alpha1.TestRunSpec{TestRef: name, Config: overrides}}
	run.GenerateName = generatePrefix(name)
	if v := strings.TrimSpace(r.PostForm.Get("notBefore")); v != "" {
		at, err := time.ParseInLocation("2006-01-02T15:04", v, time.UTC)
		if err != nil {
			redirect(w, r, back, "Start time must look like 2026-10-07T18:30 (UTC).")
			return
		}
		nb := metav1.NewTime(at)
		run.Spec.NotBefore = &nb
	}
	created, err := client.CreateRun(r.Context(), ns, run)
	if err != nil {
		redirect(w, r, back, "Could not start the run: "+messageOf(err))
		return
	}
	notice := "Run started."
	if run.Spec.NotBefore != nil && run.Spec.NotBefore.After(time.Now()) {
		notice = "Run scheduled for " + run.Spec.NotBefore.UTC().Format("2006-01-02 15:04 UTC") + "."
	}
	redirect(w, r, runPath(c.Name, ns, created.Name), notice)
}

// generatePrefix keeps generateName within the 63-character run name
// (the API server appends 5 characters).
func generatePrefix(test string) string {
	if len(test) > 50 {
		test = strings.TrimRight(test[:50], "-.")
	}
	return test + "-"
}

// --- run page ----------------------------------------------------------------------

type stepRow struct {
	Key, Kind string
	Step      apiclient.StepResult
}

type runData struct {
	Cluster      string
	Run          *apiclient.Run
	Steps        []stepRow
	Artifacts    []apiclient.Artifact
	ArtifactsErr string
	// FailedCases are the run's failed and errored JUnit test cases
	// (finished runs that reached run history).
	FailedCases []apiclient.TestCase
	// Media are the screenshots and videos among the artifacts;
	// MoreMedia counts those past the gallery's limit.
	Media     []mediaItem
	MoreMedia int
	Live      bool
	Path      string
}

func stepKind(key string) string {
	switch {
	case strings.HasPrefix(key, "attempt-"):
		return "Try"
	case strings.HasPrefix(key, "worker-"):
		return "Worker"
	}
	return "Step"
}

func (s *Server) runPage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	ns, id := r.PathValue("ns"), r.PathValue("id")
	client := api(r, c)
	run, err := client.GetRun(r.Context(), ns, id)
	if err != nil {
		s.apiError(w, r, c, err)
		return
	}
	data := runData{Cluster: c.Name, Run: run, Live: !finished(run.Phase), Path: runPath(c.Name, ns, id)}
	for _, key := range sortedStepKeys(run.Steps) {
		data.Steps = append(data.Steps, stepRow{Key: key, Kind: stepKind(key), Step: run.Steps[key]})
	}
	if arts, err := client.ListArtifacts(r.Context(), ns, id); err != nil {
		data.ArtifactsErr = messageOf(err)
	} else {
		data.Artifacts = arts
		data.Media, data.MoreMedia = mediaOf(arts)
	}
	if !data.Live && run.TestCounts != nil && run.TestCounts.Failed > 0 {
		// Best effort: without run history there are only the counts.
		data.FailedCases, _ = client.RunTestCases(r.Context(), ns, id, true)
	}
	crumbs := append(clusterCrumbs(c), views.Crumb{Label: run.TestRef, Href: testPath(c.Name, ns, run.TestRef)})
	s.page(w, r, "run", run.Name, crumbs, data)
}

func finished(phase string) bool { return finalPhases[phase] }

func sortedStepKeys(m map[string]apiclient.StepResult) []string {
	keys := slices.Collect(maps.Keys(m))
	slices.SortFunc(keys, views.NaturalCompare)
	return keys
}

// runLogPoll serves the live log from ?offset on (at most maxLogPoll
// bytes), with the run's phase so the page knows when to stop and reload.
func (s *Server) runLogPoll(w http.ResponseWriter, r *http.Request) {
	c := s.Clusters.Get(r.PathValue("cluster"))
	if c == nil {
		http.NotFound(w, r)
		return
	}
	ns, id := r.PathValue("ns"), r.PathValue("id")
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		offset = 0
	}
	client := api(r, c)
	run, err := client.GetRun(r.Context(), ns, id)
	if err != nil {
		http.Error(w, messageOf(err), http.StatusBadGateway)
		return
	}
	body, err := client.OpenLogsFrom(r.Context(), ns, id, offset)
	if err != nil {
		http.Error(w, messageOf(err), http.StatusBadGateway)
		return
	}
	defer func() { _ = body.Close() }()
	chunk, err := io.ReadAll(io.LimitReader(body, maxLogPoll+1))
	if err != nil {
		http.Error(w, "reading the log: "+err.Error(), http.StatusBadGateway)
		return
	}
	more := len(chunk) > maxLogPoll
	if more {
		chunk = chunk[:maxLogPoll]
	}
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Run-Phase", run.Phase)
	h.Set("X-Next-Offset", strconv.FormatInt(offset+int64(len(chunk)), 10))
	if more {
		h.Set("X-More", "1")
	}
	_, _ = w.Write(chunk)
}

// runLogDownload streams the whole stored log as a file.
func (s *Server) runLogDownload(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	ns, id := r.PathValue("ns"), r.PathValue("id")
	body, err := api(r, c).OpenLogs(r.Context(), ns, id)
	if err != nil {
		s.apiError(w, r, c, err)
		return
	}
	defer func() { _ = body.Close() }()
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", safeName(id)+".log"))
	_, _ = io.Copy(w, body)
}

// runArtifact streams one artifact. Workload-produced content: sandboxed
// (no script runs with Control Center's origin), never sniffed.
func (s *Server) runArtifact(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	ns, id, path := r.PathValue("ns"), r.PathValue("id"), r.PathValue("path")
	st, err := api(r, c).OpenArtifact(r.Context(), ns, id, path)
	if err != nil {
		s.apiError(w, r, c, err)
		return
	}
	defer func() { _ = st.Body.Close() }()
	h := w.Header()
	ct := st.ContentType
	if ct == "" || strings.HasPrefix(ct, "application/octet-stream") {
		// A video recorded without a type would not play inline.
		ct = cmp.Or(mediaTypes[strings.ToLower(pathpkg.Ext(path))], ct)
	}
	h.Set("Content-Type", ct)
	// Artifacts are test output: HTML reports (JMeter, Gatling,
	// Playwright, k6) need their scripts, so scripts run — but in a
	// sandbox without allow-same-origin, i.e. an opaque origin that can't
	// read Control Center's cookies or pages, submit forms or navigate
	// the top window.
	h.Set("Content-Security-Policy", artifactCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	file := path[strings.LastIndex(path, "/")+1:]
	h.Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, safeName(file)))
	if st.Length >= 0 {
		h.Set("Content-Length", strconv.FormatInt(st.Length, 10))
	}
	_, _ = io.Copy(w, st.Body)
}

// safeName keeps a file name header-safe.
func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == '"' || r == '\\' || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// --- run actions -------------------------------------------------------------------

func (s *Server) abortRun(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	ns, id := r.PathValue("ns"), r.PathValue("id")
	back := runPath(c.Name, ns, id)
	if r.PostFormValue("back") == backToSchedules {
		back = schedulesPath(c.Name)
	}
	if _, err := api(r, c).AbortRun(r.Context(), ns, id, strings.TrimSpace(r.PostFormValue("message"))); err != nil {
		redirect(w, r, back, "Could not abort: "+messageOf(err))
		return
	}
	redirect(w, r, back, "Abort requested.")
}

func (s *Server) commentRun(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	ns, id := r.PathValue("ns"), r.PathValue("id")
	back := runPath(c.Name, ns, id)
	client := api(r, c)
	text := strings.TrimSpace(r.PostFormValue(formComment))
	if text == "" {
		if err := client.DeleteComment(r.Context(), ns, id); err != nil {
			redirect(w, r, back, "Could not remove the comment: "+messageOf(err))
			return
		}
		redirect(w, r, back, "Comment removed.")
		return
	}
	if _, err := client.SetComment(r.Context(), ns, id, text); err != nil {
		redirect(w, r, back, "Could not save the comment: "+messageOf(err))
		return
	}
	redirect(w, r, back, "Comment saved.")
}

func (s *Server) deleteRun(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	ns, id := r.PathValue("ns"), r.PathValue("id")
	client := api(r, c)
	run, err := client.GetRun(r.Context(), ns, id)
	if err != nil {
		s.apiError(w, r, c, err)
		return
	}
	if err := client.DeleteRun(r.Context(), ns, id); err != nil {
		redirect(w, r, runPath(c.Name, ns, id), "Could not delete: "+messageOf(err))
		return
	}
	redirect(w, r, testPath(c.Name, ns, run.TestRef), "Run "+run.Name+" deleted.")
}
