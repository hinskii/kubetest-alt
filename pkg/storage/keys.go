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

package storage

import (
	"fmt"
	"strings"
)

// DefaultBucket is the bucket every binary uses unless configured
// otherwise. One bucket holds logs, artifacts and results for all runs;
// RunKeys partitions it.
const DefaultBucket = "kubetest-artifacts"

// RunKeys is the single source of truth for where a run's objects live in
// the bucket. Every writer (wrapper scraper, operator log tailer) and every
// reader (operator result reader, API server) derives keys from here, so
// the layout cannot drift between binaries again (fixes.md #3).
//
// Layout, one bucket for everything:
//
//	runs/<namespace>/<runUID>/result.json
//	runs/<namespace>/<runUID>/logs/<seq:08d>.log
//	runs/<namespace>/<runUID>/artifacts/<relpath>
//
// Namespace + UID (not the run name) keeps two runs that share a name —
// in different namespaces, or a deleted-and-recreated run — from
// overwriting each other's logs, artifacts and verdict. Artifacts live in
// their own subtree so a user file called result.json can never shadow
// the wrapper's verdict file.
//
// The value is the run's prefix (always ending in "/"). The zero value is
// invalid; callers check Valid() before writing.
type RunKeys string

// ForRun returns the key set for a run. Both arguments must be non-empty;
// an empty one yields an invalid RunKeys rather than a key that could
// collide with another run's subtree.
func ForRun(namespace, uid string) RunKeys {
	if namespace == "" || uid == "" {
		return ""
	}
	return RunKeys("runs/" + namespace + "/" + uid + "/")
}

// ParseRunKeys validates a prefix received over the wire (the wrapper gets
// it from request.json) and returns it as RunKeys. Rejects anything that
// ForRun could not have produced, so a malformed request can't make the
// wrapper write outside its own run's subtree.
func ParseRunKeys(prefix string) (RunKeys, error) {
	parts := strings.Split(prefix, "/")
	// "runs/<ns>/<uid>/" splits into ["runs", ns, uid, ""].
	if len(parts) != 4 || parts[0] != "runs" || parts[3] != "" {
		return "", fmt.Errorf("storage prefix %q: want runs/<namespace>/<uid>/", prefix)
	}
	for _, p := range parts[1:3] {
		if p == "" || p == "." || p == ".." {
			return "", fmt.Errorf("storage prefix %q: empty or relative segment", prefix)
		}
	}
	return ForRun(parts[1], parts[2]), nil
}

// Valid reports whether k was built from a non-empty namespace and UID.
func (k RunKeys) Valid() bool { return k != "" }

// Prefix is the run's whole subtree — what retention/cleanup removes.
func (k RunKeys) Prefix() string { return string(k) }

// Result is the wrapper's verdict file.
func (k RunKeys) Result() string { return string(k) + "result.json" }

// Logs is the prefix under which log chunks are written. Ends in "/" so
// listing it never matches a lookalike sibling.
func (k RunKeys) Logs() string { return string(k) + "logs/" }

// LogChunk is the key of log chunk number seq. Zero-padded so a
// lexicographic List returns chunks in write order.
func (k RunKeys) LogChunk(seq uint64) string {
	return fmt.Sprintf("%s%08d.log", k.Logs(), seq)
}

// LogCursor is where the operator's log tailer records how far the stored
// chunks reach (next chunk seq + last line timestamp), so a restarted
// operator resumes instead of starting over. Outside Logs() so readers
// that concatenate the log prefix never see it.
func (k RunKeys) LogCursor() string { return string(k) + "logcursor.json" }

// Service is the subtree of one spec.services replica (its logs), inside
// the run's: removed with the run.
func (k RunKeys) Service(replica string) RunKeys {
	return RunKeys(string(k) + "services/" + replica + "/")
}

// Artifacts is the prefix under which scraped artifacts are written.
func (k RunKeys) Artifacts() string { return string(k) + "artifacts/" }

// Artifact is the key for an artifact at relPath (slash-separated,
// relative to the run's working directory). Callers validate relPath
// against traversal before using it as a key.
func (k RunKeys) Artifact(relPath string) string { return k.Artifacts() + relPath }
