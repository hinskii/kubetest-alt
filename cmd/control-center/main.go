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
		configPath string
		listen     string
		devUser    string
	)
	flag.StringVar(&configPath, "config", "/etc/control-center/config.yaml", "Path to the configuration file.")
	flag.StringVar(&listen, "listen", ":8080", "HTTP listen address.")
	flag.StringVar(&devUser, "dev-user", "",
		"Email to act as when no oauth2-proxy header is present. Development only; refused when environment is production.")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, log, configPath, listen, devUser); err != nil {
		log.Error("control-center failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger, configPath, listen, devUser string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	resolver, err := auth.NewResolver(cfg, devUser)
	if err != nil {
		return err
	}
	reg, err := clusters.New(ctx, cfg.Clusters, clusters.Options{})
	if err != nil {
		return err
	}
	v, err := views.New()
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr: listen,
		Handler: (&server.Server{Clusters: reg, Auth: resolver, Views: v, Log: log,
			LiveViewKey: []byte(os.Getenv("CC_LIVE_VIEW_KEY"))}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("control-center listening", "addr", listen, "clusters", len(cfg.Clusters), "environment", cfg.Environment)

	select {
	case err := <-errc:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
