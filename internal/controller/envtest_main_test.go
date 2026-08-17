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

package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/compiler"
)

// Shared envtest state — all envtest tests in this package plug into the
// same control plane. Each test uses its own namespace to avoid cross-bleed.
var (
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
	scheme    *runtime.Scheme
	stopMgr   context.CancelFunc

	// reconcileCounts is (namespace/name → invocation count). Tests assert
	// counts stay bounded to catch hot-loops (step-04 acceptance).
	reconcileCounts sync.Map

	// fakeResults lets each test preload the wrapper's verdict per run ID.
	fakeResults *FakeResultReader
)

// FakeResultReader is the ResultReader used across envtest tests. Concurrent-
// safe because envtest reconciles run on manager goroutines.
type FakeResultReader struct {
	mu      sync.Mutex
	results map[string]*RunResult
	errs    map[string]error
}

func newFakeResultReader() *FakeResultReader {
	return &FakeResultReader{
		results: map[string]*RunResult{},
		errs:    map[string]error{},
	}
}

func (f *FakeResultReader) Set(runID string, result *RunResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[runID] = result
	delete(f.errs, runID)
}

func (f *FakeResultReader) SetErr(runID string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[runID] = err
	delete(f.results, runID)
}

func (f *FakeResultReader) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results = map[string]*RunResult{}
	f.errs = map[string]error{}
}

func (f *FakeResultReader) Read(_ context.Context, runID string) (*RunResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.errs[runID]; ok {
		return nil, err
	}
	if r, ok := f.results[runID]; ok {
		return r, nil
	}
	return nil, ErrResultNotFound
}

// countingReconciler wraps the real reconciler and bumps per-key counters.
type countingReconciler struct {
	inner reconcile.Reconciler
}

func (c *countingReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	nv, _ := reconcileCounts.LoadOrStore(req.NamespacedName, new(atomic.Int32))
	nv.(*atomic.Int32).Add(1)
	return c.inner.Reconcile(ctx, req)
}

func reconcileCount(key client.ObjectKey) int32 {
	if v, ok := reconcileCounts.Load(key); ok {
		return v.(*atomic.Int32).Load()
	}
	return 0
}

func resetReconcileCounts() {
	// NEVER reassign a sync.Map — background reconciles may be reading/writing
	// concurrently across test boundaries. Iterate + delete is safe.
	reconcileCounts.Range(func(k, _ any) bool {
		reconcileCounts.Delete(k)
		return true
	})
}

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
		return 0, err
	}

	scheme = runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(testsv1alpha1.AddToScheme(scheme))

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(repoRoot, "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: firstEnvtestBinaryDir(filepath.Join(repoRoot, "bin", "k8s")),
	}

	if cfg, err = testEnv.Start(); err != nil {
		return 0, fmt.Errorf("start envtest: %w", err)
	}
	defer func() {
		if err := testEnv.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "envtest stop: %v\n", err)
		}
	}()

	if k8sClient, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		return 0, err
	}

	mgr, err := manager.New(cfg, manager.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
	})
	if err != nil {
		return 0, err
	}

	fakeResults = newFakeResultReader()
	real := &TestRunReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		CompilerOpts: compiler.Options{ContentFetcherImage: "test/content-fetcher:v0"},
		Results:      fakeResults,
		// Short intervals for tests — production defaults are 30s / 10s.
		FallbackRequeue:         500 * time.Millisecond,
		ConcurrencyWaitInterval: 250 * time.Millisecond,
		Now:                     metav1.Now,
	}
	if err := setupWithCountingWrapper(mgr, real); err != nil {
		return 0, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	stopMgr = cancel
	defer stopMgr()
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()

	if !mgr.GetCache().WaitForCacheSync(ctx) {
		return 0, errors.New("cache sync failed")
	}

	code := m.Run()

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		fmt.Fprintln(os.Stderr, "manager did not exit within 10s of cancel")
	}
	return code, nil
}

// setupWithCountingWrapper mirrors TestRunReconciler.SetupWithManager but
// wraps the reconciler in countingReconciler for per-key invocation tracking.
// Kept in the test file so production code has no test hooks.
func setupWithCountingWrapper(mgr ctrl.Manager, r *TestRunReconciler) error {
	wrapped := &countingReconciler{inner: r}
	return ctrl.NewControllerManagedBy(mgr).
		For(&testsv1alpha1.TestRun{}).
		Owns(&batchv1.Job{}).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.mapPodToTestRun),
			builder.WithPredicates(hasRunIDLabelPredicate),
		).
		Named("testrun").
		Complete(wrapped)
}

// findRepoRoot walks up from cwd looking for go.mod.
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
			return "", errors.New("go.mod not found walking up from cwd")
		}
		dir = parent
	}
}

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
