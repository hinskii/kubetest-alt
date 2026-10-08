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
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
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
	"github.com/hinskii/kubetest-alt/internal/resolver"
	"github.com/hinskii/kubetest-alt/pkg/expr"
)

// The test editor (wizard): a form over the common shape of a Test — one
// TestTemplate or an own image, git and inline-file content, the
// template's parameters, container, pod metadata, timeout, schedule — and
// a YAML mode for everything else. Control Center keeps no state: each
// step posts the whole form back, and the Test the form describes is
// rebuilt from it every time.

// kindTest is the Test CRD's kind.
const kindTest = "Test"

// Editor modes.
const (
	modeForm = "form"
	modeYAML = "yaml"
)

// testForm is everything the wizard edits, as submitted.
type testForm struct {
	Namespace, Name string
	// Template is the TestTemplate the Test uses; "" = an own image.
	Template string
	// Tool is the kubetest.io/tool label (taken from the template).
	Tool          string
	Image         string
	Command, Args string // one item per line
	UseGit        bool
	GitURI        string
	GitRevision   string
	// GitPath is where the tests are in the repository (a file or a
	// directory): the template's main path parameter and, unless GitPaths
	// says otherwise, the sparse checkout. Required with git (step 20h).
	GitPath      string
	GitPaths     string // sparse checkout, one per line (advanced)
	GitSecret    string // Secret with the token, optional
	GitSecretKey string
	UseFiles     bool
	Files        []fileField
	// Params are the template's parameters (name → value).
	Params map[string]string
	Env    string // NAME=value per line
	CPURequest, MemoryRequest,
	CPULimit, MemoryLimit string
	ServiceAccount            string
	PodAnnotations, PodLabels string // key=value per line
	Timeout, Schedule         string
	Mode                      string // form | yaml
	YAML                      string // authoritative in yaml mode
	// ResourceVersion: the Test's version the edit started from; saving
	// is refused if it changed since.
	ResourceVersion string
}

type fileField struct{ Path, Content string }

// Form field names.
const (
	fNamespace = "namespace"
	fName      = "name"
	fTemplate  = "template"
	fFilePath  = "file.path"
	fFileBody  = "file.content"
	// fParam prefixes a template parameter: param.<template>.<name>.
	fParam = "param."
)

// formFromValues reads a submitted form.
func formFromValues(v url.Values) testForm {
	f := testForm{
		Namespace: strings.TrimSpace(v.Get(fNamespace)), Name: strings.TrimSpace(v.Get(fName)),
		Template: v.Get(fTemplate), Tool: strings.TrimSpace(v.Get("tool")),
		Image: strings.TrimSpace(v.Get("image")), Command: v.Get("command"), Args: v.Get("args"),
		UseGit: v.Get("useGit") != "", GitURI: strings.TrimSpace(v.Get("gitURI")),
		GitRevision: strings.TrimSpace(v.Get("gitRevision")), GitPaths: v.Get("gitPaths"),
		GitPath:   strings.Trim(strings.TrimSpace(v.Get("gitPath")), "/"),
		GitSecret: strings.TrimSpace(v.Get("gitSecret")), GitSecretKey: strings.TrimSpace(v.Get("gitSecretKey")),
		UseFiles: v.Get("useFiles") != "", Env: v.Get("env"),
		CPURequest: strings.TrimSpace(v.Get("cpuRequest")), MemoryRequest: strings.TrimSpace(v.Get("memoryRequest")),
		CPULimit: strings.TrimSpace(v.Get("cpuLimit")), MemoryLimit: strings.TrimSpace(v.Get("memoryLimit")),
		ServiceAccount: strings.TrimSpace(v.Get("serviceAccount")),
		PodAnnotations: v.Get("podAnnotations"), PodLabels: v.Get("podLabels"),
		Timeout: strings.TrimSpace(v.Get("timeout")), Schedule: strings.Join(strings.Fields(v.Get("schedule")), " "),
		Mode: v.Get("mode"), YAML: v.Get("yaml"), ResourceVersion: v.Get("resourceVersion"),
	}
	if f.Mode != modeYAML {
		f.Mode = modeForm
	}
	paths, bodies := v[fFilePath], v[fFileBody]
	for i, p := range paths {
		body := ""
		if i < len(bodies) {
			body = strings.ReplaceAll(bodies[i], "\r\n", "\n")
		}
		if p = strings.TrimSpace(p); p != "" || strings.TrimSpace(body) != "" {
			f.Files = append(f.Files, fileField{Path: p, Content: body})
		}
	}
	prefix := fParam + f.Template + "."
	f.Params = map[string]string{}
	for k, vals := range v {
		if name, ok := strings.CutPrefix(k, prefix); ok && f.Template != "" && len(vals) > 0 {
			f.Params[name] = strings.TrimSpace(vals[0])
		}
	}
	return f
}

// formUnsupported says why the form can't represent t (the editor then
// opens in YAML mode), or "".
func formUnsupported(t *testsv1alpha1.Test) string {
	switch {
	case len(t.Spec.Steps) > 0:
		return "A composite Test (spec.steps) is edited as YAML."
	case len(t.Spec.Use) > 1:
		return "This Test uses more than one template; it is edited as YAML."
	}
	return ""
}

// formFromTest fills the form from a Test and its template (nil: none or
// not found).
func formFromTest(t *testsv1alpha1.Test, tmpl *testsv1alpha1.TestTemplate) testForm {
	s := &t.Spec
	f := testForm{
		Namespace: t.Namespace, Name: t.Name, Tool: t.Labels[labelTool], Mode: modeForm,
		Image: s.Container.Image, Command: strings.Join(s.Container.Command, "\n"),
		Args: strings.Join(s.Container.Args, "\n"), Schedule: s.Schedule,
		ResourceVersion: t.ResourceVersion, Params: map[string]string{},
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
			f.Files = append(f.Files, fileField{Path: formPath(file.Path), Content: file.Content})
		}
	}
	f.UseFiles = len(f.Files) > 0
	if g := s.Content.Git; g != nil {
		main := ""
		if tmpl != nil {
			main = resolver.MainPathParam(tmpl.Spec.Container, tmpl.Spec.Config)
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
		main := resolver.MainPathParam(tmpl.Spec.Container, tmpl.Spec.Config)
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
func (f testForm) build(base *testsv1alpha1.Test, tmpl *testsv1alpha1.TestTemplate) (*testsv1alpha1.Test, []string, []string) {
	b := &builder{form: f, t: base.DeepCopy()}
	t := b.t
	t.APIVersion, t.Kind = testsv1alpha1.GroupVersion.String(), kindTest
	if t.Name == "" {
		t.Name, t.Namespace = f.Name, f.Namespace
		for _, msg := range validation.IsDNS1123Subdomain(f.Name) {
			b.fail("Name: " + msg)
		}
		for _, msg := range validation.IsDNS1123Label(f.Namespace) {
			b.fail("Namespace: " + msg)
		}
	}
	previous := firstOf(t.Spec.Use)
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
	form  testForm
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
	if tmpl != nil && tmpl.Labels[labelTool] != "" {
		tool = tmpl.Labels[labelTool]
	}
	setLabel(&b.t.ObjectMeta, labelTool, tool)
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

// content sets the git source and the inline files; git settings the
// form doesn't show (auth type, SSH key, mount path) and files from
// Secrets or ConfigMaps stay.
func (b *builder) content(was *testsv1alpha1.GitContent) {
	f, c := b.form, &b.t.Spec.Content
	c.Git = nil
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
	files := slices.DeleteFunc(slices.Clone(c.Files), func(x testsv1alpha1.FileContent) bool { return x.ContentFrom == nil })
	if f.UseFiles {
		for _, file := range f.Files {
			if file.Path == "" {
				b.fail("Files: every file needs a path.")
				continue
			}
			files = append(files, testsv1alpha1.FileContent{Path: dataPath(file.Path), Content: file.Content})
		}
	}
	c.Files = files
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
	main := resolver.MainPathParam(tmpl.Spec.Container, tmpl.Spec.Config)
	for _, name := range slices.Sorted(maps.Keys(tmpl.Spec.Config)) {
		p, v := tmpl.Spec.Config[name], b.form.Params[name]
		if name == main {
			v = b.mainPathValue(p, v)
			if v == "" {
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

// repoDir is where a template's tool looks for the Test's files
// (/data/repo/, CLAUDE.md §12); the editor's file paths are relative to it.
const repoDir = "repo/"

// dataPath turns an editor file path into the Test's (relative to /data):
// "load.js" → "repo/load.js"; a leading "/" means under /data itself
// ("/x.json" → "x.json"), and "/data/…" is understood too.
func dataPath(p string) string {
	p = strings.TrimSpace(p)
	if rest, ok := strings.CutPrefix(p, "/data/"); ok {
		return rest
	}
	if strings.HasPrefix(p, "/") {
		return strings.TrimLeft(p, "/")
	}
	return repoDir + p
}

// formPath is dataPath's inverse, for the form.
func formPath(p string) string {
	if rest, ok := strings.CutPrefix(p, repoDir); ok {
		return rest
	}
	return "/" + p
}

// mainPathValue is the template's main path parameter (resolver.
// MainPathParam: k6's script, JMeter's plan, playwright's projectDir, …):
// what the user typed, else the path in the repository (git), else the
// first inline file when the value names none of the Test's files.
func (b *builder) mainPathValue(p testsv1alpha1.Parameter, v string) string {
	if v != "" && v != p.Default {
		return v
	}
	if b.form.UseGit {
		return cmp.Or(v, b.form.GitPath)
	}
	var inline []string
	for _, file := range b.t.Spec.Content.Files {
		if rest, ok := strings.CutPrefix(file.Path, repoDir); ok {
			inline = append(inline, rest)
		}
	}
	if len(inline) == 0 || (v != "" && slices.Contains(inline, v)) {
		return v
	}
	return inline[0]
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
		if _, err := nextFires(f.Schedule, time.Now(), 1); err != nil {
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
var serverAnnotations = []string{tagCreatedBy, "kubectl.kubernetes.io/last-applied-configuration"}

func manifestOf(t *testsv1alpha1.Test) manifest {
	m := manifest{APIVersion: testsv1alpha1.GroupVersion.String(), Kind: kindTest, Spec: t.Spec,
		Metadata: manifestMeta{Name: t.Name, Namespace: t.Namespace, Labels: maps.Clone(t.Labels), Annotations: maps.Clone(t.Annotations)}}
	delete(m.Metadata.Labels, labelManagedBy)
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

// manifestYAML renders a Test for Git, without the empty objects
// ("resources: {}", "content: {}") the Go types leave behind.
func manifestYAML(t *testsv1alpha1.Test) string {
	b, err := json.Marshal(manifestOf(t))
	if err != nil {
		return err.Error()
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return err.Error()
	}
	pruneEmpty(doc)
	out, err := yaml.Marshal(doc)
	if err != nil {
		return err.Error()
	}
	return string(out)
}

// pruneEmpty drops empty objects, recursively; "spec: {}" stays. List
// items are left alone: there an empty object can mean something
// (a volume's "emptyDir: {}").
func pruneEmpty(m map[string]any) {
	for k, v := range m {
		if v, ok := v.(map[string]any); ok {
			pruneEmpty(v)
			if len(v) == 0 && k != "spec" {
				delete(m, k)
			}
		}
	}
}

// parseManifest reads the YAML mode's text onto base: its labels,
// annotations and spec replace base's; name and namespace must match an
// edited Test's. Unknown fields are errors (a typo would otherwise be
// dropped silently).
func parseManifest(text string, base *testsv1alpha1.Test) (*testsv1alpha1.Test, []string) {
	var m manifest
	if err := yaml.UnmarshalStrict([]byte(text), &m); err != nil {
		return nil, []string{"YAML: " + err.Error()}
	}
	var errs []string
	if m.Kind != "" && m.Kind != kindTest {
		errs = append(errs, fmt.Sprintf("YAML: kind is %q; this editor writes a Test.", m.Kind))
	}
	if m.APIVersion != "" && m.APIVersion != testsv1alpha1.GroupVersion.String() {
		errs = append(errs, fmt.Sprintf("YAML: apiVersion is %q, not %s.", m.APIVersion, testsv1alpha1.GroupVersion))
	}
	t := base.DeepCopy()
	t.APIVersion, t.Kind = testsv1alpha1.GroupVersion.String(), kindTest
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
	if v, ok := m.Metadata.Labels[labelManagedBy]; ok && v != managedByUI {
		errs = append(errs, "YAML: leave out the "+labelManagedBy+" label; the API server sets it.")
	}
	managed := t.Labels[labelManagedBy]
	t.Labels, t.Spec = m.Metadata.Labels, m.Spec
	if managed != "" {
		setLabel(&t.ObjectMeta, labelManagedBy, managed)
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

// editPatch is the JSON merge patch from old to updated: labels,
// annotations and spec, conditional on old's resourceVersion (the API
// answers 409 when the Test changed meanwhile).
func editPatch(old, updated *testsv1alpha1.Test) (json.RawMessage, error) {
	doc := func(t *testsv1alpha1.Test) ([]byte, error) {
		labels := maps.Clone(t.Labels)
		delete(labels, labelManagedBy) // the API refuses patches touching it
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
