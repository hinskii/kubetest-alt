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
	"errors"
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/hinskii/kubetest-alt/internal/controller"
	"github.com/hinskii/kubetest-alt/internal/store"
)

// RunDeleter removes archived run rows (store.Postgres implements it).
type RunDeleter interface {
	Delete(ctx context.Context, uid string) error
}

// deleteRun removes a finished run from history: its objects (logs,
// artifacts, result.json), its store row, and its CR if still present.
// User-driven cleanup — retention is a separate, partition-level path.
//
// Order is objects → CR → row:
//   - objects first, so a failure later never strands them unreachable;
//   - CR before row, because a terminal CR is re-persisted by the operator
//     (e.g. after a restart) — deleting the row first could see it come
//     back. A CR being deleted goes down the finalize path, which doesn't
//     persist.
//
// Every step tolerates "already gone", so a request that failed part-way
// is safely retried: the run is then found in the archive and finished off.
//
//   - 204: deleted (also when parts were already gone).
//   - 409: the run is still live — abort it first.
//   - 404: unknown run.
//   - 503: the run is archived but no store/deleter is wired.
func (s *Server) deleteRun(w http.ResponseWriter, r *http.Request) {
	ns, err := s.targetNamespace(r, "")
	if err != nil {
		writeLookupError(w, err)
		return
	}
	ref, err := s.findRun(r.Context(), ns, r.PathValue("id"))
	if err != nil {
		writeLookupError(w, err)
		return
	}
	if ref.CR != nil && !controller.IsTerminalPhase(ref.CR.Status.Phase) {
		writeError(w, http.StatusConflict, ReasonConflict,
			fmt.Sprintf("run %q is still %s — abort it before deleting", ref.Name, ref.CR.Status.Phase))
		return
	}
	if ref.Row != nil && s.Deleter == nil {
		writeError(w, http.StatusServiceUnavailable, ReasonServiceUnavail,
			"run history store is not configured for deletes")
		return
	}

	if s.Remover != nil && s.Bucket != "" {
		if err := s.Remover.RemovePrefix(r.Context(), s.Bucket, ref.keys().Prefix()); err != nil {
			writeError(w, http.StatusInternalServerError, ReasonInternal,
				fmt.Sprintf("remove objects: %v", err))
			return
		}
	}
	if ref.CR != nil {
		if err := s.K8sClient.Delete(r.Context(), ref.CR); err != nil && !apierrors.IsNotFound(err) {
			writeAPIError(w, err)
			return
		}
	}
	if s.Deleter != nil {
		if err := s.Deleter.Delete(r.Context(), ref.UID); err != nil && !errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, ReasonInternal,
				fmt.Sprintf("delete history row: %v", err))
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
