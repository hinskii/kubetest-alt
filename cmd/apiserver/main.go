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
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/apiserver"
	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/pkg/storage"
)

// Manager-less setup on purpose: apiserver doesn't reconcile, doesn't need
// leader election, doesn't own CRDs. controller-runtime's cluster.Cluster
// gives us the shared informer cache + a caching client — exactly the two
// things §step-10 mandates.
func main() {
	var (
		listenAddr      string
		namespace       string
		minioEndpoint   string
		logsBucket      string
		artifactsBucket string
		minioAccessKey  string
		minioSecretKey  string
		minioUseSSL     bool
		postgresDSN     string
		presignExpiry   time.Duration
	)
	flag.StringVar(&listenAddr, "listen", ":8080", "HTTP listen address.")
	flag.StringVar(&namespace, "namespace", "", "Namespace to serve. Empty = cluster-wide.")
	flag.StringVar(&minioEndpoint, "minio-endpoint", "",
		"MinIO/S3 endpoint (host:port). Empty disables logs+artifacts (health still up).")
	flag.StringVar(&logsBucket, "minio-logs-bucket", "kubetest-logs",
		"Bucket holding log chunks (kubetest-logs/<runID>/<8d>.log).")
	flag.StringVar(&artifactsBucket, "minio-artifacts-bucket", "kubetest-artifacts",
		"Bucket holding scraped artifacts (<runID>/<relpath>).")
	flag.StringVar(&minioAccessKey, "minio-access-key", "",
		"MinIO access key (or $MINIO_ACCESS_KEY).")
	flag.StringVar(&minioSecretKey, "minio-secret-key", "",
		"MinIO secret key (or $MINIO_SECRET_KEY).")
	flag.BoolVar(&minioUseSSL, "minio-use-ssl", false, "Use https for MinIO.")
	flag.StringVar(&postgresDSN, "postgres-dsn", "",
		"Postgres DSN for run history (or $POSTGRES_DSN). Empty = cluster-only listing.")
	flag.DurationVar(&presignExpiry, "presign-expiry", 15*time.Minute,
		"Presigned artifact URL expiry.")

	zapOpts := zap.Options{Development: true}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()
	logger := zap.New(zap.UseFlagOptions(&zapOpts))
	ctrl.SetLogger(logger)
	setupLog := ctrl.Log.WithName("apiserver-setup")

	if minioAccessKey == "" {
		minioAccessKey = os.Getenv("MINIO_ACCESS_KEY")
	}
	if minioSecretKey == "" {
		minioSecretKey = os.Getenv("MINIO_SECRET_KEY")
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

	srv := &apiserver.Server{
		K8sClient:          cl.GetClient(),
		Namespace:          namespace,
		LogsBucket:         logsBucket,
		ArtifactsBucket:    artifactsBucket,
		PresignedURLExpiry: presignExpiry,
	}

	// MinIO wiring — one client fulfills Downloader + Lister + Presigner.
	// If not configured, log endpoints return 503; other endpoints work.
	if minioEndpoint != "" {
		mc, err := storage.NewMinIO(storage.Config{
			Endpoint:  minioEndpoint,
			Bucket:    artifactsBucket, // presigner uses per-call bucket; this is placeholder
			UseSSL:    minioUseSSL,
			AccessKey: minioAccessKey,
			SecretKey: minioSecretKey,
		})
		if err != nil {
			setupLog.Error(err, "MinIO init failed — logs+artifacts disabled")
		} else {
			srv.Downloader = mc
			srv.Lister = mc
			srv.Presigner = mc
			setupLog.Info("MinIO wired", "endpoint", minioEndpoint)
		}
	} else {
		setupLog.Info("--minio-endpoint not set — /runs/*/logs and /runs/*/artifacts return 503")
	}

	// Postgres read wiring. Same shape as cmd/operator (§step-09), but
	// read-only: apiserver never writes to the DB, only merges its rows
	// into GET /runs and serves GET /runs/{uid} for archived rows.
	var pgPool *pgxpool.Pool
	if postgresDSN != "" {
		pool, err := pgxpool.New(ctx, postgresDSN)
		if err != nil {
			setupLog.Error(err, "pgxpool init failed — /runs archive listing disabled")
		} else {
			srv.Store = store.NewPostgres(pool)
			pgPool = pool
			setupLog.Info("Postgres run archive wired")
		}
	} else {
		setupLog.Info("--postgres-dsn not set — /runs returns cluster-only entries")
	}

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
