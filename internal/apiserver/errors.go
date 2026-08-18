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
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/hinskii/kubetest-alt/internal/store"
)

// errorEnvelope is the wire shape for API errors. Small on purpose —
// browsers surface `message`; `reason` is for programmatic UI decisions
// (managed-by-gitops badge etc.).
type errorEnvelope struct {
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message"`
}

// Reason constants map to programmatic UI behavior. Kept as constants so
// tests can assert without string comparison drift.
const (
	ReasonNotFound        = "NotFound"
	ReasonBadRequest      = "BadRequest"
	ReasonConflict        = "Conflict"
	ReasonManagedByGitOps = "ManagedByGitOps"
	ReasonInternal        = "Internal"
	ReasonServiceUnavail  = "ServiceUnavailable"
)

// writeError writes an error envelope with the given HTTP status.
func writeError(w http.ResponseWriter, status int, reason, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Reason: reason, Message: message})
}

// writeAPIError maps a Kubernetes API error → HTTP status. The webhook
// validation errors that the operator emits land here as Invalid → 400 with
// the webhook's exact message; NotFound → 404; AlreadyExists → 409; the
// rest fall through to 500.
func writeAPIError(w http.ResponseWriter, err error) {
	switch {
	case apierrors.IsNotFound(err):
		writeError(w, http.StatusNotFound, ReasonNotFound, err.Error())
	case apierrors.IsAlreadyExists(err):
		writeError(w, http.StatusConflict, ReasonConflict, err.Error())
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		// Webhook validation lands here — pass the message through
		// verbatim so the GUI surfaces the operator's exact reason.
		writeError(w, http.StatusBadRequest, ReasonBadRequest, err.Error())
	case apierrors.IsConflict(err):
		writeError(w, http.StatusConflict, ReasonConflict, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, ReasonNotFound, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, ReasonInternal, err.Error())
	}
}
