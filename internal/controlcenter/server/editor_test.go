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
	"net/url"
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
)

const newURL = clusterURL + "/tests/new"

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

func wizardForm(action string) url.Values {
	return url.Values{
		"action": {action}, "mode": {"form"}, "namespace": {"team-a"}, "name": {"checkout"},
		"template": {"k6"}, "param.k6.script": {""}, "param.k6.vus": {"10"},
		"source": {"git"}, "gitURI": {"https://github.com/org/perf"}, "gitRevision": {"main"},
		"gitPath":   {"perf/checkout.js"},
		"gitSecret": {"git-token"},
		"env":       {"TARGET=https://shop\n"}, "memoryLimit": {"1Gi"},
		"podAnnotations": {"sidecar.istio.io/inject=false"}, "timeout": {"15m"},
		// One empty file row, as the page always offers.
		"file.path": {""}, "file.content": {""},
	}
}

func TestEditor_NewTestFromTemplate(t *testing.T) {
	w := newWorld(t, smokeTest(), k6Template())

	page := w.get(t, newURL+"?namespace=team-a", developer)
	require.Equal(t, http.StatusOK, page.Code, page.Body.String())
	body := page.Body.String()
	assert.Contains(t, body, `name="template" value="k6"`)
	assert.Contains(t, body, `name="param.k6.vus" value="10"`)
	assert.Contains(t, body, `<option value="team-a">`, "namespaces with Tests are suggested")

	// Review: the YAML for Git and the API's dry run; nothing is created.
	rec := w.post(t, newURL, developer, wizardForm(actPreview))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body = rec.Body.String()
	assert.Contains(t, body, "The API would admit this Test")
	assert.Contains(t, body, "use:\n    - k6")
	assert.Contains(t, body, "script:\n      type: string\n      default: perf/checkout.js", "the main path from the path in the repository")
	assert.Contains(t, body, "paths:\n        - perf\n", "and the sparse checkout: the file's directory")
	assert.NotContains(t, body, "vus:", "a parameter equal to the template's default stays out")
	assert.Contains(t, body, "kubetest.io/tool: k6")
	assert.NotContains(t, body, "managed-by", "the YAML is for Git")
	err := w.k8s.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "checkout"}, &testsv1alpha1.Test{})
	assert.True(t, apierrors.IsNotFound(err), "a review creates nothing")

	rec = w.post(t, newURL, developer, wizardForm(actSave))
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Equal(t, clusterURL+"/tests/team-a/checkout?notice=Test+created.", rec.Header().Get("Location"))
	got := w.test(t, "checkout")
	assert.Equal(t, "ui", got.Labels["app.kubernetes.io/managed-by"])
	assert.Equal(t, developer, got.Annotations["kubetest.io/created-by"])
	assert.Equal(t, []string{"k6"}, got.Spec.Use)
	assert.Equal(t, "perf/checkout.js", got.Spec.Config["script"].Default)
	assert.Equal(t, []string{"perf"}, got.Spec.Content.Git.Paths)
	assert.Equal(t, "string", got.Spec.Config["script"].Type)
	require.NotNil(t, got.Spec.Content.Git)
	assert.Equal(t, "git-token", got.Spec.Content.Git.TokenFrom.SecretKeyRef.Name)
	assert.Equal(t, "token", got.Spec.Content.Git.TokenFrom.SecretKeyRef.Key)
	assert.Empty(t, got.Spec.Content.Files, "the empty file row is no file")
	assert.Equal(t, []corev1.EnvVar{{Name: "TARGET", Value: "https://shop"}}, got.Spec.Container.Env)
	assert.Equal(t, resource.MustParse("1Gi"), got.Spec.Container.Resources.Limits[corev1.ResourceMemory])
	assert.Equal(t, map[string]string{"sidecar.istio.io/inject": "false"}, got.Spec.Pod.Annotations)
	assert.Equal(t, 15*time.Minute, got.Spec.Timeout.Duration)
	assert.Empty(t, got.Spec.Container.Image, "the template's image")
}

func TestEditor_ErrorsKeepTheForm(t *testing.T) {
	w := newWorld(t, k6Template())
	form := wizardForm(actSave)
	form.Set("template", "")
	form.Set("name", "Bad_Name")
	form.Set("timeout", "soon")
	form.Set("memoryLimit", "lots")
	form.Set("podAnnotations", "no-equals-sign")
	rec := w.post(t, newURL, developer, form)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	for _, want := range []string{"Fix these first", "Name: ", "Image: required", "Command or arguments",
		"Timeout: &#34;soon&#34;", "Memory limit", "Pod annotations"} {
		assert.Contains(t, body, want)
	}
	assert.Contains(t, body, `value="Bad_Name"`, "what was typed stays")

	form = wizardForm(actSave)
	form.Set("param.k6.vus", "many")
	assert.Contains(t, w.post(t, newURL, developer, form).Body.String(), "is not an integer")
}

func TestEditor_DownloadYAML(t *testing.T) {
	w := newWorld(t, k6Template())
	rec := w.post(t, newURL, developer, wizardForm(actDownload))
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
	w := newWorld(t, k6Template())
	form := wizardForm(actToYAML)
	page := w.post(t, newURL, developer, form).Body.String()
	assert.Contains(t, page, `<textarea name="yaml"`)

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
	rec := w.post(t, newURL, developer, url.Values{"action": {actSave}, "mode": {"yaml"}, "yaml": {yamlText}})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Equal(t, "postgres:17", w.test(t, "with-services").Spec.Services["db"].Image)

	typo := strings.Replace(yamlText, "use:", "uses:", 1)
	body := w.post(t, newURL, developer, url.Values{"action": {actPreview}, "mode": {"yaml"}, "yaml": {typo}}).Body.String()
	assert.Contains(t, body, "unknown field")

	// Back to the form: what the form covers comes back into it.
	back := w.post(t, newURL, developer, url.Values{"action": {actToForm}, "mode": {"yaml"},
		"yaml": {strings.Replace(yamlText, "with-services", "again", 1)}}).Body.String()
	assert.Contains(t, back, `name="name" value="again"`)
	assert.Contains(t, back, `value="k6" checked`)
}

func TestEditor_GitOpsTestIsDuplicatedNotEdited(t *testing.T) {
	gitops := smokeTest()
	gitops.Labels["app.kubernetes.io/managed-by"] = "argocd"
	w := newWorld(t, gitops, k6Template())

	assert.Equal(t, http.StatusConflict, w.get(t, clusterURL+"/tests/team-a/smoke/edit", developer).Code)
	test := w.get(t, clusterURL+"/tests/team-a/smoke", developer).Body.String()
	assert.NotContains(t, test, "/edit\"")
	assert.Contains(t, test, "Duplicate")
	assert.NotContains(t, w.get(t, clusterURL+"/tests/team-a/smoke", admin).Body.String(), "Delete test")

	dup := w.get(t, newURL+"?namespace=team-a&from=smoke", developer).Body.String()
	assert.Contains(t, dup, `name="name" value="smoke-copy"`)
	assert.Contains(t, dup, "grafana/k6:1.4.0")

	rec := w.post(t, clusterURL+"/tests/team-a/smoke/delete", admin, url.Values{})
	assert.Contains(t, rec.Header().Get("Location"), "Could+not+delete")
}

func TestEditor_Roles(t *testing.T) {
	w := newWorld(t, smokeTest(), k6Template())
	viewer := "someone@example.com"
	assert.Equal(t, http.StatusForbidden, w.get(t, newURL, viewer).Code)
	assert.Equal(t, http.StatusForbidden, w.post(t, newURL, viewer, wizardForm(actSave)).Code)
	assert.NotContains(t, w.get(t, clusterURL, viewer).Body.String(), "New test")
	assert.Contains(t, w.get(t, clusterURL, developer).Body.String(), "New test")

	assert.Equal(t, http.StatusForbidden, w.post(t, clusterURL+"/tests/team-a/smoke/delete", developer, url.Values{}).Code)
	assert.Contains(t, w.get(t, clusterURL+"/tests/team-a/smoke", admin).Body.String(), "Delete test smoke")
	rec := w.post(t, clusterURL+"/tests/team-a/smoke/delete", admin, url.Values{})
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "deleted")
	err := w.k8s.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "smoke"}, &testsv1alpha1.Test{})
	assert.True(t, apierrors.IsNotFound(err))
}

func TestManifestYAML_KeepsMeaningfulEmptyObjects(t *testing.T) {
	test := &testsv1alpha1.Test{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "team-a",
		Labels: map[string]string{"app.kubernetes.io/managed-by": "ui"}}}
	test.Spec.Pod = &testsv1alpha1.PodConfig{Volumes: []corev1.Volume{
		{Name: "shm", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}}
	out := manifestYAML(test)
	assert.Contains(t, out, "emptyDir: {}")
	assert.NotContains(t, out, "resources")
	assert.NotContains(t, out, "managed-by")
}

func TestEditor_InlineFilesNeedNoPath(t *testing.T) {
	w := newWorld(t, k6Template())
	form := url.Values{"action": {actSave}, "mode": {"form"}, "namespace": {"team-a"}, "name": {"inline"},
		"template": {"k6"}, "param.k6.script": {""}, "source": {"inline"},
		"file.path": {"load.js", "/data/users.csv", ""}, "file.content": {"export default function () {}", "id\n1\n", ""}}
	rec := w.post(t, newURL, developer, form)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	got := w.test(t, "inline")
	assert.NotContains(t, got.Spec.Config, "script", "inline: no path in the Test — the first file is the script")
	assert.Equal(t, []string{"load.js", "data/users.csv"},
		[]string{got.Spec.Content.Files[0].Path, got.Spec.Content.Files[1].Path}, "paths relative to /data/repo")
	assert.Nil(t, got.Spec.Content.Git)

	page := w.get(t, clusterURL+"/tests/team-a/inline/edit", developer).Body.String()
	assert.Contains(t, page, `name="file.path" value="load.js"`)
	assert.Contains(t, page, `name="source" value="inline" checked`)

	// A value the user typed stays; with git the path in the repository.
	form.Set("name", "typed")
	form.Set("param.k6.script", "main.js")
	require.Equal(t, http.StatusSeeOther, w.post(t, newURL, developer, form).Code)
	assert.Equal(t, "main.js", w.test(t, "typed").Spec.Config["script"].Default)
	form.Set("name", "from-git")
	form.Set("param.k6.script", "")
	form.Set("source", "git")
	form.Set("gitURI", "https://github.com/org/perf")
	form.Set("gitPath", "load/main.js")
	require.Equal(t, http.StatusSeeOther, w.post(t, newURL, developer, form).Code)
	fromGit := w.test(t, "from-git")
	assert.Equal(t, "load/main.js", fromGit.Spec.Config["script"].Default)
	assert.Empty(t, fromGit.Spec.Content.Files, "git or inline, never both")

	// No files at all.
	form = url.Values{"action": {actPreview}, "mode": {"form"}, "namespace": {"team-a"}, "name": {"empty"},
		"template": {"k6"}, "source": {"inline"}}
	assert.Contains(t, w.post(t, newURL, developer, form).Body.String(), "Inline files: add at least one file.")
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
	w := newWorld(t, pw)
	page := w.get(t, newURL+"?namespace=team-a", developer).Body.String()
	assert.Contains(t, page, `data-main-kind="directory"`)
	form := url.Values{"action": {actPreview}, "mode": {"form"}, "namespace": {"team-a"}, "name": {"web"},
		"template": {"playwright"}, "source": {"inline"}, "file.path": {"package.json"}, "file.content": {"{}"}}
	assert.Contains(t, w.post(t, newURL, developer, form).Body.String(), "playwright runs a project: its code comes from git")
}

func TestEditor_TestData(t *testing.T) {
	w := newWorld(t, k6Template())
	form := wizardForm(actSave)
	form["td.kind"] = []string{"configMap", "secret", "configMap", ""}
	form["td.name"] = []string{"products-stage", "account", "fixtures", ""}
	form["td.where"] = []string{"", "files", "dir", ""}
	form["td.mountPath"] = []string{"", "", "/data/fixtures", ""}
	form["td.items"] = []string{"", "env=/data/repo/perf/.env", "", ""}
	rec := w.post(t, newURL, developer, form)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Equal(t, []testsv1alpha1.TestDataSource{
		{ConfigMap: "products-stage"},
		{Secret: "account", Items: []testsv1alpha1.TestDataItem{{Key: "env", Path: "/data/repo/perf/.env"}}},
		{ConfigMap: "fixtures", MountPath: "/data/fixtures"},
	}, w.test(t, "checkout").Spec.Content.TestData)

	page := w.get(t, clusterURL+"/tests/team-a/checkout/edit", developer).Body.String()
	assert.Contains(t, page, `name="td.name" value="account"`)
	assert.Contains(t, page, "env=/data/repo/perf/.env</textarea>")

	bad := wizardForm(actPreview)
	bad["td.name"] = []string{"x"}
	bad["td.where"] = []string{"files"}
	bad["td.items"] = []string{"no-path"}
	assert.Contains(t, w.post(t, newURL, developer, bad).Body.String(), "is not key=/absolute/path")
}

func TestEditor_GitPathIsRequiredAndStatedOnce(t *testing.T) {
	w := newWorld(t, k6Template())
	form := wizardForm(actPreview)
	form.Set("gitPath", "")
	body := w.post(t, newURL, developer, form).Body.String()
	assert.Contains(t, body, "Path in the repository: required")

	// Git without the parameter's value or a path: required, with its
	// description.
	form = wizardForm(actPreview)
	form.Set("gitPath", "")
	k6 := k6Template()
	k6.Spec.Config["script"] = testsv1alpha1.Parameter{Type: "string", Path: "file", Description: "The k6 script."}
	w = newWorld(t, k6)
	assert.Contains(t, w.post(t, newURL, developer, form).Body.String(), "script: required — The k6 script.")

	// A directory is the sparse checkout itself; "." the whole repository.
	w = newWorld(t, k6Template())
	form = wizardForm(actSave)
	form.Set("name", "dir")
	form.Set("gitPath", "e2e/web/")
	require.Equal(t, http.StatusSeeOther, w.post(t, newURL, developer, form).Code)
	got := w.test(t, "dir")
	assert.Equal(t, []string{"e2e/web"}, got.Spec.Content.Git.Paths)
	assert.Equal(t, "e2e/web", got.Spec.Config["script"].Default)
	form.Set("name", "whole")
	form.Set("gitPath", ".")
	require.Equal(t, http.StatusSeeOther, w.post(t, newURL, developer, form).Code)
	assert.Empty(t, w.test(t, "whole").Spec.Content.Git.Paths, "the whole repository: no sparse checkout")

	// The editor shows the path back once — not again as sparse paths.
	page := w.get(t, clusterURL+"/tests/team-a/dir/edit", developer).Body.String()
	assert.Contains(t, page, `name="gitPath" value="e2e/web"`)
	assert.Contains(t, page, `name="param.k6.script" value=""`, "the parameter follows the path")
	assert.Contains(t, page, `<textarea name="gitPaths" rows="2" class="mono"></textarea>`)
}

func TestEditor_MainPathOutsideTheCheckoutWarns(t *testing.T) {
	w := newWorld(t, k6Template())
	form := wizardForm(actPreview)
	form.Set("gitPaths", "perf")
	form.Set("param.k6.script", "other/x.js")
	body := w.post(t, newURL, developer, form).Body.String()
	assert.Contains(t, body, "script is other/x.js, which the sparse checkout (perf) leaves out")
	assert.Contains(t, body, "The API would admit this Test", "a warning, not an error")
}

func TestTestPages_NotReady(t *testing.T) {
	test := smokeTest()
	test.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Reason: "ParameterMissing",
		Message: "set spec.config.script: The k6 script.", LastTransitionTime: metav1.Now()}}
	w := newWorld(t, test)
	assert.Contains(t, w.get(t, clusterURL, developer).Body.String(), `<span class="chip warning" title="set spec.config.script: The k6 script.">not ready</span>`)
	page := w.get(t, clusterURL+"/tests/team-a/smoke", developer).Body.String()
	assert.Contains(t, page, "Not ready to run:</strong> set spec.config.script: The k6 script.")
	assert.Contains(t, page, `/tests/team-a/smoke/edit">edit the Test</a>`)
}
