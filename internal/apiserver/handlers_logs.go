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
	"io"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"k8s.io/apimachinery/pkg/types"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/controller"
)

// getRunLogs upgrades to a WebSocket and streams log chunks. Two modes:
//   - Live (CR exists AND phase not terminal): chunk_reader polls the MinIO
//     prefix, streaming new chunks as they appear.
//   - Archive (CR gone OR phase terminal): streams all chunks in seq order
//     then closes the connection.
//
// See chunk_reader.go for why we poll MinIO instead of tapping the
// operator's in-memory tailer registry.
func (s *Server) getRunLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ReasonBadRequest, "run id is required")
		return
	}
	if s.Downloader == nil || s.Lister == nil || s.LogsBucket == "" {
		writeError(w, http.StatusServiceUnavailable, ReasonServiceUnavail,
			"log storage is not configured")
		return
	}

	// Decide live vs archive by looking at the CR. Absent CR → archive.
	keepPolling := false
	var run testsv1alpha1.TestRun
	err := s.K8sClient.Get(r.Context(),
		types.NamespacedName{Namespace: s.Namespace, Name: id}, &run)
	switch {
	case err == nil && !controller.IsTerminalPhase(run.Status.Phase):
		keepPolling = true
	case err != nil:
		// NotFound is fine — archived run, still serve.
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
		Bucket:       s.LogsBucket,
		RunID:        id,
		KeepPolling:  keepPolling,
		PollInterval: s.LogPollInterval,
		PollDeadline: s.LogPollDeadline,
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
