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
	"io"
	"net/http"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/controller"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// HeaderUser carries the end user's identity from a trusted front end
// (Control Center behind oauth2-proxy). The API server is only reachable
// through the Kubernetes service proxy, so anyone able to set this header
// already holds services/proxy RBAC on it — it is attribution, not authn.
const HeaderUser = apiclient.HeaderUser

type abortRequestBody = apiclient.AbortOptions

// maxUserLen mirrors the CRD limit on AbortRequest.requestedBy.
const maxUserLen = 256

// requestUser returns the caller identity for attribution, or "".
func requestUser(r *http.Request) string {
	u := strings.TrimSpace(r.Header.Get(HeaderUser))
	if len(u) > maxUserLen {
		u = u[:maxUserLen]
	}
	return u
}

// maxAbortMessageLen mirrors the CRD limit on AbortRequest.message.
const maxAbortMessageLen = 512

// abortRun sets spec.abort on a live TestRun; the controller then kills the
// Job, records the run as aborted, persists it and fires webhooks. Deleting
// the CR (what the reference GUI did) skipped all of that.
//
//   - 202: abort requested (or already requested — idempotent; the first
//     request's reason/requester are kept, spec.abort is one-way).
//   - 404: no such run in the cluster (archived runs can't be aborted).
//   - 409: the run already finished.
//
// Runs of GitOps-managed Tests may be aborted: runs are ephemeral children,
// not definitions (§7), same rule as creating them.
func (s *Server) abortRun(w http.ResponseWriter, r *http.Request) {
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
	var body abortRequestBody
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, ReasonBadRequest, fmt.Sprintf("decode: %v", err))
			return
		}
	}
	if len(body.Message) > maxAbortMessageLen {
		writeError(w, http.StatusBadRequest, ReasonBadRequest,
			fmt.Sprintf("message longer than %d characters", maxAbortMessageLen))
		return
	}

	ref, err := s.findRun(r.Context(), ns, id)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	if ref.CR == nil {
		writeError(w, http.StatusConflict, ReasonConflict,
			fmt.Sprintf("run %q is archived and already finished", id))
		return
	}
	run := ref.CR
	if controller.IsTerminalPhase(run.Status.Phase) {
		writeError(w, http.StatusConflict, ReasonConflict,
			fmt.Sprintf("run %q already finished (%s)", run.Name, run.Status.Phase))
		return
	}
	if run.Spec.Abort == nil {
		patch := client.MergeFrom(run.DeepCopy())
		run.Spec.Abort = &testsv1alpha1.AbortRequest{
			Reason:      testsv1alpha1.AbortReasonUser,
			Message:     body.Message,
			RequestedBy: requestUser(r),
		}
		if err := s.K8sClient.Patch(r.Context(), run, patch); err != nil {
			writeAPIError(w, err)
			return
		}
		s.recordAudit(r, apiclient.ActionRunAbort, run.Namespace, run.Name,
			map[string]string{
				apiclient.AuditDetailUID: string(run.UID), apiclient.AuditDetailTest: run.Spec.TestRef,
				apiclient.AuditDetailMessage: body.Message,
			})
	}
	writeJSON(w, http.StatusAccepted, runEnvelopeFromCR(run))
}
