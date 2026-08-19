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

// Package resolver produces the FULLY-resolved Test spec that the compiler
// consumes: TestTemplate fragments merged under the Test (Test wins, later
// template wins over earlier), config parameters resolved with type coercion,
// then `{{ }}` expressions evaluated single-pass against the (config, env,
// run, test) scope.
//
// The output is what lands verbatim in TestRun.status.resolvedSpec so the
// snapshot is stable across template edits after run start (§15.5).
//
// Pure-ish: takes a TemplateStore rather than a client to keep it unit-
// testable without envtest. The controller supplies a store that lists
// TestTemplates from the API server; unit tests supply an in-memory map.
package resolver

import (
	"errors"
	"fmt"
	"maps"

	corev1 "k8s.io/api/core/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/pkg/expr"
)

// TemplateStore is a tiny abstraction so Resolve doesn't depend on a k8s
// client. Production wires an implementation that reads from the controller
// cache; tests wire an in-memory map.
type TemplateStore interface {
	// Get returns the TestTemplate with the given name in the given
	// namespace. Returns (nil, ErrTemplateNotFound) if missing.
	Get(namespace, name string) (*testsv1alpha1.TestTemplate, error)
}

// ErrTemplateNotFound is the sentinel a TemplateStore returns when a name
// in spec.use references a non-existent TestTemplate.
var ErrTemplateNotFound = errors.New("template not found")

// ErrUnknownConfigKey is returned by ResolveConfig when a TestRun.spec.config
// key isn't declared in the RESOLVED Test.spec.config (Test itself OR any
// referenced template). The controller unwraps this with errors.Is to
// preserve reason=InvalidConfig (user typo) vs reason=ResolveFailed
// (template resolution failure) — the two errors deserve different
// treatment in the TestRun status message.
var ErrUnknownConfigKey = errors.New("config key not declared")

// Options carries per-resolution environment. Env is separate from
// Test.Spec.Config so the caller controls exactly which environment
// variables are exposed to `{{ env.* }}` — never os.Environ().
type Options struct {
	// Env is the environment map exposed as `{{ env.X }}`. Typically
	// operator-curated (specific keys the operator wants to project),
	// NOT the operator pod's whole environment.
	Env map[string]string

	// RunID is TestRun.metadata.name; surfaced as `{{ run.id }}`.
	RunID string
}

// Resolve returns a NEW *TestSpec built from:
//  1. templates in test.Spec.Use (merged in order, later wins);
//  2. test.Spec (wins over all templates);
//  3. config resolved from Test defaults with TestRun.spec.config overrides,
//     type-coerced per Parameter.Type / Enum / Pattern;
//  4. every string field passed through expr.Eval under the scope
//     {config, env, run.id, test.name}.
//
// Never mutates its inputs.
//
// Errors are returned WITH RESOURCE CONTEXT — the caller (controller)
// surfaces them as TestRun.status.message with reason=ResolveFailed.
func Resolve(test *testsv1alpha1.Test, run *testsv1alpha1.TestRun, store TemplateStore, opts Options) (*testsv1alpha1.TestSpec, error) {
	if test == nil {
		return nil, errors.New("nil Test")
	}
	if run == nil {
		return nil, errors.New("nil TestRun")
	}

	// Deep-copy the Test so subsequent merges don't touch the input.
	// TestSpec is a struct — the shallow copy at *test.Spec still shares
	// slices and pointers. mergeSpec builds a fresh spec value from
	// scratch so we don't rely on that behavior.
	merged := &testsv1alpha1.TestSpec{}

	// Step 1: merge templates in order.
	for _, name := range test.Spec.Use {
		tmpl, err := store.Get(test.Namespace, name)
		if err != nil {
			if errors.Is(err, ErrTemplateNotFound) {
				return nil, fmt.Errorf("template %q not found in namespace %q", name, test.Namespace)
			}
			return nil, fmt.Errorf("fetch template %q: %w", name, err)
		}
		mergeTemplateInto(merged, &tmpl.Spec)
	}

	// Step 2: Test overrides templates.
	mergeTestInto(merged, &test.Spec)

	// Step 3: config resolution + coercion.
	resolvedConfig, err := ResolveConfig(merged.Config, run.Spec.Config)
	if err != nil {
		return nil, err
	}

	// Step 4: expression pass over string fields under the resolved scope.
	// RunID comes from run.Name — it's part of the TestRun object and
	// there's no reason to let callers pass a different value. Options.RunID
	// is kept as an optional override for tests that want to fabricate a
	// run.id different from the TestRun's actual name.
	runID := opts.RunID
	if runID == "" {
		runID = run.Name
	}
	scope := expr.Scope{
		Config:   resolvedConfig,
		Env:      opts.Env,
		RunID:    runID,
		TestName: test.Name,
	}
	if err := evalStringsInSpec(merged, scope); err != nil {
		return nil, err
	}

	return merged, nil
}

// ResolveConfig applies test defaults overlaid with run overrides, enforces
// required-missing (no default), and coerces via expr.CoerceParam.
//
// Exported so the controller can call it separately for
// pre-templates-resolution validation of keys that ARE known.
func ResolveConfig(declared map[string]testsv1alpha1.Parameter, runOverrides map[string]string) (map[string]string, error) {
	// Cross-check: every override key must be declared. Wrap
	// ErrUnknownConfigKey so the controller can distinguish this class of
	// bug (user typo) from other resolve failures (template missing, type
	// mismatch, expression error).
	for k := range runOverrides {
		if _, ok := declared[k]; !ok {
			return nil, fmt.Errorf("%w: %q not in resolved Test.spec.config", ErrUnknownConfigKey, k)
		}
	}
	out := make(map[string]string, len(declared))
	for name, p := range declared {
		value, ok := runOverrides[name]
		if !ok {
			// Use default; missing default = required.
			if p.Default == "" {
				return nil, fmt.Errorf("required config key %q has no default and no TestRun override", name)
			}
			value = p.Default
		}
		coerced, err := expr.CoerceParam(name, value, expr.Parameter{
			Type:    p.Type,
			Enum:    append([]string(nil), p.Enum...),
			Pattern: p.Pattern,
		})
		if err != nil {
			return nil, err
		}
		out[name] = coerced
	}
	return out, nil
}

// ValidateResolved is called by the controller AFTER Resolve — enforces
// the workflows-model invariants that the webhook can't (because they
// might depend on template contents): container.image required,
// container.command/args required (at least one).
//
// Kept separate from Resolve so a test can inspect a resolved spec that
// happens to be missing image (e.g. explore behavior on a bad template)
// without the resolver refusing to return it.
func ValidateResolved(spec *testsv1alpha1.TestSpec) error {
	if spec == nil {
		return errors.New("nil resolved spec")
	}
	if spec.Container.Image == "" {
		return errors.New("resolved spec.container.image is empty (Test declares neither image nor a template supplying one)")
	}
	if len(spec.Container.Command) == 0 && len(spec.Container.Args) == 0 {
		return errors.New("resolved spec.container: at least one of command or args is required (neither Test nor any template supplied one)")
	}
	return nil
}

// --- merge helpers ---------------------------------------------------

// mergeTemplateInto overlays a template's spec onto dst. Set fields on the
// template REPLACE dst; unset fields leave dst intact. For maps + slices
// the merge is DEEP where the semantics call for merge (env, pod labels /
// annotations, volumes) and REPLACE where the semantics call for replace
// (container.command, container.args).
//
// The distinction matters:
//   - Merging container.command would produce a nonsense argv.
//   - Merging pod.annotations preserves what the user pinned in the Test
//     while still letting a template contribute defaults.
func mergeTemplateInto(dst *testsv1alpha1.TestSpec, tmpl *testsv1alpha1.TestTemplateSpec) {
	// Content: fields are replaced individually (git, files, tarball).
	// A template supplying content.git is uncommon but permitted; the
	// Test can override it wholesale.
	if tmpl.Content.Git != nil {
		dst.Content.Git = tmpl.Content.Git.DeepCopy()
	}
	if len(tmpl.Content.Files) > 0 {
		dst.Content.Files = append(dst.Content.Files, tmpl.Content.Files...)
	}
	if len(tmpl.Content.Tarball) > 0 {
		dst.Content.Tarball = append(dst.Content.Tarball, tmpl.Content.Tarball...)
	}
	mergeContainerInto(&dst.Container, tmpl.Container)
	dst.Pod = mergePod(dst.Pod, tmpl.Pod)
	if len(tmpl.Config) > 0 {
		if dst.Config == nil {
			dst.Config = map[string]testsv1alpha1.Parameter{}
		}
		for k, v := range tmpl.Config {
			if _, exists := dst.Config[k]; !exists {
				dst.Config[k] = v
			}
		}
	}
	if tmpl.Artifacts != nil && dst.Artifacts == nil {
		dst.Artifacts = tmpl.Artifacts.DeepCopy()
	}
	if tmpl.Timeout != nil && dst.Timeout == nil {
		dst.Timeout = tmpl.Timeout.DeepCopy()
	}
	if tmpl.Retry != nil && dst.Retry == nil {
		dst.Retry = tmpl.Retry.DeepCopy()
	}
	if len(tmpl.Services) > 0 && dst.Services == nil {
		dst.Services = maps.Clone(tmpl.Services)
	}
	if tmpl.Parallel != nil && dst.Parallel == nil {
		dst.Parallel = tmpl.Parallel.DeepCopy()
	}
}

// mergeTestInto overlays the Test's OWN spec fields onto dst — Test wins
// over any template contribution. For non-map fields the rule is "unset
// on Test → keep template's value; set on Test → replace with Test's".
//
// (The Test never contributes to dst via reference-sharing: everything is
// deep-copied so mutating dst later cannot mutate test.Spec.)
func mergeTestInto(dst *testsv1alpha1.TestSpec, test *testsv1alpha1.TestSpec) {
	if test.Content.Git != nil {
		dst.Content.Git = test.Content.Git.DeepCopy()
	}
	if len(test.Content.Files) > 0 {
		dst.Content.Files = append([]testsv1alpha1.FileContent(nil), test.Content.Files...)
	}
	if len(test.Content.Tarball) > 0 {
		dst.Content.Tarball = append([]testsv1alpha1.Tarball(nil), test.Content.Tarball...)
	}
	// Container: Test overrides template on set fields.
	overrideContainerFromTest(&dst.Container, test.Container)
	dst.Pod = mergePodTestWins(dst.Pod, test.Pod)
	if len(test.Config) > 0 {
		if dst.Config == nil {
			dst.Config = map[string]testsv1alpha1.Parameter{}
		}
		// Test's declarations REPLACE template defaults on the same key.
		maps.Copy(dst.Config, test.Config)
	}
	if test.Artifacts != nil {
		dst.Artifacts = test.Artifacts.DeepCopy()
	}
	if test.Timeout != nil {
		dst.Timeout = test.Timeout.DeepCopy()
	}
	if test.Retry != nil {
		dst.Retry = test.Retry.DeepCopy()
	}
	if test.ConcurrencyPolicy != "" {
		dst.ConcurrencyPolicy = test.ConcurrencyPolicy
	}
	if test.Verdict != nil {
		dst.Verdict = test.Verdict.DeepCopy()
	}
	if test.Schedule != "" {
		dst.Schedule = test.Schedule
	}
	if len(test.Services) > 0 {
		dst.Services = maps.Clone(test.Services)
	}
	if test.Parallel != nil {
		dst.Parallel = test.Parallel.DeepCopy()
	}
	// Carry Test.Spec.Use through to resolved spec so
	// TestRun.status.resolvedSpec re-serializes with the same shape.
	if len(test.Use) > 0 {
		dst.Use = append([]string(nil), test.Use...)
	}
}

// mergeContainerInto: template contributes ONLY where dst is empty.
// Env is merged additively (template env entries appended; a later
// mergeTestInto may override individual keys).
func mergeContainerInto(dst *testsv1alpha1.ContainerConfig, src testsv1alpha1.ContainerConfig) {
	if dst.Image == "" && src.Image != "" {
		dst.Image = src.Image
	}
	if dst.ImagePullPolicy == "" && src.ImagePullPolicy != "" {
		dst.ImagePullPolicy = src.ImagePullPolicy
	}
	if dst.WorkingDir == "" && src.WorkingDir != "" {
		dst.WorkingDir = src.WorkingDir
	}
	if len(dst.Command) == 0 && len(src.Command) > 0 {
		dst.Command = append([]string(nil), src.Command...)
	}
	if len(dst.Args) == 0 && len(src.Args) > 0 {
		dst.Args = append([]string(nil), src.Args...)
	}
	// Env: additive; do NOT dedupe by name here (kubelet uses "last one
	// wins" — mergeTestInto appends the Test's env after templates so the
	// Test wins naturally).
	dst.Env = append(dst.Env, src.Env...)
	dst.EnvFrom = append(dst.EnvFrom, src.EnvFrom...)
	dst.VolumeMounts = append(dst.VolumeMounts, src.VolumeMounts...)
	if dst.SecurityContext == nil && src.SecurityContext != nil {
		dst.SecurityContext = src.SecurityContext.DeepCopy()
	}
	// Resources: a template's requests/limits fill in only where dst is empty.
	if len(dst.Resources.Requests) == 0 && len(src.Resources.Requests) > 0 {
		dst.Resources.Requests = src.Resources.Requests.DeepCopy()
	}
	if len(dst.Resources.Limits) == 0 && len(src.Resources.Limits) > 0 {
		dst.Resources.Limits = src.Resources.Limits.DeepCopy()
	}
}

// overrideContainerFromTest lets a Test WIN on each set field.
func overrideContainerFromTest(dst *testsv1alpha1.ContainerConfig, src testsv1alpha1.ContainerConfig) {
	if src.Image != "" {
		dst.Image = src.Image
	}
	if src.ImagePullPolicy != "" {
		dst.ImagePullPolicy = src.ImagePullPolicy
	}
	if src.WorkingDir != "" {
		dst.WorkingDir = src.WorkingDir
	}
	if len(src.Command) > 0 {
		dst.Command = append([]string(nil), src.Command...)
	}
	if len(src.Args) > 0 {
		dst.Args = append([]string(nil), src.Args...)
	}
	// Env: append the Test's env AFTER template contributions so kubelet
	// "last one wins" makes the Test win per-key. No dedupe.
	dst.Env = append(dst.Env, src.Env...)
	dst.EnvFrom = append(dst.EnvFrom, src.EnvFrom...)
	dst.VolumeMounts = append(dst.VolumeMounts, src.VolumeMounts...)
	if src.SecurityContext != nil {
		dst.SecurityContext = src.SecurityContext.DeepCopy()
	}
	if len(src.Resources.Requests) > 0 {
		dst.Resources.Requests = src.Resources.Requests.DeepCopy()
	}
	if len(src.Resources.Limits) > 0 {
		dst.Resources.Limits = src.Resources.Limits.DeepCopy()
	}
}

// mergePod merges a template's PodConfig into dst. Labels + annotations are
// keyed maps so we merge KEY-WISE with template LOSING on collision (Test
// pod's mergePodTestWins is applied later — same reasoning). Slices
// (Tolerations, Volumes, ImagePullSecrets) append.
//
// Reserved-key protection is applied later by the compiler
// (podmerge.go/mergeLabels) so a template's `kubetest.io/*` label attempt
// is dropped there — no need to double-guard here.
func mergePod(dst, src *testsv1alpha1.PodConfig) *testsv1alpha1.PodConfig {
	if src == nil {
		return dst
	}
	if dst == nil {
		return src.DeepCopy()
	}
	out := dst.DeepCopy()
	if len(src.Labels) > 0 {
		if out.Labels == nil {
			out.Labels = map[string]string{}
		}
		for k, v := range src.Labels {
			if _, exists := out.Labels[k]; !exists {
				out.Labels[k] = v
			}
		}
	}
	if len(src.Annotations) > 0 {
		if out.Annotations == nil {
			out.Annotations = map[string]string{}
		}
		for k, v := range src.Annotations {
			if _, exists := out.Annotations[k]; !exists {
				out.Annotations[k] = v
			}
		}
	}
	if out.ServiceAccountName == "" && src.ServiceAccountName != "" {
		out.ServiceAccountName = src.ServiceAccountName
	}
	if len(src.NodeSelector) > 0 {
		if out.NodeSelector == nil {
			out.NodeSelector = map[string]string{}
		}
		for k, v := range src.NodeSelector {
			if _, exists := out.NodeSelector[k]; !exists {
				out.NodeSelector[k] = v
			}
		}
	}
	out.Tolerations = append(out.Tolerations, src.Tolerations...)
	out.Volumes = append(out.Volumes, src.Volumes...)
	out.ImagePullSecrets = append(out.ImagePullSecrets, src.ImagePullSecrets...)
	if out.Affinity == nil && src.Affinity != nil {
		out.Affinity = src.Affinity.DeepCopy()
	}
	if out.SecurityContext == nil && src.SecurityContext != nil {
		out.SecurityContext = src.SecurityContext.DeepCopy()
	}
	return out
}

// mergePodTestWins merges the Test's PodConfig on TOP of dst — Test wins
// on every key. This is the second pass, after all templates have merged.
func mergePodTestWins(dst, src *testsv1alpha1.PodConfig) *testsv1alpha1.PodConfig {
	if src == nil {
		return dst
	}
	if dst == nil {
		return src.DeepCopy()
	}
	out := dst.DeepCopy()
	if len(src.Labels) > 0 {
		if out.Labels == nil {
			out.Labels = map[string]string{}
		}
		maps.Copy(out.Labels, src.Labels) // Test overrides template
	}
	if len(src.Annotations) > 0 {
		if out.Annotations == nil {
			out.Annotations = map[string]string{}
		}
		maps.Copy(out.Annotations, src.Annotations)
	}
	if src.ServiceAccountName != "" {
		out.ServiceAccountName = src.ServiceAccountName
	}
	if len(src.NodeSelector) > 0 {
		if out.NodeSelector == nil {
			out.NodeSelector = map[string]string{}
		}
		maps.Copy(out.NodeSelector, src.NodeSelector)
	}
	// Test's slices REPLACE templates' contributions when set.
	if len(src.Tolerations) > 0 {
		out.Tolerations = append([]corev1.Toleration(nil), src.Tolerations...)
	}
	if len(src.Volumes) > 0 {
		out.Volumes = append([]corev1.Volume(nil), src.Volumes...)
	}
	if len(src.ImagePullSecrets) > 0 {
		out.ImagePullSecrets = append([]corev1.LocalObjectReference(nil), src.ImagePullSecrets...)
	}
	if src.Affinity != nil {
		out.Affinity = src.Affinity.DeepCopy()
	}
	if src.SecurityContext != nil {
		out.SecurityContext = src.SecurityContext.DeepCopy()
	}
	return out
}

// --- expression pass over the resolved spec --------------------------

// evalStringsInSpec walks the spec and passes every string field that
// meaningfully accepts templating through expr.Eval. Fields where user-
// controlled interpolation would be a bad idea (Container.Image itself,
// SecretKeyRef names, ...) are LEFT ALONE — the workflows model's contract
// is that operators pin image refs, not that they template them.
//
// The exact set of interpolated fields:
//   - container.command[], container.args[]  (the tool invocation)
//   - container.workingDir                    (relative paths sometimes tag'd)
//   - container.env[].value                   (feature flags, tags)
//   - pod.labels values                       (tag propagation)
//   - pod.annotations values                  (mesh hints etc.)
//   - artifacts.paths[]                       (glob templates referencing config)
//
// Container.Image is intentionally NOT interpolated: image refs come from
// operator-controlled sources (spec + templates), and interpolating them
// would let a config value redirect execution to another image entirely.
func evalStringsInSpec(spec *testsv1alpha1.TestSpec, scope expr.Scope) error {
	// container.command / args
	if v, err := expr.EvalSlice(spec.Container.Command, scope); err != nil {
		return fmt.Errorf("resolve spec.container.command: %w", err)
	} else {
		spec.Container.Command = v
	}
	if v, err := expr.EvalSlice(spec.Container.Args, scope); err != nil {
		return fmt.Errorf("resolve spec.container.args: %w", err)
	} else {
		spec.Container.Args = v
	}
	if spec.Container.WorkingDir != "" {
		v, err := expr.Eval(spec.Container.WorkingDir, scope)
		if err != nil {
			return fmt.Errorf("resolve spec.container.workingDir: %w", err)
		}
		spec.Container.WorkingDir = v
	}
	for i := range spec.Container.Env {
		if spec.Container.Env[i].Value == "" {
			continue
		}
		v, err := expr.Eval(spec.Container.Env[i].Value, scope)
		if err != nil {
			return fmt.Errorf("resolve spec.container.env[%d].value: %w", i, err)
		}
		spec.Container.Env[i].Value = v
	}
	if spec.Pod != nil {
		if v, err := expr.EvalMap(spec.Pod.Labels, scope); err != nil {
			return fmt.Errorf("resolve spec.pod.labels: %w", err)
		} else {
			spec.Pod.Labels = v
		}
		if v, err := expr.EvalMap(spec.Pod.Annotations, scope); err != nil {
			return fmt.Errorf("resolve spec.pod.annotations: %w", err)
		} else {
			spec.Pod.Annotations = v
		}
	}
	if spec.Artifacts != nil {
		if v, err := expr.EvalSlice(spec.Artifacts.Paths, scope); err != nil {
			return fmt.Errorf("resolve spec.artifacts.paths: %w", err)
		} else {
			spec.Artifacts.Paths = v
		}
	}
	return nil
}

// --- TemplateStore implementations ------------------------------------

// MapStore is an in-memory TemplateStore for tests. Key format:
// "<namespace>/<name>".
type MapStore map[string]*testsv1alpha1.TestTemplate

// Get implements TemplateStore.
func (m MapStore) Get(namespace, name string) (*testsv1alpha1.TestTemplate, error) {
	if t, ok := m[namespace+"/"+name]; ok {
		return t, nil
	}
	return nil, ErrTemplateNotFound
}
