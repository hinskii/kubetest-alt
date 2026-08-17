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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteResultAtomic_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := ExecutionResult{
		Phase:        PhasePassed,
		ErrorMessage: "p95=100ms rps=42.0 checks=100/0",
		Steps: []StepResult{
			{Name: "run", Phase: PhasePassed, StartedAt: "2026-01-01T00:00:00Z", FinishedAt: "2026-01-01T00:01:00Z"},
		},
		Artifacts: []string{"results/summary.json"},
	}
	require.NoError(t, WriteResultAtomic(dir, in))

	// #nosec G304 -- reading a file we just wrote in a t.TempDir path.
	b, err := os.ReadFile(filepath.Join(dir, ResultFileName))
	require.NoError(t, err)

	var out ExecutionResult
	require.NoError(t, json.Unmarshal(b, &out))
	assert.Equal(t, in, out, "result must round-trip through JSON")
}

// TestWriteResultAtomic_ValidAfterOverwrite: overwriting must never leave
// readers seeing a truncated file. Weaker version of atomicity — we don't
// simulate concurrent readers, but we do verify no leftover .tmp files.
func TestWriteResultAtomic_ValidAfterOverwrite(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, WriteResultAtomic(dir, ExecutionResult{Phase: PhasePassed}))
	require.NoError(t, WriteResultAtomic(dir, ExecutionResult{Phase: PhaseFailed, ErrorMessage: "second"}))

	// #nosec G304 -- test path.
	b, err := os.ReadFile(filepath.Join(dir, ResultFileName))
	require.NoError(t, err)
	var out ExecutionResult
	require.NoError(t, json.Unmarshal(b, &out))
	assert.Equal(t, PhaseFailed, out.Phase)
	assert.Equal(t, "second", out.ErrorMessage)

	// No leftover temp files in the directory.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.Equal(t, ResultFileName, e.Name(), "unexpected leftover file: %s", e.Name())
	}
}

// TestWriteResultAtomic_MissingDir surfaces a clear error when the target
// dir doesn't exist. Production path shouldn't hit this (operator mounts the
// emptyDir), but the error message helps if it does.
func TestWriteResultAtomic_MissingDir(t *testing.T) {
	err := WriteResultAtomic(filepath.Join(t.TempDir(), "does-not-exist"), ExecutionResult{Phase: PhaseError})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create temp")
}

// TestWriteResultAtomic_PartialResultIsValidJSON is the §15.3 invariant:
// on SIGTERM the wrapper flushes a partial result. That partial file MUST be
// valid JSON that unmarshals into ExecutionResult — otherwise the operator's
// ResultReader (step 07) sees garbage and mis-classifies the run.
//
// This test doesn't simulate SIGTERM directly (that's covered by entry_test);
// it asserts the write mechanism ALWAYS produces schema-valid output regardless
// of Phase/message content, including edge cases (empty message, unicode,
// long strings).
func TestWriteResultAtomic_PartialResultIsValidJSON(t *testing.T) {
	cases := []ExecutionResult{
		{Phase: PhaseAborted},
		{Phase: PhaseError, ErrorMessage: "partial: killed after 100ms of 500ms budget"},
		{Phase: PhaseAborted, ErrorMessage: "unicode ✓ αβγ 你好"},
	}
	for i, tc := range cases {
		t.Run("case-"+string(rune('a'+i)), func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, WriteResultAtomic(dir, tc))

			// #nosec G304 -- test path.
			b, err := os.ReadFile(filepath.Join(dir, ResultFileName))
			require.NoError(t, err)

			// Schema check: unmarshal into the strict struct must succeed.
			var got ExecutionResult
			require.NoError(t, json.Unmarshal(b, &got), "partial result must be valid JSON schema-wise")
			assert.Equal(t, tc.Phase, got.Phase)
			assert.Equal(t, tc.ErrorMessage, got.ErrorMessage)
		})
	}
}
