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
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/store"
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
	if run.Namespace == "" {
		run.Namespace = s.Namespace
	}
	if err := s.K8sClient.Create(r.Context(), &run); err != nil {
		writeAPIError(w, err)
		return
	}
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
	// Try cluster first — an active/recent run lives here with the CR name
	// as {id}. Store is UID-keyed so we skip it when id looks like a name.
	var run testsv1alpha1.TestRun
	err := s.K8sClient.Get(r.Context(),
		types.NamespacedName{Namespace: s.Namespace, Name: id}, &run)
	if err == nil {
		writeJSON(w, http.StatusOK, runEnvelopeFromCR(&run))
		return
	}
	// Not in cluster — try store by UID.
	if s.Store == nil {
		writeAPIError(w, err)
		return
	}
	row, storeErr := s.Store.Get(r.Context(), id)
	if storeErr != nil {
		writeAPIError(w, storeErr) // ErrNotFound maps to 404
		return
	}
	writeJSON(w, http.StatusOK, runEnvelopeFromRow(row))
}

// listRuns merges cluster actives with store archive. Filters:
//   - test=<name> narrows both sides.
//   - phase=<phase> narrows both sides.
//   - limit + cursor for keyset pagination (delegates to store; cluster is
//     bounded to page size, order-stable via startedAt DESC).
//
// Merge rules (§step-10):
//   - UID de-dupes: a run present in both cluster AND store (typical during
//     the window between save and CR cleanup) appears exactly once, taking
//     the CLUSTER's fresher status.
//   - Ordering: startedAt DESC (nil StartedAt sinks to the end), tiebreak
//     on UID for determinism.
func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	testRef := q.Get("test")
	phase := q.Get("phase")
	limit := parseLimitOrDefault(q.Get("limit"))

	byUID := map[string]runEnvelope{}

	// Cluster side.
	var live testsv1alpha1.TestRunList
	listOpts := []client.ListOption{}
	if s.Namespace != "" {
		listOpts = append(listOpts, client.InNamespace(s.Namespace))
	}
	if err := s.K8sClient.List(r.Context(), &live, listOpts...); err != nil {
		writeAPIError(w, err)
		return
	}
	for i := range live.Items {
		cr := &live.Items[i]
		if testRef != "" && cr.Spec.TestRef != testRef {
			continue
		}
		if phase != "" && string(cr.Status.Phase) != phase {
			continue
		}
		env := runEnvelopeFromCR(cr)
		byUID[env.UID] = env
	}

	// Store side.
	if s.Store != nil {
		f := store.Filter{TestRef: testRef, Namespace: s.Namespace, Phase: phase}
		rows, err := s.Store.List(r.Context(), f, store.Page{Limit: limit})
		if err != nil {
			writeAPIError(w, err)
			return
		}
		for i := range rows {
			row := &rows[i]
			if _, ok := byUID[row.UID]; ok {
				// Cluster wins — its status is fresher.
				continue
			}
			byUID[row.UID] = runEnvelopeFromRow(row)
		}
	}

	// Materialize + sort.
	out := make([]runEnvelope, 0, len(byUID))
	for _, e := range byUID {
		out = append(out, e)
	}
	slices.SortStableFunc(out, func(a, b runEnvelope) int {
		// Deterministic sort: nil StartedAt sinks; newest StartedAt first;
		// tiebreak on UID ascending.
		la, lb := a.StartedAt, b.StartedAt
		switch {
		case la == nil && lb == nil:
			return strings.Compare(a.UID, b.UID)
		case la == nil:
			return 1
		case lb == nil:
			return -1
		}
		switch {
		case la.After(*lb):
			return -1
		case lb.After(*la):
			return 1
		}
		return strings.Compare(a.UID, b.UID)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	writeJSON(w, http.StatusOK, out)
}

// runEnvelope is the wire shape for /runs and /runs/{id}. Mirrors CR and
// Row shapes just enough for the GUI without leaking either fully.
type runEnvelope struct {
	UID        string     `json:"uid"`
	Name       string     `json:"name"`
	Namespace  string     `json:"namespace"`
	TestRef    string     `json:"testRef"`
	Phase      string     `json:"phase"`
	Source     string     `json:"source,omitempty"`
	QueuedAt   *time.Time `json:"queuedAt,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	DurationMs int64      `json:"durationMs,omitempty"`
	Message    string     `json:"message,omitempty"`
	// Origin flags "cluster" (still in etcd) vs "archive" (only in store).
	// GUI shows a small badge when Origin=archive so users know clicking
	// "Kill run" won't work.
	Origin string `json:"origin"`
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
	}
	f := row.FinishedAt
	e.FinishedAt = &f
	return e
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
