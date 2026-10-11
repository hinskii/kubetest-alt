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
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	uiapi "github.com/hinskii/kubetest-alt/internal/controlcenter/api"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/auth"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/clusters"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/views"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/webui"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/yamlview"
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
	// The React UI (step 20R): its pages are index.html plus /assets/*,
	// its data the JSON API. Routes move to it page by page; the ones
	// still server-rendered are registered below (s.routes).
	mux.Handle("GET /{$}", webui.Index())
	mux.Handle("GET "+webui.AssetsPrefix, webui.Assets())
	(&uiapi.Server{
		Clusters: s.Clusters, ProbeTimeout: s.ProbeTimeout, Log: s.Log,
		LiveURL: func(cluster, ns, run, entry string) string {
			return s.liveURL(cluster, ns, run, entry, time.Now())
		},
		OnProbe: func(cluster string, up bool) {
			v := 0.0
			if up {
				v = 1
			}
			s.clusterUp.WithLabelValues(cluster).Set(v)
		},
	}).Register(mux)
	mux.Handle("GET /static/", views.Static())
	// Highlighting styles, generated from Chroma's github / github-dark.
	highlightCSS := []byte(yamlview.CSS())
	mux.HandleFunc("GET /static/highlight.css", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		// The page links it with ?v=<hash of the content>: cache it long.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		_, _ = w.Write(highlightCSS)
	})
	mux.HandleFunc("GET /healthz", ok)
	// Local readiness only: one unreachable cluster must not take the
	// whole UI out of the Service. Cluster health is on the home page
	// and in controlcenter_cluster_up.
	mux.HandleFunc("GET /readyz", ok)
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{}))
	// The tool's live UI, by signed link (live.go) — no session needed.
	mux.HandleFunc("GET /live/{token}/{path...}", s.liveView)
	s.routes(mux)
	mux.HandleFunc("/", s.fallback)

	// CrossOriginProtection rejects cross-site non-safe requests
	// (Sec-Fetch-Site / Origin) — CSRF protection with no tokens and no
	// server-side state.
	var h http.Handler = mux
	h = s.Auth.Middleware(h)
	h = http.NewCrossOriginProtection().Handler(h)
	h = promhttp.InstrumentHandlerCounter(s.requests, h)
	return securityHeaders(h)
}

// OpsHandler serves only /healthz, /readyz and /metrics — for a second
// listener on the pod IP when the UI listens on localhost behind an
// oauth2-proxy sidecar (kubelet probes and Prometheus can't sign in).
func (s *Server) OpsHandler() http.Handler {
	s.initMetrics()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", ok)
	mux.HandleFunc("GET /readyz", ok)
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{}))
	return mux
}

// initMetrics creates the registry once: Handler and OpsHandler share it.
func (s *Server) initMetrics() {
	if s.registry != nil {
		return
	}
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

// fallback answers addresses no route owns. A browser navigating (GET,
// wants HTML) gets the UI, whose router shows its not-found page and
// will own the address once its page moves to React; anything else gets
// the plain 404.
func (s *Server) fallback(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
		webui.NotFound().ServeHTTP(w, r)
		return
	}
	s.notFound(w, r)
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
