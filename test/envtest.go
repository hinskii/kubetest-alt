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

// Package test hosts shared helpers for envtest-based tests.
// The helpers are intentionally minimal so each package can layer its own
// scheme/CRD wiring on top.
package test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// EnvtestOptions configures Start. All fields are optional.
type EnvtestOptions struct {
	// CRDDirectoryPaths is passed to envtest.Environment. Paths may be relative
	// to the repo root — Start resolves them via findRepoRoot.
	CRDDirectoryPaths []string
	// ErrorIfCRDPathMissing mirrors envtest.Environment.ErrorIfCRDPathMissing.
	ErrorIfCRDPathMissing bool
}

// Start boots an envtest control plane and returns its REST config plus a stop
// function. It uses KUBEBUILDER_ASSETS (set by `make setup-envtest`) to locate
// the apiserver/etcd binaries; if unset the caller must arrange for them.
//
// Failures abort the test via t.Fatalf. The returned stop func is registered
// with t.Cleanup so callers do not need to defer it, but it is returned for
// tests that need explicit control over teardown order.
func Start(t *testing.T, opts EnvtestOptions) (*rest.Config, func()) {
	t.Helper()

	root, err := findRepoRoot()
	require.NoError(t, err, "locate repo root")

	crdPaths := make([]string, 0, len(opts.CRDDirectoryPaths))
	for _, p := range opts.CRDDirectoryPaths {
		if filepath.IsAbs(p) {
			crdPaths = append(crdPaths, p)
			continue
		}
		crdPaths = append(crdPaths, filepath.Join(root, p))
	}

	env := &envtest.Environment{
		CRDDirectoryPaths:     crdPaths,
		ErrorIfCRDPathMissing: opts.ErrorIfCRDPathMissing,
	}

	cfg, err := env.Start()
	require.NoError(t, err, "start envtest")
	require.NotNil(t, cfg, "envtest returned nil rest.Config")

	stop := func() {
		if err := env.Stop(); err != nil {
			t.Logf("envtest stop: %v", err)
		}
	}
	t.Cleanup(stop)

	return cfg, stop
}
