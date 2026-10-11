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
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSafeURL_NeverShowsCredentials(t *testing.T) {
	assert.Equal(t, "https://github.com/org/repo.git", safeURL("https://user:s3cret@github.com/org/repo.git"))
	assert.Equal(t, "https://github.com/org/repo.git", safeURL("https://github.com/org/repo.git"))
	assert.Equal(t, "git@github.com:org/repo.git", safeURL("git@github.com:org/repo.git"))
}

func TestAuthOf_NamesTheFlowNotTheSecret(t *testing.T) {
	env := func(m map[string]string) EnvLookup {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	assert.Equal(t, "public, no credentials", authOf(GitContent{}, env(nil)))
	assert.Equal(t, "token from a Secret", authOf(GitContent{}, env(map[string]string{EnvGitToken: "t"})))
	assert.Equal(t, "username and token from a Secret",
		authOf(GitContent{}, env(map[string]string{EnvGitToken: "t", EnvGitUsername: "u"})))
	assert.Equal(t, "SSH key from a Secret", authOf(GitContent{AuthType: AuthTypeSSH}, env(nil)))
	assert.Equal(t, "token from a Secret, as a header", authOf(GitContent{AuthType: AuthTypeHeader}, env(nil)))
}

func TestFetchLog_InlineFiles(t *testing.T) {
	var out bytes.Buffer
	f := NewFetcher()
	f.Stdout = &out
	dst := t.TempDir()
	require.NoError(t, f.Fetch(context.Background(), Content{Files: []FileContent{
		{Path: "load.js", Content: "export default function () {}"},
		{Path: "data/users.csv", Content: "id\n1\n"},
	}}, dst))
	log := out.String()
	assert.Contains(t, log, "Wrote 2 inline files into "+dst+": load.js (29 B), data/users.csv (5 B)")
	assert.Contains(t, log, "Content ready in ")
	_, err := os.Stat(filepath.Join(dst, "data", "users.csv"))
	require.NoError(t, err)
}

func TestTallyAndBytes(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.js"), []byte("12345"), 0o600))
	n, size := tally(dir)
	assert.Equal(t, 1, n, ".git left out")
	assert.Equal(t, int64(5), size)
	assert.Equal(t, "2 files", plural(2, "file"))
	assert.Equal(t, "1 file", plural(1, "file"))
	assert.Equal(t, "an unknown commit", shortCommit(""))
	assert.Equal(t, "0123456", shortCommit("0123456789"))
}
