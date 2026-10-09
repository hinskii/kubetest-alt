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
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// Where a Test's code and data are in the pod (steps 20h, 20i).
const (
	// RepoDir holds the code: the git checkout, the inline files or the
	// unpacked tarball. Templates point their tool at a path below it.
	RepoDir = "/data/repo"
	// TestDataDir holds content.testData entries without a mountPath or
	// items: /data/testdata/<name>/<key>.
	TestDataDir = "/data/testdata"
	// legacyRepoPrefix: inline file paths used to be relative to /data
	// ("repo/load.js"); they are relative to /data/repo now. Stored Tests
	// written the old way keep working; the webhook refuses new ones.
	legacyRepoPrefix = "repo/"
)

// Main path kinds (Parameter.path).
const (
	PathFile      = "file"
	PathDirectory = "directory"
)

// ErrInlineProject: a template that runs a project (main path a
// directory) was given inline files — projects come from git (step 20i).
var ErrInlineProject = errors.New("this tool runs a project — put it in git (content.git); inline files are for single-file tools")

// MainPathParam is the parameter marked as the main path — where the tool
// finds a Test's files (k6's script, JMeter's plan, playwright's
// projectDir, …) — and its kind, file or directory; "" when there is none
// (a tool aimed at a URL, an own image). Templates mark it with
// `path:`; at most one per object (a CEL rule on the CRD).
func MainPathParam(config map[string]testsv1alpha1.Parameter) (name, kind string) {
	for _, k := range slices.Sorted(maps.Keys(config)) {
		if p := config[k].Path; p != "" {
			return k, p
		}
	}
	return "", ""
}

// InlineFilePath is where inline file p lands, relative to RepoDir. Paths
// stored the old way ("repo/x", relative to /data) mean the same file.
func InlineFilePath(p string) string {
	return strings.TrimPrefix(p, legacyRepoPrefix)
}

// inlineCode reports whether spec's code is inline files.
func inlineCode(spec *testsv1alpha1.TestSpec) bool {
	return spec.Content.Git == nil && len(spec.Content.Files) > 0
}

// bindInlineMainFile points an unset main path parameter of kind file at
// the first inline file: with inline code nobody types a path (step 20i).
// An explicit value wins.
func bindInlineMainFile(spec *testsv1alpha1.TestSpec) {
	name, kind := MainPathParam(spec.Config)
	if kind != PathFile || !inlineCode(spec) {
		return
	}
	p := spec.Config[name]
	if p.Default != "" {
		return
	}
	p.Default = InlineFilePath(spec.Content.Files[0].Path)
	spec.Config[name] = p
}

// CheckContent reports a merged spec whose content can't run: code from
// more than one source, or a project given inline.
func CheckContent(spec *testsv1alpha1.TestSpec) error {
	sources := 0
	for _, has := range []bool{spec.Content.Git != nil, len(spec.Content.Files) > 0, len(spec.Content.Tarball) > 0} {
		if has {
			sources++
		}
	}
	if sources > 1 {
		return errors.New("the code comes from more than one source (git, inline files, tarball) — a Test and its templates must use one")
	}
	if _, kind := MainPathParam(spec.Config); kind == PathDirectory && inlineCode(spec) {
		return ErrInlineProject
	}
	return nil
}

// MissingMainPath is the main path parameter a merged spec leaves empty,
// or "".
func MissingMainPath(spec *testsv1alpha1.TestSpec) string {
	if name, _ := MainPathParam(spec.Config); name != "" && spec.Config[name].Default == "" {
		return name
	}
	return ""
}

// MainPathMessage says what to set, with the parameter's description.
func MainPathMessage(spec *testsv1alpha1.TestSpec, name string) string {
	if d := spec.Config[name].Description; d != "" {
		return fmt.Sprintf("set spec.config.%s: %s", name, d)
	}
	return fmt.Sprintf("set spec.config.%s — where the Test's files are", name)
}
