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
	"fmt"
	"slices"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

// ValidateConfigKeys checks that every key set on TestRun.spec.config is
// declared in Test.spec.config. This lives in the controller (not the webhook)
// because it requires a cross-object lookup that's race-prone at admission
// time — the Test could be updated between the webhook fire and the run
// starting (plan step-02 explicitly deferred this to the controller).
//
// Returns nil if all keys are known; a human-readable error naming the first
// unknown key otherwise. The error message is what lands in TestRunStatus.Message.
func ValidateConfigKeys(runConfig map[string]string, testConfig map[string]testsv1alpha1.Parameter) error {
	if len(runConfig) == 0 {
		return nil
	}
	// Sort for deterministic error messages — otherwise two different
	// "first unknown key" outcomes make tests flaky.
	unknown := make([]string, 0)
	for k := range runConfig {
		if _, ok := testConfig[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	slices.Sort(unknown)
	if len(unknown) == 1 {
		return fmt.Errorf("config key %q not declared in Test.spec.config", unknown[0])
	}
	return fmt.Errorf("config keys %q not declared in Test.spec.config", unknown)
}
