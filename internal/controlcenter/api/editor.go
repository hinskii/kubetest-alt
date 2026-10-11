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
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"reflect"
	"slices"
	"strings"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/yamlview"
	"github.com/hinskii/kubetest-alt/internal/resolver"
	"github.com/hinskii/kubetest-alt/pkg/expr"
)

// The test editor (wizard): a form over the common shape of a Test — one
// TestTemplate or an own image, git and inline-file content, the
// template's parameters, container, pod metadata, timeout, schedule — and
// a YAML mode for everything else. Control Center keeps no state: each
// step posts the whole form back, and the Test the form describes is
// rebuilt from it every time.

// KindTest is the Test CRD's kind.
const KindTest = "Test"

// Editor modes.
const (
	ModeForm = "form"
	ModeYAML = "yaml"
)

// TestForm is everything the wizard edits, as submitted.
type TestForm struct {
	Namespace string `json:"namespace" required:"true"`
	Name      string `json:"name" required:"true"`
	// Template is the TestTemplate the Test uses; "" = an own image.
	Template string `json:"template,omitempty"`
	// Tool is the kubetest.io/tool label (taken from the template).
	Tool  string `json:"tool,omitempty"`
	Image string `json:"image,omitempty"`
	// Command and Args: one item per line.
	Command string `json:"command,omitempty"`
	Args    string `json:"args,omitempty"`
	// Source is where the code comes from: git, inline files, or none (an
	// own image that needs no files) — one, never several (step 20i).
	Source      string `json:"source,omitempty" enum:"git,inline,"`
	UseGit      bool   `json:"-"`
	GitURI      string `json:"gitUri,omitempty"`
	GitRevision string `json:"gitRevision,omitempty"`
	// GitPath is where the tests are in the repository (a file or a
	// directory): the template's main path parameter and, unless GitPaths
	// says otherwise, the sparse checkout. Required with git (step 20h).
	GitPath string `json:"gitPath,omitempty"`
	// GitPaths: the sparse checkout, one per line (advanced).
	GitPaths string `json:"gitPaths,omitempty"`
	// GitSecret: the Secret with the token, optional.
	GitSecret    string      `json:"gitSecret,omitempty"`
	GitSecretKey string      `json:"gitSecretKey,omitempty"`
	UseFiles     bool        `json:"-"`
	Files        []FileField `json:"files,omitempty"`
	// TestData are ConfigMaps/Secrets mounted as files, with any source.
	TestData []TestDataField `json:"testData,omitempty"`
	// Params are the template's parameters (name → value).
	Params map[string]string `json:"params,omitempty"`
	// Env: NAME=value per line.
	Env           string `json:"env,omitempty"`
	CPURequest    string `json:"cpuRequest,omitempty"`
	MemoryRequest string `json:"memoryRequest,omitempty"`
	CPULimit      string `json:"cpuLimit,omitempty"`
	MemoryLimit   string `json:"memoryLimit,omitempty"`
	// ServiceAccount, PodAnnotations and PodLabels (key=value per line).
	ServiceAccount string `json:"serviceAccount,omitempty"`
	PodAnnotations string `json:"podAnnotations,omitempty"`
	PodLabels      string `json:"podLabels,omitempty"`
	Timeout        string `json:"timeout,omitempty"`
	Schedule       string `json:"schedule,omitempty"`
	// Mode: the form, or YAML (authoritative in yaml mode).
	Mode string `json:"mode,omitempty" enum:"form,yaml"`
	YAML string `json:"yaml,omitempty"`
	// ResourceVersion: the Test's version the edit started from; saving
	// is refused if it changed since.
	ResourceVersion string `json:"resourceVersion,omitempty"`
	// Generation: the Test's definition (metadata.generation) the edit
	// started from. When set, only a changed definition refuses a save —
	// the operator's status updates (every run's) change resourceVersion
	// but not the definition.
	Generation int64 `json:"generation,omitempty"`
}

// FileField is one inline file: a path in the repository's place and its text.
type FileField struct {
	Path    string `json:"path" required:"true"`
	Content string `json:"content" required:"true"`
}

// TestDataField is one content.testData row: a ConfigMap or Secret, and
// where its keys land — the default directory, a directory, or single
// files ("key=/path" per line).
type TestDataField struct {
	Kind  string `json:"kind" required:"true" enum:"configMap,secret"`
	Name  string `json:"name" required:"true"`
	Where string `json:"where,omitempty" enum:"default,dir,files,"`
	// MountPath: the directory (where=dir). Items: key=/path per line
	// (where=files).
	MountPath string `json:"mountPath,omitempty"`
	Items     string `json:"items,omitempty"`
}

// Code sources and testData placements in the form.
const (
	SourceGit     = "git"
	KindConfigMap = "configMap"
	SourceInline  = "inline"
	KindSecret    = "secret"
	WhereDir      = "dir"
	WhereFiles    = "files"
)

// Normalize tidies a submitted form, from the page or the UI's API, the
// same way: spaces trimmed, the mode and the source's flags set, rows
// with nothing in them dropped, line endings and schedules made regular.
func (f *TestForm) Normalize() {
	for _, p := range []*string{&f.Namespace, &f.Name, &f.Tool, &f.Image, &f.GitURI, &f.GitRevision, &f.GitSecret,
		&f.GitSecretKey, &f.CPURequest, &f.MemoryRequest, &f.CPULimit, &f.MemoryLimit, &f.ServiceAccount, &f.Timeout} {
		*p = strings.TrimSpace(*p)
	}
	f.GitPath = strings.Trim(strings.TrimSpace(f.GitPath), "/")
	f.Schedule = strings.Join(strings.Fields(f.Schedule), " ")
	if f.Mode != ModeYAML {
		f.Mode = ModeForm
	}
	f.UseGit, f.UseFiles = f.Source == SourceGit, f.Source == SourceInline
	files := f.Files[:0:0]
	for _, file := range f.Files {
		file.Path = strings.TrimSpace(file.Path)
		file.Content = strings.ReplaceAll(file.Content, "\r\n", "\n")
		if file.Path != "" || strings.TrimSpace(file.Content) != "" {
			files = append(files, file)
		}
	}
	f.Files = files
	data := f.TestData[:0:0]
	for _, td := range f.TestData {
		td.Kind, td.Name, td.Where = strings.TrimSpace(td.Kind), strings.TrimSpace(td.Name), strings.TrimSpace(td.Where)
		td.MountPath, td.Items = strings.TrimSpace(td.MountPath), strings.TrimSpace(td.Items)
		if td.Name != "" {
			data = append(data, td)
		}
	}
	f.TestData = data
	if f.Params == nil {
		f.Params = map[string]string{}
	}
	for k, v := range f.Params {
		f.Params[k] = strings.TrimSpace(v)
	}
}

// FormUnsupported says why the form can't represent t (the editor then
// opens in YAML mode), or "".
func FormUnsupported(t *testsv1alpha1.Test) string {
	switch {
	case len(t.Spec.Steps) > 0:
		return "A composite Test (spec.steps) is edited as YAML."
	case len(t.Spec.Use) > 1:
		return "This Test uses more than one template; it is edited as YAML."
	case len(t.Spec.Content.Tarball) > 0:
		return "This Test's code is a tarball; it is edited as YAML."
	}
	return ""
}

// FormFromTest fills the form from a Test and its template (nil: none or
// not found).
func FormFromTest(t *testsv1alpha1.Test, tmpl *testsv1alpha1.TestTemplate) TestForm {
	s := &t.Spec
	f := TestForm{
		Namespace: t.Namespace, Name: t.Name, Tool: t.Labels[LabelTool], Mode: ModeForm,
		Image: s.Container.Image, Command: strings.Join(s.Container.Command, "\n"),
		Args: strings.Join(s.Container.Args, "\n"), Schedule: s.Schedule,
		ResourceVersion: t.ResourceVersion, Generation: t.Generation, Params: map[string]string{},
	}
	if len(s.Use) > 0 {
		f.Template = s.Use[0]
	}
	if g := s.Content.Git; g != nil {
		f.UseGit, f.GitURI, f.GitRevision, f.GitPaths = true, g.URI, g.Revision, strings.Join(g.Paths, "\n")
		if g.TokenFrom != nil && g.TokenFrom.SecretKeyRef != nil {
			f.GitSecret, f.GitSecretKey = g.TokenFrom.SecretKeyRef.Name, g.TokenFrom.SecretKeyRef.Key
		}
	}
	for _, file := range s.Content.Files {
		if file.ContentFrom == nil {
			f.Files = append(f.Files, FileField{Path: resolver.InlineFilePath(file.Path), Content: file.Content})
		}
	}
	f.UseFiles = len(f.Files) > 0
	switch {
	case s.Content.Git != nil:
		f.Source = SourceGit
	case f.UseFiles:
		f.Source = SourceInline
	}
	f.UseGit = s.Content.Git != nil
	for _, d := range s.Content.TestData {
		row := TestDataField{Kind: KindConfigMap, Name: d.ConfigMap, MountPath: d.MountPath}
		if d.Secret != "" {
			row.Kind, row.Name = KindSecret, d.Secret
		}
		switch {
		case len(d.Items) > 0:
			row.Where = WhereFiles
			ls := make([]string, 0, len(d.Items))
			for _, it := range d.Items {
				ls = append(ls, it.Key+"="+it.Path)
			}
			row.Items = strings.Join(ls, "\n")
		case d.MountPath != "":
			row.Where = WhereDir
		}
		f.TestData = append(f.TestData, row)
	}
	if g := s.Content.Git; g != nil {
		main := ""
		if tmpl != nil {
			main, _ = resolver.MainPathParam(tmpl.Spec.Config)
		}
		if own, ok := s.Config[main]; ok && main != "" {
			f.GitPath = own.Default
		} else if len(g.Paths) > 0 {
			f.GitPath = g.Paths[0]
		}
		if slices.Equal(g.Paths, sparseFor(f.GitPath)) {
			f.GitPaths = "" // derived from the path: nothing to show
		}
	}
	if tmpl != nil {
		main, _ := resolver.MainPathParam(tmpl.Spec.Config)
		for name, p := range tmpl.Spec.Config {
			f.Params[name] = p.Default
			if own, ok := s.Config[name]; ok {
				f.Params[name] = own.Default
			}
		}
		if f.UseGit && main != "" && f.Params[main] == f.GitPath {
			f.Params[main] = "" // follows the path in the repository
		}
	}
	var env []string
	for _, e := range s.Container.Env {
		if e.ValueFrom == nil {
			env = append(env, e.Name+"="+e.Value)
		}
	}
	f.Env = strings.Join(env, "\n")
	res := s.Container.Resources
	f.CPURequest, f.MemoryRequest = quantity(res.Requests, corev1.ResourceCPU), quantity(res.Requests, corev1.ResourceMemory)
	f.CPULimit, f.MemoryLimit = quantity(res.Limits, corev1.ResourceCPU), quantity(res.Limits, corev1.ResourceMemory)
	if p := s.Pod; p != nil {
		f.ServiceAccount = p.ServiceAccountName
		f.PodAnnotations, f.PodLabels = kvText(p.Annotations), kvText(p.Labels)
	}
	if s.Timeout != nil {
		f.Timeout = s.Timeout.Duration.String()
	}
	return f
}

func quantity(l corev1.ResourceList, name corev1.ResourceName) string {
	if q, ok := l[name]; ok {
		return q.String()
	}
	return ""
}

func kvText(m map[string]string) string {
	lines := make([]string, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		lines = append(lines, k+"="+m[k])
	}
	return strings.Join(lines, "\n")
}

// lines splits a textarea into its non-empty, trimmed lines.
func lines(s string) []string {
	var out []string
	for l := range strings.Lines(s) {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// kvLines parses key=value lines.
func kvLines(field, s string) (map[string]string, error) {
	out := map[string]string{}
	for _, l := range lines(s) {
		k, v, ok := strings.Cut(l, "=")
		if k = strings.TrimSpace(k); !ok || k == "" {
			return nil, fmt.Errorf("%s: %q is not key=value", field, l)
		}
		out[k] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// build turns the form into a Test: a copy of base (the Test being
// edited, or an empty one) with the fields the form owns replaced and
// everything else — services, volumes, verdict, … — kept. tmpl is the
// chosen template (nil for an own image). Returns every problem found,
// and warnings that don't stop a save.
func (f TestForm) Build(base *testsv1alpha1.Test, tmpl *testsv1alpha1.TestTemplate) (*testsv1alpha1.Test, []string, []string) {
	b := &builder{form: f, t: base.DeepCopy()}
	t := b.t
	t.APIVersion, t.Kind = testsv1alpha1.GroupVersion.String(), KindTest
	if t.Name == "" {
		t.Name, t.Namespace = f.Name, f.Namespace
		for _, msg := range validation.IsDNS1123Subdomain(f.Name) {
			b.fail("Name: " + msg)
		}
		for _, msg := range validation.IsDNS1123Label(f.Namespace) {
			b.fail("Namespace: " + msg)
		}
	}
	previous := FirstOf(t.Spec.Use)
	b.template(tmpl)
	b.container()
	b.content(base.Spec.Content.Git)
	if tmpl != nil {
		b.params(tmpl, previous != f.Template)
	}
	if len(t.Spec.Config) == 0 {
		t.Spec.Config = nil
	}
	b.pod()
	b.timing()
	return t, b.errs, b.warns
}

// builder collects the problems of one build.
type builder struct {
	form  TestForm
	t     *testsv1alpha1.Test
	errs  []string
	warns []string
}

func (b *builder) fail(msg string) { b.errs = append(b.errs, msg) }

func (b *builder) check(err error) {
	if err != nil {
		b.fail(err.Error())
	}
}

// template sets spec.use and the tool label.
func (b *builder) template(tmpl *testsv1alpha1.TestTemplate) {
	f, s := b.form, &b.t.Spec
	if f.Template != "" && tmpl == nil {
		b.fail(fmt.Sprintf("Template %q does not exist in namespace %s.", f.Template, f.Namespace))
	}
	s.Use = nil
	if f.Template != "" {
		s.Use = []string{f.Template}
	}
	tool := f.Tool
	if tmpl != nil && tmpl.Labels[LabelTool] != "" {
		tool = tmpl.Labels[LabelTool]
	}
	setLabel(&b.t.ObjectMeta, LabelTool, tool)
}

// container sets image, command, arguments, environment and resources.
// Environment entries from Secrets or ConfigMaps (not in the form) stay.
func (b *builder) container() {
	f, c := b.form, &b.t.Spec.Container
	c.Image = f.Image
	c.Command, c.Args = lines(f.Command), lines(f.Args)
	if f.Template == "" {
		if f.Image == "" {
			b.fail("Image: required without a template.")
		}
		if len(c.Command) == 0 && len(c.Args) == 0 {
			b.fail("Command or arguments: at least one is required without a template.")
		}
	}
	env, err := kvLines("Environment", f.Env)
	b.check(err)
	kept := slices.DeleteFunc(slices.Clone(c.Env), func(e corev1.EnvVar) bool { return e.ValueFrom == nil })
	c.Env = nil
	for _, name := range slices.Sorted(maps.Keys(env)) {
		c.Env = append(c.Env, corev1.EnvVar{Name: name, Value: env[name]})
	}
	c.Env = append(c.Env, kept...)
	b.check(setQuantity(&c.Resources.Requests, corev1.ResourceCPU, "CPU request", f.CPURequest))
	b.check(setQuantity(&c.Resources.Requests, corev1.ResourceMemory, "Memory request", f.MemoryRequest))
	b.check(setQuantity(&c.Resources.Limits, corev1.ResourceCPU, "CPU limit", f.CPULimit))
	b.check(setQuantity(&c.Resources.Limits, corev1.ResourceMemory, "Memory limit", f.MemoryLimit))
}

// content sets the code — git or inline files, one of them (step 20i) —
// and the test data. Git settings the form doesn't show (auth type, SSH
// key, mount path) stay.
func (b *builder) content(was *testsv1alpha1.GitContent) {
	f, c := b.form, &b.t.Spec.Content
	c.Git, c.Files, c.Tarball = nil, nil, nil
	if f.UseGit {
		g := testsv1alpha1.GitContent{URI: f.GitURI, Revision: f.GitRevision, Paths: lines(f.GitPaths)}
		if len(g.Paths) == 0 {
			g.Paths = sparseFor(f.GitPath)
		}
		if f.GitPath == "" {
			b.fail(`Path in the repository: required — where the tests are, a file or a directory (e.g. perf/checkout.js, e2e/web; "." for the whole repository).`)
		}
		if was != nil {
			g.MountPath, g.AuthType, g.UsernameFrom, g.SSHKeyFrom = was.MountPath, was.AuthType, was.UsernameFrom, was.SSHKeyFrom
		}
		if f.GitURI == "" {
			b.fail("Git repository: required when the content comes from git.")
		}
		if f.GitSecret != "" {
			key := f.GitSecretKey
			if key == "" {
				key = "token"
			}
			g.TokenFrom = &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: f.GitSecret}, Key: key}}
		}
		c.Git = &g
	}
	if f.UseFiles {
		for _, file := range f.Files {
			p := strings.Trim(strings.TrimSpace(file.Path), "/")
			if p == "" {
				b.fail("Files: every file needs a path.")
				continue
			}
			c.Files = append(c.Files, testsv1alpha1.FileContent{Path: p, Content: file.Content})
		}
		if len(c.Files) == 0 {
			b.fail("Inline files: add at least one file.")
		}
	}
	b.testData()
}

// testData sets content.testData from the rows.
func (b *builder) testData() {
	c := &b.t.Spec.Content
	c.TestData = nil
	for _, row := range b.form.TestData {
		d := testsv1alpha1.TestDataSource{ConfigMap: row.Name}
		if row.Kind == KindSecret {
			d = testsv1alpha1.TestDataSource{Secret: row.Name}
		}
		switch row.Where {
		case WhereDir:
			if !strings.HasPrefix(row.MountPath, "/") {
				b.fail(fmt.Sprintf("Test data %s: the directory must be an absolute path, e.g. /data/repo/e2e/data.", row.Name))
			}
			d.MountPath = row.MountPath
		case WhereFiles:
			for _, l := range lines(row.Items) {
				k, p, ok := strings.Cut(l, "=")
				if !ok || strings.TrimSpace(k) == "" || !strings.HasPrefix(strings.TrimSpace(p), "/") {
					b.fail(fmt.Sprintf("Test data %s: %q is not key=/absolute/path.", row.Name, l))
					continue
				}
				d.Items = append(d.Items, testsv1alpha1.TestDataItem{Key: strings.TrimSpace(k), Path: strings.TrimSpace(p)})
			}
			if len(d.Items) == 0 {
				b.fail(fmt.Sprintf("Test data %s: list the files, one key=/path per line.", row.Name))
			}
		}
		c.TestData = append(c.TestData, d)
	}
}

// params: the Test keeps a template parameter only where its value
// differs from the template's default. On a change of template, the old
// template's parameters go.
func (b *builder) params(tmpl *testsv1alpha1.TestTemplate, changed bool) {
	s := &b.t.Spec
	if changed {
		for name := range s.Config {
			if _, ok := tmpl.Spec.Config[name]; !ok {
				delete(s.Config, name)
			}
		}
	}
	main, kind := resolver.MainPathParam(tmpl.Spec.Config)
	if kind == resolver.PathDirectory && b.form.UseFiles {
		b.fail(fmt.Sprintf("%s runs a project: its code comes from git (choose Git as the source) — inline files are for single-file tools.", tmpl.Name))
	}
	for _, name := range slices.Sorted(maps.Keys(tmpl.Spec.Config)) {
		p, v := tmpl.Spec.Config[name], b.form.Params[name]
		if name == main {
			v = b.mainPathValue(p, v)
			if v == "" && !b.form.UseFiles {
				b.fail(fmt.Sprintf("%s: required — %s", name, cmp.Or(p.Description, "where the Test's files are.")))
				continue
			}
			b.checkInCheckout(name, v)
		}
		if v == "" || v == p.Default {
			delete(s.Config, name)
			continue
		}
		v, err := expr.CoerceParam(name, v, expr.Parameter{Type: p.Type, Enum: p.Enum, Pattern: p.Pattern})
		if err != nil {
			b.check(err)
			continue
		}
		if s.Config == nil {
			s.Config = map[string]testsv1alpha1.Parameter{}
		}
		p.Default = v
		s.Config[name] = p
	}
}

// mainPathValue is the template's main path parameter (resolver.
// MainPathParam: k6's script, JMeter's plan, playwright's projectDir, …):
// what the user typed, else with git the path in the repository. With
// inline files it stays unset unless typed — the operator takes the first
// file (step 20i), so the Test's YAML names no path.
func (b *builder) mainPathValue(p testsv1alpha1.Parameter, v string) string {
	if v != "" && v != p.Default {
		return v
	}
	if b.form.UseGit {
		return cmp.Or(v, b.form.GitPath)
	}
	return v
}

// checkInCheckout warns when the main path lies outside the sparse
// checkout — the tool would look for a file the clone leaves out.
func (b *builder) checkInCheckout(name, v string) {
	g := b.t.Spec.Content.Git
	if g == nil || len(g.Paths) == 0 {
		return
	}
	v = strings.Trim(path.Clean(v), "/")
	for _, sp := range g.Paths {
		sp = strings.Trim(path.Clean(sp), "/")
		if sp == "." || v == sp || strings.HasPrefix(v, sp+"/") {
			return
		}
	}
	b.warns = append(b.warns, fmt.Sprintf("%s is %s, which the sparse checkout (%s) leaves out — the tool won't find it.",
		name, v, strings.Join(g.Paths, ", ")))
}

// sparseFor is the sparse checkout a path in the repository needs: the
// directory itself, or a file's directory (a name with an extension is
// taken for a file); none — the whole repository — for "." or a file at
// the root.
func sparseFor(p string) []string {
	p = strings.Trim(path.Clean(strings.TrimSpace(p)), "/")
	if p == "" || p == "." {
		return nil
	}
	if path.Ext(path.Base(p)) != "" {
		p = path.Dir(p)
		if p == "." {
			return nil
		}
	}
	return []string{p}
}

// pod sets the service account, annotations and labels; the rest of the
// pod settings stay.
func (b *builder) pod() {
	s, f := &b.t.Spec, b.form
	pod := testsv1alpha1.PodConfig{}
	if s.Pod != nil {
		pod = *s.Pod
	}
	var err error
	pod.ServiceAccountName = f.ServiceAccount
	pod.Annotations, err = kvLines("Pod annotations", f.PodAnnotations)
	b.check(err)
	pod.Labels, err = kvLines("Pod labels", f.PodLabels)
	b.check(err)
	s.Pod = nil
	if !reflect.DeepEqual(pod, testsv1alpha1.PodConfig{}) {
		s.Pod = &pod
	}
}

// timing sets the timeout and the schedule.
func (b *builder) timing() {
	s, f := &b.t.Spec, b.form
	s.Timeout = nil
	if f.Timeout != "" {
		d, err := time.ParseDuration(f.Timeout)
		if err != nil || d <= 0 {
			b.fail(fmt.Sprintf("Timeout: %q is not a duration like 10m or 1h30m.", f.Timeout))
		} else {
			s.Timeout = &metav1.Duration{Duration: d}
		}
	}
	s.Schedule = f.Schedule
	if f.Schedule != "" {
		if _, err := NextFires(f.Schedule, time.Now(), 1); err != nil {
			b.fail("Schedule: " + err.Error())
		}
	}
}

func setLabel(m *metav1.ObjectMeta, key, value string) {
	if value == "" {
		delete(m.Labels, key)
		return
	}
	if m.Labels == nil {
		m.Labels = map[string]string{}
	}
	m.Labels[key] = value
}

// setQuantity sets (or, for "", removes) one resource; the others stay.
func setQuantity(l *corev1.ResourceList, name corev1.ResourceName, field, value string) error {
	if value == "" {
		delete(*l, name)
		if len(*l) == 0 {
			*l = nil
		}
		return nil
	}
	q, err := resource.ParseQuantity(value)
	if err != nil {
		return fmt.Errorf("%s: %q is not a quantity like 500m, 2 or 1Gi", field, value)
	}
	if *l == nil {
		*l = corev1.ResourceList{}
	}
	(*l)[name] = q
	return nil
}

// manifest is a Test as it goes to Git: no status, no server-set
// metadata, no managed-by label (the GUI's ownership mark).
type manifest struct {
	APIVersion string                 `json:"apiVersion"`
	Kind       string                 `json:"kind"`
	Metadata   manifestMeta           `json:"metadata"`
	Spec       testsv1alpha1.TestSpec `json:"spec"`
}

type manifestMeta struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// serverAnnotations are set by the API server, not by the Test's author.
var serverAnnotations = []string{TagCreatedBy, "kubectl.kubernetes.io/last-applied-configuration"}

func manifestOf(t *testsv1alpha1.Test) manifest {
	m := manifest{APIVersion: testsv1alpha1.GroupVersion.String(), Kind: KindTest, Spec: t.Spec,
		Metadata: manifestMeta{Name: t.Name, Namespace: t.Namespace, Labels: maps.Clone(t.Labels), Annotations: maps.Clone(t.Annotations)}}
	delete(m.Metadata.Labels, LabelManagedBy)
	for _, k := range serverAnnotations {
		delete(m.Metadata.Annotations, k)
	}
	if len(m.Metadata.Labels) == 0 {
		m.Metadata.Labels = nil
	}
	if len(m.Metadata.Annotations) == 0 {
		m.Metadata.Annotations = nil
	}
	return m
}

// ManifestYAML renders a Test for Git, for people: keys in reading order,
// scripts as | blocks, no empty objects (yamlview).
func ManifestYAML(t *testsv1alpha1.Test) string {
	out, err := yamlview.Format(manifestOf(t), yamlview.Manifest)
	if err != nil {
		return err.Error()
	}
	return out
}

// ParseManifest reads the YAML mode's text onto base: its labels,
// annotations and spec replace base's; name and namespace must match an
// edited Test's. Unknown fields are errors (a typo would otherwise be
// dropped silently).
func ParseManifest(text string, base *testsv1alpha1.Test) (*testsv1alpha1.Test, []string) {
	var m manifest
	if err := yaml.UnmarshalStrict([]byte(text), &m); err != nil {
		return nil, []string{"YAML: " + err.Error()}
	}
	var errs []string
	if m.Kind != "" && m.Kind != KindTest {
		errs = append(errs, fmt.Sprintf("YAML: kind is %q; this editor writes a Test.", m.Kind))
	}
	if m.APIVersion != "" && m.APIVersion != testsv1alpha1.GroupVersion.String() {
		errs = append(errs, fmt.Sprintf("YAML: apiVersion is %q, not %s.", m.APIVersion, testsv1alpha1.GroupVersion))
	}
	t := base.DeepCopy()
	t.APIVersion, t.Kind = testsv1alpha1.GroupVersion.String(), KindTest
	if t.Name == "" {
		t.Name, t.Namespace = m.Metadata.Name, m.Metadata.Namespace
		for _, msg := range validation.IsDNS1123Subdomain(t.Name) {
			errs = append(errs, "metadata.name: "+msg)
		}
		for _, msg := range validation.IsDNS1123Label(t.Namespace) {
			errs = append(errs, "metadata.namespace: "+msg)
		}
	} else if m.Metadata.Name != t.Name || (m.Metadata.Namespace != "" && m.Metadata.Namespace != t.Namespace) {
		errs = append(errs, fmt.Sprintf("YAML: this editor saves %s/%s; to make a copy under another name, use Duplicate.", t.Namespace, t.Name))
	}
	if v, ok := m.Metadata.Labels[LabelManagedBy]; ok && v != ManagedByUI {
		errs = append(errs, "YAML: leave out the "+LabelManagedBy+" label; the API server sets it.")
	}
	managed := t.Labels[LabelManagedBy]
	t.Labels, t.Spec = m.Metadata.Labels, m.Spec
	if managed != "" {
		setLabel(&t.ObjectMeta, LabelManagedBy, managed)
	}
	kept := map[string]string{}
	for _, k := range serverAnnotations {
		if v, ok := t.Annotations[k]; ok {
			kept[k] = v
		}
	}
	t.Annotations = m.Metadata.Annotations
	for k, v := range kept {
		if t.Annotations == nil {
			t.Annotations = map[string]string{}
		}
		t.Annotations[k] = v
	}
	return t, errs
}

// EditPatch is the JSON merge patch from old to updated: labels,
// annotations and spec, conditional on old's resourceVersion (the API
// answers 409 when the Test changed meanwhile).
func EditPatch(old, updated *testsv1alpha1.Test) (json.RawMessage, error) {
	doc := func(t *testsv1alpha1.Test) ([]byte, error) {
		labels := maps.Clone(t.Labels)
		delete(labels, LabelManagedBy) // the API refuses patches touching it
		return json.Marshal(map[string]any{
			"metadata": map[string]any{"labels": labels, "annotations": t.Annotations},
			"spec":     t.Spec,
		})
	}
	a, err := doc(old)
	if err != nil {
		return nil, err
	}
	b, err := doc(updated)
	if err != nil {
		return nil, err
	}
	patch, err := jsonpatch.CreateMergePatch(a, b)
	if err != nil {
		return nil, err
	}
	var p map[string]any
	if err := json.Unmarshal(patch, &p); err != nil {
		return nil, err
	}
	meta, _ := p["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["resourceVersion"] = old.ResourceVersion
	p["metadata"] = meta
	return json.Marshal(p)
}

// FirstOf is s[0], or "" for an empty slice.
func FirstOf(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
