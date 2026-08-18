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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/hinskii/kubetest-alt/internal/logstream"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

// chunkStream reads log chunks under logstream.LogPrefix(runID) in seq
// order and yields their bytes on out. If keepPolling is true, it re-lists
// the prefix every pollInterval and streams any new chunks until either
// the deadline expires (no new chunks appearing = run considered idle) or
// ctx cancels.
//
// # Why chunk-polling instead of an in-process tailer
//
// Apiserver and operator are separate binaries; operator's in-memory
// logstream.Registry is unreachable without gRPC/IPC. Adding IPC would
// duplicate pods/log RBAC, race the operator on chunk ordering, and put
// two writers on the same MinIO prefix. Chunk-polling reads the same
// artifacts the operator produces — stateless, single writer, correct on
// operator restart, doesn't need to talk to the operator at all. Ugly
// (an extra HEAD every second while a run is live) but MVP-correct.
type chunkStream struct {
	Downloader storage.Downloader
	Lister     storage.Lister
	Bucket     string
	RunID      string

	// KeepPolling controls the "live tail" behaviour. False means "read
	// what's there now, then EOF" (archived-run download). True means
	// "read + poll for new chunks" (live streaming).
	KeepPolling bool

	// PollInterval defaults to 1s if zero.
	PollInterval time.Duration

	// PollDeadline is the LAST-RESORT idle timeout: how long we wait for a
	// NEW chunk to appear before declaring the stream complete when
	// IsTerminal is nil (or hasn't fired). Prevents leaking a goroutine
	// when a run terminated but we missed every terminal signal. Defaults
	// to 5 minutes. In the happy path (IsTerminal wired) this NEVER fires:
	// the CR-phase check trips first and we exit cleanly within one
	// PollInterval of the phase flip.
	PollDeadline time.Duration

	// IsTerminal, if set, is polled once per round (before the poll sleep)
	// so a live→terminal transition on the CR side triggers a clean exit
	// instead of waiting for PollDeadline. The stream ALWAYS does one
	// final List+emit after IsTerminal returns true — the operator's
	// finalize() flush can race the phase Update, so a chunk may land in
	// MinIO after the CR is already terminal. Callback runs on the loop
	// goroutine, so a blocking implementation blocks the reader (use a
	// cached / short-timeout k8s Get).
	IsTerminal func() bool
}

// stream iterates chunks and calls emit for each byte batch as it reads.
// Returns nil on EOF (natural end / poll deadline) or ctx.Err() on cancel.
// emit may return io.EOF to short-circuit the stream (client disconnected).
func (c *chunkStream) stream(ctx context.Context, emit func([]byte) error) error {
	if c.PollInterval == 0 {
		c.PollInterval = 1 * time.Second
	}
	if c.PollDeadline == 0 {
		c.PollDeadline = 5 * time.Minute
	}
	if c.Downloader == nil || c.Lister == nil {
		return errors.New("chunkStream: missing Downloader or Lister")
	}

	prefix := logstream.LogPrefix(c.RunID)
	sent := map[string]bool{} // keys we've already streamed
	lastProgressAt := time.Now()
	terminalSeen := false // set once by IsTerminal; triggers ONE final round

	drainOnce := func() (emitted bool, err error) {
		keys, err := c.Lister.List(ctx, c.Bucket, prefix)
		if err != nil {
			return false, fmt.Errorf("list chunks: %w", err)
		}
		for _, k := range keys {
			if sent[k] {
				continue
			}
			body, err := c.readChunk(ctx, k)
			if err != nil {
				return emitted, fmt.Errorf("read %s: %w", k, err)
			}
			if err := emit(body); err != nil {
				if errors.Is(err, io.EOF) {
					// Client disconnected — clean exit.
					return emitted, io.EOF
				}
				return emitted, err
			}
			sent[k] = true
			emitted = true
		}
		return emitted, nil
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		emitted, err := drainOnce()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if emitted {
			lastProgressAt = time.Now()
		}

		if !c.KeepPolling {
			return nil
		}
		if terminalSeen {
			// We already did the "one final round after terminal" above —
			// no more chunks are coming. Exit before the next poll sleep.
			return nil
		}
		if c.IsTerminal != nil && c.IsTerminal() {
			// Phase flipped to terminal. Loop once more so a chunk landing
			// after the phase Update (operator's finalize() flush) doesn't
			// get lost; then exit on the check above.
			terminalSeen = true
			continue
		}
		if !emitted && time.Since(lastProgressAt) > c.PollDeadline {
			// Fallback: no CR-side phase signal and nothing new for a long
			// time. Assume the run finished, exit.
			return nil
		}
		select {
		case <-time.After(c.PollInterval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// readChunk fetches a full chunk into memory. Chunks are ~512KB by default
// (logstream.DefaultChunkBytes) so loading them fully is fine — the caller
// streams them out to the WS one at a time.
func (c *chunkStream) readChunk(ctx context.Context, key string) ([]byte, error) {
	rc, err := c.Downloader.Get(ctx, c.Bucket, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, rc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
