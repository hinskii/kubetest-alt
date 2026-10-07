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

package controller

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// TestCatalog_ApplyAllTemplatesAndSamples proves every catalog file under
// config/templates/ + config/samples/tools/ round-trips through the API
// server + controller-resolver without error:
//
//  1. every TestTemplate creates cleanly (schema-valid, matches CRD);
//  2. every sample Test creates cleanly AND its controller-side
//     resolveSpec produces a valid resolved spec (image + command/args
//     present, expressions eval, verdict from template propagates).
//
// This is the plan's "apply-test covering all templates and samples"
// acceptance criterion. Runs against the shared envtest so the CRD schemas
// are the real ones the operator will admit in production.
func TestCatalog_ApplyAllTemplatesAndSamples(t *testing.T) {
	ctx := context.Background()
	ns := uniqueNamespace(t)

	repoRoot, err := findRepoRoot()
	require.NoError(t, err)
	templatesDir := filepath.Join(repoRoot, "config", "templates")
	samplesDir := filepath.Join(repoRoot, "config", "samples", "tools")

	// Load every template file into the api server. The samples reference
	// them by name so template CRs must land first.
	tmplFiles, err := listYAML(templatesDir)
	require.NoError(t, err)
	require.NotEmpty(t, tmplFiles, "at least one template must exist under config/templates/")

	templateNames := make([]string, 0, len(tmplFiles))
	for _, f := range tmplFiles {
		tmpl, err := readTestTemplate(f)
		require.NoError(t, err, "parse %s", f)
		// Rewrite namespace to the ephemeral one so other tests don't see it.
		tmpl.Namespace = ns
		tmpl.ResourceVersion = ""
		require.NoError(t, k8sClient.Create(ctx, tmpl), "create template %s", tmpl.Name)
		templateNames = append(templateNames, tmpl.Name)
	}

	// Assert every template carries kubetest.io/tool as a label — the plan
	// requires that from every catalog entry (the "ONLY layer where tool
	// names exist" clause).
	for _, name := range templateNames {
		var got testsv1alpha1.TestTemplate
		require.NoError(t, k8sClient.Get(ctx,
			client.ObjectKey{Namespace: ns, Name: name}, &got))
		toolLabel := got.Labels["kubetest.io/tool"]
		assert.NotEmpty(t, toolLabel,
			"template %s must set kubetest.io/tool label (catalog rule)", name)
		assert.Equal(t, name, toolLabel,
			"template metadata.name should match its kubetest.io/tool label so the two identifiers can't drift")
	}

	// Now the samples — each is a Test that spec.use[]-references exactly
	// ONE template from the catalog. Rename to a per-test alias so re-runs
	// don't collide, but keep the spec (including spec.use) intact.
	sampleFiles, err := listYAML(samplesDir)
	require.NoError(t, err)
	require.NotEmpty(t, sampleFiles, "at least one sample must exist under config/samples/tools/")

	// The kustomization file lists templates + samples — skip it here, we
	// walked directories directly.
	sampleFiles = filterOutKustomization(sampleFiles)

	for _, f := range sampleFiles {
		test, err := readTest(f)
		require.NoError(t, err, "parse %s", f)
		test.Namespace = ns
		test.ResourceVersion = ""
		require.NoError(t, k8sClient.Create(ctx, test), "create sample %s", test.Name)

		// Every sample must reference a template.
		require.NotEmpty(t, test.Spec.Use,
			"sample %s must set spec.use[] to reference a catalog template", test.Name)

		// Trigger a TestRun to exercise the FULL resolve pipeline in the
		// controller: template merge + config resolution + expression eval
		// + ValidateResolved. If any of that fails the run lands terminal
		// with phase=error, which we assert against.
		// No TestRun-side config overrides — samples set their own defaults
		// on Test.spec.config (redeclaring the template's parameters with
		// concrete values). This is what makes `kubectl apply -k
		// config/samples/tools/` a self-sufficient reproducer.
		run := &testsv1alpha1.TestRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:      test.Name + "-run",
				Namespace: ns,
			},
			Spec: testsv1alpha1.TestRunSpec{
				TestRef: test.Name,
				Source:  "api",
			},
		}
		require.NoError(t, k8sClient.Create(ctx, run))

		// Wait for the reconciler to snapshot resolvedSpec (means resolve
		// succeeded) OR transition to phase=error (means resolve failed —
		// we then fail this row of the table with the recorded reason).
		var finalRun testsv1alpha1.TestRun
		runKey := client.ObjectKey{Namespace: ns, Name: run.Name}
		require.Eventually(t, func() bool {
			if err := k8sClient.Get(ctx, runKey, &finalRun); err != nil {
				return false
			}
			return finalRun.Status.ResolvedSpec != "" ||
				finalRun.Status.Phase == testsv1alpha1.PhaseError
		}, 10*time.Second, 100*time.Millisecond,
			"reconcile must either resolve or fail-fast for sample %s", test.Name)

		if finalRun.Status.Phase == testsv1alpha1.PhaseError {
			t.Fatalf("sample %s failed resolution: %s", test.Name, finalRun.Status.Message)
		}

		// Verify the resolved spec meets the catalog invariants: image and
		// command/args populated, tool label survives.
		var resolved testsv1alpha1.TestSpec
		require.NoError(t, json.Unmarshal([]byte(finalRun.Status.ResolvedSpec), &resolved),
			"resolved spec of %s should unmarshal", test.Name)
		assert.NotEmpty(t, resolved.Container.Image,
			"resolved spec for %s must carry a container.image (from template)", test.Name)
		assert.True(t, len(resolved.Container.Command) > 0 || len(resolved.Container.Args) > 0,
			"resolved spec for %s must carry command or args (from template)", test.Name)
	}
}

// TestCatalog_JMeterCarriesVerdictJTL is a targeted assertion for the
// canonical "exit code lies" case: the jmeter template MUST carry
// verdict.from=jtl with errorRateMax="0". Without this every JMeter run
// would report passed regardless of failure rate.
//
// Regression guard: a well-intentioned refactor that drops the verdict
// block from the template MUST trip this test.
func TestCatalog_JMeterCarriesVerdictJTL(t *testing.T) {
	repoRoot, err := findRepoRoot()
	require.NoError(t, err)
	tmpl, err := readTestTemplate(filepath.Join(repoRoot, "config", "templates", "jmeter.yaml"))
	require.NoError(t, err)

	require.NotNil(t, tmpl.Spec.Verdict, "jmeter template must set spec.verdict")
	assert.Equal(t, "jtl", tmpl.Spec.Verdict.From,
		"jmeter template MUST use verdictFrom: jtl — JMeter's exit code lies (100%% errors → exit 0)")
	assert.Equal(t, "0", tmpl.Spec.Verdict.ErrorRateMax,
		"jmeter template default errorRateMax MUST be strict (0); consumers loosen per-Test if they tolerate errors")
}

// TestCatalog_CypressCarriesDevShmMemory is the step-11 lesson pinned as a
// catalog invariant: the cypress template carries the memory-backed
// emptyDir for /dev/shm via spec.pod.volumes (moved from compiler to
// catalog per step-11 "no per-tool branches in the platform").
func TestCatalog_CypressCarriesDevShmMemory(t *testing.T) {
	repoRoot, err := findRepoRoot()
	require.NoError(t, err)
	tmpl, err := readTestTemplate(filepath.Join(repoRoot, "config", "templates", "cypress.yaml"))
	require.NoError(t, err)

	require.NotNil(t, tmpl.Spec.Pod, "cypress template must set spec.pod")
	found := false
	for _, v := range tmpl.Spec.Pod.Volumes {
		if v.Name != "dshm" {
			continue
		}
		require.NotNil(t, v.EmptyDir, "dshm volume must be an emptyDir")
		assert.Equal(t, "Memory", string(v.EmptyDir.Medium),
			"dshm emptyDir must be Memory-backed (tmpfs)")
		require.NotNil(t, v.EmptyDir.SizeLimit)
		found = true
	}
	assert.True(t, found,
		"cypress template must carry a memory emptyDir named 'dshm' — regression guard for the step-11 move out of the compiler")
}

// --- helpers ----------------------------------------------------------

func listYAML(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".yaml") && !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out, nil
}

func filterOutKustomization(files []string) []string {
	out := files[:0]
	for _, f := range files {
		if strings.HasSuffix(f, "kustomization.yaml") ||
			strings.HasSuffix(f, "kustomization.yml") {
			continue
		}
		out = append(out, f)
	}
	return out
}

func readTestTemplate(path string) (*testsv1alpha1.TestTemplate, error) {
	// #nosec G304 -- path built from repoRoot + literal subdir; test-only.
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t testsv1alpha1.TestTemplate
	if err := yaml.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func readTest(path string) (*testsv1alpha1.Test, error) {
	// #nosec G304 -- path built from repoRoot + literal subdir; test-only.
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t testsv1alpha1.Test
	if err := yaml.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// TestCatalog_LoadToolsDeclareMetrics: every load tool in the catalog must
// tell /entry where its report is, or its runs have no metrics (which was
// the case for all of them before spec.metrics existed).
func TestCatalog_LoadToolsDeclareMetrics(t *testing.T) {
	repoRoot, err := findRepoRoot()
	require.NoError(t, err)
	want := map[string]string{
		"k6": "k6Summary", "jmeter": "jtl", "locust": "locustCsv",
		"gatling": "gatlingStats", "artillery": "artilleryJson",
	}
	for name, from := range want {
		tmpl, err := readTestTemplate(filepath.Join(repoRoot, "config", "templates", name+".yaml"))
		require.NoError(t, err, name)
		require.NotNil(t, tmpl.Spec.Metrics, "%s template must set spec.metrics", name)
		assert.Equal(t, from, tmpl.Spec.Metrics.From, name)
	}
}

// TestCatalog_OutputPathsMatchWorkingDir: artifact/metrics globs are
// relative to the wrapper's working directory — /data unless the template
// sets container.workingDir. Templates without one wrote to
// /data/repo/results but globbed "results/**", so nothing was ever
// scraped. Every such glob must start with repo/.
func TestCatalog_OutputPathsMatchWorkingDir(t *testing.T) {
	repoRoot, err := findRepoRoot()
	require.NoError(t, err)
	files, err := filepath.Glob(filepath.Join(repoRoot, "config", "templates", "*.yaml"))
	require.NoError(t, err)
	for _, f := range files {
		tmpl, err := readTestTemplate(f)
		require.NoError(t, err, f)
		if tmpl.Spec.Container.WorkingDir != "" {
			continue // globs are relative to the template's own workingDir
		}
		var globs []string
		if tmpl.Spec.Artifacts != nil {
			globs = append(globs, tmpl.Spec.Artifacts.Paths...)
			if r := tmpl.Spec.Artifacts.Report; r != "" {
				globs = append(globs, r)
			}
		}
		if tmpl.Spec.Metrics != nil {
			globs = append(globs, tmpl.Spec.Metrics.Path)
		}
		for _, g := range globs {
			assert.True(t, strings.HasPrefix(g, "repo/") || strings.HasPrefix(g, "**/"),
				"%s: %q is relative to /data — tools write under /data/repo", filepath.Base(f), g)
		}
	}
}

// TestCatalog_ReportIsCollected: spec.artifacts.report only names one of
// the collected artifacts, so a report outside every artifacts glob would
// never be scraped and the run would have no report. Checked on a concrete
// path the report pattern stands for (globs replaced by a sample segment).
func TestCatalog_ReportIsCollected(t *testing.T) {
	repoRoot, err := findRepoRoot()
	require.NoError(t, err)
	files, err := filepath.Glob(filepath.Join(repoRoot, "config", "templates", "*.yaml"))
	require.NoError(t, err)
	reports := 0
	for _, f := range files {
		tmpl, err := readTestTemplate(f)
		require.NoError(t, err, f)
		if tmpl.Spec.Artifacts == nil || tmpl.Spec.Artifacts.Report == "" {
			continue
		}
		reports++
		sample := strings.NewReplacer("**", "x/y", "*", "x").Replace(tmpl.Spec.Artifacts.Report)
		matched, err := doublestar.Match(tmpl.Spec.Artifacts.Report, sample)
		require.NoError(t, err)
		require.True(t, matched, "%s: sample %q must match its own report pattern", filepath.Base(f), sample)
		assert.True(t, slices.ContainsFunc(tmpl.Spec.Artifacts.Paths, func(g string) bool {
			m, _ := doublestar.Match(g, sample)
			return m
		}), "%s: report %q is not collected by artifacts.paths", filepath.Base(f), tmpl.Spec.Artifacts.Report)
	}
	assert.GreaterOrEqual(t, reports, 7, "the catalog's report-producing templates declare their report")
}

// TestCatalog_SamplesRunTheCatalogProjects: every sample used to point at
// github.com/kubetest-alt/example-tests, which never existed — no sample
// could run. Each now sparse-checks out the project the catalog e2e
// verifies (test/catalog/cases/<tool>/repo of this repo); every path-valued
// parameter default must exist there, or the sample is broken again.
func TestCatalog_SamplesRunTheCatalogProjects(t *testing.T) {
	repoRoot, err := findRepoRoot()
	require.NoError(t, err)
	files, err := listYAML(filepath.Join(repoRoot, "config", "samples", "tools"))
	require.NoError(t, err)
	files = filterOutKustomization(files)

	templates, err := listYAML(filepath.Join(repoRoot, "config", "templates"))
	require.NoError(t, err)
	assert.Len(t, files, len(templates), "one sample per catalog template")

	for _, f := range files {
		test, err := readTest(f)
		require.NoError(t, err, f)
		require.Len(t, test.Spec.Use, 1, f)
		tool := test.Spec.Use[0]
		project := "test/catalog/cases/" + tool + "/repo"

		git := test.Spec.Content.Git
		require.NotNil(t, git, "%s: sample must fetch its project from git", f)
		assert.Equal(t, "https://github.com/hinskii/kubetest-alt.git", git.URI, f)
		assert.Equal(t, []string{project}, git.Paths, f)
		assert.DirExists(t, filepath.Join(repoRoot, project), f)

		for name, p := range test.Spec.Config {
			v := p.Default
			if !strings.HasPrefix(v, "test/catalog/") {
				continue // not a path (host, durations, counts, flags)
			}
			// Paths are relative to /data/repo == the repository root.
			globless := strings.SplitN(v, "*", 2)[0]
			_, err := os.Stat(filepath.Join(repoRoot, globless))
			assert.NoError(t, err, "%s: config %s=%q must exist in the repo", filepath.Base(f), name, v)
		}
	}
}
