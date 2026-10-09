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

package yamlview

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

type manifest struct {
	APIVersion string                 `json:"apiVersion"`
	Kind       string                 `json:"kind"`
	Metadata   metav1.ObjectMeta      `json:"metadata"`
	Spec       testsv1alpha1.TestSpec `json:"spec"`
}

func TestFormat_ReadingOrderBlocksAndNoEmptyObjects(t *testing.T) {
	m := manifest{APIVersion: "tests.kubetest.io/v1alpha1", Kind: "Test",
		Metadata: metav1.ObjectMeta{Name: "k6-load", Namespace: "demo", Labels: map[string]string{"kubetest.io/tool": "k6"}},
		Spec: testsv1alpha1.TestSpec{
			Use:    []string{"k6"},
			Config: map[string]testsv1alpha1.Parameter{"vus": {Description: "Users", Default: "5", Type: "integer"}},
			Content: testsv1alpha1.Content{Files: []testsv1alpha1.FileContent{
				{Path: "load.js", Content: "import http from 'k6/http';\nexport default function () {}\n"}}},
			Pod: &testsv1alpha1.PodConfig{Volumes: []corev1.Volume{
				{Name: "shm", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}},
		}}
	out, err := Format(m, Manifest)
	require.NoError(t, err)
	want := `apiVersion: tests.kubetest.io/v1alpha1
kind: Test
metadata:
  name: k6-load
  namespace: demo
  labels:
    kubetest.io/tool: k6
spec:
  use:
    - k6
  config:
    vus:
      type: integer
      default: "5"
      description: Users
  content:
    files:
      - path: load.js
        content: |
          import http from 'k6/http';
          export default function () {}
  pod:
    volumes:
      - name: shm
        emptyDir: {}
`
	assert.Equal(t, want, out, "reading order, a | block, no resources: {} — but emptyDir: {} stays")
}

func TestFormat_SpecRoot(t *testing.T) {
	out, err := Format(testsv1alpha1.TestSpec{Schedule: "0 2 * * *", Use: []string{"k6"}}, Spec)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(out, "use:\n  - k6\n"), out)
	out, err = Format(testsv1alpha1.TestSpec{}, Spec)
	require.NoError(t, err)
	assert.Equal(t, "{}\n", out, "an empty spec stays an object")
}

func TestHighlight(t *testing.T) {
	html := string(Highlight("name: <b>x</b>\nvus: 5\n"))
	assert.Contains(t, html, `class="chroma"`)
	assert.Contains(t, html, `<span class="ln">1</span>`, "line numbers")
	assert.Contains(t, html, `&lt;b&gt;`, "escaped")
	assert.NotContains(t, html, "<b>")
	assert.Contains(t, string(HighlightAs("export default function () {}", "load.js")), `class="kd"`, "JavaScript by file name")
}

func TestCSS_LightAndDark(t *testing.T) {
	css := CSS()
	assert.Contains(t, css, `:root:not([data-theme="dark"]) .chroma .nt {`)
	assert.Contains(t, css, `:root[data-theme="dark"] .chroma .nt {`)
	assert.Contains(t, css, "background-color: transparent !important")
}

func TestCSSVersion_FollowsTheContent(t *testing.T) {
	assert.Len(t, CSSVersion(), 12)
	assert.Equal(t, CSSVersion(), CSSVersion(), "stable for the same styles")
}
