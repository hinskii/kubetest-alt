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

// Package server is Control Center's HTTP layer. It is stateless: every
// page is built from the clusters' kubetest API servers on request.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/hinskii/kubetest-alt/internal/controlcenter/auth"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/clusters"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/views"
)

// defaultProbeTimeout bounds one cluster health probe on the home page.
const defaultProbeTimeout = 3 * time.Second

// Server holds the handlers' dependencies.
type Server struct {
	Clusters *clusters.Registry
	Auth     *auth.Resolver
	Views    *views.Views
	Log      *slog.Logger
	// ProbeTimeout bounds a cluster health probe. Default 3s.
	ProbeTimeout time.Duration
	// LiveViewKey signs live-view links (CC_LIVE_VIEW_KEY). Empty: a
	// random key, so links end with the process and replicas don't share
	// them.
	LiveViewKey []byte

	live      *liveSigner
	registry  *prometheus.Registry
	requests  *prometheus.CounterVec
	clusterUp *prometheus.GaugeVec
}

// Handler wires routes and middleware.
func (s *Server) Handler() http.Handler {
	if s.ProbeTimeout == 0 {
		s.ProbeTimeout = defaultProbeTimeout
	}
	if s.Log == nil {
		s.Log = slog.Default()
	}
	s.initMetrics()
	live, err := newLiveSigner(s.LiveViewKey)
	if err != nil {
		panic(fmt.Sprintf("controlcenter: live-view key: %v", err)) // crypto/rand failing is fatal
	}
	s.live = live

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.home)
	mux.Handle("GET /static/", views.Static())
	mux.HandleFunc("GET /healthz", ok)
	// Local readiness only: one unreachable cluster must not take the
	// whole UI out of the Service. Cluster health is on the home page
	// and in controlcenter_cluster_up.
	mux.HandleFunc("GET /readyz", ok)
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{}))
	// The tool's live UI, by signed link (live.go) — no session needed.
	mux.HandleFunc("GET /live/{token}/{path...}", s.liveView)
	s.routes(mux)
	mux.HandleFunc("/", s.notFound)

	// CrossOriginProtection rejects cross-site non-safe requests
	// (Sec-Fetch-Site / Origin) — CSRF protection with no tokens and no
	// server-side state.
	var h http.Handler = mux
	h = s.Auth.Middleware(h)
	h = http.NewCrossOriginProtection().Handler(h)
	h = promhttp.InstrumentHandlerCounter(s.requests, h)
	return securityHeaders(h)
}

func (s *Server) initMetrics() {
	s.registry = prometheus.NewRegistry()
	s.registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	s.requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "controlcenter_http_requests_total",
		Help: "HTTP requests served by Control Center.",
	}, []string{"code", "method"})
	s.clusterUp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "controlcenter_cluster_up",
		Help: "1 if the cluster's kubetest API server answered the last probe.",
	}, []string{"cluster"})
	s.registry.MustRegister(s.requests, s.clusterUp)
}

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

// securityHeaders: pages load only same-origin assets, no inline script.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// clusterStatus is one home-page card.
type clusterStatus struct {
	Name        string
	DisplayName string
	Err         string // "" = reachable
}

type homeData struct {
	Clusters []clusterStatus
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "home", "", homeData{Clusters: s.probeAll(r.Context())})
}

// probeAll checks every cluster's API server in parallel.
func (s *Server) probeAll(ctx context.Context) []clusterStatus {
	list := s.Clusters.List()
	out := make([]clusterStatus, len(list))
	var wg sync.WaitGroup
	for i, c := range list {
		out[i] = clusterStatus{Name: c.Name, DisplayName: c.DisplayNameOrName()}
		wg.Go(func() {
			pctx, cancel := context.WithTimeout(ctx, s.ProbeTimeout)
			defer cancel()
			up := 1.0
			if err := c.API.Healthz(pctx); err != nil {
				up = 0
				out[i].Err = err.Error()
				s.Log.Warn("cluster probe failed", "cluster", c.Name, "err", err)
			}
			s.clusterUp.WithLabelValues(c.Name).Set(up)
		})
	}
	wg.Wait()
	return out
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.renderError(w, r, http.StatusNotFound, "There is nothing at "+r.URL.Path+".")
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	s.render(w, r, status, "error", http.StatusText(status), views.ErrorData{
		Status: status, StatusText: http.StatusText(status), Message: msg,
	})
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page, title string, data any) {
	err := s.Views.Render(w, status, page, views.Page{Title: title, User: auth.FromContext(r.Context()), Data: data})
	if err != nil {
		s.Log.Error("render failed", "page", page, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
