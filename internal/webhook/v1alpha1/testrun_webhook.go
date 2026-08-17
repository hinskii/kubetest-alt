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

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

var testrunlog = logf.Log.WithName("testrun-resource")

// DefaultSource is applied when spec.source is unset.
const DefaultSource = "api"

// SetupTestRunWebhookWithManager registers the TestRun validating + defaulting webhook.
func SetupTestRunWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &testsv1alpha1.TestRun{}).
		WithValidator(&TestRunCustomValidator{}).
		WithDefaulter(&TestRunCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-tests-kubetest-io-v1alpha1-testrun,mutating=true,failurePolicy=fail,sideEffects=None,groups=tests.kubetest.io,resources=testruns,verbs=create;update,versions=v1alpha1,name=mtestrun-v1alpha1.kb.io,admissionReviewVersions=v1

// TestRunCustomDefaulter fills in default values on TestRun create/update.
type TestRunCustomDefaulter struct{}

// Default sets spec.source=api when unset. Existing values are never overwritten.
// Deliberately does NOT touch spec.pod.* — §8 PodConfig passthrough is verbatim.
func (d *TestRunCustomDefaulter) Default(_ context.Context, obj *testsv1alpha1.TestRun) error {
	testrunlog.V(1).Info("Defaulting TestRun", "name", obj.GetName())
	if obj.Spec.Source == "" {
		obj.Spec.Source = DefaultSource
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-tests-kubetest-io-v1alpha1-testrun,mutating=false,failurePolicy=fail,sideEffects=None,groups=tests.kubetest.io,resources=testruns,verbs=create;update,versions=v1alpha1,name=vtestrun-v1alpha1.kb.io,admissionReviewVersions=v1

// TestRunCustomValidator implements shape-only rules from step-02.
// Cross-object lookups (does the referenced Test exist? are config keys valid?)
// happen in the controller, not here — admission is race-prone.
type TestRunCustomValidator struct{}

// ValidateCreate runs on TestRun creation.
func (v *TestRunCustomValidator) ValidateCreate(_ context.Context, obj *testsv1alpha1.TestRun) (admission.Warnings, error) {
	return nil, validateTestRun(&obj.Spec)
}

// ValidateUpdate runs on TestRun update. Same rules as create.
func (v *TestRunCustomValidator) ValidateUpdate(_ context.Context, _, newObj *testsv1alpha1.TestRun) (admission.Warnings, error) {
	return nil, validateTestRun(&newObj.Spec)
}

// ValidateDelete is a no-op — step-02 only validates create/update.
func (v *TestRunCustomValidator) ValidateDelete(_ context.Context, _ *testsv1alpha1.TestRun) (admission.Warnings, error) {
	return nil, nil
}

// validateTestRun is a pure function so unit tests can hit each rule directly.
func validateTestRun(spec *testsv1alpha1.TestRunSpec) error {
	if spec == nil {
		return errors.New("spec is required")
	}
	if spec.TestRef == "" {
		return errors.New("spec.testRef is required")
	}
	return nil
}
