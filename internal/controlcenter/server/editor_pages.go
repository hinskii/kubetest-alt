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
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	uiapi "github.com/hinskii/kubetest-alt/internal/controlcenter/api"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/clusters"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/views"
	"github.com/hinskii/kubetest-alt/internal/resolver"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// Wizard steps; stepReview shows the YAML and the API's verdict.
const (
	stepBasics = 1
	stepReview = 5
)

// Editor actions (the submit button pressed).
const (
	actPreview  = "preview"
	actSave     = "save"
	actDownload = "download"
	actToYAML   = "to-yaml"
	actToForm   = "to-form"
	// actReload re-renders the wizard for another namespace's catalog,
	// keeping what was typed and the step — no checks, no dry run.
	actReload = "reload"
)

// templateOption is one tool of the catalog in the wizard.
type templateOption struct {
	Name, Tool, Image string
	Command, Args     string
	Params            []param
	// MainParam is where the tool finds the Test's files (step 20h): set
	// from the path in the repository unless typed. MainKind is file or
	// directory — a directory tool runs a project, from git only (20i).
	MainParam, MainKind string
}

type editorData struct {
	Cluster string
	// Edit: editing an existing Test (namespace and name fixed).
	Edit       bool
	Action     string // the form's URL
	Back       string
	Form       uiapi.TestForm
	Namespaces []string
	// NamespaceListed: the form's namespace is one of Namespaces (else
	// "Other namespace…" with the typed name).
	NamespaceListed bool
	Templates       []templateOption
	// TemplatesErr: the catalog couldn't be listed.
	TemplatesErr string
	Errors       []string
	// Warnings don't stop a save (e.g. the main path outside the sparse
	// checkout).
	Warnings []string
	// Preview is the resulting Test as YAML for Git; Admitted says the
	// API (schema, webhooks, pod policy) accepted it in a dry run, else
	// CheckErr is its answer.
	Preview  string
	Admitted bool
	CheckErr string
	// SaveFailed: CheckErr is the answer to saving, not to a dry run.
	SaveFailed bool
	Step       int
	// YAMLOnly: why this Test is edited as YAML only.
	YAMLOnly string
}

func newTestPath(c string) string { return clusterPath(c) + "/tests/new" }

// templateOptions turns the catalog into wizard choices, with the form's
// values for the chosen template's parameters.
func templateOptions(list []testsv1alpha1.TestTemplate, f uiapi.TestForm) []templateOption {
	out := make([]templateOption, 0, len(list))
	for _, t := range list {
		o := templateOption{Name: t.Name, Tool: t.Labels[labelTool], Image: t.Spec.Container.Image,
			Command: strings.Join(t.Spec.Container.Command, " "), Args: strings.Join(t.Spec.Container.Args, " ")}
		o.Params = paramsOf(&testsv1alpha1.TestSpec{Config: t.Spec.Config})
		o.MainParam, o.MainKind = resolver.MainPathParam(t.Spec.Config)
		for i := range o.Params {
			o.Params[i].Value = o.Params[i].Default
			if v, ok := f.Params[o.Params[i].Name]; ok && t.Name == f.Template {
				o.Params[i].Value = v
			}
		}
		out = append(out, o)
	}
	slices.SortFunc(out, func(a, b templateOption) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// newTestPage: GET /tests/new — the wizard, empty, for a template
// (?template=), or as a copy of a Test (?from=, Duplicate).
func (s *Server) newTestPage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	client := api(r, c)
	f := uiapi.TestForm{Namespace: strings.TrimSpace(q.Get(fieldNamespace)), Name: strings.TrimSpace(q.Get(fieldName)),
		Template: q.Get(fieldTemplate), Mode: uiapi.ModeForm, Params: map[string]string{}}
	data := editorData{Cluster: c.Name, Action: newTestPath(c.Name), Back: clusterPath(c.Name), Step: stepBasics}
	if from := q.Get("from"); from != "" && f.Namespace != "" {
		src, err := client.GetTest(r.Context(), f.Namespace, from)
		if err != nil {
			s.apiError(w, r, c, err)
			return
		}
		f = s.formOf(r.Context(), client, src, &data)
		f.Name, f.ResourceVersion = from+"-copy", ""
		data.Back = testPath(c.Name, src.Namespace, from)
	}
	data.Form = f
	s.renderEditor(w, r, c, client, data)
}

// formOf fills the editor from an existing Test; Tests the form can't
// represent open in YAML mode.
func (s *Server) formOf(ctx context.Context, client *apiclient.Client, t *testsv1alpha1.Test, data *editorData) uiapi.TestForm {
	var tmpl *testsv1alpha1.TestTemplate
	if len(t.Spec.Use) == 1 {
		if list, err := client.ListTemplates(ctx, t.Namespace); err == nil {
			tmpl = uiapi.FindTemplate(list, t.Spec.Use[0])
		}
	}
	f := uiapi.FormFromTest(t, tmpl)
	if reason := uiapi.FormUnsupported(t); reason != "" {
		data.YAMLOnly, f.Mode = reason, uiapi.ModeYAML
	}
	f.YAML = uiapi.ManifestYAML(t)
	return f
}

// editTestPage: GET /tests/{ns}/{name}/edit.
func (s *Server) editTestPage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	client := api(r, c)
	t, err := client.GetTest(r.Context(), ns, name)
	if err != nil {
		s.apiError(w, r, c, err)
		return
	}
	if t.Labels[labelManagedBy] != managedByUI {
		s.renderError(w, r, http.StatusConflict, "Test "+name+" is managed outside the GUI (Git or kubectl): "+
			"change it there, or use Duplicate on its page to make a GUI-managed copy.")
		return
	}
	data := editorData{Cluster: c.Name, Edit: true, Action: testPath(c.Name, ns, name) + "/edit",
		Back: testPath(c.Name, ns, name), Step: stepBasics}
	data.Form = s.formOf(r.Context(), client, t, &data)
	s.renderEditor(w, r, c, client, data)
}

// submitNewTest: POST /tests/new.
func (s *Server) submitNewTest(w http.ResponseWriter, r *http.Request) {
	s.submitEditor(w, r, false)
}

// submitEditTest: POST /tests/{ns}/{name}/edit.
func (s *Server) submitEditTest(w http.ResponseWriter, r *http.Request) {
	s.submitEditor(w, r, true)
}

// editorState is a submitted wizard: the form, the page's data, the Test
// being edited (or an empty one), the namespace's catalog and the Test the
// form describes (nil when it has errors).
type editorState struct {
	c         *clusters.Cluster
	client    *apiclient.Client
	f         uiapi.TestForm
	data      editorData
	base      *testsv1alpha1.Test
	templates []testsv1alpha1.TestTemplate
	t         *testsv1alpha1.Test
}

// prepareEditor reads a submitted wizard and builds the Test it
// describes — shared by every button and by the live YAML, so the preview
// is what Create would write. false: an error page was written.
func (s *Server) prepareEditor(w http.ResponseWriter, r *http.Request, edit bool) (*editorState, bool) {
	c, ok := s.cluster(w, r)
	if !ok {
		return nil, false
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Unreadable form: "+err.Error())
		return nil, false
	}
	st := &editorState{c: c, client: api(r, c), f: formFromValues(r.PostForm), base: &testsv1alpha1.Test{}}
	st.data = editorData{Cluster: c.Name, Edit: edit, Action: newTestPath(c.Name), Back: clusterPath(c.Name), Step: stepReview}
	if edit {
		ns, name := r.PathValue("ns"), r.PathValue("name")
		current, err := st.client.GetTest(r.Context(), ns, name)
		if err != nil {
			s.apiError(w, r, c, err)
			return nil, false
		}
		st.base = current
		st.f.Namespace, st.f.Name = ns, name
		st.data.Action, st.data.Back = testPath(c.Name, ns, name)+"/edit", testPath(c.Name, ns, name)
		st.data.YAMLOnly = uiapi.FormUnsupported(current)
	}
	if st.f.Mode == uiapi.ModeYAML {
		if st.t, st.data.Errors = uiapi.ParseManifest(st.f.YAML, st.base); st.t != nil {
			st.f.Namespace = st.t.Namespace
		}
	}
	if st.f.Namespace != "" {
		var err error
		if st.templates, err = st.client.ListTemplates(r.Context(), st.f.Namespace); err != nil {
			st.data.TemplatesErr = messageOf(err)
		}
		st.data.Templates = templateOptions(st.templates, st.f)
	}
	if st.f.Mode == uiapi.ModeForm {
		st.t, st.data.Errors, st.data.Warnings = st.f.Build(st.base, uiapi.FindTemplate(st.templates, st.f.Template))
	}
	return st, true
}

// yamlPanel is the live YAML beside the wizard.
type yamlPanel struct {
	YAML             string
	Errors, Warnings []string
}

// editorYAML answers the wizard's live preview: the YAML the form would
// create, highlighted — while the form is still incomplete too, with what
// is missing listed under it (people fill it in step by step). Only a
// Test that can't be built at all (broken YAML) is 422, and the page keeps
// the last YAML. No dry run: that stays on Create.
func (s *Server) editorYAML(w http.ResponseWriter, r *http.Request, edit bool) {
	st, ok := s.prepareEditor(w, r, edit)
	if !ok {
		return
	}
	panel, status := yamlPanel{Errors: st.data.Errors, Warnings: st.data.Warnings}, http.StatusOK
	if st.t == nil {
		status = http.StatusUnprocessableEntity
	} else {
		panel.YAML = uiapi.ManifestYAML(st.t)
	}
	if err := s.Views.RenderFragment(w, status, "editor", "yaml-panel", panel); err != nil {
		s.Log.Error("render failed", "fragment", "yaml-panel", "err", err)
	}
}

// newTestYAML: POST /tests/new/yaml.
func (s *Server) newTestYAML(w http.ResponseWriter, r *http.Request) { s.editorYAML(w, r, false) }

// editTestYAML: POST /tests/{ns}/{name}/edit/yaml.
func (s *Server) editTestYAML(w http.ResponseWriter, r *http.Request) { s.editorYAML(w, r, true) }

// submitEditor handles every button of the wizard: preview (dry run),
// save, download the YAML, switch between form and YAML.
func (s *Server) submitEditor(w http.ResponseWriter, r *http.Request, edit bool) {
	st, ok := s.prepareEditor(w, r, edit)
	if !ok {
		return
	}
	c, client, f, data, base, templates, t := st.c, st.client, st.f, st.data, st.base, st.templates, st.t
	action := r.PostForm.Get("action")
	switch {
	case action == actReload:
		data.Errors, data.Warnings = nil, nil
		data.Step = min(max(atoi(r.PostForm.Get("step")), stepBasics), stepReview)
		data.Form = f
		s.renderEditor(w, r, c, client, data)
		return
	case action == actToYAML && len(data.Errors) == 0:
		f.Mode, f.YAML = uiapi.ModeYAML, uiapi.ManifestYAML(t)
	case action == actToForm && len(data.Errors) == 0:
		if reason := uiapi.FormUnsupported(t); reason != "" {
			data.Errors = append(data.Errors, reason)
			break
		}
		rv := f.ResourceVersion
		f = uiapi.FormFromTest(t, uiapi.FindTemplate(templates, uiapi.FirstOf(t.Spec.Use)))
		f.ResourceVersion, data.Step = rv, stepBasics
		data.Templates = templateOptions(templates, f)
	case action == actDownload && len(data.Errors) == 0:
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+safeName(t.Name)+`.yaml"`)
		// A download, not a page: application/yaml as an attachment, with
		// nosniff (securityHeaders).
		_, _ = w.Write([]byte(uiapi.ManifestYAML(t))) //nolint:gosec // see above
		return
	}
	data.Form = f
	if len(data.Errors) > 0 {
		s.renderEditor(w, r, c, client, data)
		return
	}
	data.Preview = uiapi.ManifestYAML(t)
	if f.Mode == uiapi.ModeForm {
		data.Form.YAML = data.Preview
	}

	save := action == actSave
	written, err := uiapi.WriteTest(r.Context(), client, edit, base, t, f.ResourceVersion, !save)
	switch {
	case err != nil:
		data.CheckErr, data.SaveFailed = messageOf(err), save
		if edit && apiclient.IsConflict(err) && strings.Contains(data.CheckErr, "modified") {
			data.CheckErr = "The Test changed since you opened the editor. Reload it to start from the current version."
		}
	case save && edit:
		redirect(w, r, testPath(c.Name, written.Namespace, written.Name), "Test saved.")
		return
	case save:
		redirect(w, r, testPath(c.Name, written.Namespace, written.Name), "Test created.")
		return
	default:
		data.Admitted = true
	}
	s.renderEditor(w, r, c, client, data)
}

func (s *Server) renderEditor(w http.ResponseWriter, r *http.Request, c *clusters.Cluster, client *apiclient.Client, data editorData) {
	if data.Form.Namespace != "" && data.Templates == nil {
		list, err := client.ListTemplates(r.Context(), data.Form.Namespace)
		if err != nil {
			data.TemplatesErr = messageOf(err)
		}
		data.Templates = templateOptions(list, data.Form)
	}
	if !data.Edit {
		data.Namespaces = uiapi.NamespacesOf(r.Context(), client)
		data.NamespaceListed = slices.Contains(data.Namespaces, data.Form.Namespace)
	}
	// One empty row to add a file to (without JavaScript, too).
	data.Form.Files = append(slices.Clone(data.Form.Files), uiapi.FileField{})
	data.Form.TestData = append(slices.Clone(data.Form.TestData), uiapi.TestDataField{})
	title, crumbs := "New test", clusterCrumbs(c)
	if data.Edit {
		title = "Edit " + data.Form.Name
		crumbs = append(crumbs, views.Crumb{Label: data.Form.Name, Href: data.Back})
	}
	s.page(w, r, "editor", title, crumbs, data)
}

// deleteTest: POST /tests/{ns}/{name}/delete (admin). The run history
// stays; the API refuses Tests managed outside the GUI.
// atoi is strconv.Atoi without the error (0 for anything else).
func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
