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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildTarGz assembles a tar.gz in memory from a list of entries. Tests use
// this to construct hostile archives without touching the filesystem.
type tarEntry struct {
	Name     string
	Content  string
	Linkname string      // set for symlinks
	Type     byte        // tar.TypeReg/TypeSymlink/etc.
	Mode     os.FileMode // default 0o644
}

func buildTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.Type
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.Mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{
			Name:     e.Name,
			Mode:     int64(mode),
			Size:     int64(len(e.Content)),
			Typeflag: typ,
			Linkname: e.Linkname,
		}
		require.NoError(t, tw.WriteHeader(hdr))
		if typ == tar.TypeReg && e.Content != "" {
			_, err := tw.Write([]byte(e.Content))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func TestExtractTarGz_HappyPath(t *testing.T) {
	dir := t.TempDir()
	tgz := buildTarGz(t, []tarEntry{
		{Name: "hello.txt", Content: "world"},
		{Name: "nested/file.txt", Content: "deep"},
	})
	require.NoError(t, extractTarGz(bytes.NewReader(tgz), dir))

	// #nosec G304 -- test path.
	b, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
	require.NoError(t, err)
	assert.Equal(t, "world", string(b))
}

// TestExtractTarGz_ZipSlip_MANDATORY is the security test the plan calls out
// explicitly: an entry with `..` traversal MUST be rejected. Regression here
// = arbitrary write outside /data (into e.g. /etc/hosts of the pod).
func TestExtractTarGz_ZipSlip_MANDATORY(t *testing.T) {
	cases := []struct {
		name    string
		entries []tarEntry
	}{
		{
			name: "relative traversal",
			entries: []tarEntry{
				{Name: "../../etc/evil", Content: "pwned"},
			},
		},
		{
			name: "buried traversal",
			entries: []tarEntry{
				{Name: "a/b/../../../../etc/passwd", Content: "pwned"},
			},
		},
		{
			name: "absolute path",
			entries: []tarEntry{
				{Name: "/etc/evil", Content: "pwned"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tgz := buildTarGz(t, tc.entries)
			err := extractTarGz(bytes.NewReader(tgz), dir)
			require.Error(t, err, "zip-slip archive MUST be rejected")
			assert.Contains(t, err.Error(), "zip-slip")

			// Sanity: the target didn't actually land outside dir.
			// (Even without this check, a passing err assertion above is
			// sufficient — but this is a mandatory-security test.)
			outside, _ := filepath.Abs(filepath.Join(dir, "..", "etc", "evil"))
			_, statErr := os.Stat(outside)
			assert.True(t, os.IsNotExist(statErr), "escape target should not exist")
		})
	}
}

// TestExtractTarGz_SymlinkEscape_Rejected mirrors the zip-slip guard for
// symlinks: an entry that IS a symlink whose target escapes root must be
// rejected. Otherwise a tarball could plant a symlink /data/link → /etc/passwd
// and the wrapper container would read /etc/passwd through it.
func TestExtractTarGz_SymlinkEscape_Rejected(t *testing.T) {
	cases := []struct {
		name string
		link string
	}{
		{"absolute symlink", "/etc/passwd"},
		{"relative symlink escape", "../../etc/passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tgz := buildTarGz(t, []tarEntry{
				{Name: "link", Type: tar.TypeSymlink, Linkname: tc.link},
			})
			err := extractTarGz(bytes.NewReader(tgz), t.TempDir())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "zip-slip")
		})
	}
}

func TestExtractTarGz_MalformedGzip(t *testing.T) {
	err := extractTarGz(bytes.NewReader([]byte("not gzip")), t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gzip")
}

func TestFetchTarballs_HTTPHappyPath(t *testing.T) {
	tgz := buildTarGz(t, []tarEntry{{Name: "hello.txt", Content: "world"}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(tgz)
	}))
	defer srv.Close()

	dir := t.TempDir()
	err := fetchTarballs(context.Background(), dir, []Tarball{{URL: srv.URL}}, srv.Client())
	require.NoError(t, err)
	// #nosec G304 -- test path.
	b, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
	require.NoError(t, err)
	assert.Equal(t, "world", string(b))
}

func TestFetchTarballs_HTTPErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	err := fetchTarballs(context.Background(), t.TempDir(), []Tarball{{URL: srv.URL}}, srv.Client())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestFetchTarballs_TarballPathEscapesRejected(t *testing.T) {
	err := fetchTarballs(context.Background(), t.TempDir(),
		[]Tarball{{URL: "http://example.com/x.tgz", Path: "../../evil"}}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escape")
}
