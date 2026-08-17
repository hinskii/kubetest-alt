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

package fetcher

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPtr(v int32) *int32 { return &v }

func TestWriteFiles_HappyPath(t *testing.T) {
	dir := t.TempDir()
	files := []FileContent{
		{Path: "flat.txt", Content: "hello"},
		{Path: "nested/deep/file.txt", Content: "nested"},
	}
	require.NoError(t, writeFiles(dir, files, nil))

	// #nosec G304 -- test path.
	b, err := os.ReadFile(filepath.Join(dir, "flat.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hello", string(b))

	// #nosec G304 -- test path.
	b2, err := os.ReadFile(filepath.Join(dir, "nested/deep/file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "nested", string(b2))
}

func TestWriteFiles_ExecutableMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits don't map cleanly on Windows")
	}
	dir := t.TempDir()
	files := []FileContent{{Path: "run.sh", Content: "#!/bin/sh\necho hi\n", Mode: intPtr(0o755)}}
	require.NoError(t, writeFiles(dir, files, nil))

	info, err := os.Stat(filepath.Join(dir, "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
}

func TestWriteFiles_ContentFromEnv(t *testing.T) {
	env := func(k string) (string, bool) {
		if k == "SECRET_PAYLOAD" {
			return "from-env-value", true
		}
		return "", false
	}
	dir := t.TempDir()
	files := []FileContent{{Path: "cfg.txt", ContentFrom: "SECRET_PAYLOAD"}}
	require.NoError(t, writeFiles(dir, files, env))
	// #nosec G304 -- test path.
	b, err := os.ReadFile(filepath.Join(dir, "cfg.txt"))
	require.NoError(t, err)
	assert.Equal(t, "from-env-value", string(b))
}

func TestWriteFiles_ContentFromMissingEnv(t *testing.T) {
	env := func(_ string) (string, bool) { return "", false }
	err := writeFiles(t.TempDir(), []FileContent{{Path: "x", ContentFrom: "MISSING"}}, env)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MISSING")
}

// TestWriteFiles_RejectsPathTraversal is the security invariant: a Test spec
// with Path: "../etc/evil" MUST NOT escape dstDir. Guards against a compromised
// or malicious CR reaching into the container's other mounts.
func TestWriteFiles_RejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	cases := []string{
		"../etc/evil",
		"nested/../../outside",
		"a/b/../../../../../../etc/passwd",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			err := writeFiles(dir, []FileContent{{Path: path, Content: "x"}}, nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "escape")
		})
	}
}

func TestWriteFiles_RejectsAbsolutePath(t *testing.T) {
	err := writeFiles(t.TempDir(), []FileContent{{Path: "/etc/passwd", Content: "x"}}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}

func TestWriteFiles_EmptyPath(t *testing.T) {
	err := writeFiles(t.TempDir(), []FileContent{{Path: "", Content: "x"}}, nil)
	require.Error(t, err)
}

// TestWriteFiles_AggregateSize_512KB is the step-03 NOTE / step-06 E2E
// requirement at the fetcher layer: inline files summing near the 512KB
// webhook cap must materialize correctly.
func TestWriteFiles_AggregateSize_512KB(t *testing.T) {
	dir := t.TempDir()
	// Two files each ~250KB → 500KB aggregate, comfortably under the 512KB webhook cap.
	big := make([]byte, 250*1024)
	for i := range big {
		big[i] = byte('a' + i%26)
	}
	files := []FileContent{
		{Path: "a.txt", Content: string(big)},
		{Path: "b.txt", Content: string(big)},
	}
	require.NoError(t, writeFiles(dir, files, nil))
	// #nosec G304 -- test path.
	got, err := os.ReadFile(filepath.Join(dir, "b.txt"))
	require.NoError(t, err)
	assert.Equal(t, len(big), len(got))
}

func TestIsUnder(t *testing.T) {
	cases := []struct {
		target, base string
		want         bool
	}{
		{"/data/x", "/data", true},
		{"/data", "/data", true},
		{"/data-evil/x", "/data", false},
		{"/etc/passwd", "/data", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, isUnder(tc.target, tc.base), "isUnder(%q,%q)", tc.target, tc.base)
	}
}
