# Step 17 — Composition: execute-steps inside Test (1:1 with today's Testkube)

## Goal
Scenario orchestration exactly the way current Testkube does it: NO separate suite/plan CRD —
a Test may instead define `spec.steps[]` where each step `execute`s other Tests, sequentially
across steps and in parallel within one. Same CRD, two shapes: LEAF (image+command / use) or
COMPOSITE (steps). Semantics mirrored from TestWorkflow: per-step `condition` (default passed,
`always`), `optional`, `negative`, `retry{count}`, `timeout`, `delay`; per-reference
`count` + `config` with `{{ index }}` / `{{ count }}` available in child config values.

Out of scope (explicitly, matches what we skipped in CLAUDE.md §6): in-pod multi-step
(shell/run steps, init-container sequencing, container merging), matrix/shards on execute,
services, transfer/fetch. Composite steps ONLY contain `execute`.

## Schema (api/v1alpha1)
```go
type TestSpec struct {
    // ... existing leaf fields ...
    Steps []Step `json:"steps,omitempty"` // composite shape; mutually exclusive with leaf fields
}
type Step struct {
    Name      string           `json:"name,omitempty"`
    Condition string           `json:"condition,omitempty"` // passed(default)|always
    Optional  bool             `json:"optional,omitempty"`
    Negative  bool             `json:"negative,omitempty"`
    Retry     *RetryPolicy     `json:"retry,omitempty"`
    Timeout   *metav1.Duration `json:"timeout,omitempty"`
    Delay     *metav1.Duration `json:"delay,omitempty"`
    Execute   *StepExecute     `json:"execute,omitempty"`
}
type StepExecute struct {
    Parallelism int32              `json:"parallelism,omitempty"` // 0 = unlimited within step
    Tests       []StepExecuteTest  `json:"tests"`
}
type StepExecuteTest struct {
    Name   string            `json:"name"`             // Test in same namespace
    Count  int32             `json:"count,omitempty"`  // default 1
    Config map[string]string `json:"config,omitempty"` // may use {{ index }} / {{ count }}
}
```

## Webhook
- Shape exclusivity: steps[] set → container/content/use/verdict MUST be empty (and vice-versa
  image/command rules apply only to leaf shape — third branch, extend step-13 logic).
- Every step has execute with ≥1 test; condition enum; count ≥1; negative+optional combos allowed.
- NO cross-object ref validation in webhook (existing rule) — controller resolves.

## Controller (composite reconcile — the heart)
- TestRun of a composite Test spawns CHILD TestRuns: labels kubetest.io/parent-run=<name>,
  kubetest.io/step=<idx>, kubetest.io/exec-index=<i>; deterministic child names
  {parent}-s{step}-{test}-{i} (idempotent creation, AlreadyExists=ok — same discipline as cron).
- Child spec.config = rendered per-ref config through pkg/expr with scope {index,count} —
  strict-unknown-ref applies; child Source = parent's.
- Sequencing: step N+1 children created only when step N aggregate is terminal AND condition
  satisfied (passed→ skip-on-fail marks remaining steps skipped... use phase aborted+message OR
  a StepResult-only skipped marker — decide: StepResult keeps per-step map on parent status,
  reuse existing Steps map from step 02).
- Aggregation per step: all children terminal → step passed iff all passed (negative inverts,
  optional excluded from aggregate); parent phase = failed on first non-optional failed step
  (after remaining always-steps run), passed when all steps pass.
- retry{count}: recreate failed children up to count (new exec-index suffix -rN).
- timeout per step: watch wall clock from first child creation; expiry → abort children (delete
  their Jobs via existing abort path), step failed.
- Parent deletion: finalizer aborts+deletes children (ownerRef parent TestRun → child TestRun,
  cascade OK here — children are runs, not definitions).
- CYCLE DETECTION mandatory: resolve the execute graph at setup (composite referencing composite
  allowed, depth-limit 10); A→B→A → phase error "composition cycle: a → b → a". Also
  composite-references-missing-Test → error at the step, honoring optional.
- Concurrency: parent's concurrencyPolicy applies to parent only; children bypass child-Test's
  Forbid? NO — children respect their own Test's policy (document; test pins Forbid child
  behavior: parent step waits, step timeout is the escape hatch).

## Status
- Parent TestRun.status.Steps map (exists since step 02): key "s{idx}/{test}[{i}]" → StepResult
  {phase, timestamps}; plus per-step aggregate entries "s{idx}" — GUI-ready without schema change.
- resolvedSpec snapshot includes the full composite spec (step-13 invariant extends: editing a
  CHILD Test mid-run does not re-render already-created children; not-yet-created steps resolve
  at their creation time — document this deliberately, test pins it).

## Unit test requirements
- Webhook: shape-exclusivity table (leaf+steps → 400), enum/count rules, ok paths.
- Aggregation pure-unit table: {all pass}, {one fails → parent failed}, {failed but optional →
  parent passed}, {negative child: failed→counts passed, passed→counts failed}, {condition
  always runs after failure}, {skip-on-fail marks later steps}.
- Envtest: sequential 2-step happy path (step2 children absent until step1 terminal); parallel
  within step (parallelism honored — N children, cap respected); count=3 + {{ index }} rendered
  into child config (assert 3 distinct configs); retry recreates with -rN; step timeout aborts
  children; cycle → error; missing child Test + optional → step skipped, parent continues;
  parent delete → children gone (ownerRef + finalizer); child Forbid interplay pinned.
- Idempotency: double reconcile creates no duplicate children (deterministic names).
- Invariant-negation check (README convention) on the aggregation invariant.

## Acceptance
- kind e2e (extends step-16 suite): composite smoke→load scenario — step1 k6 quick passing,
  step2 jmeter; then failing step1 → step2 never runs, parent failed, statuses correct in
  apiserver responses.
- Report: mapping 1:1, gates + commit + push + git log.
