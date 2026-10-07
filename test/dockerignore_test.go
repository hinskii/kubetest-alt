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

package test

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDockerignore_KeepsEveryEmbed: the Dockerfile builds from a
// default-deny .dockerignore, so a //go:embed whose files aren't
// re-included builds locally and fails only inside Docker ("pattern X: no
// matching files found") — it happened for the migrations and again for
// Control Center's templates and static files.
func TestDockerignore_KeepsEveryEmbed(t *testing.T) {
	root, err := findRepoRoot()
	require.NoError(t, err)
	rules := dockerignoreRules(t, filepath.Join(root, ".dockerignore"))
	embed := regexp.MustCompile(`^//go:embed (.+)$`)
	checked := 0
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if strings.HasPrefix(rel, ".") && rel != "." || rel == "control-center" || rel == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := os.Open(path) // #nosec G304,G122 -- walking our own repo checkout
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			m := embed.FindStringSubmatch(strings.TrimSpace(sc.Text()))
			if m == nil {
				continue
			}
			dir := filepath.Dir(path)
			for pattern := range strings.FieldsSeq(m[1]) {
				matches, err := doublestar.FilepathGlob(filepath.Join(dir, pattern) + "{,/**}")
				require.NoError(t, err)
				for _, match := range matches {
					if info, err := os.Stat(match); err != nil || info.IsDir() {
						continue
					}
					file, _ := filepath.Rel(root, match)
					checked++
					assert.True(t, included(rules, filepath.ToSlash(file)),
						"%s embeds %s, but .dockerignore leaves it out of the Docker build context", rel, file)
				}
			}
		}
		return sc.Err()
	}))
	assert.Positive(t, checked, "found the embeds")
}

type ignoreRule struct {
	pattern string
	include bool // a "!" rule
}

func dockerignoreRules(t *testing.T, path string) []ignoreRule {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- the repo's .dockerignore
	require.NoError(t, err)
	var rules []ignoreRule
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r := ignoreRule{pattern: line}
		if strings.HasPrefix(line, "!") {
			r = ignoreRule{pattern: line[1:], include: true}
		}
		rules = append(rules, r)
	}
	return rules
}

// included applies .dockerignore semantics: the last matching rule wins.
func included(rules []ignoreRule, file string) bool {
	in := true
	for _, r := range rules {
		if ok, _ := doublestar.Match(r.pattern, file); ok {
			in = r.include
		}
	}
	return in
}
