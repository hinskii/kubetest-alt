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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hinskii/kubetest-alt/internal/controlcenter/auth"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/clusters"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/yamlview"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// maxBody bounds a request body.
const maxBody = 64 << 10

// TestList is every Test in a cluster; the UI groups and filters it.
type TestList struct {
	Tests []TestSummary `json:"tests" required:"true" nullable:"false"`
	// Range is the window the Tests' stats are over (?range=).
	Range string `json:"range" required:"true" enum:"24h,7d,30d"`
	// StatsUnavailable says why there are no stats when the cluster keeps
	// no run history.
	StatsUnavailable string `json:"statsUnavailable,omitempty"`
}

// TestSummary is a Test as the tests list shows it.
type TestSummary struct {
	Name      string `json:"name" required:"true"`
	Namespace string `json:"namespace" required:"true"`
	// Tool is the kubetest.io/tool label, "" without one.
	Tool string `json:"tool" required:"true"`
	// Locked: managed outside the GUI — read-only here, runs allowed.
	Locked bool `json:"locked" required:"true"`
	// Problem is why the Test can't run as it is (its Ready condition).
	Problem   string  `json:"problem,omitempty"`
	LatestRun *RunRef `json:"latestRun,omitempty"`
	// Stats are the Test's finished runs over the range; nil without run
	// history (StatsUnavailable) or without any run.
	Stats *TestStats `json:"stats,omitempty"`
}

// TestStats are a Test's finished runs over the list's range.
type TestStats struct {
	Total   int `json:"total" required:"true"`
	Passed  int `json:"passed" required:"true"`
	Failed  int `json:"failed" required:"true"`
	Errored int `json:"errored" required:"true"`
	Aborted int `json:"aborted" required:"true"`
	// PassRatio is passed of the runs with a verdict (passed, failed,
	// error), 0..1; absent without any.
	PassRatio *float64 `json:"passRatio,omitempty"`
	// P50Ms and P95Ms are over passed and failed runs; 0 without any.
	P50Ms int64 `json:"p50Ms" required:"true"`
	P95Ms int64 `json:"p95Ms" required:"true"`
	// Recent are the Test's newest runs, oldest first (also before the
	// range).
	Recent []RecentRun `json:"recent" required:"true" nullable:"false"`
}

// RecentRun is one of a Test's newest runs.
type RecentRun struct {
	// ID finds the run (its name).
	ID         string    `json:"id" required:"true"`
	Phase      string    `json:"phase" required:"true"`
	DurationMs int64     `json:"durationMs" required:"true"`
	FinishedAt time.Time `json:"finishedAt" required:"true"`
}

// RecentRuns is how many of a Test's newest runs the list shows.
const RecentRuns = 10

// RunRef is a Test's latest run.
type RunRef struct {
	Name       string     `json:"name" required:"true"`
	Phase      string     `json:"phase" required:"true"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// TestDetail is a Test as its page shows it: templates merged.
type TestDetail struct {
	Name      string   `json:"name" required:"true"`
	Namespace string   `json:"namespace" required:"true"`
	Tool      string   `json:"tool" required:"true"`
	Locked    bool     `json:"locked" required:"true"`
	Templates []string `json:"templates" required:"true" nullable:"false"`
	Problem   string   `json:"problem,omitempty"`
	// Params are spec.config: what a run may set.
	Params   []Param   `json:"params" required:"true" nullable:"false"`
	Schedule *Schedule `json:"schedule,omitempty"`
	// Definition is the spec, templates merged and expressions not
	// evaluated, as YAML in reading order.
	Definition string `json:"definition" required:"true"`
}

// Param is one spec.config parameter.
type Param struct {
	Name string `json:"name" required:"true"`
	Type string `json:"type" required:"true" enum:"string,integer,number,boolean"`
	// Default is "" for a required parameter.
	Default  string   `json:"default,omitempty"`
	Pattern  string   `json:"pattern,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	Required bool     `json:"required" required:"true"`
	// Description says what the parameter is for.
	Description string `json:"description,omitempty"`
}

// Schedule is a Test's recurring schedule (spec.schedule).
type Schedule struct {
	Expr string `json:"expr" required:"true"`
	// Next are the upcoming fire times, UTC.
	Next []time.Time `json:"next" required:"true" nullable:"false"`
	// Error: the expression doesn't parse (it never fires).
	Error string `json:"error,omitempty"`
}

// ScheduleUpdate sets a Test's schedule; "" removes it.
type ScheduleUpdate struct {
	Schedule string `json:"schedule" required:"true"`
}

// ScheduleResult is the Test's schedule after an update.
type ScheduleResult struct {
	Schedule *Schedule `json:"schedule,omitempty"`
}

func (s *Server) listTests(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	rangeName := r.URL.Query().Get("range")
	if _, ok := Ranges[rangeName]; !ok {
		rangeName = "7d"
	}
	// The stats come from run history, alongside the Tests.
	var stats []apiclient.TestStats
	var statsErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		stats, statsErr = client.TestStats(r.Context(), "", rangeName, RecentRuns)
	}()
	tests, err := client.ListTests(r.Context(), "")
	<-done
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	out := TestList{Tests: make([]TestSummary, 0, len(tests)), Range: rangeName}
	byTest := map[string]*TestStats{}
	switch {
	case apiclient.IsUnavailable(statsErr):
		out.StatsUnavailable = messageOf(statsErr)
	case statsErr != nil:
		// The list stands without them; say why they're missing.
		out.StatsUnavailable = "run statistics failed: " + messageOf(statsErr)
	default:
		for _, st := range stats {
			byTest[st.Namespace+"/"+st.Test] = statsOut(st)
		}
	}
	for _, t := range tests {
		row := TestSummary{Name: t.Name, Namespace: t.Namespace, Tool: t.Labels[LabelTool],
			Locked: Locked(t.Labels), Problem: NotReady(t.Status.Conditions),
			Stats: byTest[t.Namespace+"/"+t.Name]}
		if lr := t.Status.LatestRun; lr != nil {
			row.LatestRun = &RunRef{Name: lr.Name, Phase: string(lr.Phase)}
			if lr.FinishedAt != nil {
				at := lr.FinishedAt.UTC()
				row.LatestRun.FinishedAt = &at
			}
		}
		out.Tests = append(out.Tests, row)
	}
	slices.SortFunc(out.Tests, func(a, b TestSummary) int {
		return cmp.Or(strings.Compare(a.Namespace, b.Namespace), strings.Compare(a.Name, b.Name))
	})
	writeJSON(w, http.StatusOK, out)
}

// statsOut is a Test's stats for the list: recent runs oldest first, as
// the marks read left to right.
func statsOut(st apiclient.TestStats) *TestStats {
	out := &TestStats{Total: st.Total, Passed: st.Passed, Failed: st.Failed, Errored: st.Errored,
		Aborted: st.Aborted, PassRatio: st.PassRatio, P50Ms: st.P50Ms, P95Ms: st.P95Ms,
		Recent: make([]RecentRun, 0, len(st.Recent))}
	for _, rr := range slices.Backward(st.Recent) {
		out.Recent = append(out.Recent, RecentRun{ID: rr.Name, Phase: rr.Phase, DurationMs: rr.DurationMs,
			FinishedAt: rr.FinishedAt.UTC()})
	}
	return out
}

func (s *Server) getTest(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	t, err := client.GetResolvedTest(r.Context(), r.PathValue("ns"), r.PathValue("name"))
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	out := TestDetail{Name: t.Name, Namespace: t.Namespace, Tool: t.Tool, Locked: t.GitOpsLocked,
		Templates: t.Templates, Problem: NotReady(t.Conditions), Params: ParamsOf(t.Spec)}
	if out.Templates == nil {
		out.Templates = []string{}
	}
	if t.Spec != nil {
		out.Schedule = ScheduleOf(t.Spec.Schedule, time.Now())
		if out.Definition, err = yamlview.Format(t.Spec, yamlview.Spec); err != nil {
			out.Definition = "# cannot show the definition: " + err.Error()
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteTest(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	if err := client.DeleteTest(r.Context(), r.PathValue("ns"), r.PathValue("name")); err != nil {
		s.apiError(w, c, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setSchedule(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	var in ScheduleUpdate
	if !decode(w, r, &in) {
		return
	}
	expr := strings.Join(strings.Fields(in.Schedule), " ")
	if expr != "" {
		if _, err := NextFires(expr, time.Now(), 1); err != nil {
			writeError(w, http.StatusBadRequest, "Not a valid cron expression: "+err.Error())
			return
		}
	}
	if _, err := client.SetTestSchedule(r.Context(), r.PathValue("ns"), r.PathValue("name"), expr); err != nil {
		if apiclient.IsConflict(err) {
			writeError(w, http.StatusConflict, "This Test is managed in Git: change spec.schedule there.")
			return
		}
		s.apiError(w, c, err)
		return
	}
	writeJSON(w, http.StatusOK, ScheduleResult{Schedule: ScheduleOf(expr, time.Now())})
}

// --- plumbing -----------------------------------------------------------------

// clusterOf resolves {cluster} and the kubetest API acting for the
// signed-in user; a JSON 404 when the cluster isn't configured.
func (s *Server) clusterOf(w http.ResponseWriter, r *http.Request) (*clusters.Cluster, *apiclient.Client, bool) {
	c := s.Clusters.Get(r.PathValue("cluster"))
	if c == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("No cluster named %q is configured.", r.PathValue("cluster")))
		return nil, nil, false
	}
	return c, c.API.AsUser(auth.FromContext(r.Context()).Email), true
}

// apiError answers a failed kubetest API call: the API's own 4xx with its
// message, anything else as a bad gateway.
func (s *Server) apiError(w http.ResponseWriter, c *clusters.Cluster, err error) {
	var apiErr *apiclient.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500:
		writeError(w, apiErr.Status, apiErr.Message)
	case errors.As(err, &apiErr):
		writeError(w, http.StatusBadGateway, "The kubetest API of "+c.DisplayNameOrName()+" answered: "+apiErr.Message)
	default:
		s.Log.Warn("kubetest API unreachable", "cluster", c.Name, "err", err)
		writeError(w, http.StatusBadGateway, "Cannot reach the kubetest API of "+c.DisplayNameOrName()+".")
	}
}

// messageOf is the API's own message of err, else err's text.
func messageOf(err error) string {
	var apiErr *apiclient.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Message
	}
	return err.Error()
}

// requireRole gates h on a role, answering 403 in JSON.
func requireRole(min auth.Role, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u := auth.FromContext(r.Context()); !u.Can(min) {
			writeError(w, http.StatusForbidden,
				fmt.Sprintf("This action requires the %s role; you are %s.", min, u.Role))
			return
		}
		h(w, r)
	}
}

// decode reads a JSON body into v, answering 400 when it can't.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "Could not read the request: "+err.Error())
		return false
	}
	return true
}
