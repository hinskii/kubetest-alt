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
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/bmatcuk/doublestar/v4"
)

// MaxMatchedFiles caps how many files a single Scrape considers, even across
// multiple globs. A runaway pattern (`**/*` in a huge repo) would otherwise
// spend forever globbing. If the cap trips, we log a warning and process the
// first MaxMatchedFiles matches — the run doesn't fail.
const MaxMatchedFiles = 10000

// GlobMatch describes one file that matched an artifacts.paths pattern.
// RelPath is what becomes the object-store key suffix (<runID>/<RelPath>).
type GlobMatch struct {
	// AbsPath is the fully-qualified path on the wrapper's filesystem.
	AbsPath string
	// RelPath is AbsPath relative to the working directory the wrapper ran
	// the tool in — this is what the operator UI shows to humans and what
	// forms the object-store key.
	RelPath string
}

// ExpandGlobs walks the workingDir once per pattern (doublestar handles
// recursive `**`) and returns the matched files, deduped and capped.
//
// Rules:
//   - Empty patterns → empty result (not error). Test wraps this expectation:
//     no artifacts.paths in the CRD means "don't scrape".
//   - Absolute patterns rejected. All glob roots stay under workingDir so a
//     malicious spec can't scrape /etc/passwd out of the wrapper container.
//   - Missing files (glob matched nothing) are silently ignored — a Test that
//     lists both "results/**/*.json" and "logs/*.txt" and only produces the
//     former shouldn't fail the run.
//   - Directories are skipped (only files upload); their names DO appear in
//     doublestar matches for `**` patterns but we filter with os.Stat.
//   - Symlinks are followed one level and captured as normal files (relpath
//     is the SYMLINK path, so replay of an object dump matches the layout
//     humans expect).
//
// Returns a distinct error type ErrTooManyMatches when the cap trips, so
// callers can surface a specific "results capped" message without special-casing.
func ExpandGlobs(workingDir string, patterns []string) ([]GlobMatch, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	workingDir = filepath.Clean(workingDir)
	seen := make(map[string]struct{})
	out := make([]GlobMatch, 0)

	for _, pat := range patterns {
		if filepath.IsAbs(pat) {
			return nil, fmt.Errorf("scraper: absolute glob pattern %q rejected", pat)
		}
		// doublestar operates on a filesystem via fs.FS. Root at workingDir
		// so patterns are always relative to it.
		fsys := os.DirFS(workingDir)
		matches, err := doublestar.Glob(fsys, pat)
		if err != nil {
			return nil, fmt.Errorf("scraper: glob %q: %w", pat, err)
		}
		for _, m := range matches {
			abs := filepath.Join(workingDir, m)
			if _, dup := seen[abs]; dup {
				continue
			}
			info, err := os.Stat(abs)
			if err != nil {
				continue
			}
			if info.IsDir() {
				continue
			}
			seen[abs] = struct{}{}
			out = append(out, GlobMatch{AbsPath: abs, RelPath: m})
			if len(out) >= MaxMatchedFiles {
				return capMatches(out), ErrTooManyMatches
			}
		}
	}
	// Sort for determinism — object-store upload order matters for test
	// assertions and human-scanning of the resulting bucket listing.
	slices.SortFunc(out, func(a, b GlobMatch) int {
		if a.RelPath < b.RelPath {
			return -1
		}
		if a.RelPath > b.RelPath {
			return 1
		}
		return 0
	})
	return out, nil
}

// ErrTooManyMatches signals that ExpandGlobs hit MaxMatchedFiles. Non-fatal:
// callers still process the returned matches (which will be exactly the cap).
var ErrTooManyMatches = errors.New("scraper: matched files capped at MaxMatchedFiles")

func capMatches(m []GlobMatch) []GlobMatch {
	slices.SortFunc(m, func(a, b GlobMatch) int {
		if a.RelPath < b.RelPath {
			return -1
		}
		if a.RelPath > b.RelPath {
			return 1
		}
		return 0
	})
	return m
}
