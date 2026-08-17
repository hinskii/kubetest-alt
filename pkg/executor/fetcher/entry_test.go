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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeContentFile writes a Content JSON to a temp file and returns its path.
// Mimics what the operator projects at /etc/kubetest/content.json.
func makeContentFile(t *testing.T, c Content) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "content.json")
	b, err := json.Marshal(c)
	require.NoError(t, err)
	// #nosec G306 -- test fixture.
	require.NoError(t, os.WriteFile(path, b, 0o644))
	return path
}

func TestRunEntry_HappyPath(t *testing.T) {
	dataDir := t.TempDir()
	contentPath := makeContentFile(t, Content{
		Files: []FileContent{{Path: "hi.txt", Content: "hello"}},
	})

	var stdout bytes.Buffer
	err := RunEntry(context.Background(), EntryConfig{
		ContentPath:    contentPath,
		DataDir:        dataDir,
		TimeoutSeconds: 30,
		Stdout:         &stdout,
		Stderr:         &bytes.Buffer{},
	})
	require.NoError(t, err)
	assert.NotContains(t, stdout.String(), FetchErrorPrefix,
		"success path must not print FETCH_ERROR")

	// #nosec G304 -- test path.
	b, err := os.ReadFile(filepath.Join(dataDir, "hi.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hello", string(b))
}

// TestRunEntry_MissingContentFile: init container starts before content.json
// is mounted for any reason → clear FETCH_ERROR, non-zero exit.
func TestRunEntry_MissingContentFile(t *testing.T) {
	var stdout bytes.Buffer
	err := RunEntry(context.Background(), EntryConfig{
		ContentPath:    filepath.Join(t.TempDir(), "does-not-exist.json"),
		DataDir:        t.TempDir(),
		TimeoutSeconds: 30,
		Stdout:         &stdout,
		Stderr:         &bytes.Buffer{},
	})
	require.Error(t, err)
	assert.Contains(t, stdout.String(), FetchErrorPrefix)
	assert.Contains(t, stdout.String(), "does-not-exist.json")
}

// TestRunEntry_MalformedContentJSON: garbage content.json → FETCH_ERROR.
func TestRunEntry_MalformedContentJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "content.json")
	// #nosec G306 -- test fixture.
	require.NoError(t, os.WriteFile(path, []byte("{ not json"), 0o644))

	var stdout bytes.Buffer
	err := RunEntry(context.Background(), EntryConfig{
		ContentPath:    path,
		DataDir:        t.TempDir(),
		TimeoutSeconds: 30,
		Stdout:         &stdout,
		Stderr:         &bytes.Buffer{},
	})
	require.Error(t, err)
	assert.Contains(t, stdout.String(), FetchErrorPrefix)
	assert.Contains(t, stdout.String(), "parse json")
}

// TestRunEntry_FetchErrorPropagates: a path-traversing file spec makes Fetch
// fail; the error propagates through RunEntry as FETCH_ERROR.
func TestRunEntry_FetchErrorPropagates(t *testing.T) {
	contentPath := makeContentFile(t, Content{
		Files: []FileContent{{Path: "../etc/evil", Content: "pwn"}},
	})
	var stdout bytes.Buffer
	err := RunEntry(context.Background(), EntryConfig{
		ContentPath:    contentPath,
		DataDir:        t.TempDir(),
		TimeoutSeconds: 30,
		Stdout:         &stdout,
		Stderr:         &bytes.Buffer{},
	})
	require.Error(t, err)
	assert.Contains(t, stdout.String(), FetchErrorPrefix)
	assert.Contains(t, stdout.String(), "escape")
}

// TestRunEntry_FetchErrorFormat asserts the exact machine-readable format the
// controller (step 04 AnalyzePod → ReasonContentFetchFailed) matches on.
// Format is a contract; changing it needs a coordinated controller change.
func TestRunEntry_FetchErrorFormat(t *testing.T) {
	contentPath := makeContentFile(t, Content{
		Files: []FileContent{{Path: "../evil"}},
	})
	var stdout bytes.Buffer
	err := RunEntry(context.Background(), EntryConfig{
		ContentPath:    contentPath,
		DataDir:        t.TempDir(),
		TimeoutSeconds: 30,
		Stdout:         &stdout,
		Stderr:         &bytes.Buffer{},
	})
	require.Error(t, err)

	// Last non-empty line must start with the exact prefix.
	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	require.NotEmpty(t, lines)
	last := lines[len(lines)-1]
	assert.True(t, strings.HasPrefix(last, FetchErrorPrefix+" "),
		"last stdout line must start with %q, got %q", FetchErrorPrefix+" ", last)
}

// TestRunEntry_WritesTerminationMessage: FETCH_ERROR gets written to
// /dev/termination-log (path injected in tests). k8s reads this into
// Terminated.Message, which the controller's AnalyzePod surfaces.
func TestRunEntry_WritesTerminationMessage(t *testing.T) {
	tlog := filepath.Join(t.TempDir(), "termlog")
	contentPath := makeContentFile(t, Content{
		Files: []FileContent{{Path: "../evil"}},
	})
	err := RunEntry(context.Background(), EntryConfig{
		ContentPath:            contentPath,
		DataDir:                t.TempDir(),
		TimeoutSeconds:         30,
		TerminationMessagePath: tlog,
		Stdout:                 &bytes.Buffer{},
		Stderr:                 &bytes.Buffer{},
	})
	require.Error(t, err)

	// #nosec G304 -- test path.
	b, err := os.ReadFile(tlog)
	require.NoError(t, err)
	assert.Contains(t, string(b), FetchErrorPrefix)
}

// TestDefaultConfig_ReadsEnv: DefaultConfig picks up env overrides so the
// operator (or a debugging human) can raise the fetch timeout for a huge
// git repo without recompiling.
func TestDefaultConfig_ReadsEnv(t *testing.T) {
	t.Setenv("KUBETEST_DATADIR", "/custom/data")
	t.Setenv("KUBETEST_FETCH_TIMEOUT_SECONDS", "42")
	cfg := DefaultConfig()
	assert.Equal(t, "/custom/data", cfg.DataDir)
	assert.Equal(t, 42, cfg.TimeoutSeconds)
}
