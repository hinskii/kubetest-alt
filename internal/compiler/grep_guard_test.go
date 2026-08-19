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

package compiler

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGrepGuard_NoDeletedSymbols asserts none of the symbols the step-11
// refactor removed have snuck back in. If someone adds e.g. `spec.Type`
// or `resolveExecutor` in a future PR, this test fails LOUDLY at CI time
// — cheaper than reviewer catch.
//
// Walks Go source under api/, internal/, pkg/, cmd/ (skips sidelined
// *.old files and *_test.go which may name symbols in comments).
func TestGrepGuard_NoDeletedSymbols(t *testing.T) {
	// Symbols removed by step 11 that MUST NOT return.
	// Pattern list — regexps because bare `Type` matches half the k8s
	// API (metav1.TypeMeta, corev1.Type, etc.). Anchor to the specific
	// use we retired.
	patterns := []struct {
		name  string
		regex *regexp.Regexp
	}{
		{"ExecutorK6 const", regexp.MustCompile(`\bExecutorK6\b`)},
		{"ExecutorCypress const", regexp.MustCompile(`\bExecutorCypress\b`)},
		{"ExecutorNewman const", regexp.MustCompile(`\bExecutorNewman\b`)},
		{"ExecutorLocust const", regexp.MustCompile(`\bExecutorLocust\b`)},
		{"ExecutorJMeter const", regexp.MustCompile(`\bExecutorJMeter\b`)},
		{"DefaultExecutorImages map", regexp.MustCompile(`\bDefaultExecutorImages\b`)},
		{"resolveExecutor func", regexp.MustCompile(`\bresolveExecutor\b`)},
		{"ErrUnknownExecutor sentinel", regexp.MustCompile(`\bErrUnknownExecutor\b`)},
		{"ExecutorImages Options field", regexp.MustCompile(`\bExecutorImages\b`)},
		// req.Type / Spec.Type access — the actual field, not comments
		// about it. Look for the access form specifically.
		{"req.Type access", regexp.MustCompile(`\breq\.Type\b`)},
		{"Spec.Type access", regexp.MustCompile(`\bSpec\.Type\b`)},
	}

	root := repoRoot(t)
	dirs := []string{"api", "internal", "pkg", "cmd"}

	for _, dir := range dirs {
		base := filepath.Join(root, dir)
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			// Skip: sidelined .old files, generated deepcopy files,
			// test files (they may cite removed symbols in comments as
			// "we deleted X").
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			if strings.HasSuffix(path, ".old") {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			if strings.HasSuffix(path, "zz_generated.deepcopy.go") {
				return nil
			}
			// #nosec G304 G122 -- walking a known source tree; symlinks under
			// api/internal/pkg/cmd are absent, and this is a test.
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			body := string(b)
			for _, p := range patterns {
				if p.regex.MatchString(body) {
					// Skip if the ONLY matches are inside comment lines
					// mentioning the deletion — comment mentions are OK.
					if allMatchesAreCommentMentions(body, p.regex) {
						continue
					}
					t.Errorf("%s: contains deleted symbol %q — step 11 refactor removed it",
						relPath(root, path), p.name)
				}
			}
			return nil
		})
		require.NoError(t, err, "walk %s", dir)
	}
}

// allMatchesAreCommentMentions returns true if every match of re is on
// a line that starts with `//` or contains " // " (Go single-line
// comment or trailing comment). Multi-line /* */ comments are rare
// enough in this codebase to ignore.
func allMatchesAreCommentMentions(body string, re *regexp.Regexp) bool {
	for line := range strings.SplitSeq(body, "\n") {
		if !re.MatchString(line) {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "//") && !strings.Contains(line, "// ") {
			return false // at least one live-code match
		}
	}
	return true
}

func relPath(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return rel
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// Walk up from the test's cwd looking for go.mod.
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "walked to filesystem root without finding go.mod")
		dir = parent
	}
}

// TestGrepGuard_NoOldK6Import: the pkg/executor/k6 subpackage is gone.
// Any lingering import path referring to it means someone missed the
// move to internal/scraper/perf/k6.
func TestGrepGuard_NoOldK6Import(t *testing.T) {
	root := repoRoot(t)
	oldImport := "kubetest-alt/pkg/executor/k6"
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, ".old") ||
			strings.HasSuffix(path, "_test.go") || // grep_guard_test cites the string itself
			strings.Contains(path, "/.git/") ||
			strings.Contains(path, "/bin/") {
			return nil
		}
		// #nosec G304 G122 -- walking known source tree, test-only.
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), oldImport) {
			t.Errorf("%s: still imports the retired pkg/executor/k6 subpackage",
				relPath(root, path))
		}
		return nil
	})
	assert.NoError(t, err)
}
