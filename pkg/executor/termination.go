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

package executor

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// TerminationMessagePath is where the wrapper leaves a compact copy of its
// verdict. Kubernetes copies the file into the pod status
// (containerStatuses[].state.terminated.message), so the operator gets the
// verdict even when result.json never reached object storage — no storage
// configured, or the upload failed (fixes.md #6).
const TerminationMessagePath = "/dev/termination-log"

// maxTerminationMessageBytes is the kubelet's per-container limit.
const maxTerminationMessageBytes = 4096

// maxSummaryErrorBytes bounds ErrorMessage in the summary so a long tool
// error can't push the phase out of the limit.
const maxSummaryErrorBytes = 1024

// TerminationSummary encodes r's verdict for the termination message:
// phase, error message (truncated), test counts and — when they fit —
// metrics. Artifacts and steps stay in result.json only.
func TerminationSummary(r ExecutionResult) []byte {
	s := ExecutionResult{
		Phase:        r.Phase,
		ErrorMessage: truncateUTF8(r.ErrorMessage, maxSummaryErrorBytes),
		TestCounts:   r.TestCounts,
		Metrics:      r.Metrics,
	}
	b, err := json.Marshal(s)
	if err != nil || len(b) > maxTerminationMessageBytes {
		s.Metrics = nil
		b, _ = json.Marshal(s)
	}
	return b
}

// ParseTerminationSummary decodes a TerminationSummary. ok is false for
// anything else (empty, a crash message, a non-terminal phase), so callers
// fall back to the container's exit code.
func ParseTerminationSummary(msg string) (r ExecutionResult, ok bool) {
	msg = strings.TrimSpace(msg)
	if !strings.HasPrefix(msg, "{") {
		return ExecutionResult{}, false
	}
	if err := json.Unmarshal([]byte(msg), &r); err != nil {
		return ExecutionResult{}, false
	}
	switch r.Phase {
	case PhasePassed, PhaseFailed, PhaseError, PhaseAborted:
		return r, true
	}
	return ExecutionResult{}, false
}

func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const ellipsis = "…"
	cut := max - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}
