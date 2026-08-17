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

// Package fetcher runs the init-container side of the /entry wrapper:
// materializes Test.spec.content (git/files/tarball) into $KUBETEST_DATADIR.
//
// The types here mirror api/v1alpha1.Content by JSON tag rather than importing
// api/v1alpha1 directly. Rationale: the init container binary should not pull
// in the whole Kubernetes API surface (unused CRD deps, deepcopy, etc.). Two
// structs stay in sync via the JSON schema — the same pattern already used
// for pkg/executor.ExecutionRequest.
package fetcher

// Content is the wire shape of Test.spec.content. Read from
// $KUBETEST_REQUEST_DIR/content.json by the fetcher.
type Content struct {
	Git     *GitContent   `json:"git,omitempty"`
	Files   []FileContent `json:"files,omitempty"`
	Tarball []Tarball     `json:"tarball,omitempty"`
}

// GitContent tells the fetcher where to clone from and what auth to use.
// Auth values themselves NEVER live in this struct — the operator sets
// KUBETEST_GIT_USERNAME / KUBETEST_GIT_TOKEN / KUBETEST_GIT_SSH_KEY_PATH env
// vars from Secret refs; AuthType picks which flow the fetcher runs.
type GitContent struct {
	URI      string   `json:"uri"`
	Revision string   `json:"revision,omitempty"`
	Paths    []string `json:"paths,omitempty"` // sparse-checkout paths

	// AuthType selects the credential flow: "basic" / "header" / "ssh".
	// Empty means "public repo, no auth".
	AuthType string `json:"authType,omitempty"`
}

// FileContent describes one inline file. Content is the literal bytes;
// ContentFromEnv names an env var (set by the operator from a Secret/ConfigMap)
// to source from at fetch time.
type FileContent struct {
	Path        string `json:"path"`
	Content     string `json:"content,omitempty"`
	ContentFrom string `json:"contentFrom,omitempty"` // env var name
	Mode        *int32 `json:"mode,omitempty"`
}

// Tarball fetches a compressed archive over HTTP(S) and unpacks it. Path is
// the subdirectory under $KUBETEST_DATADIR to unpack into (empty = datadir root).
type Tarball struct {
	URL  string `json:"url"`
	Path string `json:"path,omitempty"`
}

// Env var names the fetcher reads at runtime. Compiler-side wiring (step 06+)
// sets these from Test.spec.content.git secret refs.
const (
	EnvGitUsername = "KUBETEST_GIT_USERNAME"
	// #nosec G101 -- this is an env-var NAME, not a hardcoded credential.
	EnvGitToken      = "KUBETEST_GIT_TOKEN"
	EnvGitSSHKeyPath = "KUBETEST_GIT_SSH_KEY_PATH"

	// EnvFetchTimeoutSeconds overrides the default fetch timeout. Optional.
	EnvFetchTimeoutSeconds = "KUBETEST_FETCH_TIMEOUT_SECONDS"

	// DefaultFetchTimeoutSeconds bounds the whole fetch operation. Chosen
	// long enough for a full JMeter test suite git-clone over slow links,
	// short enough that a stuck fetch doesn't outlive the Job ADS budget.
	DefaultFetchTimeoutSeconds = 300 // 5 minutes

	// DefaultDataDir is the compiler's default emptyDir mount point and the
	// fallback when $KUBETEST_DATADIR isn't set (e.g. running /entry fetch
	// standalone outside a pod).
	DefaultDataDir = "/data"
)

// AuthType values for GitContent.AuthType.
const (
	AuthTypeBasic  = "basic"
	AuthTypeHeader = "header"
	AuthTypeSSH    = "ssh"
)
