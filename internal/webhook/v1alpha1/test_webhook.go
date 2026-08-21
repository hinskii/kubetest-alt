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

package v1alpha1

import (
	"context"
	"errors"
	"fmt"

	"strconv"

	"github.com/robfig/cron/v3"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

var testlog = logf.Log.WithName("test-resource")

const (
	// Allowed values for Test.spec.concurrencyPolicy. The enum is enforced both
	// by the kubebuilder marker on the type and by the validating webhook so
	// error messages are consistent across the two rejection paths.
	ConcurrencyAllow   = "Allow"
	ConcurrencyForbid  = "Forbid"
	ConcurrencyReplace = "Replace"

	// DefaultConcurrencyPolicy is applied when spec.concurrencyPolicy is unset.
	DefaultConcurrencyPolicy = ConcurrencyAllow

	// MaxInlineContentBytes bounds the aggregate size of spec.content.files[*].content
	// on a single Test. CLAUDE.md §15.7: keep well under etcd's ~1.5MB object limit;
	// larger payloads belong in git or a tarball URL.
	MaxInlineContentBytes = 512 * 1024

	// oversizeMessage MUST contain the substring "use git/tarball" — the step-02
	// unit test asserts this exact phrase.
	oversizeMessage = "inline content exceeds %d bytes (found %d) — use git/tarball source instead"
)

// VerdictFrom* mirror api/v1alpha1.VerdictSpec.From. Kept as local
// constants so the webhook doesn't grow an import path back to the CRD
// package just to name a string.
const (
	VerdictFromExitCode = "exitCode"
	VerdictFromJUnit    = "junit"
	VerdictFromJTL      = "jtl"
)

var (
	allowedConcurrencyPolicies = map[string]struct{}{
		"":                 {}, // empty = "not set" — defaulter fills it before validation on real requests
		ConcurrencyAllow:   {},
		ConcurrencyForbid:  {},
		ConcurrencyReplace: {},
	}

	allowedVerdictFrom = map[string]struct{}{
		"":                  {}, // empty → treat as exitCode (defaulted at read time)
		VerdictFromExitCode: {},
		VerdictFromJUnit:    {},
		VerdictFromJTL:      {},
	}

	// cronParser matches k8s CronJob semantics: 5 fields, no seconds.
	cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
)

// SetupTestWebhookWithManager registers the Test validating + defaulting webhook.
func SetupTestWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &testsv1alpha1.Test{}).
		WithValidator(&TestCustomValidator{}).
		WithDefaulter(&TestCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-tests-kubetest-io-v1alpha1-test,mutating=true,failurePolicy=fail,sideEffects=None,groups=tests.kubetest.io,resources=tests,verbs=create;update,versions=v1alpha1,name=mtest-v1alpha1.kb.io,admissionReviewVersions=v1

// TestCustomDefaulter fills in default values on Test create/update.
type TestCustomDefaulter struct{}

// Default sets spec.concurrencyPolicy=Allow when unset. Existing values are
// never overwritten. Deliberately does NOT touch spec.pod.* — §8 requires the
// PodConfig passthrough to be verbatim.
func (d *TestCustomDefaulter) Default(_ context.Context, obj *testsv1alpha1.Test) error {
	testlog.V(1).Info("Defaulting Test", "name", obj.GetName())
	if obj.Spec.ConcurrencyPolicy == "" {
		obj.Spec.ConcurrencyPolicy = DefaultConcurrencyPolicy
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-tests-kubetest-io-v1alpha1-test,mutating=false,failurePolicy=fail,sideEffects=None,groups=tests.kubetest.io,resources=tests,verbs=create;update,versions=v1alpha1,name=vtest-v1alpha1.kb.io,admissionReviewVersions=v1

// TestCustomValidator implements the create/update rules from step-02.
// Shape-only: no cross-object lookups (race-prone at admission; belongs in the controller).
type TestCustomValidator struct{}

// ValidateCreate runs on Test creation.
func (v *TestCustomValidator) ValidateCreate(_ context.Context, obj *testsv1alpha1.Test) (admission.Warnings, error) {
	return nil, validateTest(&obj.Spec)
}

// ValidateUpdate runs on Test update. Same rules as create.
func (v *TestCustomValidator) ValidateUpdate(_ context.Context, _, newObj *testsv1alpha1.Test) (admission.Warnings, error) {
	return nil, validateTest(&newObj.Spec)
}

// ValidateDelete is a no-op — step-02 only validates create/update.
func (v *TestCustomValidator) ValidateDelete(_ context.Context, _ *testsv1alpha1.Test) (admission.Warnings, error) {
	return nil, nil
}

// validateTest is a pure function so unit tests can hit each rule directly
// without spinning up envtest.
//
// Workflows-model rules (step 11) + step 13 template relaxation:
//   - spec.container.image REQUIRED unless spec.use is non-empty (a template
//     may supply it). Final image+command validity is enforced by the
//     TestRun controller AFTER template resolution — phase=error with
//     reason=ResolveFailed. Reason: the webhook can't fetch the templates
//     the Test references (cross-object lookup is race-prone at admission).
//   - spec.container.{command|args} REQUIRED (at least one) UNLESS spec.use
//     is non-empty — same reasoning.
//   - spec.verdict.from in {exitCode, junit, jtl} (empty = exitCode).
//   - spec.verdict.errorRateMax: only valid when from=jtl, must parse as
//     float in [0,1]. Empty otherwise (setting it with from!=jtl is a
//     user mistake we surface early instead of silently ignoring).
func validateTest(spec *testsv1alpha1.TestSpec) error {
	if spec == nil {
		return errors.New("spec is required")
	}
	// Step 17: shape branch. A Test is EITHER a leaf (image+command / use)
	// OR a composite (steps[]) — never both. This mirrors CLAUDE.md's
	// "one CRD, two shapes" note; the alternative (a separate Suite CRD)
	// was skipped to keep GitOps + the reconciler surface small.
	composite := len(spec.Steps) > 0
	if composite {
		return validateCompositeShape(spec)
	}
	// Step 13: relax image/command requirements when spec.use is set. A
	// template may supply them; the TestRun controller re-validates
	// post-resolution and surfaces failures with reason=ResolveFailed.
	requiresLocalContainer := len(spec.Use) == 0
	if requiresLocalContainer && spec.Container.Image == "" {
		return errors.New("spec.container.image is required (workflows model: no built-in tool images) — or reference a template via spec.use[] that supplies it")
	}
	if requiresLocalContainer && len(spec.Container.Command) == 0 && len(spec.Container.Args) == 0 {
		return errors.New("spec.container: at least one of command or args is required — or reference a template via spec.use[] that supplies one")
	}
	if _, ok := allowedConcurrencyPolicies[spec.ConcurrencyPolicy]; !ok {
		return fmt.Errorf("spec.concurrencyPolicy %q is not one of [Allow Forbid Replace]", spec.ConcurrencyPolicy)
	}
	if err := validateVerdict(spec.Verdict); err != nil {
		return err
	}
	if git := spec.Content.Git; git != nil && git.URI == "" {
		return errors.New("spec.content.git.uri is required when spec.content.git is set")
	}
	if size := inlineContentSize(spec.Content.Files); size > MaxInlineContentBytes {
		return fmt.Errorf(oversizeMessage, MaxInlineContentBytes, size)
	}
	if spec.Schedule != "" {
		if _, err := cronParser.Parse(spec.Schedule); err != nil {
			return fmt.Errorf("spec.schedule %q is not a valid cron expression: %w", spec.Schedule, err)
		}
	}
	return nil
}

// validateCompositeShape covers a Test that carries spec.steps[]. Enforces
// shape exclusivity vs the leaf shape AND the per-step invariants
// (execute non-nil, ≥1 test, condition enum, count≥1) — none of which
// are expressible via +kubebuilder markers alone.
func validateCompositeShape(spec *testsv1alpha1.TestSpec) error {
	// Exclusivity: none of the leaf-only fields may be set alongside steps.
	if spec.Container.Image != "" || len(spec.Container.Command) > 0 || len(spec.Container.Args) > 0 {
		return errors.New("spec.steps and spec.container are mutually exclusive")
	}
	if len(spec.Use) > 0 {
		return errors.New("spec.steps and spec.use are mutually exclusive (composite composes children, not templates)")
	}
	if spec.Content.Git != nil || len(spec.Content.Files) > 0 || len(spec.Content.Tarball) > 0 {
		return errors.New("spec.steps and spec.content are mutually exclusive (children carry their own content)")
	}
	if spec.Verdict != nil {
		return errors.New("spec.steps and spec.verdict are mutually exclusive (composite verdict comes from step aggregation)")
	}
	if len(spec.Services) > 0 {
		return errors.New("spec.steps and spec.services are mutually exclusive")
	}
	if spec.Parallel != nil {
		return errors.New("spec.steps and spec.parallel are mutually exclusive")
	}
	// concurrencyPolicy on composite is fine — it applies to the parent run itself.
	if _, ok := allowedConcurrencyPolicies[spec.ConcurrencyPolicy]; !ok {
		return fmt.Errorf("spec.concurrencyPolicy %q is not one of [Allow Forbid Replace]", spec.ConcurrencyPolicy)
	}
	if spec.Schedule != "" {
		if _, err := cronParser.Parse(spec.Schedule); err != nil {
			return fmt.Errorf("spec.schedule %q is not a valid cron expression: %w", spec.Schedule, err)
		}
	}
	// Per-step rules.
	for i := range spec.Steps {
		if err := validateStep(i, &spec.Steps[i]); err != nil {
			return err
		}
	}
	return nil
}

func validateStep(idx int, s *testsv1alpha1.Step) error {
	if s.Execute == nil {
		return fmt.Errorf("spec.steps[%d].execute is required (step 17 supports execute-only steps)", idx)
	}
	if len(s.Execute.Tests) == 0 {
		return fmt.Errorf("spec.steps[%d].execute.tests must have at least one entry", idx)
	}
	if s.Execute.Parallelism < 0 {
		return fmt.Errorf("spec.steps[%d].execute.parallelism must be >= 0", idx)
	}
	if s.Condition != "" && s.Condition != "passed" && s.Condition != "always" {
		return fmt.Errorf("spec.steps[%d].condition %q is not one of [passed always]", idx, s.Condition)
	}
	for j := range s.Execute.Tests {
		t := &s.Execute.Tests[j]
		if t.Name == "" {
			return fmt.Errorf("spec.steps[%d].execute.tests[%d].name is required", idx, j)
		}
		if t.Count < 0 {
			return fmt.Errorf("spec.steps[%d].execute.tests[%d].count must be >= 1 (0/unset means default 1)", idx, j)
		}
	}
	if s.Retry != nil && s.Retry.Count < 1 {
		return fmt.Errorf("spec.steps[%d].retry.count must be >= 1 when set", idx)
	}
	return nil
}

// validateVerdict enforces the VerdictSpec cross-field rules.
func validateVerdict(v *testsv1alpha1.VerdictSpec) error {
	if v == nil {
		return nil
	}
	if _, ok := allowedVerdictFrom[v.From]; !ok {
		return fmt.Errorf("spec.verdict.from %q is not one of [exitCode junit jtl]", v.From)
	}
	from := v.From
	if from == "" {
		from = VerdictFromExitCode
	}
	if v.ErrorRateMax == "" {
		return nil
	}
	if from != VerdictFromJTL {
		return fmt.Errorf(
			"spec.verdict.errorRateMax is only valid when spec.verdict.from=jtl (got from=%q)",
			from)
	}
	f, err := strconv.ParseFloat(v.ErrorRateMax, 64)
	if err != nil {
		return fmt.Errorf("spec.verdict.errorRateMax %q is not a valid float: %w", v.ErrorRateMax, err)
	}
	if f < 0 || f > 1 {
		return fmt.Errorf("spec.verdict.errorRateMax %v out of range [0,1]", f)
	}
	return nil
}

func inlineContentSize(files []testsv1alpha1.FileContent) int {
	total := 0
	for i := range files {
		total += len(files[i].Content)
	}
	return total
}
