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

var (
	allowedTestTypes = map[string]struct{}{
		"k6":      {},
		"cypress": {},
		"newman":  {},
		"locust":  {},
		"jmeter":  {},
	}

	allowedConcurrencyPolicies = map[string]struct{}{
		"":                 {}, // empty = "not set" — defaulter fills it before validation on real requests
		ConcurrencyAllow:   {},
		ConcurrencyForbid:  {},
		ConcurrencyReplace: {},
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
func validateTest(spec *testsv1alpha1.TestSpec) error {
	if spec == nil {
		return errors.New("spec is required")
	}
	if _, ok := allowedTestTypes[spec.Type]; !ok {
		return fmt.Errorf("spec.type %q is not one of [k6 cypress newman locust jmeter]", spec.Type)
	}
	if _, ok := allowedConcurrencyPolicies[spec.ConcurrencyPolicy]; !ok {
		return fmt.Errorf("spec.concurrencyPolicy %q is not one of [Allow Forbid Replace]", spec.ConcurrencyPolicy)
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

func inlineContentSize(files []testsv1alpha1.FileContent) int {
	total := 0
	for i := range files {
		total += len(files[i].Content)
	}
	return total
}
