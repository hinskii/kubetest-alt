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

// kubectl-kubetest: install on $PATH and use as `kubectl kubetest …`.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hinskii/kubetest-alt/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	err := cli.NewRootCmd(cli.Connect, os.Stdout, os.Stderr).ExecuteContext(ctx)
	if err == nil {
		return
	}
	var exit *cli.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.Code) // the verdict line is already on stderr
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(cli.ExitOther)
}
