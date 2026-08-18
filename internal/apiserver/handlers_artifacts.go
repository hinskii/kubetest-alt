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
	"fmt"
	"net/http"
	"path"
	"strings"
)

// getRunArtifact returns a presigned URL the browser follows directly to
// MinIO. Never streams bytes through the API server.
//
// The path segment ({path...} in the router) is joined onto <runID>/ to
// form the object key. Path traversal is rejected: any segment starting
// with "..", an absolute "/", or an empty segment yields 400 (per
// §step-10 test requirement).
func (s *Server) getRunArtifact(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	rawPath := r.PathValue("path")
	if runID == "" || rawPath == "" {
		writeError(w, http.StatusBadRequest, ReasonBadRequest,
			"run id and artifact path are required")
		return
	}
	if s.Presigner == nil || s.ArtifactsBucket == "" {
		writeError(w, http.StatusServiceUnavailable, ReasonServiceUnavail,
			"artifact storage is not configured")
		return
	}
	if err := validateArtifactPath(rawPath); err != nil {
		writeError(w, http.StatusBadRequest, ReasonBadRequest, err.Error())
		return
	}
	// Wrapper scraper (step 07) writes to <bucket>/<runID>/<relPath>.
	key := runID + "/" + rawPath
	url, err := s.Presigner.PresignGetURL(r.Context(), s.ArtifactsBucket, key, s.PresignedURLExpiry)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ReasonInternal,
			fmt.Sprintf("presign: %v", err))
		return
	}
	// JSON envelope rather than 302: browsers XHR-ing this endpoint get a
	// stable JSON contract; a redirect would lose the expiry metadata the
	// GUI shows ("link valid until HH:MM").
	writeJSON(w, http.StatusOK, artifactURLResponse{
		URL:       url,
		ExpiresIn: int(s.PresignedURLExpiry.Seconds()),
	})
}

// artifactURLResponse is the wire shape returned by GET
// /runs/{id}/artifacts/{path}. Struct instead of map so json field names
// are compile-checked (single source of truth vs OpenAPI spec keys).
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
