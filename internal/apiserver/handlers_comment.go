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
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/hinskii/kubetest-alt/internal/controller"
	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// putComment sets the comment of a finished run. Comments live on the
// run-history row, so they go away with the run.
//
//   - 200: the stored comment.
//   - 409: the run is still live, or finished but not in history yet.
//   - 503: no run history store.
func (s *Server) putComment(w http.ResponseWriter, r *http.Request) {
	var body apiclient.CommentOptions
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, ReasonBadRequest, fmt.Sprintf("decode: %v", err))
		return
	}
	text := strings.TrimSpace(body.Text)
	switch {
	case text == "":
		writeError(w, http.StatusBadRequest, ReasonBadRequest, "text is required (DELETE removes a comment)")
		return
	case utf8.RuneCountInString(text) > apiclient.MaxCommentLen:
		writeError(w, http.StatusBadRequest, ReasonBadRequest,
			fmt.Sprintf("text longer than %d characters", apiclient.MaxCommentLen))
		return
	}
	ref, ok := s.commentTarget(w, r)
	if !ok {
		return
	}
	c := &store.Comment{Text: text, By: requestUser(r), At: s.Now().UTC()}
	if err := s.Commenter.SetComment(r.Context(), ref.UID, c); err != nil {
		writeCommentError(w, ref, err)
		return
	}
	s.recordAudit(r, apiclient.ActionRunComment, ref.Namespace, ref.Name, map[string]string{apiclient.AuditDetailUID: ref.UID, apiclient.AuditDetailText: text})
	writeJSON(w, http.StatusOK, apiComment(c))
}

// deleteComment removes a run's comment. Idempotent: 204 also when there
// was none.
func (s *Server) deleteComment(w http.ResponseWriter, r *http.Request) {
	ref, ok := s.commentTarget(w, r)
	if !ok {
		return
	}
	if err := s.Commenter.SetComment(r.Context(), ref.UID, nil); err != nil && !errors.Is(err, store.ErrNotFound) {
		writeAPIError(w, err)
		return
	}
	s.recordAudit(r, apiclient.ActionRunUncomment, ref.Namespace, ref.Name, map[string]string{apiclient.AuditDetailUID: ref.UID})
	w.WriteHeader(http.StatusNoContent)
}

// commentTarget resolves {id} to a finished run, writing the error
// response itself when it can't.
func (s *Server) commentTarget(w http.ResponseWriter, r *http.Request) (runRef, bool) {
	if s.Commenter == nil {
		writeError(w, http.StatusServiceUnavailable, ReasonServiceUnavail, "run history store is not configured")
		return runRef{}, false
	}
	ns, err := s.targetNamespace(r, "")
	if err != nil {
		writeLookupError(w, err)
		return runRef{}, false
	}
	ref, err := s.findRun(r.Context(), ns, r.PathValue("id"))
	if err != nil {
		writeLookupError(w, err)
		return runRef{}, false
	}
	if ref.CR != nil && !controller.IsTerminalPhase(ref.CR.Status.Phase) {
		writeError(w, http.StatusConflict, ReasonConflict,
			fmt.Sprintf("run %q is still %s — comment once it has finished", ref.Name, ref.CR.Status.Phase))
		return runRef{}, false
	}
	return ref, true
}

func writeCommentError(w http.ResponseWriter, ref runRef, err error) {
	if errors.Is(err, store.ErrNotFound) {
		// Terminal CR whose history row the controller hasn't written yet.
		writeError(w, http.StatusConflict, ReasonConflict,
			fmt.Sprintf("run %q is not in run history yet — retry in a moment", ref.Name))
		return
	}
	writeAPIError(w, err)
}

func apiComment(c *store.Comment) *apiclient.Comment {
	if c == nil {
		return nil
	}
	return &apiclient.Comment{Text: c.Text, By: c.By, At: c.At}
}
