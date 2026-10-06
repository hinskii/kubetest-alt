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

	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/controller"
	"github.com/hinskii/kubetest-alt/internal/resolver"
)

// resolvedTest is the wire shape of GET /tests/{name}/resolved: what a run
// form needs before any values exist.
type resolvedTest struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels,omitempty"`
	// Tool: the Test's kubetest.io/tool label, else its templates'.
	Tool string `json:"tool,omitempty"`
	// GitOpsLocked mirrors §7: the definition is read-only in the GUI
	// (runs are still allowed).
	GitOpsLocked bool `json:"gitopsLocked"`
	// Templates is spec.use, in merge order.
	Templates []string `json:"templates,omitempty"`
	// Spec is the Test merged with its templates. Expressions are NOT
	// evaluated and config is NOT resolved — spec.config is the full
	// parameter schema, including parameters only a template declares.
	Spec *testsv1alpha1.TestSpec `json:"spec"`
}

// templateStore returns the configured TemplateStore, defaulting to one
// backed by the server's (cached) client.
func (s *Server) templateStore() resolver.TemplateStore {
	if s.Templates != nil {
		return s.Templates
	}
	return &controller.ClientTemplateStore{Client: s.K8sClient}
}

// getResolvedTest merges a Test with its TestTemplates.
//
//   - 404: no such Test.
//   - 422: a template in spec.use is missing (the Test can't run either).
func (s *Server) getResolvedTest(w http.ResponseWriter, r *http.Request) {
	ns, err := s.targetNamespace(r, "")
	if err != nil {
		writeLookupError(w, err)
		return
	}
	var t testsv1alpha1.Test
	if err := s.K8sClient.Get(r.Context(), types.NamespacedName{Namespace: ns, Name: r.PathValue("name")}, &t); err != nil {
		writeAPIError(w, err)
		return
	}
	spec, tool, err := resolver.MergeTemplates(&t, s.templateStore())
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, ReasonBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resolvedTest{
		Name:         t.Name,
		Namespace:    t.Namespace,
		Labels:       t.Labels,
		Tool:         tool,
		GitOpsLocked: isManagedByGitOps(t.Labels),
		Templates:    t.Spec.Use,
		Spec:         spec,
	})
}
