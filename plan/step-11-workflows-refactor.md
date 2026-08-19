# Step 11 (REPLACES step-11-executors-remaining) — Workflows-model refactor

## Goal
Adopt the current-Testkube TestWorkflows execution model: a Test is image + command; there is NO
`spec.type`. Verdict = exit code, optionally overridden by a declarative `verdictFrom` processor
(JUnit / JTL) for tools whose exit codes lie. Tool identity lives in a label
(`kubetest.io/tool`), set by templates/users — not in the schema. /entry is a single generic
binary injected into every image; the 5 wrapper images are retired (content-fetcher remains the
only platform image).

This step DELETES code (type dispatch, resolveExecutor, DefaultExecutorImages, k6 Runner's
verdict logic) and REWRITES the spec (CLAUDE.md delta below). Expect a wide, one-time golden and
test churn — deliberate, called out in the report.

## Tasks

### 1. CRD + webhook
- Remove `Test.spec.type` entirely (pre-prod, no deprecation dance). Remove printcolumn Type;
  add printcolumn Tool: `.metadata.labels['kubetest\.io/tool']` (empty when unset — fine).
- Webhook: `spec.container.image` REQUIRED and (`command` or `args`) REQUIRED — for every Test.
  Drop the type-enum validation + its tests.
- New optional `spec.verdict`:
  ```go
  type VerdictSpec struct {
      // +kubebuilder:validation:Enum=exitCode;junit;jtl
      From string `json:"from,omitempty"` // default exitCode
      // jtl only: max fraction of failed samples, e.g. "0" or "0.01" (string for CRD simplicity)
      ErrorRateMax string `json:"errorRateMax,omitempty"`
  }
  ```
  Webhook: errorRateMax parses as float in [0,1], only valid with from=jtl.

### 2. /entry — generic only + verdict processors
- Delete Type dispatch and the k6 Runner's exit-code table; Entry runs req.Command/Args verbatim.
- Base verdict: exit 0→passed, non-zero→failed ("exit code N"), exec failure→error,
  timeout/SIGTERM per step-05 semantics (unchanged).
- Verdict processors (run AFTER the tool, BEFORE result write; only when spec.verdict set):
  - junit: parse JUnit XML(s) under artifacts workingDir → failures+errors > 0 → failed with
    counts in errorMessage; no parseable JUnit found → error ("verdictFrom=junit but no JUnit
    report found") — never silently passed.
  - jtl: parse JMeter JTL (header-driven CSV, success column) → failure ratio > errorRateMax →
    failed ("error rate 12.0% > max 0.0%"); missing/empty JTL → error.
  - Processor result OVERRIDES exit-code verdict in both directions (jmeter exit 0 + bad JTL →
    failed; flaky non-zero + clean junit → junit wins) — document why in a comment.
- ExecutionRequest: drop Type; add Verdict (mirror of VerdictSpec). Compiler passes it through.

### 3. Compiler
- Delete executor.go registry (DefaultExecutorImages, resolveExecutor, per-type branches).
  Image comes verbatim from spec.container.image — ImageRegistry option now applies ONLY to the
  content-fetcher image.
- /entry injection universal: `kubetest-bin` emptyDir mounted in init + main; content-fetcher
  copies its static binary (`cp /entry /kubetest-bin/entry`); main container command is ALWAYS
  `/kubetest-bin/entry` (exec-form, no shell needed; CGO_ENABLED=0 from step 05 is what makes
  this portable).
- Propagate `kubetest.io/tool` label (if present on Test) to Job + Pod labels — additive,
  §8 passthrough rules unchanged, reserved-prefix handling as in step 03.
- Drop the cypress /dev/shm special case keyed on type — replace with generic rule: template
  supplies the emptyDir via spec.pod.volumes (move the /dev/shm into the future cypress
  template; note it in the catalog step).

### 4. Parsers move, not die
- k6 summary parser: already in scraper perf registry (step 07) — delete the duplicate in the
  old runner path, keep fixtures.
- JTL parser lands in pkg (shared by /entry verdict processor AND scraper perf-ingest —
  one parser, two consumers). Locust CSV + newman JSON parsers → scraper perf registry
  (metrics-only), using the real-run fixtures already gathered.

### 5. CLAUDE.md delta (the spec is a living doc — update it in this step)
- §0: executors line → "any containerized tool; verdict = exit code + declarative verdict
  processors; curated templates catalog".
- §3 lesson + §11: rewrite executor contract — no Runner-per-tool; container contract + VerdictSpec
  + injection; per-tool exit-code table moves out of the Go contract into catalog notes.
- §10: TestSpec drops Type, gains Verdict; note the kubetest.io/tool label convention.
- §15.2 rewrite: "every tool lies differently" stays as CATALOG guidance — the mitigation is
  verdictFrom/template flags, not runner code. Keep the JMeter regression requirement, now
  phrased against the jtl processor.

## Unit test requirements
- Webhook: {no image → 400}, {no command/args → 400}, {verdict jtl + bad errorRateMax → 400},
  {verdict junit + errorRateMax set → 400}, happy paths.
- /entry verdict matrix (the heart of the step):
  {exit 0, no verdict → passed}, {exit 1 → failed}, {exit 0 + jtl 100% errors → FAILED},
  {exit 1 + junit all-pass → passed (override, with comment-pinned test name)},
  {verdictFrom=junit, no report → error}, {jtl boundary: ratio == max → passed, just above →
  failed (both sides pinned)}, {malformed JTL/JUnit → error not panic}.
- JTL parser: header-driven columns (not positional) — fixtures with reordered columns + no-header
  variant rejected cleanly. Real-run fixtures, versions in report.
- Compiler goldens: full regeneration — every fixture shows kubetest-bin volume, entry install,
  command /kubetest-bin/entry, image verbatim, no type in request.json; git-auth + minio fixtures
  keep their deltas; one fixture with kubetest.io/tool label propagated to Job+Pod.
- Injection e2e (docker smoke): alpine-based AND distroless/static-based user image both run
  /kubetest-bin/entry successfully (proves the no-shell path).
- Grep-guards: no references to removed symbols (ExecutorK6..., resolveExecutor, req.Type).

## Acceptance
- kind e2e: a Test with grafana/k6 image + command runs end-to-end (verdict from exit code,
  metrics via scraper ingest) — no type anywhere.
- kind e2e: jmeter image + verdictFrom=jtl with a failing plan → phase failed despite exit 0.
- Report: requirement→test mapping, golden churn summary, CLAUDE.md delta diffstat,
  gates + commit + push + git log.
