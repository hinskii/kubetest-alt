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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFetch_FilesOnly is the base case: no git, no tarball, just inline files.
// Uses NewFetcher directly to exercise the orchestrator wiring.
func TestFetch_FilesOnly(t *testing.T) {
	dir := t.TempDir()
	f := NewFetcher()
	f.Stdout = &bytes.Buffer{}
	f.Stderr = &bytes.Buffer{}
	err := f.Fetch(context.Background(),
		Content{Files: []FileContent{{Path: "hi.txt", Content: "hello"}}},
		dir,
	)
	require.NoError(t, err)
	// #nosec G304 -- test path.
	b, err := os.ReadFile(filepath.Join(dir, "hi.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hello", string(b))
}

// TestFetch_FilesPlusTarball verifies orchestration order — tarball extracted
// AFTER files so a tarball entry can overlay an inline file if intentional.
func TestFetch_FilesPlusTarball(t *testing.T) {
	tgz := buildTarGz(t, []tarEntry{{Name: "from-tar.txt", Content: "tar wins"}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(tgz)
	}))
	defer srv.Close()

	dir := t.TempDir()
	f := NewFetcher()
	f.HTTP = srv.Client()
	f.Stdout = &bytes.Buffer{}
	f.Stderr = &bytes.Buffer{}
	err := f.Fetch(context.Background(),
		Content{
			Files:   []FileContent{{Path: "from-file.txt", Content: "file"}},
			Tarball: []Tarball{{URL: srv.URL}},
		},
		dir,
	)
	require.NoError(t, err)

	// #nosec G304 -- test path.
	fb, err := os.ReadFile(filepath.Join(dir, "from-file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "file", string(fb))
	// #nosec G304 -- test path.
	tb, err := os.ReadFile(filepath.Join(dir, "from-tar.txt"))
	require.NoError(t, err)
	assert.Equal(t, "tar wins", string(tb))
}

// TestFetch_FirstErrorAborts: filesystem-invalid content spec makes writeFiles
// fail; the tarball fetch that would follow MUST NOT run.
func TestFetch_FirstErrorAborts(t *testing.T) {
	// A sentinel HTTP server that will fail the test if hit — proving tarball
	// fetch didn't execute after the files step errored.
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	f := NewFetcher()
	f.HTTP = srv.Client()
	f.Stdout = &bytes.Buffer{}
	f.Stderr = &bytes.Buffer{}
	err := f.Fetch(context.Background(),
		Content{
			Files:   []FileContent{{Path: "../evil"}},
			Tarball: []Tarball{{URL: srv.URL}},
		},
		t.TempDir(),
	)
	require.Error(t, err)
	assert.False(t, hit, "tarball fetch must not run after files step errored")
}

// TestFetch_MkdirDstOnDemand: dstDir may not exist yet (init container
// mounts /data as emptyDir — dir exists, but nested paths might not).
func TestFetch_MkdirDstOnDemand(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "does-not-exist-yet")
	f := NewFetcher()
	f.Stdout = &bytes.Buffer{}
	f.Stderr = &bytes.Buffer{}
	err := f.Fetch(context.Background(),
		Content{Files: []FileContent{{Path: "x", Content: "y"}}},
		dst,
	)
	require.NoError(t, err)
}
