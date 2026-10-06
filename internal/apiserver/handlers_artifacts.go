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

package apiserver

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/hinskii/kubetest-alt/pkg/storage"
)

// artifactEntry is one row of GET /runs/{id}/artifacts.
type artifactEntry struct {
	Path        string `json:"path"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
	ContentType string `json:"contentType,omitempty"`
}

// runArtifacts returns the artifact list recorded on the run (CR status or
// archived row). Order is the scraper's upload order.
func runArtifacts(ref runRef) []artifactEntry {
	var out []artifactEntry
	switch {
	case ref.CR != nil:
		for _, a := range ref.CR.Status.ArtifactRefs {
			out = append(out, artifactEntry{Path: a.Path, SizeBytes: a.SizeBytes, ContentType: a.ContentType})
		}
	case ref.Row != nil:
		for _, a := range ref.Row.ArtifactRefs {
			out = append(out, artifactEntry{Path: a.Path, SizeBytes: a.SizeBytes, ContentType: a.ContentType})
		}
	}
	return out
}

// listRunArtifacts lists a run's artifacts. Uses the refs recorded on the
// run; when there are none (e.g. the wrapper died before writing
// result.json) it falls back to listing the run's artifacts/ prefix so
// partially scraped files are still reachable.
func (s *Server) listRunArtifacts(w http.ResponseWriter, r *http.Request) {
	ns, err := s.targetNamespace(r, "")
	if err != nil {
		writeLookupError(w, err)
		return
	}
	ref, err := s.findRun(r.Context(), ns, r.PathValue("id"))
	if err != nil {
		writeLookupError(w, err)
		return
	}
	out := runArtifacts(ref)
	if len(out) == 0 && s.Lister != nil && s.Bucket != "" {
		prefix := ref.keys().Artifacts()
		keys, err := s.Lister.List(r.Context(), s.Bucket, prefix)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ReasonInternal, fmt.Sprintf("list artifacts: %v", err))
			return
		}
		for _, k := range keys {
			out = append(out, artifactEntry{Path: strings.TrimPrefix(k, prefix)})
		}
	}
	if out == nil {
		out = []artifactEntry{}
	}
	writeJSON(w, http.StatusOK, out)
}

// getRunArtifact serves one artifact.
//
// Default: streams the bytes through the API server. Control Center runs
// on a different cluster and cannot reach MinIO, so a presigned URL is
// useless to it. ?download=1 sets Content-Disposition: attachment.
// ?presign=1: returns {url, expiresIn} for clients that can reach MinIO.
//
// The {path...} suffix becomes storage.RunKeys.Artifact(path) for the
// resolved run (namespace + UID). Path traversal is rejected before any
// lookup: absolute paths, "..", empty segments and backslashes → 400.
func (s *Server) getRunArtifact(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	rawPath := r.PathValue("path")
	if runID == "" || rawPath == "" {
		writeError(w, http.StatusBadRequest, ReasonBadRequest,
			"run id and artifact path are required")
		return
	}
	presign := r.URL.Query().Get("presign") == "1"
	if s.Bucket == "" || (presign && s.Presigner == nil) || (!presign && s.Downloader == nil) {
		writeError(w, http.StatusServiceUnavailable, ReasonServiceUnavail,
			"artifact storage is not configured")
		return
	}
	if err := validateArtifactPath(rawPath); err != nil {
		writeError(w, http.StatusBadRequest, ReasonBadRequest, err.Error())
		return
	}
	ns, err := s.targetNamespace(r, "")
	if err != nil {
		writeLookupError(w, err)
		return
	}
	ref, err := s.findRun(r.Context(), ns, runID)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	key := ref.keys().Artifact(rawPath)

	if presign {
		url, err := s.Presigner.PresignGetURL(r.Context(), s.Bucket, key, s.PresignedURLExpiry)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ReasonInternal,
				fmt.Sprintf("presign: %v", err))
			return
		}
		writeJSON(w, http.StatusOK, artifactURLResponse{
			URL:       url,
			ExpiresIn: int(s.PresignedURLExpiry.Seconds()),
		})
		return
	}

	rc, err := s.Downloader.Get(r.Context(), s.Bucket, key)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, ReasonNotFound,
				fmt.Sprintf("artifact %q not found for run %q", rawPath, ref.Name))
			return
		}
		writeError(w, http.StatusInternalServerError, ReasonInternal, fmt.Sprintf("get artifact: %v", err))
		return
	}
	defer func() { _ = rc.Close() }()

	h := w.Header()
	h.Set("Content-Type", artifactContentType(ref, rawPath))
	// Artifacts are produced by the tested workload — treat them as
	// untrusted: no MIME sniffing, and an HTML report rendered inline gets
	// an opaque origin (no cookies, no same-origin scripting) via sandbox.
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	h.Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, safeFilename(rawPath)))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

// artifactContentType prefers the type the scraper recorded, then the
// extension, then application/octet-stream.
func artifactContentType(ref runRef, relPath string) string {
	for _, a := range runArtifacts(ref) {
		if a.Path == relPath && a.ContentType != "" {
			return a.ContentType
		}
	}
	if ct := mime.TypeByExtension(path.Ext(relPath)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// safeFilename is the base name with characters that would break a quoted
// header parameter removed.
func safeFilename(relPath string) string {
	return strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' || r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, path.Base(relPath))
}

// artifactURLResponse is the wire shape returned by
// GET /runs/{id}/artifacts/{path}?presign=1.
type artifactURLResponse struct {
	URL       string `json:"url"`
	ExpiresIn int    `json:"expiresIn"`
}

// validateArtifactPath rejects path-traversal attempts. Applied to the
// user-supplied {path...} suffix BEFORE joining with runID — so an
// attacker can't escape the runID directory in the bucket.
//
// Rejected: absolute paths, ".." segments, empty segments (leading/
// trailing slash), backslash-based tricks. Accepted: normal relpaths like
// "results/junit.xml" or "logs/step-1/out.log".
func validateArtifactPath(p string) error {
	if p == "" {
		return fmt.Errorf("artifact path is empty")
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("artifact path must be relative (got %q)", p)
	}
	if strings.Contains(p, "\\") {
		return fmt.Errorf("artifact path must not contain backslashes")
	}
	// path.Clean normalizes but preserves ".." — we reject the presence
	// explicitly rather than trusting the join semantics.
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("artifact path contains an empty or traversal segment (got %q)", p)
		}
	}
	// Defense in depth: post-clean length must equal original path length
	// (both after any prefix trimming). Any change means Clean removed
	// something we should have rejected.
	if path.Clean(p) != p {
		return fmt.Errorf("artifact path is not canonical (got %q)", p)
	}
	return nil
}
