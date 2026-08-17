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

package executor

// Filesystem + env contract with the compiler. The operator's compiler and
// the wrapper both reference these constants — moving either side breaks the
// other, which is why they live in this shared package.
const (
	// EntryCommand is the ENTRYPOINT the compiler puts on wrapper containers.
	// Each executor image (executors/<type>/Dockerfile) must ship /entry at
	// this path, statically linked (grafana/k6 etc. are Alpine/musl).
	EntryCommand = "/entry"

	// RequestPath is where the operator projects request.json via ConfigMap
	// volume. The wrapper reads it once at startup.
	RequestPath = "/etc/kubetest/request.json"

	// RequestFileName is the ConfigMap key + projected filename for the
	// wrapper's request payload. Kept split from RequestPath so the compiler
	// (which builds the CM) and the wrapper (which reads the file) agree on
	// the exact key without duplicating the string literal.
	RequestFileName = "request.json"

	// ResultFileName is the file the wrapper writes atomically inside
	// $KUBETEST_RESULTDIR. Readers open by this exact name.
	ResultFileName = "result.json"

	// ContentFileName is the ConfigMap key + projected filename for the
	// Content spec (git/files/tarball). Sits alongside request.json in the
	// same mount. Step 03 originally shipped Content in an env var on the
	// init container; step 06 moved it to this file because inline content
	// can approach 512KB (webhook cap) and env-embedded payloads bloat the
	// pod object in etcd (~1.5MB limit) and hit ARG_MAX.
	ContentFileName = "content.json"

	// EnvResultDir is the env var the compiler sets on the wrapper container
	// pointing at the emptyDir mount used for result.json.
	EnvResultDir = "KUBETEST_RESULTDIR"

	// EnvDataDir points the wrapper at the shared /data emptyDir populated
	// by the content-fetcher init container (step 06).
	EnvDataDir = "KUBETEST_DATADIR"

	// EnvRunID and EnvTestRef carry identifying context so tools that log
	// to stdout can tag their output. Optional for the wrapper itself.
	EnvRunID   = "KUBETEST_RUN_ID"
	EnvTestRef = "KUBETEST_TEST_REF"
)
