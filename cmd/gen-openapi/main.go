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

// gen-openapi emits the OpenAPI 3.1 spec to openapi/openapi.json. Called
// by `make openapi`; `make openapi-check` re-runs this and asserts the
// on-disk file is byte-identical (zero-diff gate).
//
// The spec source of truth lives in internal/apiserver/openapi.go so the
// running server serves the exact same bytes we commit.
package main

import (
	"fmt"
	"os"

	"github.com/hinskii/kubetest-alt/internal/apiserver"
)

func main() {
	out := "openapi/openapi.json"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	b, err := apiserver.OpenAPIJSON()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen-openapi:", err)
		os.Exit(1)
	}
	// Trailing newline so POSIX tools + editors don't grumble.
	b = append(b, '\n')
	// CLI tool writing an OpenAPI artifact; out is an operator-supplied
	// path (defaults to openapi/openapi.json). The process runs with the
	// user's own permissions, not elevated.
	if err := os.WriteFile(out, b, 0o600); err != nil { //nolint:gosec // path is operator-controlled
		fmt.Fprintln(os.Stderr, "gen-openapi: write:", err)
		os.Exit(1)
	}
}
