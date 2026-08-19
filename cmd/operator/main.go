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
	"crypto/tls"
	"flag"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/jackc/pgx/v5/pgxpool"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/compiler"
	"github.com/hinskii/kubetest-alt/internal/controller"
	"github.com/hinskii/kubetest-alt/internal/logstream"
	"github.com/hinskii/kubetest-alt/internal/store"
	webhookv1alpha1 "github.com/hinskii/kubetest-alt/internal/webhook/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/storage"
	// +kubebuilder:scaffold:imports
)

// placeholderContentFetcherImage is deliberately unusable in production so an
// operator started without --content-fetcher-image logs a loud warning and
// every TestRun fails fast with ImagePullBackOff (surfaced as phase=error via
// AnalyzePod). Step 06 ships the real image.
const placeholderContentFetcherImage = "ghcr.io/hinskii/kubetest-alt/content-fetcher:v0.0.0-placeholder"

// (Workflows model, step 11: --executor-images and parseExecutorImages
// were removed along with compiler.DefaultExecutorImages. The main
// container's image comes verbatim from spec.container.image.)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(testsv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")

	// Compiler-facing flags — feed into every TestRun compile.
	var contentFetcherImage string
	var imageRegistry string
	flag.StringVar(&contentFetcherImage, "content-fetcher-image",
		placeholderContentFetcherImage,
		"Init container image supplying /entry (workflows model, step 11). "+
			"Also runs the content-fetcher subcommand. MUST be overridden in "+
			"production; the placeholder default is unusable.")
	flag.StringVar(&imageRegistry, "image-registry", "",
		"If set, prepended to the content-fetcher image reference (workflows "+
			"model: main image comes verbatim from spec.container.image, not "+
			"through this prefix).")

	// MinIO wiring (step 07). When --minio-endpoint is empty, artifact
	// scraping is disabled and the controller uses NoResultReader. Setting
	// endpoint enables both the wrapper-side scraper (via env on wrapper
	// container) and the controller-side ResultReader.
	var minioEndpoint, minioBucket, minioSecret, minioAccessKey, minioSecretKey string
	var minioUseSSL bool
	flag.StringVar(&minioEndpoint, "minio-endpoint", "",
		"MinIO/S3 endpoint (host:port). Empty disables artifact scraping.")
	flag.StringVar(&minioBucket, "minio-bucket", compiler.MinIODefaultBucket,
		"Object-store bucket for artifacts + result.json.")
	flag.StringVar(&minioSecret, "minio-secret-name", "",
		"Name of the Secret in the run's namespace holding AWS_ACCESS_KEY_ID + "+
			"AWS_SECRET_ACCESS_KEY. Injected as envFrom on the wrapper container.")
	flag.BoolVar(&minioUseSSL, "minio-use-ssl", false,
		"Use https:// for the MinIO/S3 client.")
	flag.StringVar(&minioAccessKey, "minio-access-key", "",
		"Access key for the OPERATOR's own MinIO client (result.json download). "+
			"Wrapper uses --minio-secret-name; the operator needs its own creds. "+
			"Alternatively set via env $MINIO_ACCESS_KEY.")
	flag.StringVar(&minioSecretKey, "minio-secret-key", "",
		"Secret key for the operator's MinIO client. Or $MINIO_SECRET_KEY.")

	// Log-streaming (step 08). When enabled, the operator opens a follow=true
	// PodLogs stream for every Running pod, fans out to any subscribers, and
	// flushes chunk-objects to the MinIO bucket. Disabled by default because
	// it requires pods/log RBAC.
	var logsEnabled bool
	flag.BoolVar(&logsEnabled, "logs-enabled", false,
		"Tail pod logs, fan out to subscribers, and flush chunk-objects to MinIO. "+
			"Requires MinIO to be configured (--minio-endpoint) and RBAC for pods/log.")

	// Postgres run-history store (step 09). Empty DSN → no store, controller
	// runs without persisting finished TestRuns (a valid dev-mode config).
	// DSN accepts either "postgres://..." or "key=value ..." forms.
	// Fallback: $POSTGRES_DSN for k8s-Secret-mounted deployments.
	var postgresDSN string
	flag.StringVar(&postgresDSN, "postgres-dsn", "",
		"Postgres DSN for run-history + retention. Empty disables persistence. "+
			"Or set via $POSTGRES_DSN.")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	// Env-var fallbacks for operator's own creds — Kubernetes deployments
	// mount these from a Secret volume; flags are for local dev.
	if minioAccessKey == "" {
		minioAccessKey = os.Getenv("MINIO_ACCESS_KEY")
	}
	if minioSecretKey == "" {
		minioSecretKey = os.Getenv("MINIO_SECRET_KEY")
	}
	if postgresDSN == "" {
		postgresDSN = os.Getenv("POSTGRES_DSN")
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if contentFetcherImage == placeholderContentFetcherImage {
		// Loud, not fatal — the placeholder still starts the operator so
		// step-06 development doesn't block, but every TestRun will fail
		// with ImagePullBackOff and surface a "infra: ImagePullBackOff"
		// message via the AnalyzePod path. Beats debugging silent failure.
		setupLog.Info("WARNING: --content-fetcher-image is at its placeholder value; " +
			"every TestRun will fail with ImagePullBackOff. Set a real image before running tests.")
	}

	compilerOpts := compiler.Options{
		ContentFetcherImage: contentFetcherImage,
		ImageRegistry:       imageRegistry,
		MinIO: compiler.MinIOOptions{
			Endpoint:   minioEndpoint,
			Bucket:     minioBucket,
			SecretName: minioSecret,
			UseSSL:     minioUseSSL,
		},
	}

	// ResultReader wiring — when MinIO is configured, the controller reads
	// result.json from the bucket. Without MinIO, NoResultReader falls the
	// controller back to Pod terminated state analysis (§15.2).
	var resultReader controller.ResultReader = controller.NoResultReader{}
	if minioEndpoint != "" {
		mc, err := storage.NewMinIO(storage.Config{
			Endpoint:  minioEndpoint,
			Bucket:    minioBucket,
			UseSSL:    minioUseSSL,
			AccessKey: minioAccessKey,
			SecretKey: minioSecretKey,
		})
		if err != nil {
			setupLog.Error(err, "Failed to init MinIO client — controller will use NoResultReader")
		} else {
			resultReader = controller.NewStorageResultReader(mc, minioBucket)
			setupLog.Info("MinIO result reader wired", "endpoint", minioEndpoint, "bucket", minioBucket)
		}
	} else {
		setupLog.Info("--minio-endpoint not set — artifact scraping disabled, using NoResultReader")
	}

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("Disabling HTTP/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts
	webhookServerOptions := webhook.Options{
		TLSOpts: webhookTLSOpts,
	}

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}

	webhookServer := webhook.NewServer(webhookServerOptions)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	restCfg := ctrl.GetConfigOrDie()

	// Postgres run-history (step 09). Migrations under advisory lock so
	// rolling-update replicas serialize their attempts. Pool lifetime spans
	// the whole manager; closed on exit.
	var runStore controller.RunStorePersister
	var pgPool *pgxpool.Pool
	if postgresDSN != "" {
		migCtx, migCancel := context.WithTimeout(context.Background(), 60*time.Second)
		if err := store.ApplyMigrations(migCtx, postgresDSN); err != nil {
			migCancel()
			setupLog.Error(err, "Postgres migrations failed — run-history disabled")
		} else {
			migCancel()
			poolCtx, poolCancel := context.WithTimeout(context.Background(), 30*time.Second)
			pool, err := pgxpool.New(poolCtx, postgresDSN)
			poolCancel()
			if err != nil {
				setupLog.Error(err, "pgxpool init failed — run-history disabled")
			} else {
				pgStore := store.NewPostgres(pool)
				// Wire metric-parse warnings into controller-runtime's logger
				// so unparseable values surface without failing the save.
				pgStore.Warn = func(w store.MetricParseWarning) {
					setupLog.Info("metric parse skipped",
						"key", w.Key, "value", w.RawValue, "reason", w.Err.Error())
				}
				// Ensure partitions covering the current retention window +
				// a month of lookahead so a month-rollover doesn't wedge the
				// first write of the new month.
				partCtx, partCancel := context.WithTimeout(context.Background(), 30*time.Second)
				parts := store.PartitionsToCreate(time.Now().UTC(),
					30*24*time.Hour, 32*24*time.Hour)
				if err := pgStore.EnsurePartitions(partCtx, parts); err != nil {
					setupLog.Error(err, "EnsurePartitions failed — run-history disabled")
				} else {
					runStore = pgStore
					pgPool = pool
					setupLog.Info("Postgres run-history enabled", "partitions", len(parts))
				}
				partCancel()
			}
		}
	} else {
		setupLog.Info("--postgres-dsn not set — run-history disabled (dev mode)")
	}

	// Log-streaming registry. Wired only when --logs-enabled is set; the
	// reconciler treats a nil LogRegistry as "logs disabled" and never calls
	// into it. We also need MinIO up — chunk flushes go there.
	var logRegistry controller.LogRegistry
	var logRegistryConcrete *logstream.Registry
	if logsEnabled {
		if minioEndpoint == "" {
			setupLog.Info("WARNING: --logs-enabled requires --minio-endpoint; log streaming disabled")
		} else {
			kubeClient, err := kubernetes.NewForConfig(restCfg)
			if err != nil {
				setupLog.Error(err, "Failed to build kubernetes client for log source; log streaming disabled")
			} else {
				uploader, uerr := storage.NewMinIO(storage.Config{
					Endpoint:  minioEndpoint,
					Bucket:    minioBucket,
					UseSSL:    minioUseSSL,
					AccessKey: minioAccessKey,
					SecretKey: minioSecretKey,
				})
				if uerr != nil {
					setupLog.Error(uerr, "Failed to init MinIO client for logs; log streaming disabled")
				} else {
					src := &logstream.K8sLogSource{Client: kubeClient}
					// uploader is *storage.MinIO which also implements Remover —
					// same client wipes the run's chunk-prefix on restart.
					logRegistryConcrete = logstream.NewRegistry(src, uploader, uploader, minioBucket)
					logRegistry = logRegistryConcrete
					setupLog.Info("log streaming enabled", "bucket", minioBucket)
				}
			}
		}
	}

	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "ebf5d169.kubetest.io",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	// nolint:goconst
	if os.Getenv("ENABLE_WEBHOOKS") != "false" {
		if err := webhookv1alpha1.SetupTestWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "Failed to create webhook", "webhook", "Test")
			os.Exit(1)
		}
	}
	// nolint:goconst
	if os.Getenv("ENABLE_WEBHOOKS") != "false" {
		if err := webhookv1alpha1.SetupTestRunWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "Failed to create webhook", "webhook", "TestRun")
			os.Exit(1)
		}
	}

	if err := (&controller.TestRunReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		CompilerOpts: compilerOpts,
		Results:      resultReader, // step 07: real MinIO reader when configured
		LogRegistry:  logRegistry,  // step 08: nil when --logs-enabled=false
		RunStore:     runStore,     // step 09: nil when --postgres-dsn empty
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "TestRun")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
	// Manager exited — flush remaining tailers so shutdown doesn't leak
	// goroutines and the last chunks land in MinIO.
	if logRegistryConcrete != nil {
		logRegistryConcrete.Shutdown()
	}
	if pgPool != nil {
		pgPool.Close()
	}
}
