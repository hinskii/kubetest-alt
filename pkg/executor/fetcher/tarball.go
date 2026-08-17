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
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// HTTPClient is exported so tests can inject a client pointing at httptest.
// Zero value uses http.DefaultClient.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// fetchTarballs downloads and unpacks every tarball into dstDir/<t.Path>.
// Each unpack is bounded by the parent ctx (which itself is bounded by the
// fetcher-level timeout).
func fetchTarballs(ctx context.Context, dstDir string, tarballs []Tarball, httpc HTTPClient) error {
	if httpc == nil {
		httpc = &http.Client{Timeout: 60 * time.Second}
	}
	dstAbs, err := filepath.Abs(dstDir)
	if err != nil {
		return fmt.Errorf("resolve dst: %w", err)
	}
	for _, t := range tarballs {
		if err := fetchOneTarball(ctx, dstAbs, t, httpc); err != nil {
			return fmt.Errorf("tarball %s: %w", t.URL, err)
		}
	}
	return nil
}

func fetchOneTarball(ctx context.Context, dstAbs string, t Tarball, httpc HTTPClient) error {
	// Path validation BEFORE the HTTP request — a hostile spec shouldn't get
	// to make our pod initiate network traffic to attacker-chosen origins
	// under the guise of a "test payload" if the target is bogus anyway.
	sub := filepath.Join(dstAbs, t.Path)
	if !isUnder(sub, dstAbs) {
		return fmt.Errorf("tarball path %q escapes datadir", t.Path)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL, nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	// #nosec G301 -- shared init→wrapper emptyDir; both containers may run
	// as different UIDs, world-read+traversal is required for the wrapper
	// to see files the init container wrote.
	if err := os.MkdirAll(sub, 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return extractTarGz(resp.Body, sub)
}

// extractTarGz reads a gzipped tar from r and writes entries under root.
// The zip-slip guard rejects any entry whose cleaned target escapes root
// (mandatory security test — plan step-06).
//
// Symlinks are stored but rejected if their target would escape root, either
// as an absolute path or via ../ traversal.
func extractTarGz(r io.Reader, root string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve root: %w", err)
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}
		if err := extractOneEntry(tr, hdr, rootAbs); err != nil {
			return err
		}
	}
}

func extractOneEntry(tr *tar.Reader, hdr *tar.Header, rootAbs string) error {
	// Zip-slip guard #1: reject absolute names outright.
	if filepath.IsAbs(hdr.Name) {
		return fmt.Errorf("zip-slip: absolute entry %q", hdr.Name)
	}
	// Zip-slip guard #2: after cleaning, entry must land inside root.
	// #nosec G305 -- validated on the next line via isUnder; gosec's static
	// analysis doesn't see that guard.
	target := filepath.Join(rootAbs, hdr.Name)
	if !isUnder(target, rootAbs) {
		return fmt.Errorf("zip-slip: entry %q escapes root", hdr.Name)
	}

	switch hdr.Typeflag {
	case tar.TypeDir:
		// #nosec G115 -- Mode is 12 bits at most; bits above 0o777 masked off.
		return os.MkdirAll(target, os.FileMode(hdr.Mode)&0o777) //nolint:gosec // mode from archive

	case tar.TypeReg:
		// #nosec G301 -- see the sibling comment in fetchOneTarball.
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("mkdir parent: %w", err)
		}
		// #nosec G115,G304,G305 -- G115: Mode is 12 bits at most, mask makes the
		// cast safe; G304+G305: target passed the isUnder zip-slip check above.
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
		if err != nil {
			return fmt.Errorf("open %s: %w", target, err)
		}
		// #nosec G110 -- tarball size is bounded by fetch timeout + HTTP client limits.
		if _, err := io.Copy(f, tr); err != nil {
			_ = f.Close()
			return fmt.Errorf("write %s: %w", target, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close %s: %w", target, err)
		}
		return nil

	case tar.TypeSymlink, tar.TypeLink:
		// Symlink target must also stay inside root, whether it's absolute
		// or relative — otherwise the wrapper container could follow the
		// link out of /data.
		linkTarget := hdr.Linkname
		if !filepath.IsAbs(linkTarget) {
			// #nosec G305 -- validated on the next line via isUnder.
			linkTarget = filepath.Join(filepath.Dir(target), linkTarget)
		}
		if !isUnder(linkTarget, rootAbs) {
			return fmt.Errorf("zip-slip: symlink %q -> %q escapes root", hdr.Name, hdr.Linkname)
		}
		// #nosec G301 -- see the sibling comment in fetchOneTarball.
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("mkdir parent: %w", err)
		}
		if err := os.Symlink(hdr.Linkname, target); err != nil {
			return fmt.Errorf("symlink %s: %w", target, err)
		}
		return nil

	default:
		// Ignore unusual types (fifo, char/block dev) — not something a test
		// payload should contain.
		return nil
	}
}
