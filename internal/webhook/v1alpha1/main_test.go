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

package v1alpha1

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// Shared envtest across all tests in this package. Individual tests grab the
// package-level k8sClient once TestMain finishes bootstrapping.
var (
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
	cancelMgr context.CancelFunc
)

// TestMain boots envtest with CRDs + webhook configs, starts a manager hosting
// our webhooks, then runs the package's tests. Follows plan/README.md's
// "shared TestMain pattern".
func TestMain(m *testing.M) {
	code, err := runTests(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "envtest setup: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runTests(m *testing.M) (int, error) {
	repoRoot, err := findRepoRoot()
	if err != nil {
		return 0, fmt.Errorf("find repo root: %w", err)
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(testsv1alpha1.AddToScheme(scheme))

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(repoRoot, "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{filepath.Join(repoRoot, "config", "webhook")},
		},
		BinaryAssetsDirectory: firstEnvtestBinaryDir(filepath.Join(repoRoot, "bin", "k8s")),
	}

	if cfg, err = testEnv.Start(); err != nil {
		return 0, fmt.Errorf("start envtest: %w", err)
	}
	defer func() {
		// Best-effort teardown; log-and-continue on error so we don't mask the test verdict.
		if err := testEnv.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "envtest stop: %v\n", err)
		}
	}()

	if k8sClient, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		return 0, fmt.Errorf("new client: %w", err)
	}

	wh := &testEnv.WebhookInstallOptions
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    wh.LocalServingHost,
			Port:    wh.LocalServingPort,
			CertDir: wh.LocalServingCertDir,
		}),
		LeaderElection: false,
		Metrics:        metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return 0, fmt.Errorf("new manager: %w", err)
	}
	if err := SetupTestWebhookWithManager(mgr); err != nil {
		return 0, fmt.Errorf("setup Test webhook: %w", err)
	}
	if err := SetupTestRunWebhookWithManager(mgr); err != nil {
		return 0, fmt.Errorf("setup TestRun webhook: %w", err)
	}

	var mgrCtx context.Context
	mgrCtx, cancelMgr = context.WithCancel(context.Background())
	defer cancelMgr()

	mgrDone := make(chan error, 1)
	go func() { mgrDone <- mgr.Start(mgrCtx) }()

	if err := waitForWebhookReady(wh, 30*time.Second); err != nil {
		return 0, fmt.Errorf("webhook readiness: %w", err)
	}

	code := m.Run()

	cancelMgr()
	// Drain manager goroutine so tests don't race with envtest.Stop().
	select {
	case <-mgrDone:
	case <-time.After(10 * time.Second):
		fmt.Fprintln(os.Stderr, "manager did not exit within 10s of cancel")
	}
	return code, nil
}

func waitForWebhookReady(wh *envtest.WebhookInstallOptions, timeout time.Duration) error {
	addr := fmt.Sprintf("%s:%d", wh.LocalServingHost, wh.LocalServingPort)
	deadline := time.Now().Add(timeout)
	// #nosec G402 -- envtest issues self-signed serving certs; the test dials for TCP+TLS liveness only.
	tlsCfg := &tls.Config{InsecureSkipVerify: true}
	dialer := &net.Dialer{Timeout: time.Second}
	for {
		conn, err := tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("webhook not ready at %s: %w", addr, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// firstEnvtestBinaryDir mirrors kubebuilder's helper so tests work from IDEs
// without setting KUBEBUILDER_ASSETS explicitly (make setup-envtest still required).
func firstEnvtestBinaryDir(base string) string {
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			return filepath.Join(base, e.Name())
		}
	}
	return ""
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found walking up from cwd")
		}
		dir = parent
	}
}
