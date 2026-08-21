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

// /entry is the in-container wrapper. Three subcommands from ONE binary:
//
//   - default: workflows-model tool wrapper. Reads request.json,
//     runs req.Command/Args verbatim, applies the optional verdictFrom
//     processor (junit/jtl), writes result.json. See pkg/executor.
//
//   - "fetch" (argv[1] == "fetch"): init-container mode. Reads content.json
//     from the projected ConfigMap and materializes git/files/tarball into
//     $KUBETEST_DATADIR. See pkg/executor/fetcher.
//
//   - "install" is IMPLICIT — the compiler's init container that copies
//     /entry into the shared kubetest-bin emptyDir uses `sh -c "cp
//     /entry /kubetest-bin/entry"` directly (see internal/compiler). No
//     Go-side subcommand needed for a one-line file copy.
//
// Workflows-model change (step 11): the wrapper has NO per-tool Runner
// dispatch. Tool identity lives in the kubetest.io/tool label on the
// Test, propagated to Job/Pod labels by the compiler — the wrapper
// itself is generic and doesn't need to know what tool it's running.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/hinskii/kubetest-alt/internal/scraper"
	"github.com/hinskii/kubetest-alt/pkg/executor"
	"github.com/hinskii/kubetest-alt/pkg/executor/fetcher"
	"github.com/hinskii/kubetest-alt/pkg/storage"
	verdictjtl "github.com/hinskii/kubetest-alt/pkg/verdict/jtl"
	verdictjunit "github.com/hinskii/kubetest-alt/pkg/verdict/junit"
)

func main() {
	// SIGTERM/SIGINT cancel the context — subcommands flush partial state
	// before exiting. Second signal restores default handler (SIGKILL),
	// giving us one grace period.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if len(os.Args) > 1 && os.Args[1] == "fetch" {
		os.Exit(runFetch(ctx))
	}
	os.Exit(runWrapper(ctx))
}

// runFetch dispatches to the init-container fetcher. Failure has already been
// surfaced via FETCH_ERROR: <reason> on stdout by RunEntry — this function
// only translates to an exit code.
func runFetch(ctx context.Context) int {
	if err := fetcher.RunEntry(ctx, fetcher.DefaultConfig()); err != nil {
		return 1
	}
	return 0
}

// runWrapper is the generic tool-runner path. Constructs the Entry with
// the shared verdict processors (JUnit + JTL) — the tool-agnostic
// processors are always wired; whether one runs is determined by the
// declared spec.verdict.from in the Test.
func runWrapper(ctx context.Context) int {
	resultDir := os.Getenv(executor.EnvResultDir)
	if resultDir == "" {
		resultDir = "/etc/kubetest/result"
	}

	entry := &executor.Entry{
		Exec:           exec.CommandContext,
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
		JUnitProcessor: junitProcessorFromDir,
		JTLProcessor:   jtlProcessorFromDir,
		Scraper:        newScraperFromEnv(), // nil when MINIO_ENDPOINT unset
		RequestPath:    executor.RequestPath,
		ResultDir:      resultDir,
		Loader:         os.Stderr,
	}
	if err := entry.Execute(ctx); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// junitProcessorFromDir wraps pkg/verdict/junit.Scan into the signature
// Entry.JUnitProcessor expects. Uses DefaultGlobs — templates that need
// custom glob patterns supply them via Args and the tool writes reports
// wherever they end up (the default globs cover all common cases).
func junitProcessorFromDir(workingDir string) (executor.TestCounts, error) {
	counts, err := verdictjunit.Scan(workingDir, nil)
	if err != nil {
		return executor.TestCounts{}, err
	}
	return executor.TestCounts{
		Total:   counts.Total,
		Passed:  counts.Passed,
		Failed:  counts.Failed,
		Skipped: counts.Skipped,
	}, nil
}

// jtlProcessorFromDir wraps pkg/verdict/jtl.Load into the signature
// Entry.JTLProcessor expects. Resolution order:
//  1. <workingDir>/out.jtl — the "simple template" convention.
//  2. <workingDir>/results/*.jtl (any single match) — the shipped
//     jmeter template writes -l /data/repo/results/jmeter.jtl; a
//     glob catches that + any other file name a user might pick
//     (results/run1.jtl, results/loadtest.jtl, …).
//  3. <workingDir>/**/*.jtl (any single match) — long tail.
//
// Multiple JTLs across (2)+(3) is an ambiguity error — the wrapper
// can't guess which one is the primary verdict input.
func jtlProcessorFromDir(workingDir string, threshold float64) (executor.JTLProcessorResult, error) {
	path, err := findJTL(workingDir)
	if err != nil {
		return executor.JTLProcessorResult{}, err
	}
	agg, err := verdictjtl.Load(path)
	if err != nil {
		return executor.JTLProcessorResult{}, err
	}
	rate := agg.ErrorRate()
	return executor.JTLProcessorResult{
		SamplesTotal:  agg.SamplesTotal,
		SamplesFailed: agg.SamplesFailed,
		ErrorRate:     rate,
		Threshold:     threshold,
		Passed:        rate <= threshold,
	}, nil
}

// findJTL resolves the JTL file the wrapper should read. Templates
// differ on where they write it (`out.jtl` vs `results/<name>.jtl` vs
// nested); this normalizes the lookup so both styles work without a
// per-tool code path. Returns ErrNotFound if no *.jtl file exists,
// or an "ambiguous" error if 2+ candidates are visible below workingDir
// (the wrapper refuses to guess which one is the primary verdict input).
func findJTL(workingDir string) (string, error) {
	// Fast path 1: <workingDir>/out.jtl (legacy convention).
	if _, err := os.Stat(filepath.Join(workingDir, "out.jtl")); err == nil {
		return filepath.Join(workingDir, "out.jtl"), nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	// Walk workingDir for any *.jtl file (bounded by workingDir subtree).
	var matches []string
	walkErr := filepath.WalkDir(workingDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // best-effort; skip unreadable subtrees
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), ".jtl") {
			matches = append(matches, p)
		}
		return nil
	})
	if walkErr != nil {
		return "", walkErr
	}
	switch len(matches) {
	case 0:
		return "", verdictjtl.ErrNotFound
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("multiple JTL files found under %s (%v) — set the tool to write exactly one, or -l /data/repo/out.jtl", workingDir, matches)
	}
}

// newScraperFromEnv builds the wrapper-side artifact scraper (step 07) when
// the operator has configured MinIO.
func newScraperFromEnv() executor.Scraper {
	endpoint := os.Getenv("MINIO_ENDPOINT")
	if endpoint == "" {
		return nil
	}
	bucket := os.Getenv("MINIO_BUCKET")
	if bucket == "" {
		bucket = "kubetest-artifacts" // matches compiler.MinIODefaultBucket
	}
	useSSL := os.Getenv("MINIO_USE_SSL") == "true"

	client, err := storage.NewMinIO(storage.Config{
		Endpoint:  endpoint,
		Bucket:    bucket,
		UseSSL:    useSSL,
		AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"),
		SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
	})
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wrapper: minio init failed, skipping scrape: %v\n", err)
		return nil
	}
	return scraper.New(client, bucket)
}
