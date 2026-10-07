//go:build catalog

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

// Package catalog runs every catalog TestTemplate for real against a kind
// cluster (set up by test/e2e/run.sh with CATALOG_TOOLS) and asserts what
// a user would see: the verdict, JUnit test counts, load metrics and the
// scraped artifacts.
//
// Each tool has test/catalog/cases/<tool>/: case.yaml (run config +
// expectations) and repo/ (the project, shipped as inline content.files
// under /data/repo — no external git repository). Projects hit the
// in-cluster target service (/ → 200, /missing → 404) and are built to
// produce a deliberate mix of passes and failures, so the assertions
// prove failures are seen, counted and reported — not just that the tool
// started.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/yaml"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

const (
	namespace = "kubetest-catalog" // prepared by test/e2e/run.sh

	casesDir       = "cases"
	defaultTimeout = 10 * time.Minute
)

// caseSpec is test/catalog/cases/<tool>/case.yaml.
type caseSpec struct {
	// Config becomes TestRun.spec.config (parameter values).
	Config  map[string]string `json:"config"`
	Expect  expectation       `json:"expect"`
	Timeout string            `json:"timeout"`
}

type expectation struct {
	Phase      testsv1alpha1.Phase `json:"phase"`
	TestCounts *struct {
		Total  int `json:"total"`
		Failed int `json:"failed"`
	} `json:"testCounts"`
	// Metrics must be present with exactly these values.
	Metrics map[string]float64 `json:"metrics"`
	// MetricsPresent must exist (values vary run to run).
	MetricsPresent []string `json:"metricsPresent"`
	// MetricsRange bounds noisy values: key → [min, max].
	MetricsRange map[string][2]float64 `json:"metricsRange"`
	// Artifacts: each glob must match at least one scraped artifact path.
	Artifacts []string `json:"artifacts"`
	// Report: the template's spec.artifacts.report must match a scraped
	// artifact — the run has an "Open report" button.
	Report bool `json:"report"`
}

func TestCatalog(t *testing.T) {
	tools := selectedTools(t)
	require.NotEmpty(t, tools, "no catalog cases selected")
	c := newClient(t)
	// Tools run in parallel; `go test -parallel N` bounds how many at once
	// (the heavy images — cypress, artillery, zap — starve small runners).
	for _, tool := range tools {
		t.Run(tool, func(t *testing.T) {
			t.Parallel()
			runCase(t, c, tool)
		})
		// The same project once more, fetched from git exactly as the
		// sample in config/samples/tools does — the path every real user
		// takes. CATALOG_GIT_REVISION pins the commit under test (CI sets
		// it to github.sha); without it the git variant is skipped.
		if rev := os.Getenv("CATALOG_GIT_REVISION"); rev != "" {
			t.Run(tool+"-git", func(t *testing.T) {
				t.Parallel()
				runSample(t, c, tool, rev)
			})
		}
	}
}

// runSample runs config/samples/tools/<tool>.yaml (git content, sample
// parameter defaults) and asserts the same expectations as the inline case.
func runSample(t *testing.T, c client.Client, tool, rev string) {
	start := time.Now()
	defer func() {
		t.Logf("CATALOG_TIMING tool=%s-git duration=%s", tool, time.Since(start).Round(time.Second))
	}()
	cs := loadCase(t, tool)
	b, err := os.ReadFile(filepath.Join("..", "..", "config", "samples", "tools", tool+".yaml"))
	require.NoError(t, err)
	var test testsv1alpha1.Test
	require.NoError(t, yaml.Unmarshal(b, &test))
	require.NotNil(t, test.Spec.Content.Git, "%s sample must use git content", tool)
	test.Name = "catalog-git-" + tool
	test.Namespace = namespace
	test.Spec.Content.Git.Revision = rev

	ctx := context.Background()
	_ = c.Delete(ctx, &test)
	require.NoError(t, c.Create(ctx, &test))
	run := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{GenerateName: test.Name + "-", Namespace: namespace},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: test.Name, Source: "api"},
	}
	require.NoError(t, c.Create(ctx, run))
	timeout := defaultTimeout
	if cs.Timeout != "" {
		timeout, err = time.ParseDuration(cs.Timeout)
		require.NoError(t, err)
	}
	final := waitTerminal(t, ctx, c, run.Name, timeout)
	t.Logf("%s (git): phase=%s message=%q testCounts=%+v artifacts=%d",
		tool, final.Status.Phase, final.Status.Message, final.Status.TestCounts, len(final.Status.ArtifactRefs))
	if !assertCase(t, tool, cs.Expect, final) {
		dumpLogs(t, final)
	}
}

// selectedTools honours CATALOG_TOOLS (comma-separated); default = all.
func selectedTools(t *testing.T) []string {
	if env := strings.TrimSpace(os.Getenv("CATALOG_TOOLS")); env != "" && env != "all" {
		return strings.Split(env, ",")
	}
	entries, err := os.ReadDir(casesDir)
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

func runCase(t *testing.T, c client.Client, tool string) {
	start := time.Now()
	defer func() { t.Logf("CATALOG_TIMING tool=%s duration=%s", tool, time.Since(start).Round(time.Second)) }()

	cs := loadCase(t, tool)
	files := loadRepo(t, tool)
	ctx := context.Background()

	name := "catalog-" + tool
	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: testsv1alpha1.TestSpec{
			Use:     []string{tool},
			Content: testsv1alpha1.Content{Files: files},
		},
	}
	_ = c.Delete(ctx, test) // leftovers from a previous local run
	require.NoError(t, c.Create(ctx, test))

	run := &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{GenerateName: name + "-", Namespace: namespace},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: name, Config: cs.Config, Source: "api"},
	}
	require.NoError(t, c.Create(ctx, run))

	timeout := defaultTimeout
	if cs.Timeout != "" {
		d, err := time.ParseDuration(cs.Timeout)
		require.NoError(t, err)
		timeout = d
	}
	final := waitTerminal(t, ctx, c, run.Name, timeout)
	t.Logf("%s: phase=%s message=%q tool=%s metrics=%v testCounts=%+v artifacts=%d",
		tool, final.Status.Phase, final.Status.Message, final.Status.Tool,
		final.Status.Metrics, final.Status.TestCounts, len(final.Status.ArtifactRefs))

	ok := assertCase(t, tool, cs.Expect, final)
	if !ok {
		dumpLogs(t, final)
	}
}

func assertCase(t *testing.T, tool string, want expectation, run *testsv1alpha1.TestRun) bool {
	ok := assert.Equal(t, want.Phase, run.Status.Phase, "%s verdict (message: %s)", tool, run.Status.Message)
	ok = assert.Equal(t, tool, run.Status.Tool, "%s: status.tool from the template label", tool) && ok

	if want.TestCounts != nil {
		if assert.NotNil(t, run.Status.TestCounts, "%s: JUnit counts missing", tool) {
			ok = assert.Equal(t, want.TestCounts.Total, run.Status.TestCounts.Total, "%s total tests", tool) && ok
			ok = assert.Equal(t, want.TestCounts.Failed, run.Status.TestCounts.Failed, "%s failed tests", tool) && ok
		} else {
			ok = false
		}
	}
	metric := func(k string) (float64, bool) {
		v, present := run.Status.Metrics[k]
		if !present {
			return 0, false
		}
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	}
	for k, v := range want.Metrics {
		got, present := metric(k)
		ok = assert.True(t, present, "%s: metric %s missing (have %v)", tool, k, run.Status.Metrics) && ok
		ok = assert.InDelta(t, v, got, 1e-9, "%s: metric %s", tool, k) && ok
	}
	for _, k := range want.MetricsPresent {
		_, present := metric(k)
		ok = assert.True(t, present, "%s: metric %s missing (have %v)", tool, k, run.Status.Metrics) && ok
	}
	for k, r := range want.MetricsRange {
		got, present := metric(k)
		ok = assert.True(t, present && got >= r[0] && got <= r[1],
			"%s: metric %s=%v outside [%v, %v]", tool, k, got, r[0], r[1]) && ok
	}
	var paths []string
	for _, a := range run.Status.ArtifactRefs {
		paths = append(paths, a.Path)
	}
	for _, glob := range want.Artifacts {
		matched := slices.ContainsFunc(paths, func(p string) bool {
			m, _ := doublestar.Match(glob, p)
			return m
		})
		ok = assert.True(t, matched, "%s: no artifact matches %q (have %v)", tool, glob, paths) && ok
	}
	if want.Report {
		var spec testsv1alpha1.TestSpec
		require.NoError(t, json.Unmarshal([]byte(run.Status.ResolvedSpec), &spec))
		require.NotNil(t, spec.Artifacts, "%s: no spec.artifacts", tool)
		pattern := spec.Artifacts.Report
		matched := pattern != "" && slices.ContainsFunc(paths, func(p string) bool {
			m, _ := doublestar.Match(pattern, p)
			return m
		})
		ok = assert.True(t, matched, "%s: report %q matches no artifact (have %v)", tool, pattern, paths) && ok
	}
	return ok
}

func loadCase(t *testing.T, tool string) caseSpec {
	b, err := os.ReadFile(filepath.Join(casesDir, tool, "case.yaml"))
	require.NoError(t, err)
	var cs caseSpec
	require.NoError(t, yaml.UnmarshalStrict(b, &cs), "case.yaml for %s", tool)
	require.NotEmpty(t, cs.Expect.Phase, "case.yaml for %s: expect.phase is required", tool)
	return cs
}

// loadRepo ships cases/<tool>/repo/** as content.files under repo/ — the
// fetcher materialises them at /data/repo, where the templates look.
func loadRepo(t *testing.T, tool string) []testsv1alpha1.FileContent {
	root := filepath.Join(casesDir, tool, "repo")
	var files []testsv1alpha1.FileContent
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p) // #nosec G304 -- walking our own testdata
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		files = append(files, testsv1alpha1.FileContent{Path: "repo/" + filepath.ToSlash(rel), Content: string(b)})
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, files, "%s: repo/ is empty", tool)
	return files
}

func waitTerminal(t *testing.T, ctx context.Context, c client.Client, name string, timeout time.Duration) *testsv1alpha1.TestRun {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var run testsv1alpha1.TestRun
	for time.Now().Before(deadline) {
		if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &run); err == nil {
			switch run.Status.Phase {
			case testsv1alpha1.PhasePassed, testsv1alpha1.PhaseFailed,
				testsv1alpha1.PhaseError, testsv1alpha1.PhaseAborted:
				return &run
			}
		}
		time.Sleep(3 * time.Second)
	}
	dumpLogs(t, &run)
	t.Fatalf("run %s not terminal after %s (phase=%q message=%q)", name, timeout, run.Status.Phase, run.Status.Message)
	return nil
}

// dumpLogs prints the tail of the run's stored log via the API server.
func dumpLogs(t *testing.T, run *testsv1alpha1.TestRun) {
	api := os.Getenv("APISERVER_URL")
	if api == "" || run == nil || run.Name == "" {
		return
	}
	url := fmt.Sprintf("%s/runs/%s/logs.txt?namespace=%s", strings.TrimRight(api, "/"), run.Name, namespace)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return
	}
	if tok := os.Getenv("APISERVER_TOKEN"); tok != "" {
		req.Header.Set("X-Kubetest-Token", tok)
	}
	resp, err := http.DefaultClient.Do(req) // #nosec G107 -- test-only, local port-forward
	if err != nil {
		t.Logf("logs: %v", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > 80 {
		lines = lines[len(lines)-80:]
	}
	t.Logf("--- last %d log lines of %s ---\n%s", len(lines), run.Name, strings.Join(lines, "\n"))
}

func newClient(t *testing.T) client.Client {
	t.Helper()
	sch := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(sch))
	utilruntime.Must(testsv1alpha1.AddToScheme(sch))
	cfg, err := config.GetConfig()
	require.NoError(t, err)
	c, err := client.New(cfg, client.Options{Scheme: sch})
	require.NoError(t, err)
	return c
}
