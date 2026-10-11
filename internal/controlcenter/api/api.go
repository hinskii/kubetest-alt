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

// Package api is Control Center's backend for its React UI (step 20R):
// a JSON API under /api/v1. The browser never talks to a cluster: the
// signed-in user's identity comes from the sign-in header (auth), and
// every cluster call goes through the service proxy with the cluster's
// token, as the server-rendered pages did.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/hinskii/kubetest-alt/internal/controlcenter/auth"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/clusters"
)

// Prefix is where the API lives.
const Prefix = "/api/v1"

// defaultProbeTimeout bounds one cluster health probe.
const defaultProbeTimeout = 3 * time.Second

// Server holds the API's dependencies.
type Server struct {
	Clusters *clusters.Registry
	// ProbeTimeout bounds a cluster health probe. Default 3s.
	ProbeTimeout time.Duration
	// OnProbe hears every probe's outcome (the cluster_up metric).
	OnProbe func(cluster string, up bool)
	// LiveURL signs the link to a run's live UI (the server's /live/
	// route); nil: runs show no live view.
	LiveURL func(cluster, namespace, run, entry string) string
	Log     *slog.Logger
}

// Register adds the API's routes to mux. Unknown /api/v1 paths answer a
// JSON 404, never the UI's index page.
func (s *Server) Register(mux *http.ServeMux) {
	if s.ProbeTimeout == 0 {
		s.ProbeTimeout = defaultProbeTimeout
	}
	if s.Log == nil {
		s.Log = slog.Default()
	}
	for _, rt := range s.routes() {
		mux.HandleFunc(rt.Method+" "+rt.Path, rt.handler)
	}
	mux.HandleFunc(Prefix+"/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such API endpoint: "+r.Method+" "+r.URL.Path)
	})
}

// Me is the signed-in user.
type Me struct {
	// Email is "" for an anonymous request (no sign-in header).
	Email string `json:"email" required:"true"`
	Role  string `json:"role" required:"true" enum:"viewer,developer,admin"`
	// Can says what the role allows, so the UI needn't know the ladder.
	Can Capabilities `json:"can" required:"true"`
}

// Capabilities are the actions a role allows.
type Capabilities struct {
	// Run: start, abort and comment runs; create and edit Tests; set
	// schedules (developer).
	Run bool `json:"run" required:"true"`
	// Delete: delete runs and Tests, clean up (admin).
	Delete bool `json:"delete" required:"true"`
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u := auth.FromContext(r.Context())
	writeJSON(w, http.StatusOK, Me{Email: u.Email, Role: u.Role.String(),
		Can: Capabilities{Run: u.Can(auth.RoleDeveloper), Delete: u.Can(auth.RoleAdmin)}})
}

// Cluster is one configured cluster and whether its kubetest API answered.
type Cluster struct {
	Name        string `json:"name" required:"true"`
	DisplayName string `json:"displayName" required:"true"`
	Reachable   bool   `json:"reachable" required:"true"`
	// Error is why it isn't reachable — the API's or the proxy's answer.
	Error string `json:"error,omitempty"`
}

// clusters probes every cluster's API server in parallel.
func (s *Server) clusters(w http.ResponseWriter, r *http.Request) {
	list := s.Clusters.List()
	out := make([]Cluster, len(list))
	var wg sync.WaitGroup
	for i, c := range list {
		out[i] = Cluster{Name: c.Name, DisplayName: c.DisplayNameOrName(), Reachable: true}
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(r.Context(), s.ProbeTimeout)
			defer cancel()
			if err := c.API.Healthz(ctx); err != nil {
				out[i].Reachable, out[i].Error = false, err.Error()
				s.Log.Warn("cluster probe failed", "cluster", c.Name, "err", err)
			}
			if s.OnProbe != nil {
				s.OnProbe(c.Name, out[i].Reachable)
			}
		})
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, out)
}

// Error is every error response's body.
type Error struct {
	Error string `json:"error" required:"true"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, Error{Error: msg})
}
