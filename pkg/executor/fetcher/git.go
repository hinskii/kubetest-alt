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
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// ExecCommand is the shell-out factory. Tests inject a stub that returns a
// fake *exec.Cmd (typically `sh -c`) so we don't need real git in every test.
// Default is exec.CommandContext.
type ExecCommand func(ctx context.Context, name string, args ...string) *exec.Cmd

// gitBinary is overridable for tests that want to point at a specific git.
type gitCloner struct {
	Exec      ExecCommand
	Binary    string // default "git"
	Stdout    io.Writer
	Stderr    io.Writer
	EnvLookup EnvLookup
}

func newGitCloner() *gitCloner {
	return &gitCloner{
		Exec:      exec.CommandContext,
		Binary:    "git",
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		EnvLookup: os.LookupEnv,
	}
}

// clone materializes the git spec into dstDir. Shallow clone by default;
// sparse-checkout applied when g.Paths is non-empty. Auth flows chosen by
// g.AuthType, secrets sourced from env — NEVER placed in argv (visible via
// `ps`) — and instead injected via GIT_CONFIG_COUNT/KEY_N/VALUE_N or
// GIT_SSH_COMMAND.
func (c *gitCloner) clone(ctx context.Context, g GitContent, dstDir string) error {
	if g.URI == "" {
		return errors.New("empty git.uri")
	}

	authEnv, err := c.buildAuthEnv(g)
	if err != nil {
		return err
	}

	// Common env for every git invocation: base process env + auth env +
	// non-interactive flags so a stalled credential prompt can't hang the pod.
	baseEnv := append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
	)
	baseEnv = append(baseEnv, authEnv...)

	// Step 1: init + configure sparse-checkout up front (before fetch) so we
	// only download the files we asked for.
	if err := c.run(ctx, dstDir, baseEnv, "init", "--quiet"); err != nil {
		return fmt.Errorf("git init: %w", err)
	}
	if err := c.run(ctx, dstDir, baseEnv, "remote", "add", "origin", g.URI); err != nil {
		return fmt.Errorf("git remote add: %w", err)
	}

	if len(g.Paths) > 0 {
		if err := c.run(ctx, dstDir, baseEnv, "sparse-checkout", "init", "--cone"); err != nil {
			return fmt.Errorf("git sparse-checkout init: %w", err)
		}
		args := append([]string{"sparse-checkout", "set"}, g.Paths...)
		if err := c.run(ctx, dstDir, baseEnv, args...); err != nil {
			return fmt.Errorf("git sparse-checkout set: %w", err)
		}
	}

	// Step 2: fetch shallow at the requested revision (branch, tag, or sha).
	rev := g.Revision
	if rev == "" {
		rev = "HEAD"
	}
	if err := c.run(ctx, dstDir, baseEnv, "fetch", "--depth=1", "origin", rev); err != nil {
		return fmt.Errorf("git fetch: %w", err)
	}
	if err := c.run(ctx, dstDir, baseEnv, "checkout", "--quiet", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("git checkout: %w", err)
	}

	// Step 3: verify sparse-checkout paths actually landed. If a user asked
	// for scenarios/checkout and git couldn't find it, fail loud rather than
	// silently continuing with an empty dir.
	for _, p := range g.Paths {
		full := dstDir + string(os.PathSeparator) + p
		if _, statErr := os.Stat(full); statErr != nil {
			return fmt.Errorf("sparse-checkout path %q not present after fetch", p)
		}
	}
	return nil
}

// run executes one git subcommand. Args are for git itself; secrets flow via
// baseEnv only.
func (c *gitCloner) run(ctx context.Context, dir string, env []string, args ...string) error {
	// #nosec G204 -- args are literals from clone() flow, no user input reaches Args.
	cmd := c.Exec(ctx, c.Binary, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = c.Stdout
	cmd.Stderr = c.Stderr
	return cmd.Run()
}

// buildAuthEnv translates AuthType + env-var-sourced secrets into the env
// vars git will consume:
//
//   - basic: GIT_CONFIG_COUNT/KEY_0/VALUE_0 injecting an http.extraHeader
//     with a Basic-encoded credential.
//   - header: same mechanism, but the token is used verbatim as the header
//     value (for schemes like "Bearer <token>", user pre-formats).
//   - ssh: GIT_SSH_COMMAND pointing at ssh -i <keyfile> with strict host key
//     checking OFF (a test cluster's known_hosts is impractical to bootstrap).
//
// Secrets are read from the KUBETEST_GIT_* env vars the operator sets from
// Secret refs — never from the Content struct itself.
//
// Empty AuthType returns nil env (public repo).
func (c *gitCloner) buildAuthEnv(g GitContent) ([]string, error) {
	switch strings.ToLower(g.AuthType) {
	case "":
		return nil, nil

	case AuthTypeBasic:
		user, _ := c.EnvLookup(EnvGitUsername)
		token, ok := c.EnvLookup(EnvGitToken)
		if !ok || token == "" {
			return nil, fmt.Errorf("basic auth: env %s is empty", EnvGitToken)
		}
		if user == "" {
			// GitHub-style: token as password with any username.
			user = "x-access-token"
		}
		cred := user + ":" + token
		b64 := base64.StdEncoding.EncodeToString([]byte(cred))
		return gitConfigEnv("http.extraHeader", "Authorization: Basic "+b64), nil

	case AuthTypeHeader:
		token, ok := c.EnvLookup(EnvGitToken)
		if !ok || token == "" {
			return nil, fmt.Errorf("header auth: env %s is empty", EnvGitToken)
		}
		// Token is used verbatim — user provides full "Bearer xxx" or similar.
		return gitConfigEnv("http.extraHeader", "Authorization: "+token), nil

	case AuthTypeSSH:
		key, ok := c.EnvLookup(EnvGitSSHKeyPath)
		if !ok || key == "" {
			return nil, fmt.Errorf("ssh auth: env %s is empty", EnvGitSSHKeyPath)
		}
		// StrictHostKeyChecking=no is a pragmatic default for ephemeral test
		// clusters; production callers can pre-mount a known_hosts file and
		// wrap their own GIT_SSH_COMMAND if they need stricter checking.
		sshCmd := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null", key)
		return []string{"GIT_SSH_COMMAND=" + sshCmd}, nil

	default:
		return nil, fmt.Errorf("unknown authType %q (expected basic/header/ssh)", g.AuthType)
	}
}

// gitConfigEnv encodes one key/value pair into git's config-via-env protocol.
// Multiple pairs would use KEY_0/VALUE_0, KEY_1/VALUE_1, etc.; step 06 only
// ever needs one (http.extraHeader).
func gitConfigEnv(key, value string) []string {
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=" + key,
		"GIT_CONFIG_VALUE_0=" + value,
	}
}
