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
	"io"
	"net/http"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// listTests returns every Test in the selected namespace (?namespace=, or
// the server's scope), or across all namespaces on a cluster-wide server.
func (s *Server) listTests(w http.ResponseWriter, r *http.Request) {
	ns, err := s.listNamespace(r)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	var list testsv1alpha1.TestList
	opts := []client.ListOption{}
	if ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}
	if err := s.K8sClient.List(r.Context(), &list, opts...); err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list.Items)
}

// getTest returns a single Test by name.
func (s *Server) getTest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, ReasonBadRequest, "test name is required")
		return
	}
	ns, err := s.targetNamespace(r, "")
	if err != nil {
		writeLookupError(w, err)
		return
	}
	var t testsv1alpha1.Test
	if err := s.K8sClient.Get(r.Context(), types.NamespacedName{Namespace: ns, Name: name}, &t); err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// createTest is the GUI's write path. Two invariants:
//  1. managed-by=ui is set BY THE SERVER, unconditionally — spoofing
//     managed-by=gitops in the payload is rejected 400 (§step-10 spoof
//     guard). Users MUST NOT be able to create a "gitops-owned" Test via
//     the GUI, otherwise the enforcement in §7 is trivially bypassed.
//  2. Namespace is resolved by targetNamespace: a scoped server refuses any
//     other namespace; a cluster-wide one requires one to be named.
func (s *Server) createTest(w http.ResponseWriter, r *http.Request) {
	var t testsv1alpha1.Test
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeError(w, http.StatusBadRequest, ReasonBadRequest,
			fmt.Sprintf("decode: %v", err))
		return
	}
	if v, ok := t.Labels[LabelManagedBy]; ok && v != ManagedByUI {
		writeError(w, http.StatusBadRequest, ReasonBadRequest,
			fmt.Sprintf("payload sets %s=%q; the API server owns this label — omit it",
				LabelManagedBy, v))
		return
	}
	if t.Labels == nil {
		t.Labels = map[string]string{}
	}
	t.Labels[LabelManagedBy] = ManagedByUI
	if u := requestUser(r); u != "" {
		if t.Annotations == nil {
			t.Annotations = map[string]string{}
		}
		t.Annotations[TagCreatedBy] = u
	}

	ns, err := s.targetNamespace(r, t.Namespace)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	t.Namespace = ns
	if dryRun(r) {
		// Admission (webhooks, schema) without persisting: the wizard's
		// "check before creating".
		if err := s.K8sClient.Create(r.Context(), &t, client.DryRunAll); err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, t)
		return
	}
	if err := s.K8sClient.Create(r.Context(), &t); err != nil {
		writeAPIError(w, err)
		return
	}
	s.recordAudit(r, apiclient.ActionTestCreate, t.Namespace, t.Name, nil)
	writeJSON(w, http.StatusCreated, t)
}

// maxPatchBytes bounds a PATCH body (inline content is capped at 512KB by
// the Test webhook; leave room for the rest of the spec).
const maxPatchBytes = 2 << 20

// patchTest applies a JSON merge patch (RFC 7396) to a Test: objects merge
// key by key, arrays and scalars replace, null deletes. The old handler
// swapped the whole spec when the payload had an image or git source and
// silently ignored everything else — a patch of only spec.steps returned
// 200 and changed nothing (fixes.md #21). Blocked with 409 unless the Test
// is GUI-owned (§7); the patch may not touch the managed-by label.
func (s *Server) patchTest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, ReasonBadRequest, "test name is required")
		return
	}
	ns, err := s.targetNamespace(r, "")
	if err != nil {
		writeLookupError(w, err)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPatchBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, ReasonBadRequest, fmt.Sprintf("read body: %v", err))
		return
	}
	if len(body) > maxPatchBytes {
		writeError(w, http.StatusRequestEntityTooLarge, ReasonBadRequest,
			fmt.Sprintf("patch larger than %d bytes", maxPatchBytes))
		return
	}
	// The label can be set, replaced or removed (null) by a merge patch;
	// any of those except "ui" would hand the Test to someone else.
	var probe struct {
		Metadata struct {
			Labels map[string]*string `json:"labels"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		writeError(w, http.StatusBadRequest, ReasonBadRequest,
			fmt.Sprintf("decode: body must be a JSON merge patch object: %v", err))
		return
	}
	if v, ok := probe.Metadata.Labels[LabelManagedBy]; ok && (v == nil || *v != ManagedByUI) {
		writeError(w, http.StatusBadRequest, ReasonBadRequest,
			fmt.Sprintf("cannot change or remove %s via PATCH — leave it out of the payload", LabelManagedBy))
		return
	}
	var current testsv1alpha1.Test
	if err := s.K8sClient.Get(r.Context(),
		types.NamespacedName{Namespace: ns, Name: name}, &current); err != nil {
		writeAPIError(w, err)
		return
	}
	if isLockedForUI(current.Labels) {
		writeError(w, http.StatusConflict, ReasonManagedByGitOps, lockedMessage(name, current.Labels, "edit"))
		return
	}
	var opts []client.PatchOption
	if dryRun(r) {
		opts = append(opts, client.DryRunAll)
	}
	if err := s.K8sClient.Patch(r.Context(), &current, client.RawPatch(types.MergePatchType, body), opts...); err != nil {
		writeAPIError(w, err)
		return
	}
	if dryRun(r) {
		writeJSON(w, http.StatusOK, current)
		return
	}
	s.recordAudit(r, apiclient.ActionTestUpdate, current.Namespace, current.Name, nil)
	writeJSON(w, http.StatusOK, current)
}

// dryRun: ?dry-run=true — admit the write (schema, webhooks) without
// persisting it, as kubectl --dry-run=server. Not "dryRun": through the
// Kubernetes API's service proxy (Control Center, the CLI) that query
// parameter is the kube-apiserver's, which refuses it on a proxy request
// ("dryRun is not supported").
func dryRun(r *http.Request) bool { return r.URL.Query().Get(apiclient.QueryDryRun) == "true" }

// deleteTest removes a Test. Blocked with 409 for gitops-owned CRs (§7).
func (s *Server) deleteTest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, ReasonBadRequest, "test name is required")
		return
	}
	ns, err := s.targetNamespace(r, "")
	if err != nil {
		writeLookupError(w, err)
		return
	}
	var current testsv1alpha1.Test
	if err := s.K8sClient.Get(r.Context(),
		types.NamespacedName{Namespace: ns, Name: name}, &current); err != nil {
		writeAPIError(w, err)
		return
	}
	if isLockedForUI(current.Labels) {
		writeError(w, http.StatusConflict, ReasonManagedByGitOps, lockedMessage(name, current.Labels, "delete"))
		return
	}
	if err := s.K8sClient.Delete(r.Context(), &current); err != nil {
		writeAPIError(w, err)
		return
	}
	s.recordAudit(r, apiclient.ActionTestDelete, current.Namespace, current.Name, nil)
	w.WriteHeader(http.StatusNoContent)
}
