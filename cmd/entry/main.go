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

// /entry is the in-container wrapper. Two modes:
//
//   - default (no args, or unrecognized subcommand): tool wrapper. Reads
//     request.json, runs the tool via the Runner, writes result.json.
//     See pkg/executor.
//
//   - "fetch" (argv[1] == "fetch"): init-container mode. Reads content.json,
//     materializes git/files/tarball into $KUBETEST_DATADIR. See
//     pkg/executor/fetcher.
//
// One binary, two subcommands — deliberate. Per-tool /entry binaries were
// considered and rejected in step 05 (see plan/step-11 NOTE).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hinskii/kubetest-alt/internal/scraper"
	"github.com/hinskii/kubetest-alt/pkg/executor"
	"github.com/hinskii/kubetest-alt/pkg/executor/fetcher"
	"github.com/hinskii/kubetest-alt/pkg/executor/k6"
	"github.com/hinskii/kubetest-alt/pkg/storage"
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

// runWrapper dispatches to the tool wrapper. Step 05 ships only k6; step 11
// swaps this for a map[string]Runner keyed by request.Type.
func runWrapper(ctx context.Context) int {
	resultDir := os.Getenv(executor.EnvResultDir)
	if resultDir == "" {
		resultDir = "/etc/kubetest/result"
	}

	entry := &executor.Entry{
		Runner:      k6.NewRunner(),
		Scraper:     newScraperFromEnv(), // nil when MINIO_ENDPOINT unset
		RequestPath: executor.RequestPath,
		ResultDir:   resultDir,
		Stderr:      os.Stderr,
	}
	if err := entry.Execute(ctx); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// newScraperFromEnv builds the wrapper-side artifact scraper (step 07) when
// the operator has configured MinIO. Returns nil when disabled so Entry
// skips the scrape path cleanly. Credentials come from envFrom (the compiler
// wires the Secret ref on the container) — this function only reads env vars.
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
		// Log to stderr but keep going — Entry treats nil Scraper as "don't
		// scrape", the run still produces result.json on the local emptyDir.
		_, _ = fmt.Fprintf(os.Stderr, "wrapper: minio init failed, skipping scrape: %v\n", err)
		return nil
	}
	return scraper.New(client, bucket)
}
