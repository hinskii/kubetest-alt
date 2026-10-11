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
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	uiapi "github.com/hinskii/kubetest-alt/internal/controlcenter/api"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

func k6Template() *testsv1alpha1.TestTemplate {
	return &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "k6", Namespace: "team-a", Labels: map[string]string{"kubetest.io/tool": "k6"}},
		Spec: testsv1alpha1.TestTemplateSpec{
			Container: testsv1alpha1.ContainerConfig{Image: "grafana/k6:1.4.0", Command: []string{"k6"},
				Args: []string{"run", "/data/repo/{{ config.script }}"}},
			Config: map[string]testsv1alpha1.Parameter{
				"script": {Type: "string", Path: "file"},
				"vus":    {Type: "integer", Default: "10"},
			},
		},
	}
}

func (w *world) test(t *testing.T, name string) *testsv1alpha1.Test {
	t.Helper()
	var out testsv1alpha1.Test
	require.NoError(t, w.k8s.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: name}, &out))
	return &out
}

// The new-test wizard is the React UI's (step 20R R4b) and the build it
// runs is the api package's (editor_api_test.go); these are the
// server-rendered edit page's tests until R4c replaces it.

// k6Test is a GUI-managed Test of the k6 template from git; mutate
// changes it.
func k6Test(name string, mutate func(*testsv1alpha1.Test)) *testsv1alpha1.Test {
	t := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team-a",
			Labels: map[string]string{"kubetest.io/tool": "k6", "app.kubernetes.io/managed-by": "ui"}},
		Spec: testsv1alpha1.TestSpec{
			Use: []string{"k6"},
			Content: testsv1alpha1.Content{Git: &testsv1alpha1.GitContent{URI: "https://github.com/org/perf",
				Revision: "main", Paths: []string{"perf"}}},
			Config: map[string]testsv1alpha1.Parameter{"script": {Type: "string", Default: "perf/checkout.js"}},
		},
	}
	if mutate != nil {
		mutate(t)
	}
	return t
}

func editURL(name string) string { return clusterURL + "/tests/team-a/" + name + "/edit" }

// editForm is the edit page's form for k6Test("checkout") as posted.
func editForm(action, rv string) url.Values {
	return url.Values{
		"action": {action}, "mode": {"form"}, "template": {"k6"}, "resourceVersion": {rv},
		"param.k6.script": {""}, "param.k6.vus": {"10"},
		"source": {"git"}, "gitURI": {"https://github.com/org/perf"}, "gitRevision": {"main"},
		"gitPath": {"perf/checkout.js"}, "gitSecret": {"git-token"},
		"env": {"TARGET=https://shop\n"}, "memoryLimit": {"1Gi"},
		"podAnnotations": {"sidecar.istio.io/inject=false"}, "timeout": {"15m"},
		// One empty file row, as the page always offers.
		"file.path": {""}, "file.content": {""},
	}
}

func TestEditor_EditAndSave(t *testing.T) {
	w := newWorld(t, k6Test("checkout", nil), k6Template())
	page := w.get(t, editURL("checkout"), developer)
	require.Equal(t, http.StatusOK, page.Code, page.Body.String())
	assert.Contains(t, page.Body.String(), `<option value="k6" selected`)
	rv := w.test(t, "checkout").ResourceVersion

	// Review: the YAML and the API's dry run; nothing is written.
	body := w.post(t, editURL("checkout"), developer, editForm(actPreview, rv)).Body.String()
	assert.Contains(t, body, "The API would admit this Test")
	assert.Contains(t, body, "memory: 1Gi")
	assert.Nil(t, w.test(t, "checkout").Spec.Pod, "a review writes nothing")

	rec := w.post(t, editURL("checkout"), developer, editForm(actSave, rv))
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Equal(t, clusterURL+"/tests/team-a/checkout?notice=Test+saved.", rec.Header().Get("Location"))
	got := w.test(t, "checkout")
	assert.Equal(t, "git-token", got.Spec.Content.Git.TokenFrom.SecretKeyRef.Name)
	assert.Equal(t, []corev1.EnvVar{{Name: "TARGET", Value: "https://shop"}}, got.Spec.Container.Env)
	assert.Equal(t, resource.MustParse("1Gi"), got.Spec.Container.Resources.Limits[corev1.ResourceMemory])
	assert.Equal(t, map[string]string{"sidecar.istio.io/inject": "false"}, got.Spec.Pod.Annotations)
	assert.Equal(t, 15*time.Minute, got.Spec.Timeout.Duration)
}

func TestEditor_ErrorsKeepTheForm(t *testing.T) {
	w := newWorld(t, k6Test("checkout", nil), k6Template())
	form := editForm(actSave, w.test(t, "checkout").ResourceVersion)
	form.Set("template", "")
	form.Set("timeout", "soon")
	form.Set("memoryLimit", "lots")
	form.Set("podAnnotations", "no-equals-sign")
	rec := w.post(t, editURL("checkout"), developer, form)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	for _, want := range []string{"Fix these first", "Image: required", "Command or arguments",
		"Timeout: &#34;soon&#34;", "Memory limit", "Pod annotations"} {
		assert.Contains(t, body, want)
	}
	assert.Contains(t, body, `value="lots"`, "what was typed stays")
}

func TestEditor_DownloadYAML(t *testing.T) {
	w := newWorld(t, k6Test("checkout", nil), k6Template())
	rec := w.post(t, editURL("checkout"), developer, editForm(actDownload, ""))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `attachment; filename="checkout.yaml"`, rec.Header().Get("Content-Disposition"))
	assert.True(t, strings.HasPrefix(rec.Body.String(), "apiVersion: tests.kubetest.io/v1alpha1\nkind: Test\n"), rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "{}", "no empty objects in the YAML for Git")
}

func TestEditor_APIRefusalIsShown(t *testing.T) {
	w := newWorld(t, smokeTest(), k6Template())
	edit := clusterURL + "/tests/team-a/smoke/edit"
	require.Equal(t, http.StatusOK, w.get(t, edit, developer).Code)
	// Meanwhile the Test is taken over by Git: the API refuses the edit,
	// and the page says so instead of failing.
	got := w.test(t, "smoke")
	got.Labels["app.kubernetes.io/managed-by"] = "argocd"
	require.NoError(t, w.k8s.Update(context.Background(), got))
	form := url.Values{"action": {actPreview}, "mode": {"form"}, "template": {"k6"}, "resourceVersion": {got.ResourceVersion},
		"param.k6.script": {"a.js"}}
	body := w.post(t, edit, developer, form).Body.String()
	assert.Contains(t, body, "The API would refuse it")
	assert.Contains(t, body, "edit it in the source repo")
}

func TestEditor_EditKeepsWhatTheFormDoesNotCover(t *testing.T) {
	existing := smokeTest()
	existing.Spec.Use = []string{"k6"}
	existing.Spec.Container = testsv1alpha1.ContainerConfig{Args: []string{"run", "a.js"}}
	existing.Spec.Config = nil
	existing.Spec.Verdict = &testsv1alpha1.VerdictSpec{From: "junit"}
	w := newWorld(t, existing, k6Template())
	edit := clusterURL + "/tests/team-a/smoke/edit"

	page := w.get(t, edit, developer).Body.String()
	assert.Contains(t, page, "run\na.js</textarea>")
	rv := w.test(t, "smoke").ResourceVersion
	assert.Contains(t, page, `name="resourceVersion" value="`+rv+`"`)

	form := url.Values{"action": {actSave}, "mode": {"form"}, "template": {"k6"}, "resourceVersion": {rv},
		"args": {"run\nb.js"}, "param.k6.vus": {"25"}, "param.k6.script": {"b.js"}}
	rec := w.post(t, edit, developer, form)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	got := w.test(t, "smoke")
	assert.Equal(t, []string{"run", "b.js"}, got.Spec.Container.Args)
	assert.Equal(t, "25", got.Spec.Config["vus"].Default)
	assert.Equal(t, "junit", got.Spec.Verdict.From, "fields outside the form stay")
	assert.Equal(t, "ui", got.Labels["app.kubernetes.io/managed-by"])

	// The form was opened before that save: refused, not overwritten.
	stale := w.post(t, edit, developer, form).Body.String()
	assert.Contains(t, stale, "Not saved:")
	assert.Contains(t, stale, "The Test changed since you opened the editor")
}

func TestEditor_YAMLMode(t *testing.T) {
	w := newWorld(t, k6Test("checkout", nil), k6Template())
	rv := w.test(t, "checkout").ResourceVersion
	page := w.post(t, editURL("checkout"), developer, editForm(actToYAML, rv)).Body.String()
	assert.Contains(t, page, `<textarea name="yaml"`)

	yamlText := `apiVersion: tests.kubetest.io/v1alpha1
kind: Test
metadata:
  name: checkout
  namespace: team-a
spec:
  use: [k6]
  services:
    db:
      image: postgres:17
`
	typo := strings.Replace(yamlText, "use:", "uses:", 1)
	body := w.post(t, editURL("checkout"), developer, url.Values{"action": {actPreview}, "mode": {"yaml"}, "yaml": {typo}}).Body.String()
	assert.Contains(t, body, "unknown field")

	rec := w.post(t, editURL("checkout"), developer, url.Values{"action": {actSave}, "mode": {"yaml"}, "yaml": {yamlText},
		"resourceVersion": {rv}})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Equal(t, "postgres:17", w.test(t, "checkout").Spec.Services["db"].Image)

	// Back to the form: what the form covers comes back into it.
	back := w.post(t, editURL("checkout"), developer, url.Values{"action": {actToForm}, "mode": {"yaml"},
		"yaml": {yamlText}}).Body.String()
	assert.Contains(t, back, `<option value="k6" selected`)
	assert.Contains(t, back, `data-mode="form"`)
}

func TestEditor_GitOpsTestIsNotEdited(t *testing.T) {
	gitops := smokeTest()
	gitops.Labels["app.kubernetes.io/managed-by"] = "argocd"
	w := newWorld(t, gitops, k6Template())
	assert.Equal(t, http.StatusConflict, w.get(t, editURL("smoke"), developer).Code)
}

func TestEditor_Roles(t *testing.T) {
	w := newWorld(t, smokeTest(), k6Template())
	viewer := "someone@example.com"
	assert.Equal(t, http.StatusForbidden, w.get(t, editURL("smoke"), viewer).Code)
	assert.Equal(t, http.StatusForbidden, w.post(t, editURL("smoke"), viewer, editForm(actSave, "")).Code)
	// The new-test page is the UI's; creating needs the role at the API.
	assert.Equal(t, http.StatusOK, w.get(t, clusterURL+"/tests/new", viewer).Code)
}

func TestManifestYAML_KeepsMeaningfulEmptyObjects(t *testing.T) {
	test := &testsv1alpha1.Test{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "team-a",
		Labels: map[string]string{"app.kubernetes.io/managed-by": "ui"}}}
	test.Spec.Pod = &testsv1alpha1.PodConfig{Volumes: []corev1.Volume{
		{Name: "shm", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}}
	out := uiapi.ManifestYAML(test)
	assert.Contains(t, out, "emptyDir: {}")
	assert.NotContains(t, out, "resources")
	assert.NotContains(t, out, "managed-by")
}

func TestEditor_InlineFiles(t *testing.T) {
	inline := k6Test("inline", func(t *testsv1alpha1.Test) {
		t.Spec.Content = testsv1alpha1.Content{Files: []testsv1alpha1.FileContent{{Path: "load.js", Content: "export default function () {}"}}}
		t.Spec.Config = nil
	})
	w := newWorld(t, inline, k6Template())
	page := w.get(t, editURL("inline"), developer).Body.String()
	assert.Contains(t, page, `name="file.path" value="load.js"`)
	assert.Contains(t, page, `name="source" value="inline" checked`)

	form := url.Values{"action": {actPreview}, "mode": {"form"}, "template": {"k6"}, "source": {"inline"}}
	assert.Contains(t, w.post(t, editURL("inline"), developer, form).Body.String(), "Inline files: add at least one file.")
}

func TestEditor_ProjectToolsTakeGitOnly(t *testing.T) {
	pw := &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "playwright", Namespace: "team-a", Labels: map[string]string{"kubetest.io/tool": "playwright"}},
		Spec: testsv1alpha1.TestTemplateSpec{
			Container: testsv1alpha1.ContainerConfig{Image: "mcr.microsoft.com/playwright", Command: []string{"sh", "-c"},
				Args: []string{"cd /data/repo/{{ config.projectDir }} && npx playwright test"}},
			Config: map[string]testsv1alpha1.Parameter{"projectDir": {Type: "string", Path: "directory"}},
		},
	}
	web := k6Test("web", func(t *testsv1alpha1.Test) {
		t.Spec.Use, t.Spec.Config = []string{"playwright"}, nil
	})
	w := newWorld(t, pw, web)
	assert.Contains(t, w.get(t, editURL("web"), developer).Body.String(), `data-main-kind="directory"`)
	form := url.Values{"action": {actPreview}, "mode": {"form"}, "template": {"playwright"}, "source": {"inline"},
		"file.path": {"package.json"}, "file.content": {"{}"}}
	assert.Contains(t, w.post(t, editURL("web"), developer, form).Body.String(), "playwright runs a project: its code comes from git")
}

func TestEditor_TestData(t *testing.T) {
	withData := k6Test("checkout", func(t *testsv1alpha1.Test) {
		t.Spec.Content.TestData = []testsv1alpha1.TestDataSource{
			{Secret: "account", Items: []testsv1alpha1.TestDataItem{{Key: "env", Path: "/data/repo/perf/.env"}}}}
	})
	w := newWorld(t, withData, k6Template())
	page := w.get(t, editURL("checkout"), developer).Body.String()
	assert.Contains(t, page, `name="td.name" value="account"`)
	assert.Contains(t, page, "env=/data/repo/perf/.env</textarea>")

	bad := editForm(actPreview, "")
	bad["td.name"] = []string{"x"}
	bad["td.where"] = []string{"files"}
	bad["td.items"] = []string{"no-path"}
	assert.Contains(t, w.post(t, editURL("checkout"), developer, bad).Body.String(), "is not key=/absolute/path")
}

func TestEditor_GitPathIsStatedOnce(t *testing.T) {
	dir := k6Test("dir", func(t *testsv1alpha1.Test) {
		t.Spec.Content.Git.Paths = []string{"e2e/web"}
		t.Spec.Config = map[string]testsv1alpha1.Parameter{"script": {Type: "string", Default: "e2e/web"}}
	})
	w := newWorld(t, dir, k6Template())
	page := w.get(t, editURL("dir"), developer).Body.String()
	assert.Contains(t, page, `name="gitPath" value="e2e/web"`)
	assert.Contains(t, page, `name="param.k6.script" value=""`, "the parameter follows the path")
	assert.Contains(t, page, `<textarea name="gitPaths" rows="2" class="mono"></textarea>`)

	form := editForm(actPreview, "")
	form.Set("gitPath", "")
	assert.Contains(t, w.post(t, editURL("dir"), developer, form).Body.String(), "Path in the repository: required")
}

func TestEditor_MainPathOutsideTheCheckoutWarns(t *testing.T) {
	w := newWorld(t, k6Test("checkout", nil), k6Template())
	form := editForm(actPreview, "")
	form.Set("gitPaths", "perf")
	form.Set("param.k6.script", "other/x.js")
	body := w.post(t, editURL("checkout"), developer, form).Body.String()
	assert.Contains(t, body, "script is other/x.js, which the sparse checkout (perf) leaves out")
	assert.Contains(t, body, "The API would admit this Test", "a warning, not an error")
}

func TestEditor_LiveYAML(t *testing.T) {
	w := newWorld(t, k6Test("checkout", nil), k6Template())
	live := editURL("checkout") + "/yaml"
	rec := w.post(t, live, developer, editForm(actPreview, ""))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.NotContains(t, body, "<html", "a fragment, not a page")
	assert.Contains(t, body, `class="chroma"`, "highlighted")

	// The same YAML the download gives.
	dl := w.post(t, editURL("checkout"), developer, editForm(actDownload, "")).Body.String()
	assert.Contains(t, body, html.EscapeString(dl), "the preview is what Save writes")

	// Broken YAML (YAML mode) can't be built: 422, the page keeps the last.
	rec = w.post(t, live, developer, url.Values{"mode": {"yaml"}, "yaml": {"kind: [unclosed"}})
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.NotContains(t, rec.Body.String(), "yaml-live-raw")
	assert.Equal(t, http.StatusForbidden, w.post(t, live, "someone@example.com", editForm(actPreview, "")).Code)
}
