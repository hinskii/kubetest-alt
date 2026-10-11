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

package api

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/resolver"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// The test editor's API (step 20R R4): the UI holds the form; Control
// Center turns it into the Test with the same Build every time — for the
// live YAML, the dry run and the save — so there is one implementation of
// the rules, in Go.

// NamespaceList is where Tests can go: namespaces that have Tests or a
// tool catalog.
type NamespaceList struct {
	Namespaces []string `json:"namespaces" required:"true" nullable:"false"`
}

// TemplateList is a namespace's tool catalog.
type TemplateList struct {
	Templates []ToolTemplate `json:"templates" required:"true" nullable:"false"`
	// Error: the catalog couldn't be listed.
	Error string `json:"error,omitempty"`
}

// ToolTemplate is one tool of the catalog.
type ToolTemplate struct {
	Name    string  `json:"name" required:"true"`
	Tool    string  `json:"tool" required:"true"`
	Image   string  `json:"image" required:"true"`
	Command string  `json:"command,omitempty"`
	Args    string  `json:"args,omitempty"`
	Params  []Param `json:"params" required:"true" nullable:"false"`
	// MainParam is where the tool finds the Test's files (step 20h);
	// MainKind is file or directory — a directory tool runs a project,
	// from git only (20i).
	MainParam string `json:"mainParam,omitempty"`
	MainKind  string `json:"mainKind,omitempty" enum:"file,directory,"`
}

// TestFormOf is a Test as the editor shows it.
type TestFormOf struct {
	Form TestForm `json:"form" required:"true"`
	// YAMLOnly says why the Test is edited as YAML only.
	YAMLOnly string `json:"yamlOnly,omitempty"`
	// Locked: managed outside the GUI; it can be duplicated, not edited.
	Locked bool `json:"locked" required:"true"`
}

// Preview is the Test a form describes, as YAML for Git, and what is
// wrong or worth knowing about it.
type Preview struct {
	YAML     string   `json:"yaml,omitempty"`
	Errors   []string `json:"errors" required:"true" nullable:"false"`
	Warnings []string `json:"warnings" required:"true" nullable:"false"`
}

// SavedTest is the Test written (or, in a dry run, admitted).
type SavedTest struct {
	Namespace string `json:"namespace" required:"true"`
	Name      string `json:"name" required:"true"`
	// DryRun: nothing was written; the API admitted it.
	DryRun bool `json:"dryRun" required:"true"`
	// YAML is the Test as YAML for Git.
	YAML string `json:"yaml" required:"true"`
}

type templatesParams struct {
	Cluster   string `path:"cluster"`
	Namespace string `query:"namespace" required:"true"`
}

type writeParams struct {
	Cluster string `path:"cluster"`
	// DryRun: check it (schema, admission webhooks, pod policy) without
	// writing.
	DryRun bool `query:"dry-run"`
}

type writeTestParams struct {
	Cluster   string `path:"cluster"`
	Namespace string `path:"ns"`
	Name      string `path:"name"`
	DryRun    bool   `query:"dry-run"`
}

func (s *Server) listNamespaces(w http.ResponseWriter, r *http.Request) {
	_, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, NamespaceList{Namespaces: NamespacesOf(r.Context(), client)})
}

// NamespacesOf lists the namespaces that have Tests or a tool catalog.
func NamespacesOf(ctx context.Context, client *apiclient.Client) []string {
	set := map[string]bool{}
	if tests, err := client.ListTests(ctx, ""); err == nil {
		for _, t := range tests {
			set[t.Namespace] = true
		}
	}
	if templates, err := client.ListTemplates(ctx, ""); err == nil {
		for _, t := range templates {
			set[t.Namespace] = true
		}
	}
	return slices.Sorted(maps.Keys(set))
}

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	_, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if ns == "" {
		writeError(w, http.StatusBadRequest, "Which namespace's catalog? (?namespace= is missing)")
		return
	}
	out := TemplateList{Templates: []ToolTemplate{}}
	list, err := client.ListTemplates(r.Context(), ns)
	if err != nil {
		out.Error = messageOf(err)
	}
	out.Templates = ToolTemplates(list)
	writeJSON(w, http.StatusOK, out)
}

// ToolTemplates is the catalog as the editor offers it, by name.
func ToolTemplates(list []testsv1alpha1.TestTemplate) []ToolTemplate {
	out := make([]ToolTemplate, 0, len(list))
	for _, t := range list {
		o := ToolTemplate{Name: t.Name, Tool: t.Labels[LabelTool], Image: t.Spec.Container.Image,
			Command: strings.Join(t.Spec.Container.Command, " "), Args: strings.Join(t.Spec.Container.Args, " "),
			Params: ParamsOf(&testsv1alpha1.TestSpec{Config: t.Spec.Config})}
		o.MainParam, o.MainKind = resolver.MainPathParam(t.Spec.Config)
		out = append(out, o)
	}
	slices.SortFunc(out, func(a, b ToolTemplate) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// FindTemplate is the template named name in list, or nil.
func FindTemplate(list []testsv1alpha1.TestTemplate, name string) *testsv1alpha1.TestTemplate {
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

func (s *Server) testForm(w http.ResponseWriter, r *http.Request) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return
	}
	t, err := client.GetTest(r.Context(), r.PathValue("ns"), r.PathValue("name"))
	if err != nil {
		s.apiError(w, c, err)
		return
	}
	out := TestFormOf{Locked: Locked(t.Labels)}
	out.Form, out.YAMLOnly = FormOf(r.Context(), client, t)
	writeJSON(w, http.StatusOK, out)
}

// FormOf fills the editor from an existing Test (its template looked up
// in its namespace); a Test the form can't represent opens in YAML mode,
// with the reason.
func FormOf(ctx context.Context, client *apiclient.Client, t *testsv1alpha1.Test) (TestForm, string) {
	var tmpl *testsv1alpha1.TestTemplate
	if len(t.Spec.Use) == 1 {
		if list, err := client.ListTemplates(ctx, t.Namespace); err == nil {
			tmpl = FindTemplate(list, t.Spec.Use[0])
		}
	}
	f := FormFromTest(t, tmpl)
	reason := FormUnsupported(t)
	if reason != "" {
		f.Mode = ModeYAML
	}
	f.YAML = ManifestYAML(t)
	return f, reason
}

// editorBuild is a form turned into the Test it describes: base is the
// Test being edited (an empty one for a new Test); t is nil when the form
// can't be built.
type editorBuild struct {
	base             *testsv1alpha1.Test
	t                *testsv1alpha1.Test
	errors, warnings []string
	// resourceVersion is the version the edit started from (the form's).
	resourceVersion string
}

// build reads a form and builds its Test — what the preview shows is what
// a save writes. For an edit (edit=true) namespace and name are the
// path's, and the current Test is the base.
func (s *Server) build(w http.ResponseWriter, r *http.Request, edit bool) (*editorBuild, *apiclient.Client, bool) {
	c, client, ok := s.clusterOf(w, r)
	if !ok {
		return nil, nil, false
	}
	var f TestForm
	if !decode(w, r, &f) {
		return nil, nil, false
	}
	f.Normalize()
	b := &editorBuild{base: &testsv1alpha1.Test{}, errors: []string{}, warnings: []string{},
		resourceVersion: f.ResourceVersion}
	if edit {
		ns, name := r.PathValue("ns"), r.PathValue("name")
		current, err := client.GetTest(r.Context(), ns, name)
		if err != nil {
			s.apiError(w, c, err)
			return nil, nil, false
		}
		b.base = current
		f.Namespace, f.Name = ns, name
	}
	if f.Mode == ModeYAML {
		var errs []string
		if b.t, errs = ParseManifest(f.YAML, b.base); b.t != nil {
			f.Namespace = b.t.Namespace
		}
		b.errors = append(b.errors, errs...)
		return b, client, true
	}
	var templates []testsv1alpha1.TestTemplate
	if f.Namespace != "" {
		var err error
		if templates, err = client.ListTemplates(r.Context(), f.Namespace); err != nil {
			b.warnings = append(b.warnings, "The tool catalog couldn't be read: "+messageOf(err))
		}
	}
	t, errs, warns := f.Build(b.base, FindTemplate(templates, f.Template))
	b.t, b.errors, b.warnings = t, append(b.errors, errs...), append(b.warnings, warns...)
	return b, client, true
}

// preview answers the editor's live YAML: the Test the form would write —
// while the form is still incomplete too, with what is missing listed
// (people fill it in step by step). A Test that can't be built at all
// (broken YAML) has no YAML, only its errors; still a 200, as the request
// itself is fine (browsers log every 4xx). No dry run: that is Check and
// Create.
func (s *Server) preview(edit bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _, ok := s.build(w, r, edit)
		if !ok {
			return
		}
		out := Preview{Errors: b.errors, Warnings: b.warnings}
		if b.t != nil {
			out.YAML = ManifestYAML(b.t)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// FormError is a form that can't be saved: what is wrong, all of it.
type FormError struct {
	Error  string   `json:"error" required:"true"`
	Errors []string `json:"errors" required:"true" nullable:"false"`
}

// write creates the form's Test (or saves the edit), or with ?dry-run=true
// asks the API whether it would.
func (s *Server) write(edit bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, client, ok := s.build(w, r, edit)
		if !ok {
			return
		}
		if len(b.errors) > 0 || b.t == nil {
			writeJSON(w, http.StatusBadRequest, FormError{Error: strings.Join(b.errors, " "), Errors: b.errors})
			return
		}
		dryRun := r.URL.Query().Get("dry-run") == "true"
		written, err := WriteTest(r.Context(), client, edit, b.base, b.t, b.resourceVersion, dryRun)
		if err != nil {
			s.writeError(w, err, edit)
			return
		}
		status := http.StatusOK
		if !edit && !dryRun {
			status = http.StatusCreated
		}
		writeJSON(w, status, SavedTest{Namespace: written.Namespace, Name: written.Name, DryRun: dryRun,
			YAML: ManifestYAML(b.t)})
	}
}

// writeError answers a refused write: a conflict on an edit is the Test
// having changed since it was opened.
func (s *Server) writeError(w http.ResponseWriter, err error, edit bool) {
	var apiErr *apiclient.APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict && edit &&
		strings.Contains(apiErr.Message, "modified") {
		writeError(w, http.StatusConflict,
			"The Test changed since you opened it. Reload it to start from the current version.")
		return
	}
	if errors.As(err, &apiErr) {
		writeError(w, apiErr.Status, apiErr.Message)
		return
	}
	writeError(w, http.StatusBadGateway, messageOf(err))
}

// WriteTest creates t, or patches base into t (conditional on
// resourceVersion); dryRun only asks the API whether it would.
func WriteTest(ctx context.Context, client *apiclient.Client, edit bool,
	base, t *testsv1alpha1.Test, resourceVersion string, dryRun bool) (*testsv1alpha1.Test, error) {
	opts := apiclient.WriteOptions{DryRun: dryRun}
	if !edit {
		return client.CreateTest(ctx, t, opts)
	}
	old := base.DeepCopy()
	old.ResourceVersion = resourceVersion
	patch, err := EditPatch(old, t)
	if err != nil {
		return nil, err
	}
	return client.PatchTestWith(ctx, t.Namespace, t.Name, patch, opts)
}
