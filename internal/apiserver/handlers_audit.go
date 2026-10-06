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
	"net/http"
	"strconv"

	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// recordAudit appends a user action to the audit log. Best effort: the
// action already happened, so a failed audit write is logged, never
// turned into an error response for it.
func (s *Server) recordAudit(r *http.Request, action, namespace, target string, details map[string]string) {
	if s.Audit == nil {
		return
	}
	e := store.AuditEntry{
		At: s.Now().UTC(), Actor: requestUser(r), Action: action,
		Namespace: namespace, Target: target, Details: details,
	}
	if err := s.Audit.AppendAudit(r.Context(), e); err != nil {
		ctrllog.FromContext(r.Context()).WithName("apiserver").Error(err, "audit write failed",
			"action", action, "namespace", namespace, "target", target)
	}
}

// listAudit returns audit entries newest first. Filters: namespace (forced
// to the server's scope), actor, action. Paging: pass X-Next-Cursor as
// ?before=.
func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	if s.Audit == nil {
		writeError(w, http.StatusServiceUnavailable, ReasonServiceUnavail, "run history store is not configured")
		return
	}
	ns, err := s.listNamespace(r)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	q := r.URL.Query()
	f := store.AuditFilter{Namespace: ns, Actor: q.Get("actor"), Action: q.Get("action")}
	if v := q.Get("before"); v != "" {
		if f.BeforeID, err = strconv.ParseInt(v, 10, 64); err != nil || f.BeforeID <= 0 {
			writeError(w, http.StatusBadRequest, ReasonBadRequest, "before: want a positive audit entry id")
			return
		}
	}
	limit := parseLimitOrDefault(q.Get("limit"))
	entries, err := s.Audit.ListAudit(r.Context(), f, limit)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	out := make([]apiclient.AuditEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, apiclient.AuditEntry{
			ID: e.ID, At: e.At, Actor: e.Actor, Action: e.Action,
			Namespace: e.Namespace, Target: e.Target, Details: e.Details,
		})
	}
	if len(out) == limit {
		w.Header().Set(HeaderNextCursor, strconv.FormatInt(out[len(out)-1].ID, 10))
	}
	writeJSON(w, http.StatusOK, out)
}
