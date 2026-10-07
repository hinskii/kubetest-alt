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

package apiserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/google/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/controller"
	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// TagCreatedBy records who started a run from the GUI (X-Kubetest-User).
// Server-owned: a payload value is overwritten when the header is present.
const TagCreatedBy = apiclient.TagCreatedBy

// Wire types live in pkg/apiclient (one contract for server and client).
type (
	runEnvelope = apiclient.Run
	stepResult  = apiclient.StepResult
)

// createRun creates a TestRun. Two invariants (§7):
//  1. source is set to "ui" server-side; payload override rejected.
//  2. Runs against GitOps-managed Tests are ALLOWED — the enforcement rule
//     from §7 says runs are ephemeral children, not definitions. We do NOT
//     block based on the referenced Test's managed-by label.
func (s *Server) createRun(w http.ResponseWriter, r *http.Request) {
	var run testsv1alpha1.TestRun
	if err := json.NewDecoder(r.Body).Decode(&run); err != nil {
		writeError(w, http.StatusBadRequest, ReasonBadRequest,
			fmt.Sprintf("decode: %v", err))
		return
	}
	if run.Spec.Source != "" && run.Spec.Source != "ui" {
		writeError(w, http.StatusBadRequest, ReasonBadRequest,
			fmt.Sprintf("payload sets source=%q; the API server owns this field — omit it",
				run.Spec.Source))
		return
	}
	run.Spec.Source = "ui"
	if u := requestUser(r); u != "" {
		if run.Spec.Tags == nil {
			run.Spec.Tags = map[string]string{}
		}
		run.Spec.Tags[TagCreatedBy] = u
	}
	ns, err := s.targetNamespace(r, run.Namespace)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	run.Namespace = ns
	if err := s.K8sClient.Create(r.Context(), &run); err != nil {
		writeAPIError(w, err)
		return
	}
	details := map[string]string{apiclient.AuditDetailTest: run.Spec.TestRef, apiclient.AuditDetailUID: string(run.UID)}
	if run.Spec.NotBefore != nil {
		details[apiclient.AuditDetailNotBefore] = run.Spec.NotBefore.UTC().Format(time.RFC3339)
	}
	s.recordAudit(r, apiclient.ActionRunCreate, run.Namespace, run.Name, details)
	writeJSON(w, http.StatusCreated, run)
}

// getRun returns a single TestRun. Looks up the CR first (still active or
// recently-terminated); falls back to the store for archived runs. Both
// paths return the same wire shape via runEnvelope.
func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ReasonBadRequest, "run id is required")
		return
	}
	ns, err := s.targetNamespace(r, "")
	if err != nil {
		writeLookupError(w, err)
		return
	}
	// Cluster first ({id} = CR name), then the store ({id} = run UID).
	ref, err := s.findRun(r.Context(), ns, id)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	if ref.CR != nil {
		env := runEnvelopeFromCR(ref.CR)
		env.Comment = s.historyComment(r.Context(), ref.CR)
		writeJSON(w, http.StatusOK, env)
		return
	}
	writeJSON(w, http.StatusOK, runEnvelopeFromRow(ref.Row))
}

// historyComment returns the comment of a finished run whose CR still
// exists: comments live on the history row, which the CR doesn't carry.
// Best effort — a missing row or store error just means no comment shown.
func (s *Server) historyComment(ctx context.Context, cr *testsv1alpha1.TestRun) *apiclient.Comment {
	if s.Store == nil || !controller.IsTerminalPhase(cr.Status.Phase) {
		return nil
	}
	row, err := s.Store.Get(ctx, string(cr.UID))
	if err != nil {
		return nil
	}
	return apiComment(row.Comment)
}

// HeaderNextCursor carries the opaque cursor for the next page of
// finished runs on GET /runs. Absent when there is no next page.
const HeaderNextCursor = apiclient.HeaderNextCursor

// listRuns returns runs newest-first:
//
//  1. live runs (not yet terminal) — first page only, never paginated
//     (bounded by what is in the cluster);
//  2. finished runs ordered by (finishedAt, uid) DESC, keyset-paginated:
//     pass the previous response's X-Next-Cursor as ?after=.
//
// Finished runs come from the store, merged with terminal CRs not yet (or
// never) persisted; a run present in both appears once, with the
// cluster's fresher status. The merge is exact: the store returns its top
// `limit` rows after the cursor, the cluster's terminal CRs are filtered
// by the same cursor, and the union is cut to `limit`.
//
// Filters: test, phase, source, namespace, finishedAfter (RFC 3339 —
// finished runs only; used by Control Center's analytics catch-up).
func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	lq, err := s.parseListRunsQuery(r)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	liveOut, finished, liveUIDs, err := s.collectClusterRuns(r.Context(), lq)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	storeFull, err := s.mergeStoreRuns(r.Context(), lq, finished, liveUIDs)
	if err != nil {
		writeAPIError(w, err)
		return
	}

	slices.SortStableFunc(liveOut, func(a, b runEnvelope) int {
		return compareNewestFirst(a.StartedAt, b.StartedAt, a.UID, b.UID)
	})
	finishedOut := make([]runEnvelope, 0, len(finished))
	for _, e := range finished {
		finishedOut = append(finishedOut, e)
	}
	slices.SortStableFunc(finishedOut, func(a, b runEnvelope) int {
		return compareNewestFirst(a.FinishedAt, b.FinishedAt, a.UID, b.UID)
	})
	more := storeFull || len(finishedOut) > lq.limit
	if len(finishedOut) > lq.limit {
		finishedOut = finishedOut[:lq.limit]
	}
	if more && len(finishedOut) > 0 {
		last := finishedOut[len(finishedOut)-1]
		w.Header().Set(HeaderNextCursor, encodeRunCursor(*last.FinishedAt, last.UID))
	}

	out := append(liveOut, finishedOut...)
	if out == nil {
		out = []runEnvelope{}
	}
	writeJSON(w, http.StatusOK, out)
}

// listRunsQuery is the parsed GET /runs query.
type listRunsQuery struct {
	ns, testRef, phase, source string
	limit                      int
	finishedAfter              *time.Time
	cursor                     *runCursor
	wantLive, wantFinished     bool
}

// matches applies the test/phase/source filters to a CR.
func (q listRunsQuery) matches(cr *testsv1alpha1.TestRun) bool {
	return (q.testRef == "" || cr.Spec.TestRef == q.testRef) &&
		(q.phase == "" || string(cr.Status.Phase) == q.phase) &&
		(q.source == "" || cr.Spec.Source == q.source)
}

// parseListRunsQuery validates the query; errors are client errors (400).
func (s *Server) parseListRunsQuery(r *http.Request) (listRunsQuery, error) {
	q := r.URL.Query()
	lq := listRunsQuery{
		testRef: q.Get("test"),
		phase:   q.Get("phase"),
		source:  q.Get("source"),
		limit:   parseLimitOrDefault(q.Get("limit")),
	}
	var err error
	if lq.ns, err = s.listNamespace(r); err != nil {
		return lq, err
	}
	if v := q.Get("finishedAfter"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return lq, errBadRequest{"finishedAfter: want RFC 3339"}
		}
		lq.finishedAfter = &t
	}
	if lq.cursor, err = decodeRunCursor(q.Get("after")); err != nil {
		return lq, errBadRequest{err.Error()}
	}
	terminalPhase := lq.phase != "" && controller.IsTerminalPhase(testsv1alpha1.Phase(lq.phase))
	lq.wantLive = lq.cursor == nil && lq.finishedAfter == nil && !terminalPhase
	lq.wantFinished = lq.phase == "" || terminalPhase
	return lq, nil
}

// collectClusterRuns splits matching CRs into live runs (first page only)
// and finished runs past the cursor. liveUIDs lists every live run so the
// store merge can skip rows whose CR the informer still shows as live.
func (s *Server) collectClusterRuns(ctx context.Context, lq listRunsQuery) (
	live []runEnvelope, finished map[string]runEnvelope, liveUIDs map[string]bool, err error) {
	var list testsv1alpha1.TestRunList
	opts := []client.ListOption{}
	if lq.ns != "" {
		opts = append(opts, client.InNamespace(lq.ns))
	}
	if err := s.K8sClient.List(ctx, &list, opts...); err != nil {
		return nil, nil, nil, err
	}
	finished = map[string]runEnvelope{}
	liveUIDs = map[string]bool{}
	for i := range list.Items {
		cr := &list.Items[i]
		if !lq.matches(cr) {
			continue
		}
		env := runEnvelopeFromCR(cr)
		if !controller.IsTerminalPhase(cr.Status.Phase) {
			liveUIDs[env.UID] = true
			if lq.wantLive {
				live = append(live, env)
			}
			continue
		}
		if lq.wantFinished && finishedAfterOK(env, lq.finishedAfter) && lq.cursor.before(env) {
			finished[env.UID] = env
		}
	}
	return live, finished, liveUIDs, nil
}

// mergeStoreRuns adds the store's next page of finished runs to finished,
// skipping runs the cluster already provided (cluster wins — fresher).
// storeFull reports a full page, i.e. there may be more.
func (s *Server) mergeStoreRuns(ctx context.Context, lq listRunsQuery,
	finished map[string]runEnvelope, liveUIDs map[string]bool) (storeFull bool, err error) {
	if !lq.wantFinished || s.Store == nil {
		return false, nil
	}
	f := store.Filter{
		TestRef: lq.testRef, Namespace: lq.ns, Phase: lq.phase, Source: lq.source,
		SinceInclusive: lq.finishedAfter,
	}
	page := store.Page{Limit: lq.limit}
	if lq.cursor != nil {
		page.After, page.AfterFinishedAt = lq.cursor.UID, &lq.cursor.FinishedAt
	}
	rows, err := s.Store.List(ctx, f, page)
	if err != nil {
		return false, err
	}
	for i := range rows {
		if env, inCluster := finished[rows[i].UID]; inCluster {
			// Cluster status wins, but the comment only exists in history.
			env.Comment = apiComment(rows[i].Comment)
			finished[rows[i].UID] = env
			continue
		}
		if liveUIDs[rows[i].UID] {
			continue
		}
		finished[rows[i].UID] = runEnvelopeFromRow(&rows[i])
	}
	return len(rows) >= lq.limit, nil
}

// compareNewestFirst orders by time DESC (nil last), then uid DESC — the
// same total order as the store's keyset (finished_at DESC, uid DESC).
func compareNewestFirst(ta, tb *time.Time, ua, ub string) int {
	switch {
	case ta == nil && tb == nil:
	case ta == nil:
		return 1
	case tb == nil:
		return -1
	case ta.After(*tb):
		return -1
	case tb.After(*ta):
		return 1
	}
	return strings.Compare(ub, ua)
}

func finishedAfterOK(e runEnvelope, after *time.Time) bool {
	return after == nil || (e.FinishedAt != nil && !e.FinishedAt.Before(*after))
}

// runCursor is the keyset position of the last finished run on a page.
type runCursor struct {
	FinishedAt time.Time
	UID        string
}

// before reports whether e sorts strictly after the cursor position, i.e.
// belongs on a later page. A nil cursor admits everything.
func (c *runCursor) before(e runEnvelope) bool {
	if c == nil {
		return true
	}
	if e.FinishedAt == nil {
		return false
	}
	ft := e.FinishedAt.UTC()
	return ft.Before(c.FinishedAt) || (ft.Equal(c.FinishedAt) && e.UID < c.UID)
}

func encodeRunCursor(finishedAt time.Time, uid string) string {
	raw := finishedAt.UTC().Format(time.RFC3339Nano) + "|" + uid
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeRunCursor(s string) (*runCursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, errors.New("after: malformed cursor")
	}
	ts, uid, ok := strings.Cut(string(raw), "|")
	if !ok {
		return nil, errors.New("after: malformed cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return nil, errors.New("after: malformed cursor")
	}
	if _, err := uuid.Parse(uid); err != nil {
		return nil, errors.New("after: malformed cursor")
	}
	return &runCursor{FinishedAt: t.UTC(), UID: uid}, nil
}

func runEnvelopeFromCR(cr *testsv1alpha1.TestRun) runEnvelope {
	e := runEnvelope{
		UID:        string(cr.UID),
		Name:       cr.Name,
		Namespace:  cr.Namespace,
		TestRef:    cr.Spec.TestRef,
		Phase:      string(cr.Status.Phase),
		Source:     cr.Spec.Source,
		DurationMs: cr.Status.DurationMs,
		Message:    cr.Status.Message,
		Origin:     "cluster",
		Tool:       crTool(cr),
		ParentRun:  cr.Labels[store.LabelParentRun],
		Tags:       cr.Spec.Tags,
		Abort:      cr.Spec.Abort,
	}
	if cr.Spec.NotBefore != nil {
		t := cr.Spec.NotBefore.UTC()
		e.NotBefore = &t
	}
	if cr.Status.ResolvedSpec != "" {
		var spec testsv1alpha1.TestSpec
		if err := json.Unmarshal([]byte(cr.Status.ResolvedSpec), &spec); err == nil {
			e.Config = store.EffectiveConfig(spec.Config, cr.Spec.Config)
			if c := cr.Status.Content; c != nil && spec.Content.Git != nil {
				e.Git = gitCheckout(spec.Content.Git.URI, c.GitRevision, c.GitCommit)
			}
			if spec.Artifacts != nil {
				paths := make([]string, len(cr.Status.ArtifactRefs))
				for i, a := range cr.Status.ArtifactRefs {
					paths[i] = a.Path
				}
				e.Report = reportArtifact(spec.Artifacts.Report, paths)
			}
		}
	} else {
		e.Config = store.EffectiveConfig(nil, cr.Spec.Config)
	}
	if tc := cr.Status.TestCounts; tc != nil {
		e.TestCounts = &apiclient.TestCounts{Total: tc.Total, Passed: tc.Passed, Failed: tc.Failed, Skipped: tc.Skipped}
	}
	if len(cr.Status.Metrics) > 0 {
		e.Metrics = map[string]float64{}
		for k, v := range cr.Status.Metrics {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				e.Metrics[k] = f
			}
		}
	}
	if len(cr.Status.Steps) > 0 {
		e.Steps = make(map[string]stepResult, len(cr.Status.Steps))
		for k, sr := range cr.Status.Steps {
			out := stepResult{Phase: string(sr.Phase), Message: sr.Message}
			if sr.StartedAt != nil {
				t := sr.StartedAt.UTC()
				out.StartedAt = &t
			}
			if sr.FinishedAt != nil {
				t := sr.FinishedAt.UTC()
				out.FinishedAt = &t
			}
			e.Steps[k] = out
		}
	}
	if cr.Status.QueuedAt != nil {
		t := cr.Status.QueuedAt.UTC()
		e.QueuedAt = &t
	}
	if cr.Status.StartedAt != nil {
		t := cr.Status.StartedAt.UTC()
		e.StartedAt = &t
	}
	if cr.Status.FinishedAt != nil {
		t := cr.Status.FinishedAt.UTC()
		e.FinishedAt = &t
	}
	return e
}

func runEnvelopeFromRow(row *store.Row) runEnvelope {
	e := runEnvelope{
		UID:        row.UID,
		Name:       row.Name,
		Namespace:  row.Namespace,
		TestRef:    row.TestRef,
		Phase:      row.Phase,
		Source:     row.Source,
		QueuedAt:   row.QueuedAt,
		StartedAt:  row.StartedAt,
		DurationMs: row.DurationMs,
		Message:    row.Message,
		Origin:     "archive",
		Tool:       row.Tool,
		ParentRun:  row.ParentRun,
		Tags:       row.Tags,
		Config:     row.Config,
		TestCounts: storeCounts(row.TestCounts),
		Metrics:    row.Metrics,
		Comment:    apiComment(row.Comment),
	}
	if pattern, _ := nested(row.ResolvedSpec, "artifacts", "report").(string); pattern != "" {
		paths := make([]string, len(row.ArtifactRefs))
		for i, a := range row.ArtifactRefs {
			paths[i] = a.Path
		}
		e.Report = reportArtifact(pattern, paths)
	}
	if row.GitCommit != "" || row.GitRevision != "" {
		uri, _ := nested(row.ResolvedSpec, "content", "git", "uri").(string)
		e.Git = gitCheckout(uri, row.GitRevision, row.GitCommit)
	}
	f := row.FinishedAt
	e.FinishedAt = &f
	if len(row.Steps) > 0 {
		// Steps are stored as generic JSON; re-decode into the wire shape.
		if b, err := json.Marshal(row.Steps); err == nil {
			_ = json.Unmarshal(b, &e.Steps)
		}
	}
	return e
}

// reportArtifact returns the first artifact path (sorted) that pattern
// (spec.artifacts.report) matches, or "". A parallel run's artifacts live
// under workers/<i>/artifacts/, so those match on the rest of the path —
// the first worker's report stands for the run.
func reportArtifact(pattern string, paths []string) string {
	if pattern == "" {
		return ""
	}
	sorted := slices.Clone(paths)
	slices.Sort(sorted)
	for _, p := range sorted {
		rel := p
		if rest, ok := strings.CutPrefix(p, "workers/"); ok {
			if _, after, found := strings.Cut(rest, "/artifacts/"); found {
				rel = after
			}
		}
		if ok, _ := doublestar.Match(pattern, rel); ok {
			return p
		}
	}
	return ""
}

func gitCheckout(uri, revision, commit string) *apiclient.GitCheckout {
	return &apiclient.GitCheckout{URI: stripUserinfo(uri), Revision: revision, Commit: commit}
}

// stripUserinfo drops "user:token@" from a URI, so a credential someone
// put into spec.content.git.uri doesn't travel to every API client.
func stripUserinfo(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.User == nil {
		return uri
	}
	u.User = nil
	return u.String()
}

// nested walks string keys of decoded JSON; nil when a key is missing.
func nested(m map[string]any, keys ...string) any {
	var v any = m
	for _, k := range keys {
		mm, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = mm[k]
	}
	return v
}

// crTool mirrors the controller's runTool: status.tool, else the label.
func crTool(cr *testsv1alpha1.TestRun) string {
	if cr.Status.Tool != "" {
		return cr.Status.Tool
	}
	return cr.Labels[store.LabelTool]
}

// parseLimitOrDefault clamps limit query param to a reasonable range. Zero
// or non-numeric → default 50; over 500 → 500.
func parseLimitOrDefault(s string) int {
	if s == "" {
		return 50
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 50
	}
	if n > 500 {
		return 500
	}
	return n
}

func storeCounts(tc *store.TestCounts) *apiclient.TestCounts {
	if tc == nil {
		return nil
	}
	return &apiclient.TestCounts{Total: tc.Total, Passed: tc.Passed, Failed: tc.Failed, Skipped: tc.Skipped}
}
