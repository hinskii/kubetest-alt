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

// Package expr is the templating engine for Kubetest resources — a small,
// deliberately-limited implementation of `{{ config.x }}` /
// `{{ env.FOO }}` / `{{ run.id }}` / `{{ test.name }}` interpolation.
//
// Why not text/template:
//   - text/template silently substitutes empty string for a missing key
//     when a struct is nil; we need STRICT MODE (unknown ref = compile-time
//     error with position + name, so a typo doesn't ship as an empty arg
//     to a load test).
//   - text/template exposes the full Go template language — including
//     `range`, `if`, pipelines, function calls — which is a much larger
//     attack surface than we want in a CRD field. The four namespaces
//     supported here (config/env/run/test) are the entire feature set.
//   - text/template has no built-in "single-pass" guarantee. We need
//     INJECTION SAFETY: a config value that HAPPENS to contain `{{ ... }}`
//     text must land verbatim, never re-evaluated (would let a Test author
//     read $env.SECRET via an attacker-controlled config value).
//
// Grammar:
//
//	template ::= (literal | escape | expression)*
//	escape   ::= "{{" "{{" '"' "}}"        // literal `{{`, escape hatch
//	           | "{{" "}}" '"' "}}"        // literal `}}`
//	expression ::= "{{" ws ref ws "}}"
//	ref      ::= "config" "." Ident
//	           | "env" "." Ident
//	           | "run.id"
//	           | "test.name"
//	Ident    ::= [A-Za-z_][A-Za-z0-9_]*
//	ws       ::= ' ' | '\t'
//
// Errors carry a source position (line + column) and the offending ref
// (or the raw expression body) so a user editing YAML sees "unknown
// namespace 'confg' at line 12, col 24".
package expr

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Canonical string values for boolean coercion. Extracting the literals as
// consts silences goconst warnings AND keeps the "these two strings are
// what CoerceParam produces" contract explicit for downstream consumers.
const (
	boolTrue  = "true"
	boolFalse = "false"
)

// Parameter.Type string constants. Values MUST match the CRD enum on
// v1alpha1.Parameter.Type (see api/v1alpha1/common_types.go); keeping
// them centralized in one place is the point.
const (
	TypeString  = "string"
	TypeInteger = "integer"
	TypeNumber  = "number"
	TypeBoolean = "boolean"
)

// Scope holds every value expressions can resolve. Nil sub-maps are treated
// as empty (no keys), which lets callers pass a Scope with only the
// namespaces they know about.
type Scope struct {
	// Config is TestRun.spec.config overlaid on Test.spec.config defaults —
	// the caller resolves precedence before handing us the map.
	Config map[string]string

	// Env is the wrapper-visible environment (typically limited to a curated
	// set the operator projects — NOT os.Environ()) so a template can't leak
	// arbitrary operator-pod env vars into a TestRun.
	Env map[string]string

	// RunID is TestRun.metadata.name — surfaced as {{ run.id }}.
	RunID string

	// TestName is Test.metadata.name — surfaced as {{ test.name }}.
	TestName string
}

// Error is returned from Eval on any resolution failure. Fields are exported
// so callers can format their own messages (e.g. wrap with resource context).
type Error struct {
	// Line is 1-based; Col is 1-based; both point at the START of the
	// offending expression's `{{`.
	Line int
	Col  int

	// Ref is the reference the user wrote (e.g. "config.vus") or, on a
	// malformed expression, the raw body between `{{` and `}}`. May be
	// empty for unterminated-expression errors — Message is the load-bearing
	// field there.
	Ref string

	// Message is a human-readable explanation ("unknown namespace 'confg'",
	// "missing required config key 'vus'", "unterminated expression at EOF").
	Message string
}

func (e *Error) Error() string {
	if e.Ref == "" {
		return fmt.Sprintf("expr: %s at line %d, col %d", e.Message, e.Line, e.Col)
	}
	return fmt.Sprintf("expr: %s (ref %q at line %d, col %d)", e.Message, e.Ref, e.Line, e.Col)
}

// identRe validates the identifier after a namespace dot. Kept strict
// (letters, digits, underscore; must start with a letter or underscore)
// because a permissive rule lets typos slip through as valid syntax.
var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// escapeOpenBrace / escapeCloseBrace are the escape hatches. Chosen to
// match Go's text/template idiom so YAML authors familiar with it don't
// have to relearn: `{{"{{"}}` produces literal `{{`; `{{"}}"}}` produces
// literal `}}`. No other escapes; no template-language keywords.
const (
	escapeOpenBrace  = `"{{"`
	escapeCloseBrace = `"}}"`
)

// Eval evaluates s under scope. Single-pass: the produced output is NEVER
// re-scanned for expressions. A config value that happens to contain
// `{{ config.secret }}` lands verbatim — this is the INJECTION SAFETY
// invariant, tested by TestEval_InjectionSafety.
//
// Empty input returns ("", nil) — the common case for optional fields.
//
// Unknown refs, malformed expressions, and unterminated `{{` all return
// *Error with position + ref/body. Callers should wrap this error in
// resource context ("resolve Test.spec.container.args[0]: ...") — the
// engine itself doesn't know which field it's evaluating.
func Eval(s string, scope Scope) (string, error) {
	if s == "" {
		return "", nil
	}
	var out strings.Builder
	out.Grow(len(s))

	// Track (line, col) by scanning newlines as we consume input.
	line, col := 1, 1
	i := 0
	for i < len(s) {
		if s[i] == '\n' {
			out.WriteByte('\n')
			i++
			line++
			col = 1
			continue
		}
		// A `{{` opens an expression OR an escape. Anything else is a
		// literal character.
		if s[i] == '{' && i+1 < len(s) && s[i+1] == '{' {
			startLine, startCol := line, col

			// Escape hatches — literal `{{` / `}}` in output. Check these
			// as literal prefixes BEFORE the generic `{{ ... }}` scan
			// because the `}}` escape's body contains `}}` inside quotes,
			// which would otherwise fool the naive close-search below.
			const escOpen = `{{` + escapeOpenBrace + `}}`   // {{"{{"}}
			const escClose = `{{` + escapeCloseBrace + `}}` // {{"}}"}}
			if strings.HasPrefix(s[i:], escOpen) {
				out.WriteString("{{")
				line, col = advancePos(s[i:i+len(escOpen)], line, col)
				i += len(escOpen)
				continue
			}
			if strings.HasPrefix(s[i:], escClose) {
				out.WriteString("}}")
				line, col = advancePos(s[i:i+len(escClose)], line, col)
				i += len(escClose)
				continue
			}

			// Try to consume the WHOLE `{{ ... }}` in one shot.
			end := strings.Index(s[i:], "}}")
			if end < 0 {
				return "", &Error{
					Line:    startLine,
					Col:     startCol,
					Message: "unterminated `{{` (no matching `}}`)",
				}
			}
			body := s[i+2 : i+end]
			raw := strings.TrimSpace(body)

			val, err := resolveRef(raw, scope, startLine, startCol)
			if err != nil {
				return "", err
			}
			out.WriteString(val)
			advance := end + 2
			line, col = advancePos(s[i:i+advance], line, col)
			i += advance
			continue
		}
		// A bare `}}` in a literal position is fine; the user might legitimately
		// have "}}" in their content (JSON-heavy YAML) as long as it's not
		// mated with `{{`. No special handling — write and continue.
		out.WriteByte(s[i])
		col++
		i++
	}
	return out.String(), nil
}

// advancePos updates (line, col) by walking through chunk. Public-ish so
// tests can be written against consistent position semantics.
func advancePos(chunk string, line, col int) (int, int) {
	for _, r := range chunk {
		if r == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}

// resolveRef parses one reference body and looks it up in scope. `raw` is
// already trimmed of surrounding whitespace. Returns the resolved string
// value, or an *Error with the given start position on any failure.
func resolveRef(raw string, scope Scope, line, col int) (string, error) {
	if raw == "" {
		return "", &Error{
			Line:    line,
			Col:     col,
			Ref:     raw,
			Message: "empty expression `{{ }}`",
		}
	}

	// Namespace + key split. Well-formed: exactly one `.` between namespace
	// and key EXCEPT for zero-arg refs (run.id, test.name) which we
	// special-case.
	switch raw {
	case "run.id":
		return scope.RunID, nil
	case "test.name":
		return scope.TestName, nil
	}

	ns, key, ok := strings.Cut(raw, ".")
	if !ok {
		return "", &Error{
			Line: line,
			Col:  col,
			Ref:  raw,
			Message: fmt.Sprintf(
				"reference %q has no namespace (expected e.g. `config.x`, `env.FOO`, `run.id`, `test.name`)",
				raw),
		}
	}

	// Reject sub-namespaces (config.foo.bar) — keeps the surface flat.
	if strings.ContainsRune(key, '.') {
		return "", &Error{
			Line:    line,
			Col:     col,
			Ref:     raw,
			Message: fmt.Sprintf("reference %q has nested key (only top-level keys supported per namespace)", raw),
		}
	}
	if !identRe.MatchString(key) {
		return "", &Error{
			Line:    line,
			Col:     col,
			Ref:     raw,
			Message: fmt.Sprintf("reference %q has invalid key %q (must match [A-Za-z_][A-Za-z0-9_]*)", raw, key),
		}
	}

	switch ns {
	case "config":
		v, ok := scope.Config[key]
		if !ok {
			return "", &Error{
				Line: line,
				Col:  col,
				Ref:  raw,
				Message: fmt.Sprintf(
					"unknown config key %q (declare it in Test.spec.config or provide it in TestRun.spec.config)",
					key),
			}
		}
		return v, nil
	case "env":
		v, ok := scope.Env[key]
		if !ok {
			return "", &Error{
				Line:    line,
				Col:     col,
				Ref:     raw,
				Message: fmt.Sprintf("unknown env var %q (env namespace exposes only the operator-curated set)", key),
			}
		}
		return v, nil
	case "run", "test":
		return "", &Error{
			Line:    line,
			Col:     col,
			Ref:     raw,
			Message: fmt.Sprintf("unknown %s field %q (only %s.%s is exposed)", ns, key, ns, defaultLeafFor(ns)),
		}
	default:
		return "", &Error{
			Line:    line,
			Col:     col,
			Ref:     raw,
			Message: fmt.Sprintf("unknown namespace %q (allowed: config, env, run, test)", ns),
		}
	}
}

// defaultLeafFor names the ONE valid key for run.* / test.* so the
// error message can point to it.
func defaultLeafFor(ns string) string {
	if ns == "run" {
		return "id"
	}
	return "name"
}

// EvalSlice is a convenience for the compiler wiring: evaluate every entry
// of a []string in place. Empty slice → empty slice; nil → nil.
func EvalSlice(xs []string, scope Scope) ([]string, error) {
	if xs == nil {
		return nil, nil
	}
	out := make([]string, len(xs))
	for i, x := range xs {
		v, err := Eval(x, scope)
		if err != nil {
			return nil, fmt.Errorf("index %d: %w", i, err)
		}
		out[i] = v
	}
	return out, nil
}

// EvalMap evaluates every VALUE of a map[string]string. Keys are NEVER
// evaluated — expressions in map keys would confuse merge semantics.
// nil map → nil map.
func EvalMap(m map[string]string, scope Scope) (map[string]string, error) {
	if m == nil {
		return nil, nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		val, err := Eval(v, scope)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", k, err)
		}
		out[k] = val
	}
	return out, nil
}

// CoerceParam validates + coerces a resolved-string param value against
// the declared Parameter.Type / Enum / Pattern rules. Returns the (possibly
// normalized) value or a human-readable error.
//
// Kept in this package so the config resolver (which imports expr) has one
// place for both interpolation AND value validation — the two are usually
// applied in the same pass.
//
// Type rules:
//
//	string  : no coercion; empty allowed unless Enum/Pattern reject it.
//	integer : must parse as decimal int (base 10). "5" ok; "5.5" fails.
//	number  : must parse as float64. Accepts "5", "5.5", "1e-3".
//	boolean : accepts "true"/"false"/"1"/"0" case-insensitively. Returns
//	          canonical "true" / "false" (so downstream code doesn't have
//	          to re-parse) — the CRD stores strings anyway.
type Parameter struct {
	Type    string   // "string" | "integer" | "number" | "boolean" (defaults to string)
	Enum    []string // if non-empty: value must be one of Enum
	Pattern string   // if non-empty: value must match this Go regexp
}

// CoerceParam validates value against p. Returns the (possibly normalized)
// value. Empty p.Type is treated as "string".
func CoerceParam(name, value string, p Parameter) (string, error) {
	t := p.Type
	if t == "" {
		t = TypeString
	}
	switch t {
	case TypeString:
		// nothing to coerce
	case TypeInteger:
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return "", fmt.Errorf("config %q: %q is not an integer", name, value)
		}
	case TypeNumber:
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return "", fmt.Errorf("config %q: %q is not a number", name, value)
		}
	case TypeBoolean:
		switch strings.ToLower(value) {
		case boolTrue, "1":
			value = boolTrue
		case boolFalse, "0":
			value = boolFalse
		default:
			return "", fmt.Errorf("config %q: %q is not a boolean (accepted: true, false, 1, 0)", name, value)
		}
	default:
		return "", fmt.Errorf("config %q: unknown Parameter.Type %q", name, t)
	}
	if len(p.Enum) > 0 && !slices.Contains(p.Enum, value) {
		return "", fmt.Errorf("config %q: %q not in enum %v", name, value, p.Enum)
	}
	if p.Pattern != "" {
		re, err := regexp.Compile(p.Pattern)
		if err != nil {
			return "", fmt.Errorf("config %q: invalid pattern %q: %w", name, p.Pattern, err)
		}
		if !re.MatchString(value) {
			return "", fmt.Errorf("config %q: %q does not match pattern %q", name, value, p.Pattern)
		}
	}
	return value, nil
}
