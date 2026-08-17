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
	"os"
	"path/filepath"
	"strings"
)

// EnvLookup abstracts os.LookupEnv so tests can inject values without touching
// the process env (parallel tests would race otherwise).
type EnvLookup func(key string) (string, bool)

// writeFiles materializes every FileContent into dstDir. It refuses paths
// that would escape dstDir (defense against a malicious Test spec) and honors
// the optional mode override.
//
// Content vs ContentFrom precedence: Content wins (explicit inline). Both
// empty means "empty file" (valid — some tests want empty scratch files).
func writeFiles(dstDir string, files []FileContent, env EnvLookup) error {
	if env == nil {
		env = os.LookupEnv
	}
	dstAbs, err := filepath.Abs(dstDir)
	if err != nil {
		return fmt.Errorf("resolve dst: %w", err)
	}
	for _, f := range files {
		if err := writeOneFile(dstAbs, f, env); err != nil {
			return fmt.Errorf("file %q: %w", f.Path, err)
		}
	}
	return nil
}

func writeOneFile(dstAbs string, f FileContent, env EnvLookup) error {
	if f.Path == "" {
		return fmt.Errorf("empty path")
	}
	// Reject absolute paths outright — they'd bypass dstDir entirely.
	if filepath.IsAbs(f.Path) {
		return fmt.Errorf("absolute path forbidden")
	}
	target := filepath.Join(dstAbs, f.Path)
	// Belt+suspenders: even after Join+Clean, target must stay under dstAbs.
	// Guards against "../evil" inside Path.
	if !isUnder(target, dstAbs) {
		return fmt.Errorf("path escapes datadir")
	}

	// #nosec G301 -- shared init→wrapper emptyDir; both containers may run
	// as different UIDs, need dir traversal.
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	// Resolve the payload.
	var payload []byte
	switch {
	case f.Content != "":
		payload = []byte(f.Content)
	case f.ContentFrom != "":
		v, ok := env(f.ContentFrom)
		if !ok {
			return fmt.Errorf("contentFrom env %q not set", f.ContentFrom)
		}
		payload = []byte(v)
	default:
		payload = nil // empty file, valid
	}

	mode := os.FileMode(0o644)
	if f.Mode != nil {
		// #nosec G115 -- k8s file modes are 12 bits; API type is int32 but
		// values fit in uint32/FileMode without truncation.
		mode = os.FileMode(*f.Mode)
	}
	// #nosec G306 -- mode is caller-controlled (Test.spec.content.files[].mode);
	// executable bits are intentional for scripts.
	if err := os.WriteFile(target, payload, mode); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// isUnder reports whether target is inside base (after cleaning), avoiding
// substring-prefix false positives like /data-evil under /data.
func isUnder(target, base string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, "..") && !strings.HasPrefix(rel, string(filepath.Separator))
}
