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

package logstream

import (
	"context"
	"errors"
	"io"
	"sync"

	ctrlLog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/hinskii/kubetest-alt/pkg/storage"
)

// Registry owns the set of active tailers keyed by an opaque tailer ID
// (the controller uses "<namespace>/<name>"). The controller's
// reconcile loop calls EnsureTailer on pod Running and StopTailer on
// terminal phase — both are safe to call from multiple reconciles and
// idempotent, which matters because controller-runtime reconciles are
// event-driven and duplicate events are expected (§15.4 watch reconnect).
//
// # Restart-resume policy: wipe prefix, do NOT resume seq
//
// When the operator restarts mid-run, EnsureTailer starts a fresh Tailer
// for a still-Running pod. That Tailer reads pod logs from the beginning
// again (follow=true, no TailLines) and its chunk sequence resets to 0 —
// but the old operator may have left chunks 0..K under the same prefix in
// object storage. Their byte boundaries were flush-timing-dependent, so
// new chunks 0..K′ almost certainly split the same log at different
// positions, producing a mixed prefix with duplicates and interleaved
// bytes when the API server serves the log by lex-listing the prefix.
//
// We resolve this by wiping the run's logs/ prefix BEFORE the new Tailer
// starts. Fresh start, monotonic chunks. The alternative — resume from
// seq K+1 — would require the Tailer to also skip already-flushed bytes
// on the source side, and there is no way to correlate "object-storage chunk
// boundary" to "pod stdout byte offset" cheaply. Kubelet log rotation
// (§15.4) can lose bytes between the old operator's crash and the new
// operator's start regardless; wipe-and-restart doesn't lose anything the
// resume path would have kept.
//
// The Registry does NOT own the LogSource — production wires a
// K8sLogSource, tests inject a fake. Same for the Uploader and Remover.
type Registry struct {
	mu       sync.Mutex
	tailers  map[string]*Tailer
	source   PodLogSource
	uploader storage.Uploader
	remover  storage.Remover
	bucket   string

	// TailerConfig is a template applied to every EnsureTailer call. Keys +
	// OpenSource are filled in by EnsureTailer; other fields flow through.
	// Zero fields fall back to the Tailer's own defaults.
	TailerConfig Config
}

// PodLogSource is the Registry-facing interface over K8sLogSource. Kept
// separate from LogSource (which is per-open) so the controller passes
// pod coordinates once and lets the tailer reopen on its own.
type PodLogSource interface {
	Open(ctx context.Context, namespace, podName string) (io.ReadCloser, error)
}

// NewRegistry constructs an empty registry. remover may be nil — the
// restart-resume wipe is best-effort and a nil Remover simply skips it
// (with the caveat documented on Registry).
func NewRegistry(source PodLogSource, uploader storage.Uploader, remover storage.Remover, bucket string) *Registry {
	return &Registry{
		tailers:  map[string]*Tailer{},
		source:   source,
		uploader: uploader,
		remover:  remover,
		bucket:   bucket,
	}
}

// ErrRegistryClosed is returned by EnsureTailer after Shutdown.
var ErrRegistryClosed = errors.New("logstream: registry closed")

// EnsureTailer starts (or no-ops) a tailer for the given run + pod. Safe to
// call from multiple reconciles; the second and later calls are cheap
// map-lookups. Returns nil on success — callers that need the Tailer for
// subscription use Get(id) instead. id is the map key; keys says where the
// chunks go (pkg/storage.RunKeys — namespace + UID, never the bare name).
//
// On first creation for a given id, wipes keys.Logs() so a
// restarted operator doesn't produce a mixed-boundary chunk prefix (see
// package-level docstring). The wipe is best-effort: on Remover error we
// log but continue — the tailer runs, some chunks may be duplicates, and
// step 10's reader can still show live logs from the new stream.
//
// Because the controller calls this from Reconcile and Reconcile's ctx is
// per-request, we DO NOT pass it as the tailer's parent — the tailer must
// outlive the reconcile. We use context.Background() and rely on
// StopTailer / Shutdown for the tailer's lifecycle.
func (r *Registry) EnsureTailer(ctx context.Context, id string, keys storage.RunKeys, namespace, podName string) error {
	if !keys.Valid() {
		return errors.New("logstream: invalid storage keys")
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.tailers == nil {
		return ErrRegistryClosed
	}

	if _, ok := r.tailers[id]; ok {
		return nil
	}

	// Wipe any stale chunks left by a previous operator lifetime BEFORE
	// starting the new tailer. Same-lifetime re-creation is impossible
	// (map lookup above catches it), so a wipe here always targets crash-
	// recovery leftovers, never in-progress writes.
	if r.remover != nil && r.uploader != nil {
		if err := r.remover.RemovePrefix(ctx, r.bucket, keys.Logs()); err != nil {
			// Log-and-continue: we've decided log durability is second to
			// run durability. A failed wipe means the new tailer may
			// produce a mixed prefix, but the run itself proceeds.
			ctrlLog.Log.Info("logstream: RemovePrefix failed, continuing with fresh tailer",
				"tailer", id, "error", err.Error())
		}
	}

	cfg := r.TailerConfig
	cfg.Keys = keys
	cfg.Uploader = r.uploader
	if cfg.Bucket == "" {
		cfg.Bucket = r.bucket
	}
	src := r.source
	cfg.OpenSource = func(openCtx context.Context) (io.ReadCloser, error) {
		if src == nil {
			return nil, errors.New("logstream: no pod log source configured")
		}
		return src.Open(openCtx, namespace, podName)
	}
	t := New(cfg)
	t.Start(context.Background())
	r.tailers[id] = t
	return nil
}

// Get returns the tailer for id, or nil if none exists.
func (r *Registry) Get(id string) *Tailer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tailers[id]
}

// StopTailer stops and removes the tailer for id. No-op if none exists.
// Blocks until the tailer's run loop has flushed and exited so the caller
// can be sure the final chunk has landed before proceeding.
func (r *Registry) StopTailer(id string) {
	r.mu.Lock()
	t := r.tailers[id]
	if t != nil {
		delete(r.tailers, id)
	}
	r.mu.Unlock()

	if t != nil {
		t.Stop()
	}
}

// Shutdown stops every tailer and marks the registry closed. Called from
// cmd/operator on manager exit so we don't leak goroutines on program
// shutdown.
func (r *Registry) Shutdown() {
	r.mu.Lock()
	tailers := r.tailers
	r.tailers = nil
	r.mu.Unlock()

	for _, t := range tailers {
		t.Stop()
	}
}

// Active returns the IDs of the tailers currently running. Test/inspection
// helper.
func (r *Registry) Active() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.tailers))
	for id := range r.tailers {
		out = append(out, id)
	}
	return out
}
