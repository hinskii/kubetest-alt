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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
	"github.com/hinskii/kubetest-alt/internal/compiler"
)

func stepWithTests(refs ...string) testsv1alpha1.Step {
	step := testsv1alpha1.Step{Execute: &testsv1alpha1.StepExecute{}}
	for _, r := range refs {
		step.Execute.Tests = append(step.Execute.Tests, testsv1alpha1.StepExecuteTest{Name: r})
	}
	return step
}

// fixes.md #8: with "api" and "api-smoke" in one step, a name-fragment
// match paired the "api" expectation with the "api-smoke" child, so "api"
// was never created and its outcome came from the wrong run.
func TestFindChild_OneTestRefContainingAnother(t *testing.T) {
	parent := &testsv1alpha1.TestRun{ObjectMeta: metav1.ObjectMeta{Name: "suite"}}
	expected := expectedChildren(parent, stepWithTests("api-smoke", "api"), 0)
	require.Len(t, expected, 2)

	smoke := testsv1alpha1.TestRun{ObjectMeta: metav1.ObjectMeta{
		Name:   expected[0].Name,
		Labels: map[string]string{compiler.LabelStep: "0", compiler.LabelExecIndex: "0"},
	}}
	kids := []testsv1alpha1.TestRun{smoke}
	assert.Same(t, &kids[0], findChild(kids, expected[0].Name))
	assert.Nil(t, findChild(kids, expected[1].Name), `"api" must not match the "api-smoke" child`)
}

func TestExpectedChildren_NamesFitAJob(t *testing.T) {
	parent := &testsv1alpha1.TestRun{ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("p", 60)}}
	for _, e := range expectedChildren(parent, stepWithTests(strings.Repeat("t", 40)), 3) {
		assert.LessOrEqual(t, len(e.Name), 63)
	}
}
