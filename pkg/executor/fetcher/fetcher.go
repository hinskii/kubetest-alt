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
	"context"
	"io"
	"io/fs"
	"os"
)

// Fetcher orchestrates the three fetch modes: git → files → tarballs.
// Each field is exported so cmd/entry can compose a production instance and
// tests can inject fakes for the underlying resources.
type Fetcher struct {
	// Git is the injectable git cloner. NewFetcher wires the real one.
	Git *gitCloner

	// HTTP client for tarball downloads. nil = http.DefaultClient with a
	// 60s per-request timeout.
	HTTP HTTPClient

	// EnvLookup abstracts os.LookupEnv (for inline files' ContentFrom
	// resolution). nil = os.LookupEnv.
	EnvLookup EnvLookup

	// Stdout/Stderr are inherited by git subcommand invocations. Tests set
	// them to bytes.Buffer to assert on git's output. nil = os.Stdout/os.Stderr.
	Stdout io.Writer
	Stderr io.Writer
}

// NewFetcher wires the production fetcher: real git via exec.CommandContext,
// os process env, http.DefaultClient. Callers may still override any field.
func NewFetcher() *Fetcher {
	return &Fetcher{
		Git:       newGitCloner(),
		EnvLookup: os.LookupEnv,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
	}
}

// Fetch materializes the Content spec into dstDir. Order:
//  1. git (may populate a large subtree; runs first so files can overlay it)
//  2. inline files (idempotent, no network)
//  3. tarballs (extracted; may add many files, run last so failure doesn't
//     block the cheaper stages)
//
// Returns first error encountered (does NOT best-effort continue) — partial
// content in dstDir is worse than an early exit because the wrapper container
// downstream would see an incomplete workspace and produce a misleading
// failure verdict.
//
// ctx cancellation kills any in-flight git subcommand (via exec.CommandContext)
// and aborts HTTP downloads.
func (f *Fetcher) Fetch(ctx context.Context, c Content, dstDir string) error {
	// #nosec G301 -- shared init→wrapper emptyDir permissions rationale in files.go.
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}

	// Wire the shared writers/env down into the git cloner so tests see the
	// same output stream.
	if f.Stdout != nil {
		f.Git.Stdout = f.Stdout
	}
	if f.Stderr != nil {
		f.Git.Stderr = f.Stderr
	}
	if f.EnvLookup != nil {
		f.Git.EnvLookup = f.EnvLookup
	}

	if c.Git != nil {
		if err := f.Git.clone(ctx, *c.Git, dstDir); err != nil {
			return err
		}
	}
	if len(c.Files) > 0 {
		if err := writeFiles(dstDir, c.Files, f.EnvLookup); err != nil {
			return err
		}
	}
	if len(c.Tarball) > 0 {
		if err := fetchTarballs(ctx, dstDir, c.Tarball, f.HTTP); err != nil {
			return err
		}
	}
	return shareWithAnyUID(dstDir)
}

// shareWithAnyUID makes the fetched tree writable by whatever user the
// tool image runs as. The fetcher runs as root; vendor tool images often
// don't (grafana/k6 is uid 12345), and with the fetcher's 0755 dirs such a
// tool could not write its reports — k6 then silently skipped
// --summary-export and artillery failed. dstDir is the pod's private
// emptyDir, so "any user" means "any container of this one pod".
// Directories become 0777; files gain read+write for all, keeping their
// execute bits. Symlinks are left alone, and every chmod goes through an
// os.Root scoped to dstDir, so a symlink planted by a tarball can never
// redirect it outside the volume.
func shareWithAnyUID(dstDir string) error {
	root, err := os.OpenRoot(dstDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			return nil
		case d.IsDir():
			// #nosec G302 -- pod-private emptyDir handed from root fetcher to non-root tool.
			return root.Chmod(p, 0o777)
		default:
			info, err := d.Info()
			if err != nil {
				return err
			}
			mode := info.Mode().Perm() | 0o666
			if info.Mode().Perm()&0o111 != 0 {
				mode |= 0o111
			}
			return root.Chmod(p, mode)
		}
	})
}
