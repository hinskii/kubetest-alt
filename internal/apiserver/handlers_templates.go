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

	"sigs.k8s.io/controller-runtime/pkg/client"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// listTemplates serves GET /templates: the TestTemplates a Test in
// ?namespace= can `use` — the tool catalog of Control Center's wizard.
func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	ns, err := s.listNamespace(r)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	var list testsv1alpha1.TestTemplateList
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
