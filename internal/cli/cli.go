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
// Package podpolicy is what a test pod may ask for (fixes.md #1).
// Package cli is kubectl-kubetest: Tests and runs from a terminal or a CI
// pipeline. It reaches the kubetest API the way Control Center does —
// through the Kubernetes API's service proxy, with the API token from the
// chart's Secret — so who may use it is decided by Kubernetes RBAC on that
// Service and Secret.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/apiclient"
)

// Exit codes of `run --wait`: a CI step fails on anything but 0.
const (
	ExitPassed = 0
	ExitFailed = 1 // the run's verdict: failed
	ExitOther  = 2 // error, aborted, timed out waiting, or the CLI failed
)

// How often waiting and following poll the API (tests shorten them).
var (
	pollEvery   = 2 * time.Second
	logsPollMin = time.Second
)

// ExitError carries the process exit code for main.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// Connection is what a command needs: the API and the namespace of Tests.
type Connection struct {
	API       *apiclient.Client
	Namespace string
}

// Connector opens a Connection from the global flags (connect.go in
// production, a fake in tests).
type Connector func(ctx context.Context, f *GlobalFlags) (*Connection, error)

// GlobalFlags are the kubectl-style flags every command takes.
type GlobalFlags struct {
	Kubeconfig, Context, Namespace string
	// KubetestNamespace is where the chart is installed (its API Service
	// and token Secret).
	KubetestNamespace string
	APIService        string // override discovery
	TokenSecret       string // override discovery
	Output            string // "", json, yaml
}

// NewRootCmd builds the command tree. out/errOut are stdout/stderr.
func NewRootCmd(connect Connector, out, errOut io.Writer) *cobra.Command {
	f := &GlobalFlags{}
	root := &cobra.Command{
		Use:           "kubectl-kubetest",
		Short:         "kubetest from the terminal: Tests, runs, logs, artifacts",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(out)
	root.SetErr(errOut)
	pf := root.PersistentFlags()
	pf.StringVar(&f.Kubeconfig, "kubeconfig", "", "Path to the kubeconfig (default: $KUBECONFIG, ~/.kube/config).")
	pf.StringVar(&f.Context, "context", "", "Kubeconfig context (default: the current one).")
	pf.StringVarP(&f.Namespace, "namespace", "n", "", "Namespace of the Tests (default: the context's).")
	pf.StringVar(&f.KubetestNamespace, "kubetest-namespace", "kubetest-alt", "Namespace kubetest is installed in.")
	pf.StringVar(&f.APIService, "api-service", "", "kubetest API Service name (default: discovered by label).")
	pf.StringVar(&f.TokenSecret, "api-token-secret", "",
		"Secret with the API token, key \"token\" (default: discovered; $KUBETEST_API_TOKEN wins).")
	pf.StringVarP(&f.Output, "output", "o", "", "Output format: json or yaml (default: a table).")

	c := &cmds{connect: connect, f: f, out: out, errOut: errOut}
	root.AddCommand(c.testsCmd(), c.testCmd(), c.runCmd(), c.runsCmd(), c.logsCmd(), c.abortCmd(), c.artifactsCmd())
	return root
}

type cmds struct {
	connect     Connector
	f           *GlobalFlags
	out, errOut io.Writer
}

func (c *cmds) conn(cmd *cobra.Command) (*Connection, error) {
	if c.f.Output != "" && c.f.Output != "json" && c.f.Output != "yaml" {
		return nil, fmt.Errorf("--output %q: must be json or yaml", c.f.Output)
	}
	return c.connect(cmd.Context(), c.f)
}

// print writes v as JSON/YAML when asked, else calls table.
func (c *cmds) print(v any, table func(w *tabwriter.Writer)) error {
	switch c.f.Output {
	case "json":
		enc := json.NewEncoder(c.out)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	case "yaml":
		b, err := yaml.Marshal(v)
		if err != nil {
			return err
		}
		_, err = c.out.Write(b)
		return err
	}
	w := tabwriter.NewWriter(c.out, 0, 4, 2, ' ', 0)
	table(w)
	return w.Flush()
}

func (c *cmds) testsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tests",
		Short: "List Tests",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cn, err := c.conn(cmd)
			if err != nil {
				return err
			}
			tests, err := cn.API.ListTests(cmd.Context(), cn.Namespace)
			if err != nil {
				return err
			}
			slices.SortFunc(tests, func(a, b testsv1alpha1.Test) int { return strings.Compare(a.Name, b.Name) })
			return c.print(tests, func(w *tabwriter.Writer) {
				_, _ = fmt.Fprintln(w, "NAME\tTOOL\tSCHEDULE\tLAST RUN\tPHASE")
				for _, t := range tests {
					last, phase := "-", "-"
					if lr := t.Status.LatestRun; lr != nil {
						last, phase = lr.Name, string(lr.Phase)
					}
					_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", t.Name, dash(t.Labels["kubetest.io/tool"]),
						dash(t.Spec.Schedule), last, phase)
				}
			})
		},
	}
}

func (c *cmds) testCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test NAME",
		Short: "Show a Test with its templates merged and its parameters",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cn, err := c.conn(cmd)
			if err != nil {
				return err
			}
			t, err := cn.API.GetResolvedTest(cmd.Context(), cn.Namespace, args[0])
			if err != nil {
				return err
			}
			return c.print(t, func(w *tabwriter.Writer) {
				_, _ = fmt.Fprintf(w, "Name:\t%s\nNamespace:\t%s\nTool:\t%s\n", t.Name, t.Namespace, dash(t.Tool))
				if len(t.Templates) > 0 {
					_, _ = fmt.Fprintf(w, "Templates:\t%s\n", strings.Join(t.Templates, ", "))
				}
				if t.GitOpsLocked {
					_, _ = fmt.Fprintln(w, "Managed:\tin Git (read-only in the GUI; runs allowed)")
				}
				if t.Spec == nil {
					return
				}
				if t.Spec.Schedule != "" {
					_, _ = fmt.Fprintf(w, "Schedule:\t%s\n", t.Spec.Schedule)
				}
				if len(t.Spec.Config) > 0 {
					_, _ = fmt.Fprintln(w, "\nPARAMETER\tTYPE\tDEFAULT")
					for _, name := range slices.Sorted(maps.Keys(t.Spec.Config)) {
						p := t.Spec.Config[name]
						def := p.Default
						if def == "" {
							def = "(required)"
						}
						_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", name, p.Type, def)
					}
				}
			})
		},
	}
}

func (c *cmds) runCmd() *cobra.Command {
	var (
		config  []string
		wait    bool
		follow  bool
		timeout time.Duration
		at      string
	)
	cmd := &cobra.Command{
		Use:   "run TEST",
		Short: "Start a run of a Test; with --wait, exit with its verdict (0 passed, 1 failed, 2 otherwise)",
		Example: `  kubectl kubetest run checkout-smoke -n shop --wait
  kubectl kubetest run load -n shop --config vus=50 --config duration=5m --follow`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cn, err := c.conn(cmd)
			if err != nil {
				return err
			}
			overrides, err := parseConfig(config)
			if err != nil {
				return err
			}
			run := &testsv1alpha1.TestRun{Spec: testsv1alpha1.TestRunSpec{TestRef: args[0], Source: "cli", Config: overrides}}
			run.GenerateName = generatePrefix(args[0])
			if at != "" {
				t, err := time.Parse(time.RFC3339, at)
				if err != nil {
					return fmt.Errorf("--at %q: want RFC 3339, e.g. 2026-10-07T18:30:00Z", at)
				}
				nb := metav1.NewTime(t)
				run.Spec.NotBefore = &nb
			}
			created, err := cn.API.CreateRun(cmd.Context(), cn.Namespace, run)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(c.errOut, "run %s started\n", created.Name)
			if !wait && !follow {
				_, _ = fmt.Fprintln(c.out, created.Name)
				return nil
			}
			ctx := cmd.Context()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			var final *apiclient.Run
			if follow {
				final, err = c.followLogs(ctx, cn, created.Name)
			} else {
				final, err = waitFinished(ctx, cn, created.Name)
			}
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					return &ExitError{Code: ExitOther, Err: fmt.Errorf("run %s did not finish within %s (it keeps running)", created.Name, timeout)}
				}
				return err
			}
			return c.verdict(final)
		},
	}
	fl := cmd.Flags()
	fl.StringArrayVar(&config, "config", nil, "Parameter override NAME=VALUE (repeatable).")
	fl.BoolVar(&wait, "wait", false, "Wait for the verdict and exit with it.")
	fl.BoolVarP(&follow, "follow", "f", false, "Stream the run's log until it finishes (implies --wait).")
	fl.DurationVar(&timeout, "timeout", 0, "Give up waiting after this long (exit 2); 0 = no limit.")
	fl.StringVar(&at, "at", "", "Start at this time (RFC 3339) instead of now.")
	return cmd
}

// verdict prints the run's outcome and maps it to the exit code.
func (c *cmds) verdict(r *apiclient.Run) error {
	summary := fmt.Sprintf("run %s %s", r.Name, r.Phase)
	if r.DurationMs > 0 {
		summary += fmt.Sprintf(" in %s", (time.Duration(r.DurationMs) * time.Millisecond).Round(100*time.Millisecond))
	}
	if r.Message != "" {
		summary += ": " + r.Message
	}
	_, _ = fmt.Fprintln(c.errOut, summary)
	switch r.Phase {
	case string(testsv1alpha1.PhasePassed):
		_, _ = fmt.Fprintln(c.out, r.Name)
		return nil
	case string(testsv1alpha1.PhaseFailed):
		return &ExitError{Code: ExitFailed, Err: errors.New(summary)}
	}
	return &ExitError{Code: ExitOther, Err: errors.New(summary)}
}

func terminal(phase string) bool {
	switch testsv1alpha1.Phase(phase) {
	case testsv1alpha1.PhasePassed, testsv1alpha1.PhaseFailed, testsv1alpha1.PhaseError, testsv1alpha1.PhaseAborted:
		return true
	}
	return false
}

func waitFinished(ctx context.Context, cn *Connection, name string) (*apiclient.Run, error) {
	for {
		r, err := cn.API.GetRun(ctx, cn.Namespace, name)
		if err != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil && terminal(r.Phase) {
			return r, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollEvery):
		}
	}
}

// followLogs prints the run's log as it grows until the run is over,
// then the rest of it.
func (c *cmds) followLogs(ctx context.Context, cn *Connection, name string) (*apiclient.Run, error) {
	var offset int64
	drain := func() {
		rc, err := cn.API.OpenLogsFrom(ctx, cn.Namespace, name, offset)
		if err != nil {
			return
		}
		defer func() { _ = rc.Close() }()
		n, _ := io.Copy(c.out, rc)
		offset += n
	}
	for {
		r, err := cn.API.GetRun(ctx, cn.Namespace, name)
		if err != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		drain()
		if err == nil && terminal(r.Phase) {
			drain() // what was flushed while the run finished
			return r, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(logsPollMin):
		}
	}
}

func (c *cmds) runsCmd() *cobra.Command {
	var test string
	var limit int
	cmd := &cobra.Command{
		Use:   "runs [RUN]",
		Short: "List runs (newest first), or show one",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cn, err := c.conn(cmd)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				return c.showRun(cmd.Context(), cn, args[0])
			}
			page, err := cn.API.ListRuns(cmd.Context(), apiclient.ListRunsOptions{Namespace: cn.Namespace, Test: test, Limit: limit})
			if err != nil {
				return err
			}
			return c.print(page.Runs, func(w *tabwriter.Writer) {
				_, _ = fmt.Fprintln(w, "NAME\tTEST\tPHASE\tSTARTED\tDURATION\tSOURCE")
				for _, r := range page.Runs {
					_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.TestRef, r.Phase, when(r.StartedAt),
						dur(r.DurationMs), dash(r.Source))
				}
			})
		},
	}
	cmd.Flags().StringVar(&test, "test", "", "Only runs of this Test.")
	cmd.Flags().IntVar(&limit, "limit", 20, "How many runs.")
	return cmd
}

func (c *cmds) showRun(ctx context.Context, cn *Connection, name string) error {
	r, err := cn.API.GetRun(ctx, cn.Namespace, name)
	if err != nil {
		return err
	}
	return c.print(r, func(w *tabwriter.Writer) {
		_, _ = fmt.Fprintf(w, "Name:\t%s\nTest:\t%s\nPhase:\t%s\n", r.Name, r.TestRef, r.Phase)
		if r.Message != "" {
			_, _ = fmt.Fprintf(w, "Message:\t%s\n", r.Message)
		}
		_, _ = fmt.Fprintf(w, "Started:\t%s\nDuration:\t%s\nSource:\t%s\n", when(r.StartedAt), dur(r.DurationMs), dash(r.Source))
		if by := r.Tags[apiclient.TagCreatedBy]; by != "" {
			_, _ = fmt.Fprintf(w, "Started by:\t%s\n", by)
		}
		if g := r.Git; g != nil && g.Commit != "" {
			_, _ = fmt.Fprintf(w, "Commit:\t%s %s\n", g.Commit, g.Revision)
		}
		if tc := r.TestCounts; tc != nil {
			_, _ = fmt.Fprintf(w, "Tests:\t%d passed, %d failed, %d skipped\n", tc.Passed, tc.Failed, tc.Skipped)
		}
		if r.Report != "" {
			_, _ = fmt.Fprintf(w, "Report:\t%s\n", r.Report)
		}
		for _, k := range slices.Sorted(maps.Keys(r.Config)) {
			_, _ = fmt.Fprintf(w, "Param %s:\t%s\n", k, r.Config[k])
		}
		for _, k := range slices.Sorted(maps.Keys(r.Metrics)) {
			_, _ = fmt.Fprintf(w, "Metric %s:\t%g\n", k, r.Metrics[k])
		}
	})
}

func (c *cmds) logsCmd() *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs RUN",
		Short: "Print a run's log; -f follows a running one",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cn, err := c.conn(cmd)
			if err != nil {
				return err
			}
			if follow {
				_, err := c.followLogs(cmd.Context(), cn, args[0])
				return err
			}
			rc, err := cn.API.OpenLogs(cmd.Context(), cn.Namespace, args[0])
			if err != nil {
				return err
			}
			defer func() { _ = rc.Close() }()
			_, err = io.Copy(c.out, rc)
			return err
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Keep printing until the run finishes.")
	return cmd
}

func (c *cmds) abortCmd() *cobra.Command {
	var msg string
	cmd := &cobra.Command{
		Use:   "abort RUN",
		Short: "Stop a run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cn, err := c.conn(cmd)
			if err != nil {
				return err
			}
			if _, err := cn.API.AbortRun(cmd.Context(), cn.Namespace, args[0], msg); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(c.errOut, "abort of %s requested\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&msg, "message", "aborted from the CLI", "Why (recorded on the run).")
	return cmd
}

func (c *cmds) artifactsCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "artifacts RUN",
		Short: "List a run's artifacts; --download saves them",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cn, err := c.conn(cmd)
			if err != nil {
				return err
			}
			list, err := cn.API.ListArtifacts(cmd.Context(), cn.Namespace, args[0])
			if err != nil {
				return err
			}
			if dir == "" {
				return c.print(list, func(w *tabwriter.Writer) {
					_, _ = fmt.Fprintln(w, "PATH\tSIZE\tTYPE")
					for _, a := range list {
						_, _ = fmt.Fprintf(w, "%s\t%d\t%s\n", a.Path, a.SizeBytes, dash(a.ContentType))
					}
				})
			}
			failed := 0
			for _, a := range list {
				target, err := safeJoin(dir, a.Path)
				if err == nil {
					err = download(cmd.Context(), cn, args[0], a.Path, target)
				}
				if err != nil {
					failed++
					_, _ = fmt.Fprintf(c.errOut, "skipped %s: %v\n", a.Path, err)
					continue
				}
				_, _ = fmt.Fprintln(c.out, target)
			}
			if failed > 0 {
				return &ExitError{Code: ExitOther, Err: fmt.Errorf("%d of %d artifacts not downloaded", failed, len(list))}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "download", "", "Save every artifact under this directory.")
	return cmd
}

// safeJoin keeps an artifact path inside dir — artifact names come from
// test output and must not write outside it ("../", absolute paths).
func safeJoin(dir, p string) (string, error) {
	clean := path.Clean("/" + p)
	if clean == "/" {
		return "", fmt.Errorf("artifact path %q has no file name", p)
	}
	return filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(clean, "/"))), nil
}

func download(ctx context.Context, cn *Connection, run, p, target string) error {
	st, err := cn.API.OpenArtifact(ctx, cn.Namespace, run, p)
	if err != nil {
		return err
	}
	defer func() { _ = st.Body.Close() }()
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}
	f, err := os.Create(target) // #nosec G304 -- safeJoin keeps it under --download
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, st.Body); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func parseConfig(kvs []string) (map[string]string, error) {
	if len(kvs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("--config %q: want NAME=VALUE", kv)
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, nil
}

// generatePrefix keeps the run name within 63 characters (the API server
// appends five).
func generatePrefix(test string) string {
	if len(test) > 50 {
		test = strings.TrimRight(test[:50], "-.")
	}
	return test + "-"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func when(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

func dur(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return (time.Duration(ms) * time.Millisecond).Round(100 * time.Millisecond).String()
}
