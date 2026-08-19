# Onboarding a new tool to the catalog

The catalog under `config/templates/` is the ONLY layer in this repo
where tool names exist. The operator itself is tool-agnostic. Adding a
new tool is authoring one TestTemplate + one sample + one docker-smoke
check — no operator code changes.

The complexity of the template is a function of how honest the tool's
exit code is:

| Tool behavior | Effort | Where |
|---|---|---|
| Exit code honest + writes JUnit | ~15 min | plain template |
| Exit code lies (JMeter-style) | ~45 min | template + `spec.verdict.from` + smoke both directions |
| Custom metrics wanted | +1 hour | template + a `pkg/scraper/perf/<tool>` parser |

## Step-by-step

### 1. Pick a pinned image

Any container image will do — the wrapper (`/entry`) is injected via
an emptyDir at pod start and doesn't need to exist inside the tool
image (verified by the docker smoke in step 11).

**Pin the tag exactly** — no `:latest`. If a project doesn't publish
a version-tagged image, either mirror one yourself or defer the tool
(see the gatling deferral note in `config/templates/README.md`).

Record the arch you smoked on. `linux/amd64` under emulation on
`arm64` is fine for smoke; a template committed only with an
emulation-verified smoke should carry a comment.

### 2. Verify the exit-code contract empirically

This is the CATALOG'S CORE RULE, per plan step 15. Before committing
any template you MUST run:

```bash
# passing scenario
docker run --rm -v /tmp/smoke:/data -w /data <image> <invocation-that-passes>
echo "pass exit=$?"

# failing scenario — write an assertion / input that MUST fail
docker run --rm -v /tmp/smoke:/data -w /data <image> <invocation-that-fails>
echo "fail exit=$?"
```

Record both exit codes in the template's file-header comment. A tool
that returns 0 in both cases (JMeter-style) means `verdictFrom` is
mandatory — see section 3.

### 3. Wire the verdict strategy in the template

**Honest exit code (0 pass / non-zero fail):**

```yaml
spec:
  container:
    image: your/tool:v1.2.3
    command: ["tool"]
    args: ["run", "/data/repo/{{ config.plan }}"]
  artifacts:
    paths: ["results/**/*.xml"]
  # No verdict block needed — default is exit-code.
```

**Cap at 255 (Cypress) or noisy non-zero for script bugs (k6):**

Either accept the trade-off (k6 does; comment it) or add JUnit override:

```yaml
spec:
  # ...
  verdict:
    from: junit
```

The JUnit processor reads all `results/**/*.xml` files matching the
JUnit schema and overrides the exit-code verdict from real counts.

**Exit code lies (JMeter):**

Every load-test tool that reports a JTL file goes here. `verdictFrom:
jtl` reads `results/**/*.jtl` and flips phase to failed when the CSV
error rate exceeds the declared threshold.

```yaml
spec:
  # ...
  verdict:
    from: jtl
    errorRateMax: "0"     # strict; consumers loosen per-Test
```

The invariant that JMeter's 100%-fail run reports `failed` (not
`passed`) is regression-guarded in
`pkg/executor/entry_test.go::TestEntry_Verdict_ExitZeroButJTL100PctErrors_ShouldFail`.

**Neither JUnit nor JTL fits (rare):**

- Add a new `verdictFrom` processor under `pkg/verdict/<name>/`.
- Update the `spec.verdict.from` webhook enum
  (`internal/webhook/v1alpha1/test_webhook.go`).
- Add a table-driven unit test for the processor.
- Wire it into `pkg/executor/entry.go`'s `applyVerdict` switch.

This is the same shape as JUnit/JTL — a few hundred lines. But
before doing this, exhaust tool-flag options first (`--exit-code-on-
error`, `--fail-fast`, etc.) — a template that carries a tool flag
is cheaper to maintain than a new processor.

### 4. Artifacts glob

Every template's `spec.artifacts.paths` must cover the tool's report
output. Use `results/**/*.<ext>` conventions — the scraper walks
via `doublestar` globbing.

If the tool writes to a non-configurable location (e.g. Cypress
screenshots default to `cypress/screenshots/`), list both:

```yaml
artifacts:
  paths:
    - "results/**/*.xml"
    - "cypress/screenshots/**/*"
    - "cypress/videos/**/*"
```

### 5. `kubetest.io/tool` label

Every template MUST set `metadata.labels."kubetest.io/tool"` to the
tool's canonical name. The label:

- shows up in `kubectl get tests` under the Tool column;
- lets kubectl selectors group runs by tool
  (`-l kubetest.io/tool=k6`);
- is propagated from Test → Job → Pod by the compiler;
- MUST equal `metadata.name` — the apply-test enforces this so a
  rename in one place can't drift out of sync with the other.

### 6. Config parameters

Declare tool-specific inputs (script path, target URL, iteration
count) via `spec.config`. Required parameters have no default; the
resolver errors with the parameter's name in the message if a Test /
TestRun doesn't supply one.

```yaml
spec:
  config:
    script:            # required — every k6 Test names its script
      type: string
    users:             # optional with a default
      type: integer
      default: "10"
    duration:
      type: string
      default: "30s"
```

Users override defaults on their Test.spec.config (redeclaring the
parameter with a new default) OR on TestRun.spec.config (per-run
override).

### 7. Pod-level quirks

Anything the platform WON'T inject (per CLAUDE.md §8) but the tool
NEEDS goes on `spec.pod`. Two prominent cases:

- **Cypress /dev/shm** — Chromium needs ≥2Gi shared memory or the
  browser dies mid-test. The cypress template supplies a memory
  emptyDir via `spec.pod.volumes` + a matching `container.volumeMounts`
  entry. Moved out of the compiler in step 11 — this is the CORRECT
  home for per-tool quirks.
- **Mesh sidecar opt-out** — tests that don't need the mesh proxy
  set `sidecar.istio.io/inject: "false"` in `spec.pod.annotations`.
  The catalog does NOT do this by default (some tests DO need mesh
  traffic); it's a per-project decision.

### 8. Sample Test + apply-test

Add `config/samples/tools/<tool>.yaml` with a self-sufficient Test —
`spec.config` supplies concrete defaults so the sample resolves
without any TestRun-side override. Add the file to
`config/samples/tools/kustomization.yaml`.

The `TestCatalog_ApplyAllTemplatesAndSamples` envtest asserts every
`config/templates/*.yaml` + every `config/samples/tools/*.yaml`
round-trips through the API server and resolver cleanly. Run it:

```bash
go test -race -run TestCatalog -v ./internal/controller/...
```

Any new template that fails to produce a `resolvedSpec` (missing
image, unresolved required config, expression that references a
non-declared param) trips this test.

### 9. Metrics (optional)

If the tool writes structured performance output and you want the
operator to project metrics into TestRun.status.metrics, add a
parser under `pkg/scraper/perf/<tool>/` and wire it into the
scraper's dispatch. See `pkg/scraper/perf/k6/` for the shape —
one function that reads the tool's output file and returns
`map[string]float64`.

This step is OPTIONAL. Without it the artifacts still land in MinIO
and the JUnit processor still reports counts — you only need a
custom parser when a per-tool metric (k6's `p95_ms`, JMeter's
`throughput`, etc.) is worth first-class UI treatment.

## Anti-patterns

- **`:latest` tags** — breaks reproducibility. Pin.
- **No smoke** — the catalog rule is empirical verification BEFORE
  commit. A template that only "should work" is a bug waiting to
  ship.
- **Per-tool code in the operator** — the whole point of step 11
  was collapsing per-tool wrapper images into one `/entry` binary +
  declarative templates. If you find yourself wanting to add a case
  to a Go switch statement for a tool, stop and ask why the template
  layer isn't enough.
- **Templates that mutate user config** — templates SUPPLY defaults;
  the user's project structure (cypress.config.js, playwright.config.ts)
  is off-limits. If the user's project needs configuration to work
  with the template, document that in the sample, don't inject it.
