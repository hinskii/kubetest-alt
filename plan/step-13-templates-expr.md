# Step 13 — Templates + config params + expression engine

## Goal
`TestTemplate` resolution + typed `config` + `{{ }}` interpolation (CLAUDE.md §2 lessons, §10 Parameter).

## Tasks
- `pkg/expr`: `{{ config.x }}`, `{{ env.X }}`, `{{ run.id }}`, `{{ test.name }}`; strict mode — unknown reference = error at compile time (before Job creation), not runtime.
- Config resolution: Test defaults → TestRun overrides; missing required (no default) → validation error naming the param; type coercion per Parameter.Type; enum/pattern enforcement.
- TestTemplate: named reusable fragments (content/container/pod/artifacts) merged under the Test (`use: [name]`), deep-merge semantics documented: Test wins over template, later template wins over earlier.
- resolvedSpec snapshot (§15.5) stores the FULLY resolved (templates+config applied) spec.

## Unit test requirements
- expr table: valid refs, unknown ref → error with position/name, escaping `{{"{{"}}`, nested/adjacent expressions in one string, non-string coercion (int param into string field), empty template.
- Injection safety: config value containing `{{ ... }}` is NOT re-evaluated (no second-pass expansion — security test, mandatory).
- Config table: required-missing → error names param; enum violation; pattern violation; integer "5" ok, "5.5" fails; boolean coercion.
- Merge semantics golden tests: template+test combos → resolved spec fixtures; two templates same key → order wins; pod.annotations from template + test merge per §8 rules (still zero operator-injected keys).
- resolvedSpec: snapshot equals resolution at run creation even if template edited after (envtest).

## Acceptance
- Coverage `pkg/expr` >= 90%. Sample templated Test end-to-end in kind.
