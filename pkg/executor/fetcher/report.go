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
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// The fetcher says what it does, step by step: its output is the run's
// "fetch" stage log in Control Center (step 20a). Never a secret: URLs
// lose their user info, auth is named, not shown.

// say writes one line of the fetch log.
func (f *Fetcher) say(format string, args ...any) {
	w := f.Stdout
	if w == nil {
		w = io.Discard
	}
	_, _ = fmt.Fprintf(w, format+"\n", args...)
}

// safeURL is u without user info (https://user:token@host → https://host).
func safeURL(u string) string {
	p, err := url.Parse(u)
	if err != nil || p.User == nil {
		return u
	}
	p.User = nil
	return p.String()
}

// authOf names how the clone authenticates.
func authOf(g GitContent, lookup EnvLookup) string {
	has := func(k string) bool {
		if lookup == nil {
			return false
		}
		v, ok := lookup(k)
		return ok && v != ""
	}
	switch g.AuthType {
	case AuthTypeSSH:
		return "SSH key from a Secret"
	case AuthTypeHeader:
		return "token from a Secret, as a header"
	}
	if has(EnvGitToken) {
		if has(EnvGitUsername) {
			return "username and token from a Secret"
		}
		return "token from a Secret"
	}
	return "public, no credentials"
}

// tally counts the files under dir and their bytes, leaving out .git.
func tally(dir string) (files int, size int64) {
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			files++
			size += info.Size()
		}
		return nil
	})
	return files, size
}

// bytesOf is n bytes for people: 14 B, 1.2 kB, 3.4 MB.
func bytesOf(n int64) string {
	const k = 1000
	switch {
	case n < k:
		return fmt.Sprintf("%d B", n)
	case n < k*k:
		return fmt.Sprintf("%.1f kB", float64(n)/k)
	case n < k*k*k:
		return fmt.Sprintf("%.1f MB", float64(n)/(k*k))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(k*k*k))
}

// plural is "1 file" / "2 files".
func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %ss", n, one)
}

// since is how long ago start was, rounded for people.
func since(start time.Time) string {
	d := time.Since(start)
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return d.Round(100 * time.Millisecond).String()
}

// listFiles is "a.js (1.2 kB), data/users.csv (14 B)", at most max names.
func listFiles(files []FileContent, sizes []int64, max int) string {
	parts := make([]string, 0, min(len(files), max))
	for i, f := range files {
		if i == max {
			parts = append(parts, fmt.Sprintf("and %d more", len(files)-max))
			break
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", f.Path, bytesOf(sizes[i])))
	}
	return strings.Join(parts, ", ")
}

// shortCommit is a commit's 7-character form ("" stays "an unknown commit").
func shortCommit(sha string) string {
	if len(sha) < 7 {
		return "an unknown commit"
	}
	return sha[:7]
}
