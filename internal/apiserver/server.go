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

// Package apiserver is the HTTP surface the GUI consumes. Thin translation
// layer over the k8s API and the Postgres run store (§7): reads go through
// a controller-runtime shared informer cache; writes go straight to the
// k8s API server (Test CRUD + TestRun POST). The store is read-only from
// here — the operator owns the write path (§step-09).
//
// # Router
//
// stdlib http.ServeMux with 1.22+ pattern routing. Chosen over chi/gorilla
// for zero deps; method-scoped patterns handle all our routes natively.
//
// # Live logs
//
// MinIO chunk-polling. Apiserver and operator are separate binaries so the
// operator's in-memory tailer registry from step 08 is unreachable without
// gRPC/IPC. Chunk-polling is stateless and needs only the existing
// Downloader + Lister — see chunk_reader.go for the design rationale.
package apiserver

import (
	"context"
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

// Server holds all the dependencies handlers need. Constructor injects
// fakes in tests, real cluster clients in production.
type Server struct {
	// K8sClient is a controller-runtime client backed by a shared informer
	// cache (cluster.New at the cmd/apiserver level). List/Get read from
	// cache; Create/Update/Patch/Delete go direct to the API server.
	K8sClient client.Client

	// Namespace scopes every read/write. Empty string means "all namespaces"
	// — accepted for a cluster-scoped API server, callers that need
	// per-tenant multi-namespace can front this with an auth layer.
	Namespace string

	// Store is the archived-runs read path. Live runs come from K8sClient
	// (they're still CRs); terminal runs older than the CR retention window
	// (once implemented) live only in Postgres. Nil is legal — GET /runs
	// then returns cluster-only results.
	Store RunReader

	// Downloader + Lister + Presigner make up the MinIO surface handlers
	// need. All three optional individually — endpoints degrade gracefully
	// when a dep is missing (logs return 503, artifacts return 503).
	Downloader storage.Downloader
	Lister     storage.Lister
	Presigner  storage.Presigner

	// LogsBucket is the bucket log chunks live in
	// (kubetest-logs/<runID>/<8d>.log — see logstream.LogPrefix).
	LogsBucket string

	// ArtifactsBucket holds files uploaded by the wrapper's scraper
	// (<runID>/<path> — see internal/scraper).
	ArtifactsBucket string

	// PresignedURLExpiry bounds how long browsers can use a returned
	// artifact URL. Default 15 minutes if zero.
	PresignedURLExpiry time.Duration

	// LogPollInterval is how often the WS handler re-lists the chunk prefix
	// while waiting for new chunks. Default 1s. Test hook.
	LogPollInterval time.Duration

	// LogPollDeadline is how long the WS handler waits for a NEW chunk to
	// appear before treating the stream as complete (defensive — normal
	// termination is either the run being archived in the store or the
	// client disconnecting). Default 5 minutes.
	LogPollDeadline time.Duration

	// Now is the wall-clock source. Overridable in tests. Default time.Now.
	Now func() time.Time
}

// RunReader is the read-only surface of store.RunStore the apiserver needs.
// Kept small so tests inject a hand-rolled fake without pulling pgx.
type RunReader interface {
	Get(ctx context.Context, uid string) (*store.Row, error)
	List(ctx context.Context, f store.Filter, p store.Page) ([]store.Row, error)
}

// Handler returns the ready-to-serve http.Handler. Constructor rejects
// zero-value K8sClient — that's the only truly-required dep; everything
// else has a graceful-degradation path.
func (s *Server) Handler() http.Handler {
	if s.PresignedURLExpiry == 0 {
		s.PresignedURLExpiry = 15 * time.Minute
	}
	if s.LogPollInterval == 0 {
		s.LogPollInterval = 1 * time.Second
	}
	if s.LogPollDeadline == 0 {
		s.LogPollDeadline = 5 * time.Minute
	}
	if s.Now == nil {
		s.Now = time.Now
	}

	mux := http.NewServeMux()

	// Tests CRUD — GUI writes flow through here.
	mux.HandleFunc("GET /tests", s.listTests)
	mux.HandleFunc("POST /tests", s.createTest)
	mux.HandleFunc("GET /tests/{name}", s.getTest)
	mux.HandleFunc("PATCH /tests/{name}", s.patchTest)
	mux.HandleFunc("DELETE /tests/{name}", s.deleteTest)

	// Runs — creation + list (cluster + store merge) + detail.
	mux.HandleFunc("POST /runs", s.createRun)
	mux.HandleFunc("GET /runs", s.listRuns)
	mux.HandleFunc("GET /runs/{id}", s.getRun)

	// Logs + artifacts.
	mux.HandleFunc("GET /runs/{id}/logs", s.getRunLogs)
	mux.HandleFunc("GET /runs/{id}/artifacts/{path...}", s.getRunArtifact)

	// OpenAPI spec — hand-served, source of truth is openapi.go.
	mux.HandleFunc("GET /openapi.json", s.getOpenAPI)

	// Healthz — trivial 200, useful for Deployment probes.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	return mux
}
