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

	"github.com/hinskii/kubetest-alt/pkg/storage"
)

// Registry owns the set of active tailers keyed by runID. The controller's
// reconcile loop calls EnsureTailer on pod Running and StopTailer on
// terminal phase — both are safe to call from multiple reconciles and
// idempotent, which matters because controller-runtime reconciles are
// event-driven and duplicate events are expected (§15.4 watch reconnect).
//
// The Registry does NOT own the LogSource — production wires a
// K8sLogSource, tests inject a fake. Same for the Uploader.
type Registry struct {
	mu       sync.Mutex
	tailers  map[string]*Tailer
	source   PodLogSource
	uploader storage.Uploader
	bucket   string

	// TailerConfig is a template applied to every EnsureTailer call. RunID +
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

// NewRegistry constructs an empty registry.
func NewRegistry(source PodLogSource, uploader storage.Uploader, bucket string) *Registry {
	return &Registry{
		tailers:  map[string]*Tailer{},
		source:   source,
		uploader: uploader,
		bucket:   bucket,
	}
}

// ErrRegistryClosed is returned by EnsureTailer after Shutdown.
var ErrRegistryClosed = errors.New("logstream: registry closed")

// EnsureTailer starts (or no-ops) a tailer for the given run + pod. Safe to
// call from multiple reconciles; the second and later calls are cheap
// map-lookups. Returns nil on success — callers that need the Tailer for
// subscription (API server) use Get(runID) instead.
//
// Because the controller calls this from Reconcile and Reconcile's ctx is
// per-request, we DO NOT pass it as the tailer's parent — the tailer must
// outlive the reconcile. We use context.Background() and rely on
// StopTailer / Shutdown for the tailer's lifecycle.
func (r *Registry) EnsureTailer(_ context.Context, runID, namespace, podName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.tailers == nil {
		return ErrRegistryClosed
	}

	if _, ok := r.tailers[runID]; ok {
		return nil
	}

	cfg := r.TailerConfig
	cfg.RunID = runID
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
	r.tailers[runID] = t
	return nil
}

// Get returns the tailer for runID, or nil if none exists. Used by the
// API server (step 10) to attach a subscriber to a live run.
func (r *Registry) Get(runID string) *Tailer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tailers[runID]
}

// StopTailer stops and removes the tailer for runID. No-op if none exists.
// Blocks until the tailer's run loop has flushed and exited so the caller
// can be sure the final chunk has landed before proceeding.
func (r *Registry) StopTailer(runID string) {
	r.mu.Lock()
	t := r.tailers[runID]
	if t != nil {
		delete(r.tailers, runID)
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

// Active returns the set of runIDs currently being tailed. Test/inspection
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
