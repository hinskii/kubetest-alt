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

package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// helper fixtures --------------------------------------------------------

// mkTestBase returns a Test with the minimum shape the resolver expects
// callers to supply. Tests mutate a copy per case.
func mkTestBase() *testsv1alpha1.Test {
	return &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "sample", Namespace: "ns"},
		Spec: testsv1alpha1.TestSpec{
			Container: testsv1alpha1.ContainerConfig{
				Image: "grafana/k6:2.2.0",
				Args:  []string{"run", "s.js"},
			},
		},
	}
}

func mkRun() *testsv1alpha1.TestRun {
	return &testsv1alpha1.TestRun{
		ObjectMeta: metav1.ObjectMeta{Name: "sample-run-1", Namespace: "ns"},
		Spec:       testsv1alpha1.TestRunSpec{TestRef: "sample"},
	}
}

// --- Config resolution --------------------------------------------------

func TestResolveConfig_Defaults(t *testing.T) {
	got, err := ResolveConfig(map[string]testsv1alpha1.Parameter{
		"vus":      {Type: "integer", Default: "10"},
		"duration": {Type: "string", Default: "30s"},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "10", got["vus"])
	assert.Equal(t, "30s", got["duration"])
}

func TestResolveConfig_OverridesWinOverDefaults(t *testing.T) {
	got, err := ResolveConfig(map[string]testsv1alpha1.Parameter{
		"vus": {Type: "integer", Default: "10"},
	}, map[string]string{"vus": "50"})
	require.NoError(t, err)
	assert.Equal(t, "50", got["vus"])
}

func TestResolveConfig_RequiredMissing_ErrorNamesParam(t *testing.T) {
	_, err := ResolveConfig(map[string]testsv1alpha1.Parameter{
		"vus": {Type: "integer"}, // no default = required
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"vus"`)
	assert.Contains(t, err.Error(), "required")
}

func TestResolveConfig_UnknownOverrideKey(t *testing.T) {
	_, err := ResolveConfig(map[string]testsv1alpha1.Parameter{
		"vus": {Type: "integer", Default: "10"},
	}, map[string]string{"nope": "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"nope"`)
}

func TestResolveConfig_TypeCoercion_Table(t *testing.T) {
	cases := []struct {
		name      string
		param     testsv1alpha1.Parameter
		override  string
		wantError bool
		wantValue string
	}{
		{"integer 5 ok", testsv1alpha1.Parameter{Type: "integer", Default: "5"}, "", false, "5"},
		{"integer 5.5 fails", testsv1alpha1.Parameter{Type: "integer", Default: "5.5"}, "", true, ""},
		{"number 5.5 ok", testsv1alpha1.Parameter{Type: "number", Default: "5.5"}, "", false, "5.5"},
		{"boolean true canonicalized",
			testsv1alpha1.Parameter{Type: "boolean", Default: "True"}, "", false, "true"},
		{"boolean 1 canonicalized to true",
			testsv1alpha1.Parameter{Type: "boolean", Default: "1"}, "", false, "true"},
		{"enum valid",
			testsv1alpha1.Parameter{Type: "string", Default: "small", Enum: []string{"small", "large"}}, "", false, "small"},
		{"enum override invalid",
			testsv1alpha1.Parameter{Type: "string", Default: "small", Enum: []string{"small", "large"}}, "medium", true, ""},
		{"pattern valid",
			testsv1alpha1.Parameter{Type: "string", Default: "v1.2.3", Pattern: `^v\d+\.\d+\.\d+$`}, "", false, "v1.2.3"},
		{"pattern invalid",
			testsv1alpha1.Parameter{Type: "string", Default: "latest", Pattern: `^v\d+\.\d+\.\d+$`}, "", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var overrides map[string]string
			if tc.override != "" {
				overrides = map[string]string{"k": tc.override}
			}
			got, err := ResolveConfig(map[string]testsv1alpha1.Parameter{"k": tc.param}, overrides)
			if tc.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantValue, got["k"])
		})
	}
}

// --- Merge semantics ----------------------------------------------------

func TestResolve_TemplateSuppliesImage_TestWithNoImage(t *testing.T) {
	test := mkTestBase()
	test.Spec.Container.Image = "" // Test has no image
	test.Spec.Use = []string{"k6-template"}

	tmpl := &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "k6-template", Namespace: "ns"},
		Spec: testsv1alpha1.TestTemplateSpec{
			Container: testsv1alpha1.ContainerConfig{
				Image: "grafana/k6:2.2.0",
				Args:  []string{"run", "-"},
			},
		},
	}
	store := MapStore{"ns/k6-template": tmpl}

	spec, err := Resolve(test, mkRun(), store, Options{})
	require.NoError(t, err)
	// Template supplies image; Test's empty image doesn't clobber.
	assert.Equal(t, "grafana/k6:2.2.0", spec.Container.Image)
	// Test overrides Args (both are set → Test wins).
	assert.Equal(t, []string{"run", "s.js"}, spec.Container.Args)
	// ValidateResolved passes.
	require.NoError(t, ValidateResolved(spec))
}

func TestResolve_TestWinsOverTemplateOnConflict(t *testing.T) {
	test := mkTestBase()
	test.Spec.Container.Image = "user/override:latest"
	test.Spec.Use = []string{"k6-template"}

	tmpl := &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "k6-template", Namespace: "ns"},
		Spec: testsv1alpha1.TestTemplateSpec{
			Container: testsv1alpha1.ContainerConfig{
				Image: "grafana/k6:2.2.0",
			},
		},
	}
	store := MapStore{"ns/k6-template": tmpl}

	spec, err := Resolve(test, mkRun(), store, Options{})
	require.NoError(t, err)
	assert.Equal(t, "user/override:latest", spec.Container.Image,
		"Test wins over template on container.image")
}

func TestResolve_MultipleTemplates_LaterWins(t *testing.T) {
	test := mkTestBase()
	test.Spec.Container.Image = "" // let templates supply
	test.Spec.Container.Args = nil // let templates supply
	test.Spec.Use = []string{"first", "second"}

	first := &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "first", Namespace: "ns"},
		Spec: testsv1alpha1.TestTemplateSpec{
			Container: testsv1alpha1.ContainerConfig{
				Image: "first/img:v1",
				Args:  []string{"first-args"},
			},
		},
	}
	second := &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: "ns"},
		Spec: testsv1alpha1.TestTemplateSpec{
			Container: testsv1alpha1.ContainerConfig{
				// second's image left empty → first's image survives.
				Args: []string{"second-args"},
			},
		},
	}
	store := MapStore{
		"ns/first":  first,
		"ns/second": second,
	}

	spec, err := Resolve(test, mkRun(), store, Options{})
	require.NoError(t, err)
	assert.Equal(t, "first/img:v1", spec.Container.Image,
		"later template only overrides where it SETS a value; first template's image survives")
	assert.Equal(t, []string{"first-args"}, spec.Container.Args,
		"later template did NOT re-set Args because mergeContainerInto only fills where dst is empty; first template's args survive")
}

func TestResolve_MultipleTemplates_LaterFillsWhereFirstEmpty(t *testing.T) {
	test := mkTestBase()
	test.Spec.Container.Image = ""
	test.Spec.Container.Args = nil
	test.Spec.Use = []string{"first", "second"}

	first := &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "first", Namespace: "ns"},
		Spec: testsv1alpha1.TestTemplateSpec{
			Container: testsv1alpha1.ContainerConfig{
				// only WorkingDir set — leaves image/args empty for `second`.
				WorkingDir: "/from-first",
			},
		},
	}
	second := &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: "ns"},
		Spec: testsv1alpha1.TestTemplateSpec{
			Container: testsv1alpha1.ContainerConfig{
				Image: "second/img:v1",
				Args:  []string{"second-args"},
			},
		},
	}
	store := MapStore{"ns/first": first, "ns/second": second}

	spec, err := Resolve(test, mkRun(), store, Options{})
	require.NoError(t, err)
	assert.Equal(t, "second/img:v1", spec.Container.Image)
	assert.Equal(t, "/from-first", spec.Container.WorkingDir)
	assert.Equal(t, []string{"second-args"}, spec.Container.Args)
}

func TestResolve_PodAnnotations_TemplateContributes_TestWins(t *testing.T) {
	test := mkTestBase()
	test.Spec.Use = []string{"tpl"}
	test.Spec.Pod = &testsv1alpha1.PodConfig{
		Annotations: map[string]string{
			"sidecar.istio.io/inject": "false", // test's choice, per §8
			"user/pinned":             "yes",
		},
	}
	tmpl := &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "tpl", Namespace: "ns"},
		Spec: testsv1alpha1.TestTemplateSpec{
			Pod: &testsv1alpha1.PodConfig{
				Annotations: map[string]string{
					"sidecar.istio.io/inject": "true",  // template default — Test overrides
					"template/default":        "hello", // Test doesn't touch this
				},
			},
		},
	}
	store := MapStore{"ns/tpl": tmpl}

	spec, err := Resolve(test, mkRun(), store, Options{})
	require.NoError(t, err)
	require.NotNil(t, spec.Pod)
	// §8: platform hardcodes NO annotations, no special-casing. Merge:
	//   - Test's key wins (istio.io/inject = false)
	//   - template's unique key survives (template/default = hello)
	//   - Test's unique key present (user/pinned = yes)
	assert.Equal(t, "false", spec.Pod.Annotations["sidecar.istio.io/inject"])
	assert.Equal(t, "hello", spec.Pod.Annotations["template/default"])
	assert.Equal(t, "yes", spec.Pod.Annotations["user/pinned"])
}

// --- Expression pass ----------------------------------------------------

func TestResolve_InterpolatesContainerArgsAndEnv(t *testing.T) {
	test := mkTestBase()
	test.Spec.Container.Args = []string{
		"run", "--vus", "{{ config.vus }}", "--tag", "run={{ run.id }}", "s.js",
	}
	test.Spec.Container.Env = []corev1.EnvVar{
		{Name: "TEST_NAME", Value: "{{ test.name }}"},
		{Name: "REGION", Value: "{{ env.REGION }}"},
	}
	test.Spec.Config = map[string]testsv1alpha1.Parameter{
		"vus": {Type: "integer", Default: "10"},
	}
	run := mkRun()
	run.Spec.Config = map[string]string{"vus": "50"}

	spec, err := Resolve(test, run, MapStore{}, Options{
		Env: map[string]string{"REGION": "eu-west-1"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"run", "--vus", "50", "--tag", "run=sample-run-1", "s.js"},
		spec.Container.Args)
	assert.Equal(t, "sample", spec.Container.Env[0].Value)
	assert.Equal(t, "eu-west-1", spec.Container.Env[1].Value)
}

func TestResolve_UnknownRef_ErrorNamesField(t *testing.T) {
	test := mkTestBase()
	test.Spec.Container.Args = []string{"--vus", "{{ config.nope }}"}
	test.Spec.Config = map[string]testsv1alpha1.Parameter{
		"vus": {Type: "integer", Default: "10"},
	}
	_, err := Resolve(test, mkRun(), MapStore{}, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.container.args")
	assert.Contains(t, err.Error(), `unknown config key "nope"`)
}

// TestResolve_InjectionSafety_ConfigValueContainingExpr: single-pass
// evaluation invariant. A config value that HAPPENS to contain `{{...}}`
// lands verbatim in the resolved spec — NOT re-evaluated. Mandatory
// per the plan's security requirement.
func TestResolve_InjectionSafety_ConfigValueContainingExpr(t *testing.T) {
	test := mkTestBase()
	test.Spec.Container.Args = []string{"--tag", "{{ config.attack }}"}
	test.Spec.Config = map[string]testsv1alpha1.Parameter{
		"attack": {Type: "string", Default: "{{ config.secret }}"},
		"secret": {Type: "string", Default: "s3cr3t"},
	}

	spec, err := Resolve(test, mkRun(), MapStore{}, Options{})
	require.NoError(t, err)
	// If the resolver re-scanned the substituted string, "s3cr3t" would
	// appear here — a config-value-carried second-pass injection would
	// exfiltrate other config values.
	assert.Equal(t, []string{"--tag", "{{ config.secret }}"}, spec.Container.Args,
		"resolver must be single-pass — config values carrying `{{...}}` land verbatim")
}

// --- Template lookup errors --------------------------------------------

func TestResolve_MissingTemplate_ErrorNamesTemplate(t *testing.T) {
	test := mkTestBase()
	test.Spec.Use = []string{"missing"}
	_, err := Resolve(test, mkRun(), MapStore{}, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"missing"`)
	assert.Contains(t, err.Error(), "not found")
}

// --- ValidateResolved ---------------------------------------------------

func TestValidateResolved_Table(t *testing.T) {
	cases := []struct {
		name    string
		spec    *testsv1alpha1.TestSpec
		wantErr bool
	}{
		{"ok — image + args", &testsv1alpha1.TestSpec{
			Container: testsv1alpha1.ContainerConfig{Image: "x", Args: []string{"y"}},
		}, false},
		{"ok — image + command", &testsv1alpha1.TestSpec{
			Container: testsv1alpha1.ContainerConfig{Image: "x", Command: []string{"z"}},
		}, false},
		{"fail — missing image", &testsv1alpha1.TestSpec{
			Container: testsv1alpha1.ContainerConfig{Args: []string{"y"}},
		}, true},
		{"fail — missing command AND args", &testsv1alpha1.TestSpec{
			Container: testsv1alpha1.ContainerConfig{Image: "x"},
		}, true},
		// Step 17: composite Tests carry Steps and NO container.
		{"ok — composite has no image/command", &testsv1alpha1.TestSpec{
			Steps: []testsv1alpha1.Step{{
				Execute: &testsv1alpha1.StepExecute{
					Tests: []testsv1alpha1.StepExecuteTest{{Name: "child"}},
				},
			}},
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateResolved(tc.spec)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestResolve_CarriesSteps_ThroughMerge (step 17): a composite Test's
// spec.steps[] MUST survive the merge pipeline verbatim so the
// reconciler reads the composite plan back from resolvedSpec. This
// pins the mergeTestInto step-copy that the composite envtest also
// exercises indirectly.
func TestResolve_CarriesSteps_ThroughMerge(t *testing.T) {
	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "comp", Namespace: "ns"},
		Spec: testsv1alpha1.TestSpec{
			Steps: []testsv1alpha1.Step{
				{Name: "smoke", Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "child-a", Count: 2}}}},
				{Name: "load", Condition: "passed", Execute: &testsv1alpha1.StepExecute{Tests: []testsv1alpha1.StepExecuteTest{{Name: "child-b"}}}},
			},
		},
	}
	spec, err := Resolve(test, mkRun(), MapStore{}, Options{})
	require.NoError(t, err)
	require.Len(t, spec.Steps, 2)
	assert.Equal(t, "smoke", spec.Steps[0].Name)
	require.NotNil(t, spec.Steps[0].Execute)
	require.Len(t, spec.Steps[0].Execute.Tests, 1)
	assert.Equal(t, "child-a", spec.Steps[0].Execute.Tests[0].Name)
	assert.Equal(t, int32(2), spec.Steps[0].Execute.Tests[0].Count)
	assert.Equal(t, "passed", spec.Steps[1].Condition)
	// Deep-copy: mutating the resolved output must NOT touch the input.
	spec.Steps[0].Name = "mutated"
	assert.Equal(t, "smoke", test.Spec.Steps[0].Name)
}

// TestResolve_TemplateVerdictContributes_TestOverrides: the step-15
// invariant — a template can carry verdictFrom (e.g. JMeter's
// jtl+errorRateMax="0") and consumers inherit it; if the Test explicitly
// declares its own Verdict, the Test wins per §13 merge semantics.
func TestResolve_TemplateVerdictContributes_TestOverrides(t *testing.T) {
	// Case 1: template supplies verdict, Test doesn't → template wins.
	tmpl := &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "jmeter-catalog", Namespace: "ns"},
		Spec: testsv1alpha1.TestTemplateSpec{
			Container: testsv1alpha1.ContainerConfig{
				Image: "alpine/jmeter:5.6.3",
				Args:  []string{"-n", "-t", "plan.jmx"},
			},
			Verdict: &testsv1alpha1.VerdictSpec{From: "jtl", ErrorRateMax: "0"},
		},
	}
	store := MapStore{"ns/jmeter-catalog": tmpl}

	test := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "load", Namespace: "ns"},
		Spec:       testsv1alpha1.TestSpec{Use: []string{"jmeter-catalog"}},
	}
	spec, err := Resolve(test, mkRun(), store, Options{})
	require.NoError(t, err)
	require.NotNil(t, spec.Verdict)
	assert.Equal(t, "jtl", spec.Verdict.From)
	assert.Equal(t, "0", spec.Verdict.ErrorRateMax)

	// Case 2: Test declares its own Verdict → wins over template.
	test2 := &testsv1alpha1.Test{
		ObjectMeta: metav1.ObjectMeta{Name: "load2", Namespace: "ns"},
		Spec: testsv1alpha1.TestSpec{
			Use:     []string{"jmeter-catalog"},
			Verdict: &testsv1alpha1.VerdictSpec{From: "junit"},
		},
	}
	spec2, err := Resolve(test2, mkRun(), store, Options{})
	require.NoError(t, err)
	require.NotNil(t, spec2.Verdict)
	assert.Equal(t, "junit", spec2.Verdict.From,
		"Test-side Verdict wins over template per §13 merge semantics")
	assert.Empty(t, spec2.Verdict.ErrorRateMax,
		"Test's Verdict REPLACES template's — no cross-field bleed")
}

// TestResolve_DoesNotMutateInputs is the "pure-ish" invariant: resolving
// twice should yield equivalent output; the first call must not have
// touched the input Test or template.
func TestResolve_DoesNotMutateInputs(t *testing.T) {
	test := mkTestBase()
	test.Spec.Use = []string{"tpl"}
	origArgs := append([]string(nil), test.Spec.Container.Args...)
	tmpl := &testsv1alpha1.TestTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "tpl", Namespace: "ns"},
		Spec: testsv1alpha1.TestTemplateSpec{
			Container: testsv1alpha1.ContainerConfig{Image: "grafana/k6:2.2.0"},
			Pod: &testsv1alpha1.PodConfig{
				Annotations: map[string]string{"foo": "bar"},
			},
		},
	}
	store := MapStore{"ns/tpl": tmpl}

	_, err := Resolve(test, mkRun(), store, Options{})
	require.NoError(t, err)
	// Inputs untouched.
	assert.Equal(t, origArgs, test.Spec.Container.Args)
	assert.Nil(t, test.Spec.Pod, "resolver must not backfill Pod on the input Test")
	assert.NotNil(t, tmpl.Spec.Pod, "template unchanged")
}
