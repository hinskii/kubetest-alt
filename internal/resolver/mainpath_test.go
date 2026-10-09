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

package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

func TestMainPathParam(t *testing.T) {
	name, kind := MainPathParam(map[string]testsv1alpha1.Parameter{
		"vus": {Type: "integer"}, "projectDir": {Type: "string", Path: PathDirectory}})
	assert.Equal(t, "projectDir", name)
	assert.Equal(t, PathDirectory, kind)
	name, _ = MainPathParam(map[string]testsv1alpha1.Parameter{"target": {Type: "string"}})
	assert.Empty(t, name, "none marked: a tool aimed at a URL")
}

func k6Like(files []testsv1alpha1.FileContent, git *testsv1alpha1.GitContent, scriptDefault string) *testsv1alpha1.TestSpec {
	return &testsv1alpha1.TestSpec{
		Config: map[string]testsv1alpha1.Parameter{
			"script": {Type: "string", Path: PathFile, Default: scriptDefault, Description: "The k6 script."}},
		Content: testsv1alpha1.Content{Files: files, Git: git},
	}
}

func TestBindInlineMainFile(t *testing.T) {
	files := []testsv1alpha1.FileContent{{Path: "load.js"}, {Path: "data.csv"}}
	s := k6Like(files, nil, "")
	bindInlineMainFile(s)
	assert.Equal(t, "load.js", s.Config["script"].Default, "the first file")

	s = k6Like([]testsv1alpha1.FileContent{{Path: "repo/old.js"}}, nil, "")
	bindInlineMainFile(s)
	assert.Equal(t, "old.js", s.Config["script"].Default, "a path stored the old way")

	s = k6Like(files, nil, "data-driven.js")
	bindInlineMainFile(s)
	assert.Equal(t, "data-driven.js", s.Config["script"].Default, "an explicit value wins")

	s = k6Like(nil, &testsv1alpha1.GitContent{URI: "https://x"}, "")
	bindInlineMainFile(s)
	assert.Empty(t, s.Config["script"].Default, "git: the Test must say")
	assert.Equal(t, "script", MissingMainPath(s))
	assert.Equal(t, "set spec.config.script: The k6 script.", MainPathMessage(s, "script"))
}

func TestCheckContent(t *testing.T) {
	git := &testsv1alpha1.GitContent{URI: "https://x"}
	assert.NoError(t, CheckContent(k6Like(nil, git, "")))
	assert.ErrorContains(t, CheckContent(k6Like([]testsv1alpha1.FileContent{{Path: "a.js"}}, git, "")), "more than one source")

	project := &testsv1alpha1.TestSpec{
		Config:  map[string]testsv1alpha1.Parameter{"projectDir": {Type: "string", Path: PathDirectory}},
		Content: testsv1alpha1.Content{Files: []testsv1alpha1.FileContent{{Path: "package.json"}}},
	}
	assert.ErrorIs(t, CheckContent(project), ErrInlineProject)
}
