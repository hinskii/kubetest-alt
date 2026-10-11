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

package server

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	pathpkg "path"
	"slices"
	"strconv"
	"strings"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	uiapi "github.com/hinskii/kubetest-alt/internal/controlcenter/api"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/auth"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/clusters"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/views"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/webui"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
	"github.com/hinskii/kubetest-alt/pkg/expr"
)

// Labels Control Center reads from Tests.
// (The UI's API package owns them; see api/view.go.)
const (
	labelTool      = uiapi.LabelTool
	labelManagedBy = uiapi.LabelManagedBy
	managedByUI    = uiapi.ManagedByUI
	// tagCreatedBy names who started a run (a TestRun tag) or created a
	// Test (an annotation).
	tagCreatedBy = uiapi.TagCreatedBy
)

// HTML input types of parameter fields (inputNumber is also the
// parameter type).
const (
	inputNumber = "number"
	inputText   = "text"
)

// dispositionInline: the browser shows the file rather than saving it.
const dispositionInline = "inline"

// artifactCSP is the Content-Security-Policy of a served artifact: its
// scripts run in an opaque origin, and only Control Center's own pages
// may frame it (the run page shows the report).
const artifactCSP = "sandbox allow-scripts; frame-ancestors 'self'"

// routes registers the cluster pages on mux. Mutations need a role.
func (s *Server) routes(mux *http.ServeMux) {
	// React pages (step 20R): the UI's router renders them.
	mux.Handle("GET /clusters/{cluster}", webui.Index())
	mux.Handle("GET /clusters/{cluster}/tests/{ns}/{name}", webui.Index())
	mux.Handle("GET /clusters/{cluster}/tests/new", auth.Require(auth.RoleDeveloper, http.HandlerFunc(s.newTestPage)))
	mux.Handle("POST /clusters/{cluster}/tests/new", auth.Require(auth.RoleDeveloper, http.HandlerFunc(s.submitNewTest)))
	mux.Handle("GET /clusters/{cluster}/tests/{ns}/{name}/edit", auth.Require(auth.RoleDeveloper, http.HandlerFunc(s.editTestPage)))
	mux.Handle("POST /clusters/{cluster}/tests/{ns}/{name}/edit", auth.Require(auth.RoleDeveloper, http.HandlerFunc(s.submitEditTest)))
	mux.Handle("POST /clusters/{cluster}/tests/new/yaml", auth.Require(auth.RoleDeveloper, http.HandlerFunc(s.newTestYAML)))
	mux.Handle("POST /clusters/{cluster}/tests/{ns}/{name}/edit/yaml", auth.Require(auth.RoleDeveloper, http.HandlerFunc(s.editTestYAML)))
	mux.Handle("GET /clusters/{cluster}/tests/{ns}/{name}/cases", webui.Index())
	mux.Handle("GET /clusters/{cluster}/tests/{ns}/{name}/cases/history", webui.Index())
	mux.Handle("GET /clusters/{cluster}/schedules", webui.Index())
	mux.Handle("GET /clusters/{cluster}/tests/{ns}/{name}/cleanup", webui.Index())
	mux.Handle("GET /clusters/{cluster}/tests/{ns}/{name}/analytics", webui.Index())
	mux.Handle("GET /clusters/{cluster}/runs/{ns}/{id}", webui.Index())
	mux.Handle("GET /clusters/{cluster}/runs/{ns}/{id}/compare", webui.Index())
	mux.HandleFunc("GET /clusters/{cluster}/runs/{ns}/{id}/logs.txt", s.runLogDownload)
	mux.HandleFunc("GET /clusters/{cluster}/runs/{ns}/{id}/artifacts/{path...}", s.runArtifact)
}

// --- paths -------------------------------------------------------------------

func clusterPath(c string) string { return "/clusters/" + url.PathEscape(c) }

func testPath(c, ns, name string) string {
	return clusterPath(c) + "/tests/" + url.PathEscape(ns) + "/" + url.PathEscape(name)
}

// --- plumbing ------------------------------------------------------------------

// cluster resolves {cluster}, rendering a 404 when it isn't configured.
func (s *Server) cluster(w http.ResponseWriter, r *http.Request) (*clusters.Cluster, bool) {
	c := s.Clusters.Get(r.PathValue("cluster"))
	if c == nil {
		s.renderError(w, r, http.StatusNotFound, fmt.Sprintf("No cluster named %q is configured.", r.PathValue("cluster")))
		return nil, false
	}
	return c, true
}

// api is the cluster's kubetest API, acting for the signed-in user.
func api(r *http.Request, c *clusters.Cluster) *apiclient.Client {
	return c.API.AsUser(auth.FromContext(r.Context()).Email)
}

// apiError renders a failed kubetest API call: its 404 as ours, anything
// else as a bad gateway with the API's own message.
func (s *Server) apiError(w http.ResponseWriter, r *http.Request, c *clusters.Cluster, err error) {
	var apiErr *apiclient.APIError
	switch {
	case apiclient.IsNotFound(err):
		s.renderError(w, r, http.StatusNotFound, "Not found in "+c.DisplayNameOrName()+": "+messageOf(err))
	case errors.As(err, &apiErr):
		s.renderError(w, r, http.StatusBadGateway, "The kubetest API of "+c.DisplayNameOrName()+" answered: "+messageOf(err))
	default:
		s.Log.Warn("kubetest API unreachable", "cluster", c.Name, "err", err)
		s.renderError(w, r, http.StatusBadGateway, "Cannot reach the kubetest API of "+c.DisplayNameOrName()+".")
	}
}

func messageOf(err error) string {
	var apiErr *apiclient.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Message
	}
	return err.Error()
}

// redirect finishes a POST with a GET of target, carrying a notice.
// Targets come from clusterPath/testPath/runPath (escaped segments under
// /clusters/); anything that isn't a local path goes home instead.
func redirect(w http.ResponseWriter, r *http.Request, target, notice string) {
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.HasPrefix(target, "/\\") {
		target = "/"
	}
	if notice != "" {
		target += "?notice=" + url.QueryEscape(notice)
	}
	http.Redirect(w, r, target, http.StatusSeeOther) //nolint:gosec // local path only, checked above
}

// page renders a page with the notice of the previous action.
func (s *Server) page(w http.ResponseWriter, r *http.Request, name, title string, crumbs []views.Crumb, data any) {
	notice := r.URL.Query().Get("notice")
	if len(notice) > 300 {
		notice = notice[:300]
	}
	p := views.Page{Title: title, User: auth.FromContext(r.Context()), Notice: notice, Breadcrumbs: crumbs, Data: data}
	if err := s.Views.Render(w, http.StatusOK, name, p); err != nil {
		s.Log.Error("render failed", "page", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func clusterCrumbs(c *clusters.Cluster) []views.Crumb {
	return []views.Crumb{{Label: "Clusters", Href: "/"}, {Label: c.DisplayNameOrName(), Href: clusterPath(c.Name)}}
}

// --- test page ----------------------------------------------------------------------

// param is one spec.config parameter as a form field.
type param struct {
	Name, Type, Default, Pattern, Description string
	Value                                     string // the editor's current value
	Enum                                      []string
	Required                                  bool
	InputType, Step                           string
}

func paramsOf(spec *testsv1alpha1.TestSpec) []param {
	if spec == nil {
		return nil
	}
	out := make([]param, 0, len(spec.Config))
	for _, name := range slices.Sorted(maps.Keys(spec.Config)) {
		p := spec.Config[name]
		f := param{Name: name, Type: p.Type, Default: p.Default, Pattern: p.Pattern,
			Enum: p.Enum, Required: p.Default == "", InputType: inputText}
		switch p.Type {
		case expr.TypeInteger:
			f.InputType, f.Step = inputNumber, "1"
		case inputNumber:
			f.InputType, f.Step = inputNumber, "any"
		}
		out = append(out, f)
	}
	return out
}

// runLogDownload streams the whole stored log as a file.
func (s *Server) runLogDownload(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	ns, id := r.PathValue("ns"), r.PathValue("id")
	body, err := api(r, c).OpenLogs(r.Context(), ns, id)
	if err != nil {
		s.apiError(w, r, c, err)
		return
	}
	defer func() { _ = body.Close() }()
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", safeName(id)+".log"))
	_, _ = io.Copy(w, body)
}

// runArtifact streams one artifact. Workload-produced content: sandboxed
// (no script runs with Control Center's origin), never sniffed.
func (s *Server) runArtifact(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	ns, id, path := r.PathValue("ns"), r.PathValue("id"), r.PathValue("path")
	st, err := api(r, c).OpenArtifact(r.Context(), ns, id, path)
	if err != nil {
		s.apiError(w, r, c, err)
		return
	}
	defer func() { _ = st.Body.Close() }()
	h := w.Header()
	ct := st.ContentType
	if ct == "" || strings.HasPrefix(ct, "application/octet-stream") {
		// A video recorded without a type would not play inline.
		ct = cmp.Or(uiapi.MediaTypes[strings.ToLower(pathpkg.Ext(path))], ct)
	}
	h.Set("Content-Type", ct)
	// Artifacts are test output: HTML reports (JMeter, Gatling,
	// Playwright, k6) need their scripts, so scripts run — but in a
	// sandbox without allow-same-origin, i.e. an opaque origin that can't
	// read Control Center's cookies or pages, submit forms or navigate
	// the top window.
	h.Set("Content-Security-Policy", artifactCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	disposition := dispositionInline
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	file := path[strings.LastIndex(path, "/")+1:]
	h.Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, safeName(file)))
	if disposition == dispositionInline && strings.HasPrefix(ct, "text/html") {
		// Shown, not downloaded: reports that need localStorage get one
		// (storageshim.go); the length changes.
		_ = copyWithStorageShim(w, st.Body)
		return
	}
	if st.Length >= 0 {
		h.Set("Content-Length", strconv.FormatInt(st.Length, 10))
	}
	_, _ = io.Copy(w, st.Body)
}

// safeName keeps a file name header-safe.
func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == '"' || r == '\\' || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
