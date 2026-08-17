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

package fetcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/hinskii/kubetest-alt/pkg/executor"
)

// FetchErrorPrefix is the machine-readable prefix the controller (step 04)
// grep-matches on init-container termination messages. Format: "FETCH_ERROR: <reason>".
// The exact prefix is part of the operator↔fetcher contract — changing it
// requires a controller update in lockstep.
const FetchErrorPrefix = "FETCH_ERROR:"

// TerminationMessagePath is where k8s reads the init container's failure
// reason from. Writing FETCH_ERROR here surfaces the message in
// pod.status.initContainerStatuses[i].state.terminated.message, which the
// controller reads via AnalyzePod.
const TerminationMessagePath = "/dev/termination-log"

// EntryConfig is the runtime config the init container binary reads at start.
// Extracted from Env so tests can inject deterministic values without touching
// the process env.
type EntryConfig struct {
	// ContentPath — path to content.json (default: /etc/kubetest/content.json).
	ContentPath string
	// DataDir — where fetched content should land (from KUBETEST_DATADIR).
	DataDir string
	// TimeoutSeconds — hard cap on the whole fetch (from KUBETEST_FETCH_TIMEOUT_SECONDS
	// or DefaultFetchTimeoutSeconds).
	TimeoutSeconds int
	// TerminationMessagePath — where to write FETCH_ERROR for k8s to surface
	// on failure. Empty disables the write (tests use "" to avoid touching /dev).
	TerminationMessagePath string
	// Stdout/Stderr — writers the fetcher inherits. cmd/entry sets to
	// os.Stdout/os.Stderr; tests inject buffers.
	Stdout io.Writer
	Stderr io.Writer
}

// DefaultConfig returns the config /entry fetch reads at pod start.
func DefaultConfig() EntryConfig {
	dataDir := os.Getenv(executor.EnvDataDir)
	if dataDir == "" {
		dataDir = DefaultDataDir
	}
	timeoutSec := DefaultFetchTimeoutSeconds
	if v := os.Getenv(EnvFetchTimeoutSeconds); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			timeoutSec = n
		}
	}
	return EntryConfig{
		ContentPath:            filepath.Join("/etc/kubetest", executor.ContentFileName),
		DataDir:                dataDir,
		TimeoutSeconds:         timeoutSec,
		TerminationMessagePath: TerminationMessagePath,
		Stdout:                 os.Stdout,
		Stderr:                 os.Stderr,
	}
}

// RunEntry is /entry fetch's whole lifecycle: load content.json → Fetch →
// write FETCH_ERROR on failure. Called by cmd/entry when argv[1] == "fetch".
//
// Returns nil on success (caller exits 0), non-nil on any failure (caller
// exits 1). Failure has ALREADY been written to stdout as `FETCH_ERROR: <msg>`
// and (best-effort) to /dev/termination-log before this returns.
func RunEntry(ctx context.Context, cfg EntryConfig) error {
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}

	// Timeout the whole fetch — plan step-06 "hard timeout on fetch".
	if cfg.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	content, err := loadContent(cfg.ContentPath)
	if err != nil {
		return emitFetchError(cfg, fmt.Sprintf("load %s: %v", cfg.ContentPath, err))
	}

	f := NewFetcher()
	f.Stdout = cfg.Stdout
	f.Stderr = cfg.Stderr
	if err := f.Fetch(ctx, content, cfg.DataDir); err != nil {
		reason := err.Error()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			reason = fmt.Sprintf("timeout after %ds: %v", cfg.TimeoutSeconds, err)
		}
		return emitFetchError(cfg, reason)
	}
	return nil
}

func loadContent(path string) (Content, error) {
	// #nosec G304 -- path is /etc/kubetest/content.json (operator-controlled
	// projection) in production; tests inject their own path.
	b, err := os.ReadFile(path)
	if err != nil {
		return Content{}, err
	}
	var c Content
	if err := json.Unmarshal(b, &c); err != nil {
		return Content{}, fmt.Errorf("parse json: %w", err)
	}
	return c, nil
}

// emitFetchError writes the FETCH_ERROR line to stdout as the last thing the
// container prints, and best-effort to /dev/termination-log so k8s surfaces
// it on pod.status. Returns an error carrying the same reason so cmd/entry
// exits non-zero.
func emitFetchError(cfg EntryConfig, reason string) error {
	line := FetchErrorPrefix + " " + reason
	_, _ = fmt.Fprintln(cfg.Stdout, line)

	// termination-log write is best-effort. In envtest / unit tests it points
	// at a temp file (or is empty to skip entirely). In production it's
	// /dev/termination-log which k8s exposes on the tmpfs inside the pod.
	if cfg.TerminationMessagePath != "" {
		// #nosec G304,G306 -- path is a compile-time constant in production;
		// tests inject a t.TempDir() location. World-readable is fine — this
		// is a status message, not a secret.
		_ = os.WriteFile(cfg.TerminationMessagePath, []byte(line), 0o644)
	}
	return errors.New(reason)
}
