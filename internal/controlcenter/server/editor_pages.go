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
	"maps"
	"net/http"
	"slices"
	"strings"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/clusters"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/views"
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
)

// templateOption is one tool of the catalog in the wizard.
type templateOption struct {
	Name, Tool, Image string
	Command, Args     string
	Params            []param
}

type editorData struct {
	Cluster string
	// Edit: editing an existing Test (namespace and name fixed).
	Edit       bool
	Action     string // the form's URL
	Back       string
	Form       testForm
	Namespaces []string
	Templates  []templateOption
	// TemplatesErr: the catalog couldn't be listed.
	TemplatesErr string
	Errors       []string
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
func templateOptions(list []testsv1alpha1.TestTemplate, f testForm) []templateOption {
	out := make([]templateOption, 0, len(list))
	for _, t := range list {
		o := templateOption{Name: t.Name, Tool: t.Labels[labelTool], Image: t.Spec.Container.Image,
			Command: strings.Join(t.Spec.Container.Command, " "), Args: strings.Join(t.Spec.Container.Args, " ")}
		o.Params = paramsOf(&testsv1alpha1.TestSpec{Config: t.Spec.Config})
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

func findTemplate(list []testsv1alpha1.TestTemplate, name string) *testsv1alpha1.TestTemplate {
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

// namespacesOf lists the namespaces that have Tests, for the namespace
// field's suggestions.
func namespacesOf(ctx context.Context, client *apiclient.Client, extra string) []string {
	set := map[string]bool{}
	if extra != "" {
		set[extra] = true
	}
	if tests, err := client.ListTests(ctx, ""); err == nil {
		for _, t := range tests {
			set[t.Namespace] = true
		}
	}
	return slices.Sorted(maps.Keys(set))
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
	f := testForm{Namespace: strings.TrimSpace(q.Get(fNamespace)), Name: strings.TrimSpace(q.Get(fName)),
		Template: q.Get(fTemplate), Mode: modeForm, Params: map[string]string{}}
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
func (s *Server) formOf(ctx context.Context, client *apiclient.Client, t *testsv1alpha1.Test, data *editorData) testForm {
	var tmpl *testsv1alpha1.TestTemplate
	if len(t.Spec.Use) == 1 {
		if list, err := client.ListTemplates(ctx, t.Namespace); err == nil {
			tmpl = findTemplate(list, t.Spec.Use[0])
		}
	}
	f := formFromTest(t, tmpl)
	if reason := formUnsupported(t); reason != "" {
		data.YAMLOnly, f.Mode = reason, modeYAML
	}
	f.YAML = manifestYAML(t)
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

// submitEditor handles every button of the wizard: preview (dry run),
// save, download the YAML, switch between form and YAML.
func (s *Server) submitEditor(w http.ResponseWriter, r *http.Request, edit bool) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Unreadable form: "+err.Error())
		return
	}
	client := api(r, c)
	f := formFromValues(r.PostForm)
	data := editorData{Cluster: c.Name, Edit: edit, Action: newTestPath(c.Name), Back: clusterPath(c.Name), Step: stepReview}
	base := &testsv1alpha1.Test{}
	if edit {
		ns, name := r.PathValue("ns"), r.PathValue("name")
		current, err := client.GetTest(r.Context(), ns, name)
		if err != nil {
			s.apiError(w, r, c, err)
			return
		}
		base = current
		f.Namespace, f.Name = ns, name
		data.Action, data.Back = testPath(c.Name, ns, name)+"/edit", testPath(c.Name, ns, name)
		data.YAMLOnly = formUnsupported(current)
	}
	action := r.PostForm.Get("action")
	var t *testsv1alpha1.Test
	if f.Mode == modeYAML {
		if t, data.Errors = parseManifest(f.YAML, base); t != nil {
			f.Namespace = t.Namespace
		}
	}
	var templates []testsv1alpha1.TestTemplate
	if f.Namespace != "" {
		var err error
		if templates, err = client.ListTemplates(r.Context(), f.Namespace); err != nil {
			data.TemplatesErr = messageOf(err)
		}
		data.Templates = templateOptions(templates, f)
	}
	if f.Mode == modeForm {
		t, data.Errors = f.build(base, findTemplate(templates, f.Template))
	}
	switch {
	case action == actToYAML && len(data.Errors) == 0:
		f.Mode, f.YAML = modeYAML, manifestYAML(t)
	case action == actToForm && len(data.Errors) == 0:
		if reason := formUnsupported(t); reason != "" {
			data.Errors = append(data.Errors, reason)
			break
		}
		rv := f.ResourceVersion
		f = formFromTest(t, findTemplate(templates, firstOf(t.Spec.Use)))
		f.ResourceVersion, data.Step = rv, stepBasics
		data.Templates = templateOptions(templates, f)
	case action == actDownload && len(data.Errors) == 0:
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+safeName(t.Name)+`.yaml"`)
		// A download, not a page: application/yaml as an attachment, with
		// nosniff (securityHeaders).
		_, _ = w.Write([]byte(manifestYAML(t))) //nolint:gosec // see above
		return
	}
	data.Form = f
	if len(data.Errors) > 0 {
		s.renderEditor(w, r, c, client, data)
		return
	}
	data.Preview = manifestYAML(t)
	if f.Mode == modeForm {
		data.Form.YAML = data.Preview
	}

	save := action == actSave
	written, err := s.writeTest(r.Context(), client, edit, base, t, f.ResourceVersion, !save)
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

func firstOf(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// writeTest creates t, or patches base into t; dryRun only asks the API
// whether it would.
func (s *Server) writeTest(ctx context.Context, client *apiclient.Client, edit bool,
	base, t *testsv1alpha1.Test, resourceVersion string, dryRun bool) (*testsv1alpha1.Test, error) {
	opts := apiclient.WriteOptions{DryRun: dryRun}
	if !edit {
		return client.CreateTest(ctx, t, opts)
	}
	old := base.DeepCopy()
	old.ResourceVersion = resourceVersion
	patch, err := editPatch(old, t)
	if err != nil {
		return nil, err
	}
	return client.PatchTestWith(ctx, t.Namespace, t.Name, patch, opts)
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
		data.Namespaces = namespacesOf(r.Context(), client, data.Form.Namespace)
	}
	// One empty row to add a file to (without JavaScript, too).
	data.Form.Files = append(slices.Clone(data.Form.Files), fileField{})
	title, crumbs := "New test", clusterCrumbs(c)
	if data.Edit {
		title = "Edit " + data.Form.Name
		crumbs = append(crumbs, views.Crumb{Label: data.Form.Name, Href: data.Back})
	}
	s.page(w, r, "editor", title, crumbs, data)
}

// deleteTest: POST /tests/{ns}/{name}/delete (admin). The run history
// stays; the API refuses Tests managed outside the GUI.
func (s *Server) deleteTest(w http.ResponseWriter, r *http.Request) {
	c, ok := s.cluster(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	if err := api(r, c).DeleteTest(r.Context(), ns, name); err != nil {
		redirect(w, r, testPath(c.Name, ns, name), "Could not delete the Test: "+messageOf(err))
		return
	}
	redirect(w, r, clusterPath(c.Name), "Test "+ns+"/"+name+" deleted. Its run history stays.")
}
