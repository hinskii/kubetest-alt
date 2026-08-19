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

package expr

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mkScope is the fixture every eval test starts from. Tests mutate a copy.
func mkScope() Scope {
	return Scope{
		Config: map[string]string{
			"vus":       "10",
			"duration":  "30s",
			"multiline": "line1\nline2",
			// Injection-safety fixture: this VALUE contains what LOOKS like
			// an expression. Eval must NOT re-scan it (single-pass).
			"attack": "{{ config.vus }}",
		},
		Env: map[string]string{
			"FEATURE_X": "on",
			"REGION":    "eu-west-1",
		},
		RunID:    "sample-run-123",
		TestName: "sample",
	}
}

func TestEval_Table(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty input", "", ""},
		{"no expressions — literal passthrough", "hello world", "hello world"},
		{"single config ref", "vus={{ config.vus }}", "vus=10"},
		{"single env ref", "region={{ env.REGION }}", "region=eu-west-1"},
		{"run.id", "id={{ run.id }}", "id=sample-run-123"},
		{"test.name", "test={{ test.name }}", "test=sample"},
		{"two adjacent expressions", "{{ config.vus }}{{ config.duration }}", "1030s"},
		{"nested (separated) expressions in one string",
			"--vus {{ config.vus }} --duration {{ config.duration }} --tag env={{ env.REGION }}",
			"--vus 10 --duration 30s --tag env=eu-west-1"},
		{"whitespace tolerated around ref (spaces + tabs)",
			"vus={{\t  config.vus \t}}", "vus=10"},
		{"escape hatch — literal {{ produced by escapeOpenBrace",
			`prefix {{"{{"}} not_evaluated`, "prefix {{ not_evaluated"},
		{"escape hatch — literal }} produced by escapeCloseBrace",
			`prefix {{"}}"}} suffix`, "prefix }} suffix"},
		{"lone }} in literal position is fine",
			"json like {}} ends", "json like {}} ends"},
		{"multiline preserved verbatim, refs work across lines",
			"one={{ config.vus }}\ntwo={{ env.REGION }}", "one=10\ntwo=eu-west-1"},
		{"non-string coercion — integer config lands as string",
			"{{ config.vus }}", "10"}, // vus is declared integer in Parameter, but stored as string; expr never coerces
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Eval(tc.in, mkScope())
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestEval_UnknownRef_ErrorHasPositionAndName(t *testing.T) {
	// Position: `x={{ config.missing }}` — the `{{` starts at col 3.
	got, err := Eval("x={{ config.missing }}", mkScope())
	require.Error(t, err)
	assert.Empty(t, got)

	var e *Error
	require.True(t, errors.As(err, &e), "should be *expr.Error")
	assert.Equal(t, 1, e.Line)
	assert.Equal(t, 3, e.Col, "col points at start of `{{`")
	assert.Equal(t, "config.missing", e.Ref)
	assert.Contains(t, e.Message, `unknown config key "missing"`)
}

func TestEval_UnknownRef_Position_MultiLine(t *testing.T) {
	// `\n` puts us on line 2; `{{` starts at col 3 on line 2.
	_, err := Eval("first line\nx={{ env.NOPE }}", mkScope())
	require.Error(t, err)
	var e *Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, 2, e.Line)
	assert.Equal(t, 3, e.Col)
}

func TestEval_UnknownNamespace_ErrorNamesAllowed(t *testing.T) {
	_, err := Eval("{{ confg.vus }}", mkScope())
	require.Error(t, err)
	var e *Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, `unknown namespace "confg"`)
	assert.Contains(t, e.Message, "config")
	assert.Contains(t, e.Message, "env")
	assert.Contains(t, e.Message, "run")
	assert.Contains(t, e.Message, "test")
}

func TestEval_UnterminatedExpression(t *testing.T) {
	_, err := Eval("start {{ config.vus", mkScope())
	require.Error(t, err)
	var e *Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "unterminated")
	assert.Equal(t, 7, e.Col)
}

func TestEval_EmptyExpressionBody(t *testing.T) {
	_, err := Eval("x={{}}", mkScope())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty expression")
}

func TestEval_NestedKey_Rejected(t *testing.T) {
	_, err := Eval("{{ config.foo.bar }}", mkScope())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nested key")
}

func TestEval_InvalidIdent_Rejected(t *testing.T) {
	// A hyphen is not a valid Ident (must be [A-Za-z_][A-Za-z0-9_]*).
	_, err := Eval("{{ config.foo-bar }}", mkScope())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid key")
}

func TestEval_RunOrTest_WrongLeaf_ErrorNamesCorrectLeaf(t *testing.T) {
	_, err := Eval("{{ run.name }}", mkScope())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only run.id is exposed")

	_, err = Eval("{{ test.id }}", mkScope())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only test.name is exposed")
}

// TestEval_InjectionSafety is the mandatory per-plan security guard:
// a config value that CONTAINS what looks like an expression must land
// verbatim — Eval never re-scans its own output.
//
// If this test ever regresses, an attacker who can set TestRun.spec.config
// could inject `{{ env.SECRET }}` and read operator-side secrets. Do not
// remove or weaken this test without a corresponding security review.
func TestEval_InjectionSafety(t *testing.T) {
	scope := mkScope()
	// scope.Config["attack"] == "{{ config.vus }}"
	got, err := Eval("value={{ config.attack }}", scope)
	require.NoError(t, err)
	// The KEY invariant: literal `{{ config.vus }}` in output, NOT "10".
	// A broken (re-scanning) impl would produce "value=10" here.
	assert.Equal(t, `value={{ config.vus }}`, got,
		"single-pass expansion: config value containing `{{...}}` must NOT be re-evaluated")
}

// TestEval_InjectionSafety_EscapedInjection: even a config value carrying
// the escape hatch `{{"{{"}}` shouldn't be interpreted as an escape when
// substituted — same single-pass invariant, harder-to-spot case.
func TestEval_InjectionSafety_EscapedInjection(t *testing.T) {
	scope := mkScope()
	scope.Config["sneak"] = `{{"{{"}}config.vus{{"}}"}}`
	got, err := Eval("{{ config.sneak }}", scope)
	require.NoError(t, err)
	// Verbatim — no escape interpretation on second pass.
	assert.Equal(t, `{{"{{"}}config.vus{{"}}"}}`, got)
}

func TestEval_NilInputMap_UnknownRefFails(t *testing.T) {
	// Nil sub-maps should be treated as "no keys" — any lookup errors.
	scope := Scope{RunID: "r", TestName: "t"}
	_, err := Eval("{{ config.vus }}", scope)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown config key "vus"`)

	_, err = Eval("{{ env.HOME }}", scope)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown env var "HOME"`)
}

func TestEvalSlice(t *testing.T) {
	scope := mkScope()
	out, err := EvalSlice([]string{"a", "b={{ config.vus }}", "c={{ run.id }}"}, scope)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b=10", "c=sample-run-123"}, out)

	// Nil in, nil out — matches encoding/json map behavior + keeps callers
	// terse (no "if len == 0" guards needed).
	got, err := EvalSlice(nil, scope)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestEvalSlice_ErrorNamesIndex(t *testing.T) {
	_, err := EvalSlice([]string{"ok", "{{ config.nope }}"}, mkScope())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "index 1")
}

func TestEvalMap(t *testing.T) {
	scope := mkScope()
	in := map[string]string{
		"HELLO": "world",
		"VUS":   "{{ config.vus }}",
		"RUN":   "{{ run.id }}",
	}
	out, err := EvalMap(in, scope)
	require.NoError(t, err)
	assert.Equal(t, "world", out["HELLO"])
	assert.Equal(t, "10", out["VUS"])
	assert.Equal(t, "sample-run-123", out["RUN"])

	// Nil in → nil out.
	got, err := EvalMap(nil, scope)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestEvalMap_KeysAreNeverEvaluated(t *testing.T) {
	// {{ ... }} in the KEY lands verbatim in output — keys aren't scanned.
	scope := mkScope()
	in := map[string]string{"{{ config.vus }}": "value"}
	out, err := EvalMap(in, scope)
	require.NoError(t, err)
	_, ok := out["{{ config.vus }}"]
	assert.True(t, ok, "map keys must NOT be evaluated (would break merge semantics)")
}

// --- CoerceParam tests ------------------------------------------------

func TestCoerceParam_Table(t *testing.T) {
	cases := []struct {
		name       string
		value      string
		param      Parameter
		wantValue  string
		wantErr    bool
		wantErrSub string
	}{
		{"string empty ok", "", Parameter{Type: "string"}, "", false, ""},
		{"string default type", "hello", Parameter{}, "hello", false, ""},
		{"integer valid", "5", Parameter{Type: "integer"}, "5", false, ""},
		{"integer float rejected", "5.5", Parameter{Type: "integer"}, "", true, "not an integer"},
		{"integer non-numeric rejected", "many", Parameter{Type: "integer"}, "", true, "not an integer"},
		{"number int accepted", "5", Parameter{Type: "number"}, "5", false, ""},
		{"number float accepted", "5.5", Parameter{Type: "number"}, "5.5", false, ""},
		{"number scientific accepted", "1e-3", Parameter{Type: "number"}, "1e-3", false, ""},
		{"number junk rejected", "many", Parameter{Type: "number"}, "", true, "not a number"},
		{"boolean true", "true", Parameter{Type: "boolean"}, "true", false, ""},
		{"boolean false", "false", Parameter{Type: "boolean"}, "false", false, ""},
		{"boolean True case-insensitive → true", "True", Parameter{Type: "boolean"}, "true", false, ""},
		{"boolean 1 → true", "1", Parameter{Type: "boolean"}, "true", false, ""},
		{"boolean 0 → false", "0", Parameter{Type: "boolean"}, "false", false, ""},
		{"boolean yes rejected", "yes", Parameter{Type: "boolean"}, "", true, "not a boolean"},
		{"enum valid", "small", Parameter{Type: "string", Enum: []string{"small", "large"}}, "small", false, ""},
		{"enum invalid", "medium", Parameter{Type: "string", Enum: []string{"small", "large"}}, "", true, "not in enum"},
		{"pattern valid", "v1.2.3", Parameter{Type: "string", Pattern: `^v\d+\.\d+\.\d+$`}, "v1.2.3", false, ""},
		{"pattern invalid", "latest", Parameter{Type: "string", Pattern: `^v\d+\.\d+\.\d+$`}, "", true, "does not match"},
		{"pattern invalid regex", "x", Parameter{Type: "string", Pattern: `(bad`}, "", true, "invalid pattern"},
		{"unknown type", "x", Parameter{Type: "date"}, "", true, "unknown Parameter.Type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CoerceParam("mykey", tc.value, tc.param)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrSub)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantValue, got)
		})
	}
}

// TestCoerceParam_ErrorNamesTheParam: error messages MUST cite the param
// name — otherwise a user staring at "5.5 is not an integer" has no idea
// which field went wrong.
func TestCoerceParam_ErrorNamesTheParam(t *testing.T) {
	_, err := CoerceParam("vus", "5.5", Parameter{Type: "integer"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"vus"`, "error must name the offending param")
}
