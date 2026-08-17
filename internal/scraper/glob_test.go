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

package scraper

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// touch creates a file with a trivial payload for glob tests.
func touch(t *testing.T, dir, path string) {
	t.Helper()
	full := filepath.Join(dir, path)
	// #nosec G301 -- test scratch dir; dir is t.TempDir()-rooted.
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	// #nosec G306 -- test fixture; perms harmless in t.TempDir().
	require.NoError(t, os.WriteFile(full, []byte("x"), 0o644))
}

func TestExpandGlobs_Empty(t *testing.T) {
	matches, err := ExpandGlobs(t.TempDir(), nil)
	require.NoError(t, err)
	assert.Nil(t, matches, "no patterns → nil matches, no error")
}

func TestExpandGlobs_NoMatches(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "results/summary.json")

	// Pattern doesn't match anything → empty, NOT error.
	matches, err := ExpandGlobs(dir, []string{"never/**/*.xml"})
	require.NoError(t, err)
	assert.Empty(t, matches)
}

func TestExpandGlobs_RecursiveXML(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "a.xml")
	touch(t, dir, "sub/b.xml")
	touch(t, dir, "sub/deep/c.xml")
	touch(t, dir, "other.json")

	matches, err := ExpandGlobs(dir, []string{"**/*.xml"})
	require.NoError(t, err)

	rels := make([]string, 0, len(matches))
	for _, m := range matches {
		rels = append(rels, m.RelPath)
	}
	assert.Equal(t, []string{"a.xml", "sub/b.xml", "sub/deep/c.xml"}, rels,
		"deterministic sorted order + no non-xml")
}

func TestExpandGlobs_SubdirRecursive(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "out/one.txt")
	touch(t, dir, "out/two.txt")
	touch(t, dir, "out/nested/three.txt")
	touch(t, dir, "outside/nope.txt")

	matches, err := ExpandGlobs(dir, []string{"out/**"})
	require.NoError(t, err)

	rels := make([]string, 0, len(matches))
	for _, m := range matches {
		rels = append(rels, m.RelPath)
	}
	assert.ElementsMatch(t, []string{"out/one.txt", "out/two.txt", "out/nested/three.txt"}, rels)
}

func TestExpandGlobs_MultiplePatternsDedupe(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "results/one.xml")
	touch(t, dir, "results/two.json")

	// Both patterns match one.xml — must appear ONCE in the result.
	matches, err := ExpandGlobs(dir, []string{"**/*.xml", "results/one.xml"})
	require.NoError(t, err)
	assert.Len(t, matches, 1, "same file matched by two patterns → deduped")
	assert.Equal(t, "results/one.xml", matches[0].RelPath)
}

func TestExpandGlobs_AbsolutePathRejected(t *testing.T) {
	_, err := ExpandGlobs(t.TempDir(), []string{"/etc/passwd"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}

// TestExpandGlobs_DirectoriesSkipped: a match on `**` includes directories
// too — the scraper only uploads files.
func TestExpandGlobs_DirectoriesSkipped(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "results/a.txt")
	// results/ directory is also matched by "**"
	matches, err := ExpandGlobs(dir, []string{"**"})
	require.NoError(t, err)
	for _, m := range matches {
		info, err := os.Stat(m.AbsPath)
		require.NoError(t, err)
		assert.False(t, info.IsDir(), "dir slipped through: %s", m.RelPath)
	}
}

// TestExpandGlobs_CapWithWarning: >MaxMatchedFiles → ErrTooManyMatches
// (non-fatal), returns exactly the cap. Uses a tighter cap via test helper.
func TestExpandGlobs_CapWithWarning(t *testing.T) {
	dir := t.TempDir()
	// Create MaxMatchedFiles+50 files. Small (1 byte each) so this stays fast.
	// Trim if the constant ever bumps to something huge — but 10k * 1 byte = 10KB.
	total := MaxMatchedFiles + 50
	for i := range total {
		touch(t, dir, filepath.Join("bulk", "f"+itoa(i)+".txt"))
	}
	matches, err := ExpandGlobs(dir, []string{"bulk/*.txt"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTooManyMatches))
	assert.Len(t, matches, MaxMatchedFiles, "cap enforced exactly")
}

// itoa is a tiny helper to avoid pulling in strconv for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := "0123456789"
	buf := make([]byte, 0, 6)
	for n > 0 {
		buf = append(buf, digits[n%10])
		n /= 10
	}
	// reverse
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
