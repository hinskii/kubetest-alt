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

// Command control-center is the web UI over one or more kubetest
// installations. Stateless: no database; it reads and writes kubetest
// through each cluster's API server. Run it behind oauth2-proxy.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hinskii/kubetest-alt/internal/controlcenter/auth"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/clusters"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/config"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/server"
	"github.com/hinskii/kubetest-alt/internal/controlcenter/views"
)

func main() {
	var (
		configPath  string
		listen      string
		opsListen   string
		emailHeader string
		devUser     string
	)
	flag.StringVar(&configPath, "config", "/etc/control-center/config.yaml", "Path to the configuration file.")
	flag.StringVar(&listen, "listen", ":8080", "HTTP listen address.")
	flag.StringVar(&opsListen, "ops-listen", "",
		"Extra listen address for /healthz, /readyz and /metrics only (empty = none). "+
			"For probes and Prometheus when --listen is localhost behind an oauth2-proxy sidecar.")
	flag.StringVar(&emailHeader, "email-header", auth.HeaderEmail,
		"Header carrying the signed-in email: "+auth.HeaderEmail+" (oauth2-proxy auth_request mode behind an ingress) or "+
			auth.HeaderForwardedEmail+" (oauth2-proxy as reverse proxy). The proxy in front must strip it from client requests.")
	flag.StringVar(&devUser, "dev-user", "",
		"Email to act as when no oauth2-proxy header is present. Development only; refused when environment is production.")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, log, configPath, listen, opsListen, emailHeader, devUser); err != nil {
		log.Error("control-center failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger, configPath, listen, opsListen, emailHeader, devUser string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	resolver, err := auth.NewResolver(cfg, devUser)
	if err != nil {
		return err
	}
	resolver.EmailHeader = emailHeader
	reg, err := clusters.New(ctx, cfg.Clusters, clusters.Options{})
	if err != nil {
		return err
	}
	v, err := views.New()
	if err != nil {
		return err
	}
	cc := &server.Server{Clusters: reg, Auth: resolver, Views: v, Log: log,
		LiveViewKey: []byte(os.Getenv("CC_LIVE_VIEW_KEY"))}
	srv := &http.Server{Addr: listen, Handler: cc.Handler(), ReadHeaderTimeout: 10 * time.Second}
	servers := []*http.Server{srv}
	if opsListen != "" {
		servers = append(servers,
			&http.Server{Addr: opsListen, Handler: cc.OpsHandler(), ReadHeaderTimeout: 10 * time.Second})
	}

	errc := make(chan error, len(servers))
	for _, s := range servers {
		go func() { errc <- s.ListenAndServe() }()
	}
	log.Info("control-center listening", "addr", listen, "ops", opsListen, "emailHeader", emailHeader,
		"clusters", len(cfg.Clusters), "environment", cfg.Environment)

	select {
	case err := <-errc:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, s := range servers {
		if err := s.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("shutdown: %w", err)
		}
	}
	return nil
}
