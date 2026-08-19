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
	"maps"
	"net/http"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// listTests returns every Test in the configured namespace (or all
// namespaces when Namespace is empty).
func (s *Server) listTests(w http.ResponseWriter, r *http.Request) {
	var list testsv1alpha1.TestList
	opts := []client.ListOption{}
	if s.Namespace != "" {
		opts = append(opts, client.InNamespace(s.Namespace))
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
	var t testsv1alpha1.Test
	if err := s.K8sClient.Get(r.Context(), types.NamespacedName{Namespace: s.Namespace, Name: name}, &t); err != nil {
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
//  2. Namespace comes from the server config, not the payload — the API
//     server is namespace-scoped by construction.
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

	if t.Namespace == "" {
		t.Namespace = s.Namespace
	}
	if err := s.K8sClient.Create(r.Context(), &t); err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// patchTest applies a merge-patch to a Test. Blocked with 409 if the target
// carries a non-ui managed-by label (§7).
func (s *Server) patchTest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, ReasonBadRequest, "test name is required")
		return
	}
	var current testsv1alpha1.Test
	if err := s.K8sClient.Get(r.Context(),
		types.NamespacedName{Namespace: s.Namespace, Name: name}, &current); err != nil {
		writeAPIError(w, err)
		return
	}
	if isManagedByGitOps(current.Labels) {
		writeError(w, http.StatusConflict, ReasonManagedByGitOps,
			fmt.Sprintf("Test %q is managed by GitOps — edit it in the source repo", name))
		return
	}
	// Merge-patch: decode into a fresh Test carrying only the fields the
	// user supplied, then apply as a patch on top of the current object.
	var patch testsv1alpha1.Test
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, ReasonBadRequest,
			fmt.Sprintf("decode: %v", err))
		return
	}
	// Guard against a Ui-owner accidentally handing itself over to GitOps
	// via PATCH (would silently make future edits impossible without
	// touching the CR by hand).
	if v, ok := patch.Labels[LabelManagedBy]; ok && v != ManagedByUI {
		writeError(w, http.StatusBadRequest, ReasonBadRequest,
			fmt.Sprintf("cannot re-label managed-by to %q via PATCH — remove the label from the payload", v))
		return
	}
	// Overlay: labels merged; spec swapped (last-write-wins) if payload
	// supplied one.
	updated := current.DeepCopy()
	if updated.Labels == nil {
		updated.Labels = map[string]string{}
	}
	maps.Copy(updated.Labels, patch.Labels)
	if patch.Spec.Container.Image != "" || patch.Spec.Content.Git != nil {
		updated.Spec = patch.Spec
	}
	// managed-by always stays ui (spoof rejected above; also survives spec swap).
	updated.Labels[LabelManagedBy] = ManagedByUI

	if err := s.K8sClient.Update(r.Context(), updated); err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// deleteTest removes a Test. Blocked with 409 for gitops-owned CRs (§7).
func (s *Server) deleteTest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, ReasonBadRequest, "test name is required")
		return
	}
	var current testsv1alpha1.Test
	if err := s.K8sClient.Get(r.Context(),
		types.NamespacedName{Namespace: s.Namespace, Name: name}, &current); err != nil {
		writeAPIError(w, err)
		return
	}
	if isManagedByGitOps(current.Labels) {
		writeError(w, http.StatusConflict, ReasonManagedByGitOps,
			fmt.Sprintf("Test %q is managed by GitOps — delete it in the source repo", name))
		return
	}
	if err := s.K8sClient.Delete(r.Context(), &current); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
