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
	"testing"

	"github.com/stretchr/testify/assert"

	testsv1alpha1 "github.com/hinskii/kubetest-alt/api/v1alpha1"
)

func TestValidateConfigKeys(t *testing.T) {
	testConfig := map[string]testsv1alpha1.Parameter{
		"vus":      {Type: "integer", Default: "10"},
		"duration": {Type: "string", Default: "30s"},
	}
	cases := []struct {
		name       string
		runConfig  map[string]string
		testConfig map[string]testsv1alpha1.Parameter
		wantErr    bool
		wantSubstr string
	}{
		{"nil runConfig OK", nil, testConfig, false, ""},
		{"empty runConfig OK", map[string]string{}, testConfig, false, ""},
		{"all known keys OK", map[string]string{"vus": "50", "duration": "1m"}, testConfig, false, ""},
		{"one unknown key", map[string]string{"vus": "50", "nope": "x"}, testConfig, true, `"nope"`},
		{
			"multiple unknown keys — deterministic sorted list",
			map[string]string{"z": "1", "a": "2", "m": "3"},
			testConfig, true, `["a" "m" "z"]`,
		},
		{
			"empty testConfig rejects everything",
			map[string]string{"vus": "1"}, nil, true, `"vus"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConfigKeys(tc.runConfig, tc.testConfig)
			if tc.wantErr {
				if assert.Error(t, err) {
					assert.Contains(t, err.Error(), tc.wantSubstr)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateConfigKeys_Deterministic(t *testing.T) {
	// Same input → same error string every time (guards against map-iteration
	// order flake in the message).
	run := map[string]string{"q": "1", "b": "2", "d": "3"}
	first := ValidateConfigKeys(run, nil).Error()
	for i := range 20 {
		got := ValidateConfigKeys(run, nil).Error()
		assert.Equal(t, first, got, "iteration %d produced different message", i)
	}
}
