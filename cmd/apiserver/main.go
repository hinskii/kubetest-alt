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

package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/apiserver"
	"github.com/hinskii/kubetest-alt/internal/metrics"
	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

// Manager-less setup on purpose: apiserver doesn't reconcile, doesn't need
// leader election, doesn't own CRDs. controller-runtime's cluster.Cluster
// gives us the shared informer cache + a caching client — exactly the two
// things §step-10 mandates.
func main() {
	var (
		listenAddr    string
		namespace     string
		postgresDSN   string
		presignExpiry time.Duration
		tokenFile     string
	)
	flag.StringVar(&listenAddr, "listen", ":8080", "HTTP listen address.")
	flag.StringVar(&namespace, "namespace", "",
		"Namespace to serve. Empty = cluster-wide (callers pass ?namespace= per request).")
	// Same flags as the operator (pkg/storage.BindFlags) — must point at
	// the same backend and bucket the operator writes to.
	storageFlags := storage.BindFlags(flag.CommandLine)
	flag.StringVar(&postgresDSN, "postgres-dsn", "",
		"Postgres DSN for run history (or $POSTGRES_DSN). Empty = cluster-only listing.")
	flag.DurationVar(&presignExpiry, "presign-expiry", 15*time.Minute,
		"Presigned artifact URL expiry.")
	flag.StringVar(&tokenFile, "auth-token-file", "",
		"File holding the API token every request must carry in "+apiclient.HeaderToken+
			" (probes and /metrics excepted). Empty = no authentication: development only.")

	zapOpts := zap.Options{Development: true}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()
	logger := zap.New(zap.UseFlagOptions(&zapOpts))
	ctrl.SetLogger(logger)
	setupLog := ctrl.Log.WithName("apiserver-setup")

	storageCfg := storageFlags.Config()
	if err := storageCfg.Validate(); err != nil {
		setupLog.Error(err, "invalid storage flags")
		os.Exit(1)
	}
	if postgresDSN == "" {
		postgresDSN = os.Getenv("POSTGRES_DSN")
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(testsv1alpha1.AddToScheme(scheme))

	restCfg := ctrl.GetConfigOrDie()
	cl, err := cluster.New(restCfg, func(o *cluster.Options) {
		o.Scheme = scheme
	})
	if err != nil {
		setupLog.Error(err, "failed to init cluster")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Start the informer cache and block until it syncs. Handlers depend
	// on the cache being warm — otherwise the first GET /tests returns
	// empty for a beat.
	go func() {
		if err := cl.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			setupLog.Error(err, "cluster stopped with error")
		}
	}()
	if !cl.GetCache().WaitForCacheSync(ctx) {
		setupLog.Error(nil, "cache sync failed")
		os.Exit(1)
	}
	setupLog.Info("informer cache synced")

	var token string
	if tokenFile != "" {
		b, err := os.ReadFile(tokenFile) // #nosec G304 -- operator-supplied flag
		if err != nil {
			setupLog.Error(err, "read --auth-token-file")
			os.Exit(1)
		}
		if token = strings.TrimSpace(string(b)); len(token) < 32 {
			setupLog.Error(nil, "the API token must be at least 32 characters", "file", tokenFile)
			os.Exit(1)
		}
	} else {
		setupLog.Info("WARNING: no --auth-token-file — the API accepts unauthenticated requests (development only)")
	}

	srv := &apiserver.Server{
		AuthToken:          token,
		K8sClient:          cl.GetClient(),
		Namespace:          namespace,
		Bucket:             storageCfg.Bucket,
		PresignedURLExpiry: presignExpiry,
	}

	// Object storage — one backend serves logs, artifacts, presigning and
	// deletes. Without it those endpoints return 503; the rest work.
	if storageCfg.Enabled() {
		backend, err := storage.New(ctx, storageCfg)
		if err != nil {
			setupLog.Error(err, "object storage init failed — logs+artifacts disabled", "type", storageCfg.Type)
		} else {
			srv.Downloader = backend
			srv.Lister = backend
			srv.Presigner = backend
			srv.Remover = backend
			setupLog.Info("object storage wired", "type", storageCfg.Type, "bucket", storageCfg.Bucket)
		}
	} else {
		setupLog.Info("--storage-type not set — /runs/*/logs and /runs/*/artifacts return 503")
	}

	// Postgres wiring. Same shape as cmd/operator (§step-09). The operator
	// writes run rows; the apiserver reads them (GET /runs, archived
	// GET /runs/{uid}) and writes only user state: deletes, comments and
	// the audit log.
	//
	// Migrations run here too (advisory-locked, idempotent): a new API
	// server reads columns a new migration adds, and must not depend on
	// the operator's pod having started first.
	var pgPool *pgxpool.Pool
	if postgresDSN != "" {
		migCtx, migCancel := context.WithTimeout(ctx, 60*time.Second)
		migErr := store.ApplyMigrations(migCtx, postgresDSN)
		migCancel()
		if migErr != nil {
			setupLog.Error(migErr, "Postgres migrations failed — /runs archive listing disabled")
		} else if pool, err := pgxpool.New(ctx, postgresDSN); err != nil {
			setupLog.Error(err, "pgxpool init failed — /runs archive listing disabled")
		} else {
			pg := store.NewPostgres(pool)
			srv.Store = pg
			srv.Deleter = pg
			srv.Commenter = pg
			srv.Audit = pg
			srv.Cases = pg
			pgPool = pool
			setupLog.Info("Postgres run archive wired")
		}
	} else {
		setupLog.Info("--postgres-dsn not set — /runs returns cluster-only entries")
	}

	// Prometheus registry + /metrics handler. Registered on a dedicated
	// registry (not the default) so a rogue module can't leak metrics
	// through this apiserver without going through metrics.All().
	apiRegistry := prometheus.NewRegistry()
	for _, c := range metrics.All() {
		apiRegistry.MustRegister(c)
	}
	srv.MetricsHandler = promhttp.HandlerFor(apiRegistry, promhttp.HandlerOpts{})

	httpSrv := &http.Server{
		Addr:              listenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Shutdown handler — wait for ctx (SIGINT/SIGTERM) then drain.
	go func() {
		<-ctx.Done()
		setupLog.Info("shutting down")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		if pgPool != nil {
			pgPool.Close()
		}
	}()

	setupLog.Info("listening", "addr", listenAddr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		setupLog.Error(err, "listen failed")
		os.Exit(1)
	}
}
