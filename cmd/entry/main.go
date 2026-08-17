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

	"github.com/hinskii/kubetest-alt/pkg/executor"
	"github.com/hinskii/kubetest-alt/pkg/executor/fetcher"
	"github.com/hinskii/kubetest-alt/pkg/executor/k6"
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
