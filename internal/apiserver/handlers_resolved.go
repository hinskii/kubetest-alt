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
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

type resolvedTest = apiclient.ResolvedTest

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
		GitOpsLocked: isLockedForUI(t.Labels),
		Templates:    t.Spec.Use,
		Spec:         spec,
		Conditions:   t.Status.Conditions,
	})
}
