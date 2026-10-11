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
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/cctest"
)

const (
	testsAPI     = "/api/v1/clusters/dev/tests"
	previewAPI   = testsAPI + "/preview"
	smokeEdit    = smokePath
	smokePreview = smokePath + "/preview"
)

func k6Template() *testsv1alpha1.TestTemplate {
	return &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "k6", Namespace: "team-a", Labels: map[string]string{LabelTool: "k6"}},
		Spec: testsv1alpha1.TestTemplateSpec{
			Container: testsv1alpha1.ContainerConfig{Image: "grafana/k6:1.4.0", Command: []string{"k6"},
				Args: []string{"run", "/data/repo/{{ config.script }}"}},
			Config: map[string]testsv1alpha1.Parameter{
				"script": {Type: "string", Path: "file", Description: "The k6 script."},
				"vus":    {Type: "integer", Default: "10"},
			},
		},
	}
}

func playwrightTemplate() *testsv1alpha1.TestTemplate {
	return &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "playwright", Namespace: "team-a", Labels: map[string]string{LabelTool: "playwright"}},
		Spec: testsv1alpha1.TestTemplateSpec{
			Container: testsv1alpha1.ContainerConfig{Image: "mcr.microsoft.com/playwright", Command: []string{"sh", "-c"},
				Args: []string{"cd /data/repo/{{ config.projectDir }} && npx playwright test"}},
			Config: map[string]testsv1alpha1.Parameter{"projectDir": {Type: "string", Path: "directory"}},
		},
	}
}

// gitForm is a k6 Test from git with most of the form filled.
func gitForm() TestForm {
	return TestForm{Namespace: "team-a", Name: "checkout", Template: "k6", Params: map[string]string{"vus": "10"},
		Source: SourceGit, GitURI: "https://github.com/org/perf", GitRevision: "main", GitPath: "perf/checkout.js",
		GitSecret: "git-token", Env: "TARGET=https://shop\n", MemoryLimit: "1Gi",
		PodAnnotations: "sidecar.istio.io/inject=false", Timeout: "15m"}
}

func (w *world) test(t *testing.T, name string) *testsv1alpha1.Test {
	t.Helper()
	var out testsv1alpha1.Test
	require.NoError(t, w.k8s.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: name}, &out))
	return &out
}

func TestEditor_NamespacesAndCatalog(t *testing.T) {
	other := k6Template()
	other.Namespace, other.Name = "team-b", "jmeter"
	w := newWorld(t, smokeTest(), k6Template(), other)

	ns := decodeAs[NamespaceList](t, w.do(t, http.MethodGet, "/api/v1/clusters/dev/namespaces", viewer, nil))
	assert.Equal(t, []string{"team-a", "team-b"}, ns.Namespaces, "namespaces with Tests or a catalog")

	cat := decodeAs[TemplateList](t, w.do(t, http.MethodGet, "/api/v1/clusters/dev/templates?namespace=team-a", viewer, nil))
	require.Len(t, cat.Templates, 1)
	k6 := cat.Templates[0]
	assert.Equal(t, "k6", k6.Tool)
	assert.Equal(t, "grafana/k6:1.4.0", k6.Image)
	assert.Equal(t, "script", k6.MainParam)
	assert.Equal(t, "file", k6.MainKind)
	assert.Equal(t, Param{Name: "script", Type: "string", Required: true, Description: "The k6 script."}, k6.Params[0])

	missing := w.do(t, http.MethodGet, "/api/v1/clusters/dev/templates", viewer, nil)
	assert.Equal(t, http.StatusBadRequest, missing.Code)
}

func TestEditor_CreateFromTemplate(t *testing.T) {
	w := newWorld(t, smokeTest(), k6Template())

	// The live YAML is what Create writes; nothing is created.
	rec := w.do(t, http.MethodPost, previewAPI, viewer, gitForm())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	p := decodeAs[Preview](t, rec)
	assert.Empty(t, p.Errors)
	assert.Contains(t, p.YAML, "use:\n    - k6")
	assert.Contains(t, p.YAML, "script:\n      type: string\n      default: perf/checkout.js", "the main path from the repository")
	assert.Contains(t, p.YAML, "paths:\n        - perf\n", "and the sparse checkout: the file's directory")
	assert.NotContains(t, p.YAML, "vus:", "a parameter equal to the template's default stays out")
	assert.Contains(t, p.YAML, "kubetest.io/tool: k6")
	assert.NotContains(t, p.YAML, "managed-by", "the YAML is for Git")
	assert.True(t, strings.HasPrefix(p.YAML, "apiVersion: tests.kubetest.io/v1alpha1\nkind: Test\n"))

	// Check (dry run): admitted, still nothing written.
	rec = w.do(t, http.MethodPost, testsAPI+"?dry-run=true", developer, gitForm())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, decodeAs[SavedTest](t, rec).DryRun)
	err := w.k8s.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "checkout"}, &testsv1alpha1.Test{})
	assert.True(t, apierrors.IsNotFound(err), "a check creates nothing")

	rec = w.do(t, http.MethodPost, testsAPI, developer, gitForm())
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	saved := decodeAs[SavedTest](t, rec)
	assert.Equal(t, SavedTest{Namespace: "team-a", Name: "checkout", YAML: p.YAML}, saved, "Create writes the preview")
	got := w.test(t, "checkout")
	assert.Equal(t, ManagedByUI, got.Labels[LabelManagedBy])
	assert.Equal(t, cctest.Developer, got.Annotations[TagCreatedBy])
	assert.Equal(t, []string{"k6"}, got.Spec.Use)
	assert.Equal(t, "perf/checkout.js", got.Spec.Config["script"].Default)
	require.NotNil(t, got.Spec.Content.Git)
	assert.Equal(t, []string{"perf"}, got.Spec.Content.Git.Paths)
	assert.Equal(t, "git-token", got.Spec.Content.Git.TokenFrom.SecretKeyRef.Name)
	assert.Equal(t, "token", got.Spec.Content.Git.TokenFrom.SecretKeyRef.Key)
	assert.Empty(t, got.Spec.Content.Files)
	assert.Equal(t, []corev1.EnvVar{{Name: "TARGET", Value: "https://shop"}}, got.Spec.Container.Env)
	assert.Equal(t, resource.MustParse("1Gi"), got.Spec.Container.Resources.Limits[corev1.ResourceMemory])
	assert.Equal(t, map[string]string{"sidecar.istio.io/inject": "false"}, got.Spec.Pod.Annotations)
	assert.Equal(t, 15*time.Minute, got.Spec.Timeout.Duration)

	assert.Equal(t, http.StatusForbidden, w.do(t, http.MethodPost, testsAPI, viewer, gitForm()).Code)
}

func TestEditor_FormErrors(t *testing.T) {
	w := newWorld(t, k6Template())
	f := gitForm()
	f.Template, f.Name, f.Timeout, f.MemoryLimit, f.PodAnnotations = "", "Bad_Name", "soon", "lots", "no-equals-sign"
	rec := w.do(t, http.MethodPost, testsAPI, developer, f)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	fe := decodeAs[FormError](t, rec)
	all := strings.Join(fe.Errors, "\n")
	for _, want := range []string{"Name: ", "Image: required", "Command or arguments", `Timeout: "soon"`,
		"Memory limit", "Pod annotations"} {
		assert.Contains(t, all, want)
	}

	// The live preview still answers, with what is missing.
	p := decodeAs[Preview](t, w.do(t, http.MethodPost, previewAPI, viewer, f))
	assert.NotEmpty(t, p.Errors)

	f = gitForm()
	f.Params["vus"] = "many"
	assert.Contains(t, errorOf(t, w.do(t, http.MethodPost, testsAPI, developer, f)), "is not an integer")
}

func TestEditor_InlineAndProjectSources(t *testing.T) {
	w := newWorld(t, k6Template(), playwrightTemplate())
	inline := TestForm{Namespace: "team-a", Name: "inline", Template: "k6", Source: SourceInline,
		Files: []FileField{{Path: "load.js", Content: "export default function () {}\r\n"},
			{Path: "/data/users.csv", Content: "id\n1\n"}, {Path: " ", Content: ""}}}
	rec := w.do(t, http.MethodPost, testsAPI, developer, inline)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	got := w.test(t, "inline")
	assert.NotContains(t, got.Spec.Config, "script", "inline: no path — the first file is the script")
	require.Len(t, got.Spec.Content.Files, 2, "the empty row is no file")
	assert.Equal(t, "data/users.csv", got.Spec.Content.Files[1].Path, "paths relative to /data/repo")
	assert.Equal(t, "export default function () {}\n", got.Spec.Content.Files[0].Content, "line endings made regular")
	assert.Nil(t, got.Spec.Content.Git)

	empty := TestForm{Namespace: "team-a", Name: "empty", Template: "k6", Source: SourceInline}
	assert.Contains(t, decodeAs[Preview](t, w.do(t, http.MethodPost, previewAPI, viewer, empty)).Errors,
		"Inline files: add at least one file.")

	project := TestForm{Namespace: "team-a", Name: "web", Template: "playwright", Source: SourceInline,
		Files: []FileField{{Path: "package.json", Content: "{}"}}}
	p := decodeAs[Preview](t, w.do(t, http.MethodPost, previewAPI, viewer, project))
	assert.Contains(t, strings.Join(p.Errors, "\n"), "playwright runs a project: its code comes from git")
}

func TestEditor_TestDataAndPaths(t *testing.T) {
	w := newWorld(t, k6Template())
	f := gitForm()
	f.TestData = []TestDataField{{Kind: KindConfigMap, Name: "products-stage"},
		{Kind: KindSecret, Name: "account", Where: WhereFiles, Items: "env=/data/repo/perf/.env"},
		{Kind: KindConfigMap, Name: "fixtures", Where: WhereDir, MountPath: "/data/fixtures"}, {Name: " "}}
	require.Equal(t, http.StatusCreated, w.do(t, http.MethodPost, testsAPI, developer, f).Code)
	assert.Equal(t, []testsv1alpha1.TestDataSource{
		{ConfigMap: "products-stage"},
		{Secret: "account", Items: []testsv1alpha1.TestDataItem{{Key: "env", Path: "/data/repo/perf/.env"}}},
		{ConfigMap: "fixtures", MountPath: "/data/fixtures"},
	}, w.test(t, "checkout").Spec.Content.TestData)

	bad := gitForm()
	bad.TestData = []TestDataField{{Kind: KindConfigMap, Name: "x", Where: WhereFiles, Items: "no-path"}}
	assert.Contains(t, strings.Join(decodeAs[Preview](t, w.do(t, http.MethodPost, previewAPI, viewer, bad)).Errors, "\n"),
		"is not key=/absolute/path")

	noPath := gitForm()
	noPath.GitPath = ""
	assert.Contains(t, strings.Join(decodeAs[Preview](t, w.do(t, http.MethodPost, previewAPI, viewer, noPath)).Errors, "\n"),
		"script: required — The k6 script.")

	outside := gitForm()
	outside.GitPaths, outside.Params["script"] = "perf", "other/x.js"
	p := decodeAs[Preview](t, w.do(t, http.MethodPost, previewAPI, viewer, outside))
	assert.Empty(t, p.Errors)
	assert.Contains(t, strings.Join(p.Warnings, "\n"), "script is other/x.js, which the sparse checkout (perf) leaves out")
}

func TestEditor_YAMLMode(t *testing.T) {
	w := newWorld(t, k6Template())
	yamlText := `apiVersion: tests.kubetest.io/v1alpha1
kind: Test
metadata:
  name: with-services
  namespace: team-a
spec:
  use: [k6]
  services:
    db:
      image: postgres:17
`
	rec := w.do(t, http.MethodPost, testsAPI, developer, TestForm{Mode: ModeYAML, YAML: yamlText})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "postgres:17", w.test(t, "with-services").Spec.Services["db"].Image)

	typo := strings.Replace(yamlText, "use:", "uses:", 1)
	p := decodeAs[Preview](t, w.do(t, http.MethodPost, previewAPI, viewer, TestForm{Mode: ModeYAML, YAML: typo}))
	assert.Contains(t, strings.Join(p.Errors, "\n"), "unknown field")

	broken := w.do(t, http.MethodPost, previewAPI, viewer, TestForm{Mode: ModeYAML, YAML: "kind: [unclosed"})
	require.Equal(t, http.StatusOK, broken.Code, "an answer, not a failed request: the browser logs those")
	p = decodeAs[Preview](t, broken)
	assert.Empty(t, p.YAML, "nothing to show: the UI keeps the last YAML")
	assert.NotEmpty(t, p.Errors)
}

func TestEditor_EditAndDuplicate(t *testing.T) {
	existing := smokeTest()
	existing.Spec.Use = []string{"k6"}
	existing.Spec.Container = testsv1alpha1.ContainerConfig{Args: []string{"run", "a.js"}}
	existing.Spec.Config = nil
	existing.Spec.Verdict = &testsv1alpha1.VerdictSpec{From: "junit"}
	w := newWorld(t, existing, k6Template())

	of := decodeAs[TestFormOf](t, w.do(t, http.MethodGet, smokePath+"/form", viewer, nil))
	assert.False(t, of.Locked)
	assert.Equal(t, "run\na.js", of.Form.Args)
	assert.Equal(t, ModeForm, of.Form.Mode)
	rv := w.test(t, "smoke").ResourceVersion
	assert.Equal(t, rv, of.Form.ResourceVersion)
	assert.Contains(t, of.Form.YAML, "kind: Test")

	f := of.Form
	f.Args, f.Params = "run\nb.js", map[string]string{"vus": "25", "script": "b.js"}
	assert.Contains(t, decodeAs[Preview](t, w.do(t, http.MethodPost, smokePreview, viewer, f)).YAML, "b.js")
	rec := w.do(t, http.MethodPut, smokeEdit, developer, f)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := w.test(t, "smoke")
	assert.Equal(t, []string{"run", "b.js"}, got.Spec.Container.Args)
	assert.Equal(t, "25", got.Spec.Config["vus"].Default)
	assert.Equal(t, "junit", got.Spec.Verdict.From, "fields outside the form stay")
	assert.Equal(t, ManagedByUI, got.Labels[LabelManagedBy])

	// The form was opened before that save: refused, not overwritten.
	stale := w.do(t, http.MethodPut, smokeEdit, developer, f)
	assert.Equal(t, http.StatusConflict, stale.Code)
	assert.Equal(t, "The Test changed since you opened it. Reload it to start from the current version.", errorOf(t, stale))
	assert.Equal(t, http.StatusForbidden, w.do(t, http.MethodPut, smokeEdit, viewer, f).Code)

	// Meanwhile taken over by Git: the API refuses, and says why.
	got.Labels[LabelManagedBy] = "argocd"
	require.NoError(t, w.k8s.Update(context.Background(), got))
	of = decodeAs[TestFormOf](t, w.do(t, http.MethodGet, smokePath+"/form", viewer, nil))
	assert.True(t, of.Locked, "duplicate it instead")
	refused := w.do(t, http.MethodPut, smokeEdit, developer, of.Form)
	assert.Equal(t, http.StatusConflict, refused.Code)
	assert.Contains(t, errorOf(t, refused), "edit it in the source repo")
}

func TestEditor_YAMLOnlyTests(t *testing.T) {
	composite := smokeTest()
	composite.Spec.Steps = []testsv1alpha1.Step{{Name: "a", Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "other"}}}}}
	w := newWorld(t, composite)
	of := decodeAs[TestFormOf](t, w.do(t, http.MethodGet, smokePath+"/form", viewer, nil))
	assert.Equal(t, ModeYAML, of.Form.Mode)
	assert.Equal(t, "A composite Test (spec.steps) is edited as YAML.", of.YAMLOnly)
}

func TestEditor_FormRoundTrip(t *testing.T) {
	// Everything the form covers, read back into the form and saved again:
	// the same Test.
	w := newWorld(t, k6Template())
	f := gitForm()
	f.GitPaths, f.GitSecretKey, f.Params["vus"] = "perf\nlib", "pat", "25"
	f.Env, f.CPURequest, f.MemoryRequest, f.CPULimit = "A=1\nB=two", "100m", "128Mi", "1"
	f.ServiceAccount, f.PodLabels, f.Schedule = "runner", "team=perf", "0 2 * * *"
	f.TestData = []TestDataField{{Kind: KindSecret, Name: "account", Where: WhereFiles, Items: "env=/data/repo/perf/.env"},
		{Kind: KindConfigMap, Name: "fixtures", Where: WhereDir, MountPath: "/data/fixtures"}}
	require.Equal(t, http.StatusCreated, w.do(t, http.MethodPost, testsAPI, developer, f).Code)
	first := w.test(t, "checkout")

	of := decodeAs[TestFormOf](t, w.do(t, http.MethodGet, "/api/v1/clusters/dev/tests/team-a/checkout/form", viewer, nil))
	back := of.Form
	assert.Equal(t, "perf/checkout.js", back.GitPath)
	assert.Equal(t, "perf\nlib", back.GitPaths)
	assert.Equal(t, "pat", back.GitSecretKey)
	assert.Equal(t, "A=1\nB=two", back.Env)
	assert.Equal(t, "100m", back.CPURequest)
	assert.Equal(t, "runner", back.ServiceAccount)
	assert.Equal(t, "team=perf", back.PodLabels)
	assert.Equal(t, "sidecar.istio.io/inject=false", back.PodAnnotations)
	assert.Equal(t, "0 2 * * *", back.Schedule)
	assert.Equal(t, "15m0s", back.Timeout)
	assert.Equal(t, f.TestData, back.TestData)
	assert.Equal(t, "25", back.Params["vus"])

	rec := w.do(t, http.MethodPut, "/api/v1/clusters/dev/tests/team-a/checkout", developer, back)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	again := w.test(t, "checkout")
	assert.Equal(t, first.Spec, again.Spec, "saving the form unchanged changes nothing")
}

func TestEditor_YAMLRefusals(t *testing.T) {
	w := newWorld(t, k6Template())
	for name, c := range map[string]struct{ yaml, want string }{
		"not a Test":       {"apiVersion: v1\nkind: ConfigMap\nmetadata: {name: x, namespace: team-a}\n", "Test"},
		"managed-by label": {"apiVersion: tests.kubetest.io/v1alpha1\nkind: Test\nmetadata:\n  name: x\n  namespace: team-a\n  labels: {app.kubernetes.io/managed-by: argocd}\nspec: {use: [k6]}\n", "managed-by"},
		"no name":          {"apiVersion: tests.kubetest.io/v1alpha1\nkind: Test\nmetadata: {namespace: team-a}\nspec: {use: [k6]}\n", "name"},
	} {
		p := decodeAs[Preview](t, w.do(t, http.MethodPost, previewAPI, viewer, TestForm{Mode: ModeYAML, YAML: c.yaml}))
		assert.Contains(t, strings.Join(p.Errors, "\n"), c.want, name)
	}
}

func TestEditor_OnlyAChangedDefinitionRefusesASave(t *testing.T) {
	existing := smokeTest()
	existing.Generation, existing.Spec.Use = 1, []string{"k6"}
	existing.Spec.Config = map[string]testsv1alpha1.Parameter{"script": {Type: "string", Default: "a.js"}}
	w := newWorld(t, existing, k6Template())
	f := decodeAs[TestFormOf](t, w.do(t, http.MethodGet, smokePath+"/form", viewer, nil)).Form
	assert.Equal(t, int64(1), f.Generation)

	// A run finishes: the operator writes the Test's status — a new
	// resourceVersion, the same definition. The save goes through.
	got := w.test(t, "smoke")
	got.Status.LatestRun = &testsv1alpha1.RunReference{Name: "smoke-next", Phase: "passed"}
	require.NoError(t, w.k8s.Update(context.Background(), got))
	require.NotEqual(t, f.ResourceVersion, w.test(t, "smoke").ResourceVersion)
	f.Timeout = "20m"
	rec := w.do(t, http.MethodPut, smokeEdit, developer, f)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 20*time.Minute, w.test(t, "smoke").Spec.Timeout.Duration)

	// Someone else changes the definition: refused, nothing overwritten.
	got = w.test(t, "smoke")
	got.Generation = 2
	got.Spec.Timeout = &metav1.Duration{Duration: time.Hour}
	require.NoError(t, w.k8s.Update(context.Background(), got))
	f.Timeout = "5m"
	stale := w.do(t, http.MethodPut, smokeEdit, developer, f)
	assert.Equal(t, http.StatusConflict, stale.Code)
	assert.Equal(t, testChanged, errorOf(t, stale))
	assert.Equal(t, time.Hour, w.test(t, "smoke").Spec.Timeout.Duration)
}
