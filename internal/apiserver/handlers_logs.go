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
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/coder/websocket"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/controller"
)

// getRunLogs upgrades to a WebSocket and streams log chunks. Two modes:
//   - Live (CR exists AND phase not terminal): chunk_reader polls the object-storage
//     prefix, streaming new chunks as they appear.
//   - Archive (CR gone OR phase terminal): streams all chunks in seq order
//     then closes the connection.
//
// See chunk_reader.go for why we poll object storage instead of tapping the
// operator's in-memory tailer registry.
func (s *Server) getRunLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ReasonBadRequest, "run id is required")
		return
	}
	if s.Downloader == nil || s.Lister == nil || s.Bucket == "" {
		writeError(w, http.StatusServiceUnavailable, ReasonServiceUnavail,
			"log storage is not configured")
		return
	}

	ns, err := s.targetNamespace(r, "")
	if err != nil {
		writeLookupError(w, err)
		return
	}
	// Resolve the run (CR by name, else archived row by UID) — the chunk
	// prefix is derived from namespace + UID, so an unknown id is a 404
	// rather than an empty stream.
	ref, err := s.findRun(r.Context(), ns, id)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	// Live vs archive: a CR in a non-terminal phase keeps the stream open.
	keepPolling := ref.CR != nil && !controller.IsTerminalPhase(ref.CR.Status.Phase)

	// isTerminal is checked once per poll round so a live→terminal transition
	// on the CR closes the stream within one PollInterval of the phase flip,
	// instead of waiting out PollDeadline (default 5 minutes). Returns true
	// when the CR is gone OR its phase is terminal — either is a stop signal.
	//
	// Uses a bounded child context so a wedged API server can't stall the
	// reader loop; failure to fetch phase returns false (better to keep
	// polling than to close prematurely on a network flake).
	isTerminal := func() bool {
		if !keepPolling {
			return true // archived-run path never sets IsTerminal, but be safe
		}
		checkCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var cur testsv1alpha1.TestRun
		if err := s.K8sClient.Get(checkCtx,
			types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &cur); err != nil {
			if apierrors.IsNotFound(err) {
				return true // CR gone → nothing more coming
			}
			return false // transient error — retry next round
		}
		return controller.IsTerminalPhase(cur.Status.Phase)
	}

	// Upgrade. Origin check: default-deny; production wires CORS via a
	// reverse proxy, tests hit http://127.0.0.1 which the "insecure" mode
	// below accepts.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // origin allowlist belongs at the ingress
	})
	if err != nil {
		// Accept already wrote the error response.
		return
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	// Bound the whole handler so a slow client can't pin a goroutine.
	// PollDeadline is per-idle-round; this is the outer wall-clock cap.
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Hour)
	defer cancel()

	stream := &chunkStream{
		Downloader:   s.Downloader,
		Lister:       s.Lister,
		Bucket:       s.Bucket,
		Prefix:       ref.keys().Logs(),
		KeepPolling:  keepPolling,
		PollInterval: s.LogPollInterval,
		PollDeadline: s.LogPollDeadline,
	}
	if keepPolling {
		stream.IsTerminal = isTerminal
	}
	err = stream.stream(ctx, func(chunk []byte) error {
		if err := conn.Write(ctx, websocket.MessageBinary, chunk); err != nil {
			// Client hung up (or network fault). Signal EOF to the stream
			// so it stops polling; this is a clean exit, not an error.
			return io.EOF
		}
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded) {
		// Best-effort close-frame with an error code; if the write fails
		// (client already gone) the defer above still cleans up the conn.
		_ = conn.Close(websocket.StatusInternalError, err.Error())
	}
}

// getRunLogsText returns everything stored for the run's log so far as one
// text/plain body — the non-streaming counterpart of the WebSocket
// endpoint, for history pages and downloads. Live runs return what has
// been flushed so far (no follow).
func (s *Server) getRunLogsText(w http.ResponseWriter, r *http.Request) {
	if s.Downloader == nil || s.Lister == nil || s.Bucket == "" {
		writeError(w, http.StatusServiceUnavailable, ReasonServiceUnavail,
			"log storage is not configured")
		return
	}
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
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	if r.URL.Query().Get("download") == "1" {
		h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", safeFilename(ref.Name+".log")))
	}
	stream := &chunkStream{
		Downloader: s.Downloader,
		Lister:     s.Lister,
		Bucket:     s.Bucket,
		Prefix:     ref.keys().Logs(),
	}
	wroteHeader := false
	err = stream.stream(r.Context(), func(chunk []byte) error {
		if !wroteHeader {
			w.WriteHeader(http.StatusOK)
			wroteHeader = true
		}
		if _, err := w.Write(chunk); err != nil {
			return io.EOF // client went away
		}
		return nil
	})
	if err != nil && !wroteHeader {
		writeError(w, http.StatusInternalServerError, ReasonInternal, fmt.Sprintf("read logs: %v", err))
		return
	}
	if !wroteHeader {
		w.WriteHeader(http.StatusOK) // no chunks yet: empty log, not an error
	}
}
