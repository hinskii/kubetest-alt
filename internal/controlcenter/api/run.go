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
	"io"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// MaxChildren bounds a composite run's child list.
const MaxChildren = 200

// MaxLogChunk bounds one log response; the UI asks again at once while
// X-More says there is more.
const MaxLogChunk = 1 << 20

// finalPhases are the phases a run never leaves.
var finalPhases = map[string]bool{
	string(testsv1alpha1.PhasePassed): true, string(testsv1alpha1.PhaseFailed): true,
	string(testsv1alpha1.PhaseError): true, string(testsv1alpha1.PhaseAborted): true,
}

// Finished reports a run phase it never leaves.
func Finished(phase string) bool { return finalPhases[phase] }

// RunDetail is a run as its page shows it.
type RunDetail struct {
	// ID finds the run (its name, or its UID once only history has it).
	ID        string `json:"id" required:"true"`
	Name      string `json:"name" required:"true"`
	Namespace string `json:"namespace" required:"true"`
	TestRef   string `json:"testRef" required:"true"`
	Phase     string `json:"phase" required:"true"`
	// Finished: the phase is final (passed, failed, error, aborted).
	Finished bool   `json:"finished" required:"true"`
	Tool     string `json:"tool,omitempty"`
	Message  string `json:"message,omitempty"`
	// Archived: only run history has it (the TestRun is gone).
	Archived   bool              `json:"archived" required:"true"`
	ParentRun  string            `json:"parentRun,omitempty"`
	Source     string            `json:"source,omitempty"`
	CreatedBy  string            `json:"createdBy,omitempty"`
	NotBefore  *time.Time        `json:"notBefore,omitempty"`
	QueuedAt   *time.Time        `json:"queuedAt,omitempty"`
	StartedAt  *time.Time        `json:"startedAt,omitempty"`
	FinishedAt *time.Time        `json:"finishedAt,omitempty"`
	DurationMs int64             `json:"durationMs,omitempty"`
	Git        *GitInfo          `json:"git,omitempty"`
	TestCounts *TestCounts       `json:"testCounts,omitempty"`
	Abort      *Abort            `json:"abort,omitempty"`
	Config     map[string]string `json:"config" required:"true" nullable:"false"`
	Metrics    []Metric          `json:"metrics" required:"true" nullable:"false"`
	Steps      []Step            `json:"steps" required:"true" nullable:"false"`
	// Stages are a leaf run's course: queued, fetch, run, collect (step
	// 20a); empty for a composite or parallel run, and for runs from before.
	Stages []Stage `json:"stages" required:"true" nullable:"false"`
	// Children are a composite run's child runs, by name.
	Children []ChildRun `json:"children" required:"true" nullable:"false"`
	// Report is the artifact path of the tool's main report.
	Report string `json:"report,omitempty"`
	// LiveView is the tool's live UI by signed link, while the run isn't
	// finished. A new link is signed on every request: keep the first.
	LiveView  string      `json:"liveView,omitempty"`
	Comment   *RunComment `json:"comment,omitempty"`
	Artifacts []Artifact  `json:"artifacts" required:"true" nullable:"false"`
	// ArtifactsError: the artifact list couldn't be read.
	ArtifactsError string `json:"artifactsError,omitempty"`
	// Media are the screenshots and videos among the artifacts; MoreMedia
	// counts those past the gallery's limit.
	Media     []Media `json:"media" required:"true" nullable:"false"`
	MoreMedia int     `json:"moreMedia,omitempty"`
	// Events are Kubernetes events of the run's objects (about an hour
	// back), oldest first.
	Events      []Event `json:"events" required:"true" nullable:"false"`
	EventsError string  `json:"eventsError,omitempty"`
	// FailedCases are the failed and errored JUnit cases of a finished
	// run that reached run history.
	FailedCases []TestCase `json:"failedCases" required:"true" nullable:"false"`
}

// GitInfo is the code a finished run checked out.
type GitInfo struct {
	Repository string `json:"repository,omitempty"`
	Revision   string `json:"revision,omitempty"`
	Commit     Commit `json:"commit" required:"true"`
}

// TestCounts are a run's JUnit totals.
type TestCounts struct {
	Total   int `json:"total" required:"true"`
	Passed  int `json:"passed" required:"true"`
	Failed  int `json:"failed" required:"true"`
	Skipped int `json:"skipped" required:"true"`
}

// Abort is who asked the run to stop, and why.
type Abort struct {
	Reason      string `json:"reason" required:"true"`
	RequestedBy string `json:"requestedBy,omitempty"`
	Message     string `json:"message,omitempty"`
}

// Metric is one value a run reported (k6 p95, error rate, …).
type Metric struct {
	Name  string  `json:"name" required:"true"`
	Value float64 `json:"value" required:"true"`
}

// Step is one try, worker or composite step of a run.
type Step struct {
	Key string `json:"key" required:"true"`
	// Kind is Try, Worker or Step.
	Kind       string     `json:"kind" required:"true" enum:"Try,Worker,Step"`
	Phase      string     `json:"phase,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Message    string     `json:"message,omitempty"`
	// Stages and Params: a parallel worker's own course and its matrix
	// and shard values.
	Stages []Stage           `json:"stages,omitempty"`
	Params map[string]string `json:"params,omitempty"`
	// LiveView is a parallel worker's live UI by signed link, until the
	// worker finishes (a parallel run has no live view of its own).
	LiveView string `json:"liveView,omitempty"`
	// Report is a parallel worker's own report (its artifact path).
	Report string `json:"report,omitempty"`
}

// Stage is one part of a leaf run's course.
type Stage struct {
	Kind       string     `json:"kind" required:"true" enum:"queue,fetch,run,collect"`
	Name       string     `json:"name" required:"true"`
	Phase      string     `json:"phase" required:"true"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Message    string     `json:"message,omitempty"`
}

// ChildRun is one run of a composite run.
type ChildRun struct {
	ID         string     `json:"id" required:"true"`
	Name       string     `json:"name" required:"true"`
	TestRef    string     `json:"testRef" required:"true"`
	Phase      string     `json:"phase" required:"true"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	DurationMs int64      `json:"durationMs,omitempty"`
}

// RunComment is a note on a finished run.
type RunComment struct {
	Text string    `json:"text" required:"true"`
	By   string    `json:"by,omitempty"`
	At   time.Time `json:"at" required:"true"`
}

// Artifact is one file a run stored.
type Artifact struct {
	Path        string `json:"path" required:"true"`
	ContentType string `json:"contentType,omitempty"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
}

// Event is a Kubernetes event about one of the run's objects.
type Event struct {
	Time    time.Time `json:"time" required:"true"`
	Type    string    `json:"type" required:"true" enum:"Normal,Warning"`
	Reason  string    `json:"reason" required:"true"`
	Object  string    `json:"object" required:"true"`
	Message string    `json:"message" required:"true"`
	Count   int32     `json:"count,omitempty"`
}

// TestCase is one JUnit case of a run.
type TestCase struct {
	Key        string `json:"key" required:"true"`
	Name       string `json:"name" required:"true"`
	Suite      string `json:"suite,omitempty"`
	Class      string `json:"class,omitempty"`
	Status     string `json:"status" required:"true"`
	DurationMs int64  `json:"durationMs,omitempty"`
	Message    string `json:"message,omitempty"`
	Details    string `json:"details,omitempty"`
}

// AbortRequest stops a run.
type AbortRequest struct {
	Message string `json:"message,omitempty" maxLength:"512"`
}

// CommentUpdate sets a finished run's comment.
type CommentUpdate struct {
	Text string `json:"text" required:"true" maxLength:"500"`
}

// stepKind names a step key: attempt-N (retries), worker-N (parallel).
func stepKind(key string) string {
	switch {
	case strings.HasPrefix(key, "attempt-"):
		return "Try"
	case strings.HasPrefix(key, "worker-"):
		return "Worker"
	}
	return "Step"
}

// isComposite: a composite run's status has per-step aggregates s0, s1, …
func isComposite(steps map[string]apiclient.StepResult) bool {
	for k := range steps {
		if len(k) > 1 && k[0] == 's' && strings.Trim(k[1:], "0123456789") == "" {
			return true
		}
	}
	return false
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	ns, id := r.PathValue("ns"), r.PathValue("id")
	run, err := client.GetRun(r.Context(), ns, id)
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	out := RunDetail{ID: RunID(*run), Name: run.Name, Namespace: run.Namespace, TestRef: run.TestRef,
		Phase: run.Phase, Finished: Finished(run.Phase), Tool: run.Tool, Message: run.Message,
		Archived: run.Origin == apiclient.OriginArchive, ParentRun: run.ParentRun, Source: run.Source,
		CreatedBy: run.Tags[TagCreatedBy], NotBefore: utc(run.NotBefore), QueuedAt: utc(run.QueuedAt),
		StartedAt: utc(run.StartedAt), FinishedAt: utc(run.FinishedAt), DurationMs: run.DurationMs,
		Report: run.Report, Config: run.Config, Metrics: []Metric{}, Steps: []Step{}, Stages: []Stage{}, Children: []ChildRun{},
		Artifacts: []Artifact{}, Media: []Media{}, Events: []Event{}, FailedCases: []TestCase{}}
	if out.Config == nil {
		out.Config = map[string]string{}
	}
	if g := run.Git; g != nil && g.Commit != "" {
		out.Git = &GitInfo{Repository: g.URI, Revision: g.Revision,
			Commit: Commit{SHA: g.Commit, URL: CommitURL(g.URI, g.Commit)}}
	}
	if tc := run.TestCounts; tc != nil {
		out.TestCounts = &TestCounts{Total: tc.Total, Passed: tc.Passed, Failed: tc.Failed, Skipped: tc.Skipped}
	}
	if a := run.Abort; a != nil {
		out.Abort = &Abort{Reason: a.Reason, RequestedBy: a.RequestedBy, Message: a.Message}
	}
	if cm := run.Comment; cm != nil {
		out.Comment = &RunComment{Text: cm.Text, By: cm.By, At: cm.At.UTC()}
	}
	for _, k := range slices.Sorted(maps.Keys(run.Metrics)) {
		out.Metrics = append(out.Metrics, Metric{Name: k, Value: run.Metrics[k]})
	}
	keys := slices.Collect(maps.Keys(run.Steps))
	slices.SortFunc(keys, NaturalCompare)
	for _, k := range keys {
		st := run.Steps[k]
		out.Steps = append(out.Steps, Step{Key: k, Kind: stepKind(k), Phase: st.Phase,
			StartedAt: utc(st.StartedAt), FinishedAt: utc(st.FinishedAt), Message: st.Message,
			Stages: stagesOut(st.Stages), Params: st.Params, Report: st.Report})
	}
	out.Stages = append(out.Stages, stagesOut(run.Stages)...)
	if run.LiveView != "" && !out.Finished && s.LiveURL != nil {
		parallel := false
		for i, st := range out.Steps {
			if st.Kind != "Worker" {
				continue
			}
			parallel = true
			if !Finished(st.Phase) {
				out.Steps[i].LiveView = s.LiveURL(c.Name, ns, out.ID, strings.TrimPrefix(st.Key, "worker-"), run.LiveView)
			}
		}
		if !parallel {
			out.LiveView = s.LiveURL(c.Name, ns, out.ID, "", run.LiveView)
		}
	}
	if arts, err := client.ListArtifacts(r.Context(), ns, id); err != nil {
		out.ArtifactsError = messageOf(err)
	} else {
		for _, a := range arts {
			out.Artifacts = append(out.Artifacts, Artifact{Path: a.Path, ContentType: a.ContentType, SizeBytes: a.SizeBytes})
		}
		out.Media, out.MoreMedia = mediaOf(arts)
	}
	if events, err := client.RunEvents(r.Context(), ns, id); err != nil {
		out.EventsError = messageOf(err)
	} else {
		for _, e := range events {
			out.Events = append(out.Events, Event{Time: e.Time.UTC(), Type: e.Type, Reason: e.Reason,
				Object: e.Object, Message: e.Message, Count: e.Count})
		}
	}
	if isComposite(run.Steps) {
		if page, err := client.ListRuns(r.Context(), apiclient.ListRunsOptions{
			Namespace: ns, Parent: run.Name, Limit: MaxChildren}); err == nil {
			for _, ch := range page.Runs {
				out.Children = append(out.Children, ChildRun{ID: RunID(ch), Name: ch.Name, TestRef: ch.TestRef,
					Phase: ch.Phase, StartedAt: utc(ch.StartedAt), DurationMs: ch.DurationMs})
			}
			slices.SortFunc(out.Children, func(a, b ChildRun) int { return NaturalCompare(a.Name, b.Name) })
		}
	}
	if out.Finished && run.TestCounts != nil && run.TestCounts.Failed > 0 {
		// Best effort: without run history there are only the counts.
		cases, _ := client.RunTestCases(r.Context(), ns, id, true)
		for _, tc := range cases {
			out.FailedCases = append(out.FailedCases, TestCase{Key: tc.Key, Name: tc.Name, Suite: tc.Suite,
				Class: tc.Class, Status: tc.Status, DurationMs: tc.DurationMs, Message: tc.Message, Details: tc.Details})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// stageLog names a stage with a log of its own: the fetch, a worker.
var stageLog = regexp.MustCompile(`^(fetch|worker-[0-9]+(/fetch)?)$`)

// runLog serves the run's log from ?offset= on (at most MaxLogChunk
// bytes), with the run's phase, so the UI knows when to stop polling.
func (s *Server) runLog(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	ns, id := r.PathValue("ns"), r.PathValue("id")
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		offset = 0
	}
	run, err := client.GetRun(r.Context(), ns, id)
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	stage := r.URL.Query().Get("stage")
	if stage != "" && !stageLog.MatchString(stage) {
		writeError(w, http.StatusBadRequest, "stage: fetch, worker-N or worker-N/fetch")
		return
	}
	body, err := client.OpenStageLogsFrom(r.Context(), ns, id, stage, offset)
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	defer func() { _ = body.Close() }()
	chunk, err := io.ReadAll(io.LimitReader(body, MaxLogChunk+1))
	if err != nil {
		writeError(w, http.StatusBadGateway, "Reading the log: "+err.Error())
		return
	}
	more := len(chunk) > MaxLogChunk
	if more {
		chunk = chunk[:MaxLogChunk]
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

func (s *Server) abortRun(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	var in AbortRequest
	if !decode(w, r, &in) {
		return
	}
	if _, err := client.AbortRun(r.Context(), r.PathValue("ns"), r.PathValue("id"), strings.TrimSpace(in.Message)); err != nil {
		s.apiError(w, c, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// rerun starts the run's Test again with the parameters that run used
// (its effective config from run history, so it works after the TestRun
// left the cluster). Parameters the Test no longer has are dropped;
// values equal to today's defaults are left to the default.
func (s *Server) rerun(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	ns, id := r.PathValue("ns"), r.PathValue("id")
	old, err := client.GetRun(r.Context(), ns, id)
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	t, err := client.GetResolvedTest(r.Context(), ns, old.TestRef)
	if err != nil {
		if apiclient.IsNotFound(err) {
			writeError(w, http.StatusConflict, fmt.Sprintf("Test %s no longer exists.", old.TestRef))
			return
		}
		s.apiError(w, c, err)
		return
	}
	overrides := map[string]string{}
	for _, p := range ParamsOf(t.Spec) {
		v, had := old.Config[p.Name]
		switch {
		case had && v != p.Default:
			overrides[p.Name] = v
		case !had && p.Required:
			writeError(w, http.StatusConflict,
				fmt.Sprintf("The Test now requires parameter %s — start it from the Test page.", p.Name))
			return
		}
	}
	run := &testsv1alpha1.TestRun{Spec: testsv1alpha1.TestRunSpec{TestRef: old.TestRef, Config: overrides}}
	run.GenerateName = GeneratePrefix(old.TestRef)
	created, err := client.CreateRun(r.Context(), ns, run)
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	writeJSON(w, http.StatusCreated, StartedRun{Name: created.Name, Namespace: ns})
}

func (s *Server) setComment(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	var in CommentUpdate
	if !decode(w, r, &in) {
		return
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, "The comment is empty; delete it instead.")
		return
	}
	cm, err := client.SetComment(r.Context(), r.PathValue("ns"), r.PathValue("id"), text)
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	writeJSON(w, http.StatusOK, RunComment{Text: cm.Text, By: cm.By, At: cm.At.UTC()})
}

func (s *Server) deleteComment(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	if err := client.DeleteComment(r.Context(), r.PathValue("ns"), r.PathValue("id")); err != nil {
		s.apiError(w, c, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteRun(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	if err := client.DeleteRun(r.Context(), r.PathValue("ns"), r.PathValue("id")); err != nil {
		s.apiError(w, c, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// stagesOut is a run's or a worker's stages for the UI.
func stagesOut(stages []apiclient.Stage) []Stage {
	if len(stages) == 0 {
		return nil
	}
	out := make([]Stage, 0, len(stages))
	for _, st := range stages {
		out = append(out, Stage{Kind: st.Kind, Name: st.Name, Phase: cmp.Or(st.Phase, "queued"),
			StartedAt: utc(st.StartedAt), FinishedAt: utc(st.FinishedAt), Message: st.Message})
	}
	return out
}
