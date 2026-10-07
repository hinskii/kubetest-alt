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
	"fmt"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	ctrltypes "k8s.io/apimachinery/pkg/types"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/discovery"
	memcached "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/restmapper"
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
	"github.com/hinskii/kubetest-alt/internal/metrics"
	"github.com/hinskii/kubetest-alt/internal/retention"
	"github.com/hinskii/kubetest-alt/internal/scheduler"
	"github.com/hinskii/kubetest-alt/internal/store"
	"github.com/hinskii/kubetest-alt/internal/webhookdelivery"

	webhookv1alpha1 "github.com/hinskii/kubetest-alt/internal/webhook/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/storage"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
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

	// Register kubetest metrics on controller-runtime's shared registry
	// (backs /metrics on the manager's metrics endpoint). Doing this in
	// init means every operator binary gets the same set — no risk of
	// forgetting one at wire-up time.
	for _, c := range metrics.All() {
		ctrlmetrics.Registry.MustRegister(c)
	}
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

	// Object storage (S3-compatible or GCS). Unset --storage-type disables
	// artifacts, stored logs and result.json; the controller then judges
	// runs from pod state (NoResultReader).
	storageFlags := storage.BindFlags(flag.CommandLine)
	var storageSecret string
	flag.StringVar(&storageSecret, "storage-secret-name", "",
		"S3 only: Secret in each run's namespace with AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY, "+
			"injected into the wrapper via envFrom. Empty = the AWS credential chain in the pod (e.g. IRSA). "+
			"GCS uses the run pod's service account (Workload Identity) instead.")

	// Log-streaming (step 08). When enabled, the operator opens a follow=true
	// PodLogs stream for every Running pod, fans out to any subscribers, and
	// flushes chunk-objects to object storage. Disabled by default because
	// it requires pods/log RBAC.
	var logsEnabled bool
	flag.BoolVar(&logsEnabled, "logs-enabled", false,
		"Tail pod logs, fan out to subscribers, and flush chunk-objects to object storage. "+
			"Requires object storage (--storage-type) and RBAC for pods/log.")

	// Postgres run-history store (step 09). Empty DSN → no store, controller
	// runs without persisting finished TestRuns (a valid dev-mode config).
	// DSN accepts either "postgres://..." or "key=value ..." forms.
	// Fallback: $POSTGRES_DSN for k8s-Secret-mounted deployments.
	var postgresDSN string
	flag.StringVar(&postgresDSN, "postgres-dsn", "",
		"Postgres DSN for run-history + retention. Empty disables persistence. "+
			"Or set via $POSTGRES_DSN.")

	// Retention (§9, fixes.md #4): how long run history, its objects and
	// the audit log are kept. Needs --postgres-dsn.
	var retentionDays int
	flag.IntVar(&retentionDays, "retention-days", 30,
		"Days to keep finished runs (history rows, their logs/artifacts/results) and audit entries; "+
			"removal is per month, so runs live up to a month longer. 0 keeps everything.")

	// Finished TestRuns leave the cluster once they're in run history.
	var finishedRunTTL time.Duration
	flag.DurationVar(&finishedRunTTL, "finished-run-ttl", time.Hour,
		"How long a finished TestRun stays in the cluster after it reached run history; then it is deleted "+
			"(history, logs and artifacts stay until retention). 0 keeps finished TestRuns. Needs --postgres-dsn.")

	// Step 12 tuning knobs. Defaults match the plan: cron tick every 30s,
	// trigger gate evaluation every 1s. Both are safe to leave at defaults
	// in production — testing knobs are here for kind runs / debugging.
	var schedulerTickInterval time.Duration
	var triggerGateEvalInterval time.Duration
	flag.DurationVar(&schedulerTickInterval, "scheduler-tick-interval", 30*time.Second,
		"How often the cron scheduler evaluates Tests for scheduled instants. "+
			"Behind manager leader election. Reduce for tighter cron responsiveness.")
	flag.DurationVar(&triggerGateEvalInterval, "trigger-gate-eval-interval", 1*time.Second,
		"How often TestTrigger pending gates are evaluated for "+
			"conditions/timeout/delay resolution. Behind leader election.")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	storageCfg := storageFlags.Config()
	if err := storageCfg.Validate(); err != nil {
		setupLog.Error(err, "invalid storage flags")
		os.Exit(1)
	}
	// One backend for everything the operator does with object storage:
	// reading result.json and streaming log chunks.
	var objectStore storage.Backend
	if storageCfg.Enabled() {
		var err error
		objectStore, err = storage.New(context.Background(), storageCfg)
		if err != nil {
			setupLog.Error(err, "object storage init failed — running without it", "type", storageCfg.Type)
		} else {
			setupLog.Info("object storage wired", "type", storageCfg.Type, "bucket", storageCfg.Bucket)
		}
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
		Storage:             compiler.StorageOptions{Config: storageCfg, SecretName: storageSecret},
	}

	// ResultReader: result.json from object storage when configured;
	// otherwise NoResultReader falls back to pod terminated state (§15.2).
	var resultReader controller.ResultReader = controller.NoResultReader{}
	if objectStore != nil {
		resultReader = controller.NewStorageResultReader(objectStore, storageCfg.Bucket)
	} else {
		setupLog.Info("no object storage — no artifacts, no stored logs, verdicts from pod state")
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
					time.Duration(max(retentionDays, 1))*24*time.Hour, 32*24*time.Hour)
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
	// into it. Needs object storage — chunk flushes go there.
	var logRegistry controller.LogRegistry
	var logRegistryConcrete *logstream.Registry
	if logsEnabled {
		if objectStore == nil {
			setupLog.Info("WARNING: --logs-enabled requires object storage (--storage-type); log streaming disabled")
		} else {
			kubeClient, err := kubernetes.NewForConfig(restCfg)
			if err != nil {
				setupLog.Error(err, "Failed to build kubernetes client for log source; log streaming disabled")
			} else {
				src := &logstream.K8sLogSource{Client: kubeClient}
				logRegistryConcrete = logstream.NewRegistry(src, objectStore, objectStore, objectStore, storageCfg.Bucket)
				logRegistry = logRegistryConcrete
				setupLog.Info("log streaming enabled", "bucket", storageCfg.Bucket)
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

	// Step 14: outbound webhook dispatcher. Async pool with retry +
	// secret-safe logs. Wired into the reconciler below via the
	// WebhookDispatcher field; a nil dispatcher disables the outbound
	// path entirely (unit tests / dev clusters). The SecretResolver
	// pulls Secret-backed header values from the manager cache — same
	// namespace as the Webhook CR.
	webhookDispatcher := &webhookdelivery.Dispatcher{
		Logger:  setupLog.WithName("webhook-dispatcher"),
		Secrets: newSecretResolver(mgr.GetClient()),
	}
	webhookDispatcher.Start(context.Background())

	if err := (&controller.TestRunReconciler{
		Client:       mgr.GetClient(),
		APIReader:    mgr.GetAPIReader(), // orphan-detection cache-lag guard
		Scheme:       mgr.GetScheme(),
		CompilerOpts: compilerOpts,
		Results:      resultReader, // object storage reader when configured
		LogRegistry:  logRegistry,  // step 08: nil when --logs-enabled=false
		RunStore:     runStore,     // step 09: nil when --postgres-dsn empty
		// Finished CRs leave etcd once in run history (no-op without a store).
		FinishedRunTTL: finishedRunTTL,
		// Step 13: template resolution. Store reads TestTemplates from the
		// manager cache; ResolverEnv is intentionally empty by default —
		// the operator does NOT project os.Environ() into `{{ env.* }}`
		// (would leak operator-pod secrets). Populate via a future
		// --resolver-env flag when a use case appears.
		TemplateStore:     &controller.ClientTemplateStore{Client: mgr.GetClient()},
		ResolverEnv:       nil,
		WebhookDispatcher: webhookDispatcher, // step 14
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "TestRun")
		os.Exit(1)
	}

	// Step 12: cron scheduler + TestTrigger controller. Both live in-process,
	// behind manager leader election (their Runnables opt in via
	// NeedLeaderElection()). No separate binaries, no CronJob-per-Test.
	if err := mgr.Add(&scheduler.Scheduler{
		Client:       mgr.GetClient(),
		Clock:        scheduler.RealClock{},
		TickInterval: schedulerTickInterval,
	}); err != nil {
		setupLog.Error(err, "Failed to add scheduler runnable")
		os.Exit(1)
	}

	// Retention: leader-elected, hourly. Without object storage only the
	// history rows go; objects then need a bucket lifecycle rule.
	if pg, ok := runStore.(*store.Postgres); ok && retentionDays > 0 {
		job := &retention.Job{
			Store:     pg,
			Bucket:    storageCfg.Bucket,
			Retention: time.Duration(retentionDays) * 24 * time.Hour,
			Log:       ctrl.Log.WithName("retention"),
		}
		if objectStore != nil {
			job.Objects = objectStore
		}
		if err := mgr.Add(job); err != nil {
			setupLog.Error(err, "Failed to add retention runnable")
			os.Exit(1)
		}
		setupLog.Info("retention enabled", "days", retentionDays)
	}

	// Dynamic client + REST mapper feed the TestTrigger reconciler's
	// per-GVK informer machinery. Mapper is cached in-memory so repeated
	// resource resolutions don't re-hit discovery on every event.
	dynClient, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		setupLog.Error(err, "Failed to build dynamic client for TestTrigger controller")
		os.Exit(1)
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(restCfg)
	if err != nil {
		setupLog.Error(err, "Failed to build discovery client for RESTMapper")
		os.Exit(1)
	}
	cachedDisco := memcached.NewMemCacheClient(discoveryClient)
	restMapper := restmapper.NewDeferredDiscoveryRESTMapper(cachedDisco)

	if err := (&controller.TestTriggerReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Clock:            scheduler.RealClock{},
		Dyn:              dynClient,
		Mapper:           restMapper,
		GateEvalInterval: triggerGateEvalInterval,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "TestTrigger")
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
	// goroutines and the last chunks land in object storage.
	if logRegistryConcrete != nil {
		logRegistryConcrete.Shutdown()
	}
	if pgPool != nil {
		pgPool.Close()
	}
}

// newSecretResolver builds a webhookdelivery.SecretResolver backed by the
// controller-runtime client cache. Reads go through the manager cache
// (already watching Secrets via the reconciler's RBAC), so this is
// effectively a hashmap lookup after initial sync — no per-delivery
// API-server round-trip.
//
// Secret VALUES are returned to the caller (dispatcher) which then
// redacts them from log output. The value NEVER lives outside the
// dispatcher's request-building stack.
func newSecretResolver(c ctrlclient.Client) webhookdelivery.SecretResolver {
	return func(namespace, name, key string) (string, error) {
		var s corev1.Secret
		if err := c.Get(context.Background(),
			ctrltypes.NamespacedName{Namespace: namespace, Name: name}, &s); err != nil {
			return "", fmt.Errorf("get Secret %s/%s: %w", namespace, name, err)
		}
		v, ok := s.Data[key]
		if !ok {
			return "", fmt.Errorf("secret %s/%s has no key %q", namespace, name, key)
		}
		return string(v), nil
	}
}
