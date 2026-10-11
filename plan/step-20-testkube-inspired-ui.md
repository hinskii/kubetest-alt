# Step 20 — Control Center: what Testkube's dashboard does better

Reference: the last open-source Testkube dashboard (v1.16.15, agent chart
1.16.63) run against a real agent with tests, a suite and executions —
screenshots of every view. This step takes over what it does better and
keeps everything Control Center already does better (live view, report in
the page, wizard, test cases/flaky, analytics/compare, schedules, cleanup,
comments, Kubernetes events, multi-cluster, sign-in).

Not taken: their 2-field "Create a test" modal (our wizard), the in-browser
"API endpoint" prompt (we proxy server-side), telemetry/cookie and Pro
banners.

**How it's built (since step 20R).** Control Center is the React app in
`web/` (TypeScript, TanStack Router/Query/Table, shadcn/ui, Recharts,
CodeMirror), embedded in the Go binary. Its data is Control Center's own
JSON API (`internal/controlcenter/api`, spec reflected from the Go types
into `openapi/control-center.json`, TS types generated). Control Center
stays stateless (no database): what a page shows comes from the cluster's
kubetest API and run history. CSP: scripts same-origin only. Every UI
state worth sharing lives in the address (search params validated with
zod), so links and Back work.

**Gates for every part:** Go (`make lint test test-coverage openapi-check`),
web (`make web-gen-api-check web-typecheck web-lint web-test web-build
web-size web-audit web-licenses`; web-test fails under 80% of lines),
Vitest + MSW for pages, a Playwright spec for each new flow (runs in the
e2e job on kind; fails on any console error, which catches CSP
violations), a size budget for any new on-demand chunk.

Order: 20h, 20i (done); 20j (done: wizard and Settings, as R4 of step
20R); **step 20R done** → 20a … 20g below, one commit each, gates green
before the next.

---

## 20a — Run drawer

Opening a run from a list slides it in from the right over the list,
instead of leaving the page. The full run page stays at its own URL.

**Behaviour**
- A plain left click on a run link opens the drawer. Ctrl/Cmd/Shift/
  middle click, "open in new tab", direct links and viewports under
  900 px keep the full page (the link is a normal `<a href>` to it).
- The drawer is shadcn's Sheet (Radix Dialog: focus moved in and back,
  `aria-modal`), ~85 % wide. Header: run name, phase, "Open full page", ✕.
- The address bar shows the run's URL (TanStack Router route masking:
  the list with `?run=<ns>/<id>` underneath, the run's URL shown), so a
  copied link opens the full page and a reload does too. Back, Esc, ✕
  or a click on the backdrop close it; the list keeps its scroll and
  filters. Forward reopens it.
- ↑ / ↓ step to the previous / next run of the list it was opened from
  (e.g. through failures) without closing.
- Content: the run page's own component (`RunView`, moved out of the
  route file), same queries — no second rendering path. Log polling and
  the queued refresh are TanStack Query intervals tied to the component:
  closing the drawer stops them.

**Actions inside the drawer** (Run again, Abort, Comment, Delete) are
the run page's mutations: run again / abort / comment stay in the drawer
(run again opens the new run there), Delete closes it and invalidates
the list's query.

**Run links that open the drawer:** a Test's run history, a run's child
runs, schedules, test-case history, analytics, run compare, cleanup
preview, the tests list's last run (and later 20b's recent marks, 20c's
bars). One `RunLink` component does it, so every list behaves alike.

**Acceptance**
- Vitest: opening from a list (the address is the run's), ↑/↓, Esc and
  Back restore the list, modifier clicks aren't intercepted, Run again
  and Delete, polling stops on close (no further requests in MSW).
- Playwright: open, step with ↑/↓, Back closes, reload shows the full
  page, Cmd-click opens a tab.
- No new chunk: the drawer reuses the run page's.

---

## 20b — Tests list: statistics, search, status filter

**kubetest API — `GET /tests/stats?namespace=&since=<duration>&recent=<n>`**
(new): per Test over finished runs since `since` (default 7d, max 90d):
`total, passed, failed, errored, aborted, passRatio, p50Ms, p95Ms`, and
the `recent` (default 10, max 30) newest runs as
`{name, uid, phase, durationMs, finishedAt}`. One store query:
`row_number() over (partition by test …)` for the recent runs,
`percentile_cont` for p50/p95. Store interface `TestStats(ctx,
StatsFilter)`; Postgres integration test; fake for unit tests; apiclient
`TestStats`; OpenAPI. Without a store (no history) → 200 with empty
stats.

**Control Center API:** `GET /clusters/{c}/tests` gains the stats per
Test (`?since=`), or "unavailable" with the reason when the cluster
keeps no history (as test cases do).

**Page** — the table grouped by tool stays (denser than tiles), built
with TanStack Table; new columns: *Pass rate (7d)*, *P50*, *Recent* —
one coloured mark per recent run, oldest → newest, each a `RunLink`
(drawer). Plus, all in the address (`?q=&status=`):
- *Search* by name, filtered as you type (the list is already loaded);
- *Status*: last run passed / failed (failed, error, aborted) / never run;
- the count line says what the filters left; sorting by any column.

**Acceptance:** store integration test (window and percentiles against a
seeded history), API handler tests, Vitest for the columns, filters and
sorting, Playwright: filter the e2e list.

---

## 20c — Test page header: tiles, timeframe, duration chart

On the Test page's Overview, above the run history, for a timeframe
`?range=24h|7d|30d` (default 7d):
- tiles (shadcn Card): pass rate, P50, P95, failed, total (20b's stats
  for this Test);
- a bar chart of run durations (Recharts, in the analytics chunk's
  budget or its own): one bar per run, coloured by phase, dashed P50/P95
  lines, each bar opens the run in the drawer; at most the last 100 runs
  of the range (`ListRuns` with `FinishedAfter`). Bars have accessible
  names (run, phase, duration).

The Analytics page stays as the deep view (metrics trends, comparison);
the header links to it.

**Run history:** a status filter (`?phase=`, server-side — the API
already filters) and a search over run name, creator and comment on the
shown page; both keep paging.

**Acceptance:** Vitest (tiles, range switch, bars open runs, filters
keep paging), Playwright: switch the range on the e2e Test.

---

## 20d — Log viewer

The run log (page and drawer) moves into CodeMirror, read-only — already
loaded for YAML, and built for large text:
- *Search* (CodeMirror's search: highlights, n of m, Enter / ⇧Enter),
  working while the log still grows: new chunks are appended with a
  transaction, not by replacing the document, so the search, selection
  and scroll stay; it follows the end only while you are at the end;
- *Wrap lines* toggle (remembered per browser), *Full screen* (the log
  fills the viewport, Esc leaves), *Copy* (the whole log as loaded),
  *Download* stays;
- line numbers; ANSI colour codes stripped (shown as text today).

Client-side only; the log endpoint and its polling stay.

**Acceptance:** Vitest for appending (search and scroll kept) and the
toggles; Playwright: search a finished run's log, full screen and back;
the CodeMirror budget holds.

---

## 20e — CLI commands

A collapsible *CLI* panel on the Test page and the run page with ready
commands and copy buttons, filled with the namespace, Test and run:
`kubectl kubetest run <test> -n <ns> --follow`, `… --config k=v` (the
run form's current values), `runs --test`, `logs <run> -f`,
`artifacts <run> --download ./out`, `abort <run>`. Names are
shell-quoted. A note line: the kube context is the user's
(`--context`), the token comes from `$KUBETEST_API_TOKEN` or the chart
Secret (docs/cli.md).

**Acceptance:** Vitest for the commands (quoting, the run form's values),
copy goes to the clipboard.

---

## 20f — Labels

A Test's labels as chips on the tests list and the Test page — without
the platform's own (`app.kubernetes.io/managed-by`, `kubetest.io/*`;
the tool already has its chip). The Control Center API returns them on
`TestSummary` and `TestDetail`, the platform's left out there. Clicking
one filters the list (`?label=k=v`, repeatable, AND); the filter shows
as a removable chip. Settings → General edits them for GUI-managed
Tests.

**Acceptance:** API tests (system labels left out), Vitest (filter,
removing a chip, odd characters in keys and values), Playwright: filter
by a label.

---

## 20g — About / versions

- kubetest API: `GET /version` → `{version, commit, namespace,
  chartVersion}` (build info via `-ldflags`, as the CLI; the chart passes
  its version as `KUBETEST_CHART_VERSION` to every component); OpenAPI.
- Control Center API: `GET /api/v1/version` (its own) and each cluster's
  in `GET /clusters` (from the probe it already makes).
- UI: an *About* page (`/about`, linked from the header) listing Control
  Center's version and, per cluster, the API server's, the chart's and
  the namespace; the home page's cluster cards show the API version.

**Acceptance:** API tests, helm test (env present), Vitest for the page.

---

## 20h — The test's path: required, stated once, no defaults

Today the location of a Test's files is defaulted and stated twice:
- eight templates default the path: `projectDir: "."` (playwright,
  cypress, gradle, maven), `testsDir: "."` (pytest), `featuresDir: "."`
  (cucumber), `locustfile: locustfile.py`, gatling's simulations folder —
  so a Test "works" without saying where its tests are, and runs whatever
  sits at the repository's root;
- with git, the same directory goes into `content.git.paths` (sparse
  checkout) and into the template's main parameter — the wizard asks for
  it twice, and a mismatch fails only at run time.

**The rule:** a Test says where its tests are — a template's main path
parameter has no default, ever. Where the checkout sits in the pod
(`/data/repo`) stays the platform's business (no `{{ content.repo }}`:
custom mount paths are not a goal).

**The main path parameter** of a template is the `config` parameter its
container puts right after `/data/repo/` (in command, args or workingDir):
k6's `script`, JMeter's `plan`, newman's `collection`, playwright's
`projectDir`, … Found from the template itself
(`resolver.MainPathParam`), shared by the operator and Control Center —
no tool is named in code.

**Templates (catalog)**
- The eight defaults go; every main path parameter gets a `description`
  ("relative to the repository root", or to /data/repo for inline files).
- A test over config/templates: each main path parameter has no default
  and has a description.

**Operator: Test status**
- A small Test controller sets `status.conditions[type=Ready]`:
  `False/ParameterMissing` ("set spec.config.script — the k6 script,
  relative to the repository") when the merged spec leaves the main path
  parameter empty; `False/TemplateMissing` when a template in `spec.use`
  doesn't exist; else `True/Resolved`. Re-evaluated when a TestTemplate
  changes. Status is patched (conditions only), never clobbering
  `latestRun`.
- Admission stays template-agnostic — no template lookups in the webhook
  (ArgoCD may sync a Test before its template).
- A run without the path still fails at once (required-parameter rule),
  never runs the repository root.

**API / Control Center**
- `GET /tests/{name}/resolved` carries the Test's conditions; the Test
  page shows a ParameterMissing / TemplateMissing banner; the tests list a
  warning chip (from the Test's status).
- Wizard, git source: one required field, *Path in the repository* (a
  file or a directory, e.g. `perf/checkout.js`, `e2e/web`). It sets the
  main path parameter (unless the user typed one) and, unless *Sparse
  paths* (advanced) is filled, the sparse checkout: the directory, or a
  file's directory. Own image: the path is the sparse checkout only.
- Inline files: the main parameter is bound to the first file (as now);
  with no file and no git, the review names the missing parameter.
- Review warns when the main parameter points outside the sparse paths.
- Edit shows the path back (the parameter, else the first sparse path).

**Samples, catalog e2e, docs**
- The catalog cases that leaned on a default (cucumber, gatling, locust,
  playwright) set the parameter; the samples already do.
- docs/onboarding-a-tool.md: the main path parameter never has a default;
  docs/control-center.md: the wizard's git path; upgrading note: Tests
  that relied on a removed default report ParameterMissing — set it.

**Acceptance**
- `MainPathParam` unit tests; the catalog template test.
- Controller (envtest): ParameterMissing / TemplateMissing / Resolved,
  re-evaluated on a template change, latestRun untouched.
- Wizard tests: git path required; one path → parameter + sparse path;
  typed parameter kept; the outside-the-checkout warning.
- Catalog e2e green for every tool.

---

## 20i — Content: code from git or inline, test data apart

Follow-up to 20h. A Test's **code** comes from exactly one place — a git
repository or files typed into the Test — and lands in `/data/repo`;
**test data** from the cluster (ConfigMaps, Secrets) is a separate field
and lands in its own directory. Only git asks where the tests are.

### The content model (CRD, `v1alpha1` — breaking, see Migration)

```yaml
content:
  # code — exactly one of: git | files
  git: {uri: https://github.com/org/shop, revision: main}   # → /data/repo
  files:                                  # inline code → /data/repo/<path>
    - path: load.js                       # relative to /data/repo, no "repo/" prefix
      content: |
        ...
  # data — allowed with either
  testData:
    - configMap: products-stage           # default → /data/testdata/products-stage/<key>
    - configMap: products-stage
      mountPath: /data/repo/e2e/web/data  # all keys as files in this directory
    - secret: shop-test-account
      items:                              # single files at exact paths
        - {key: env, path: /data/repo/e2e/web/.env}
```

- `content.git` and `content.files` are mutually exclusive (Test webhook:
  "a Test's code comes from git or is written inline, not both").
- `files[]`: `path` (relative to /data/repo; no absolute, no `..`) +
  `content` (required). `contentFrom` leaves `files` — cluster data is
  `testData`.
- `testData[]`: exactly one of `configMap` / `secret` (a name in the
  Test's namespace); where it lands — at most one of:
  - nothing: `/data/testdata/<name>/<key>` (the default, recommended for
    new Tests);
  - `mountPath`: a directory, all keys as files in it (the user's choice,
    so existing tests don't have to change where they read);
  - `items: [{key, path}]`: chosen keys as single files at exact paths
    (subPath mounts) — the only way to replace a file of the repository,
    and it shows in the YAML.
  Field names as in Kubernetes volumes. Webhook: absolute paths, no `..`;
  not into the platform's directories (`/kubetest-bin`, `/etc/kubetest`,
  the result directory); no two entries on the same path (nor two
  default entries with the same name); not `mountPath` and `items`
  together.
- Tarballs stay as they are (code source, exclusive with git and files
  too — one code source).

### Where things are in the pod

- Code: always `/data/repo` (git checkout or inline files).
- Data: by default `/data/testdata/<object name>/<key>`, env
  `KUBETEST_TESTDATA_DIR=/data/testdata`; else where `mountPath` / `items`
  say.
- **No silent shadowing.** A `mountPath` hides whatever the directory held
  (any Kubernetes mount does). So after the checkout, the content fetcher
  checks every `mountPath`: if it exists in /data and isn't empty, the run
  fails at once — "testData products-stage: mountPath
  /data/repo/e2e/web/data would hide 3 files from the repository — mount
  single files with items, or pick an empty directory". `items` replace
  exactly the files they name, nothing else.
- `testData` is mounted as read-only ConfigMap/Secret volumes (not copied
  through env vars as `contentFrom` is today): no env-size limit, a
  Secret never passes through the environment. Pod policy already admits
  configMap and secret volumes.

### Templates say what their main path is

```yaml
config:
  script:
    type: string
    path: file        # file | directory — the main path parameter
    description: "The k6 script, relative to the repository root."
```

- New optional `Parameter.path` marks the main path parameter;
  `resolver.MainPathParam` reads it (the `/data/repo/{{ config.X }}` scan
  goes). At most one per template / Test — a CEL rule on the CRD.
- Catalog: `file` — k6 `script`, JMeter `plan`, newman `collection`,
  Locust `locustfile`, Artillery `scenario`, kubepug `inputFile`, SoapUI
  `project`; `directory` — Playwright, Cypress, Gradle, Maven
  `projectDir`, pytest `testsDir`, Cucumber `featuresDir`, Gatling
  `simulationsFolder`.

### Projects only from git; single files either way

- `path: directory` tools run a project (package.json + config + specs +
  lockfile, pom.xml, …) — it belongs in a repository: reviewed, run
  locally, versioned with the app, and over the 512 KB inline cap. With
  inline files the operator's Ready condition is
  `False/InlineNotSupported` ("this tool runs a project: put it in git"),
  a run fails at once, and the wizard doesn't offer inline for them.
  Admission stays template-agnostic (ArgoCD may sync a Test before its
  template).
- `path: file` tools (k6, JMeter, newman, Locust, Artillery, kubepug,
  SoapUI) take git or inline.

### Inline: no path, ever (YAML and GUI)

With inline files and the main path parameter unset, the resolver sets it
to the first file (relative to /data/repo). `content.files` is the whole
story; an explicit value still wins (e.g. a helper listed first). The
Ready condition counts it as set.

### Git: unchanged from 20h

The path in the repository is required (`ParameterMissing` without it);
the wizard's *Path in the repository* fills it.

### Composite Tests mix freely

A composite Test (`spec.steps`) has no content of its own; its children
each have theirs. One step can run a Playwright Test from git next to an
inline k6 Test — the git-or-inline rule is per Test.

### A deliberate difference from Testkube

TestWorkflows allow git + inline files + files from ConfigMaps together
in one `/data`, files overlaying the checkout (e.g. swapping a config
file). We don't: one code source, data apart, nothing overwritten
silently, paths predictable from the YAML. What overlays are mostly used
for — data, config, secrets for a git Test — is `testData`, which can land
where the test already reads (`mountPath`, `items`), so tests moving from
Testkube don't have to change. Replacing a repository file is possible
only explicitly (`items`), and hiding repository files is an error.

### Control Center (wizard)

- *Source* is a choice: Git | Inline files (| none for an own image).
  Inline isn't offered for `directory` templates.
- A *Test data* section: rows of ConfigMap / Secret + name, and where:
  the default directory, a directory (`mountPath`) or single files
  (`items`); the review explains the shadowing rule.
  Names are typed — the API doesn't list ConfigMaps or Secrets, so the API
  server gets no new permissions (none on Secrets).
- The run page shows the Test's data sources (names only).

### Migration

- `files[].contentFrom` → `testData`; inline paths lose the `repo/`
  prefix. For one release the webhook refuses the old shapes with the
  rewrite in the message ("move it to content.testData: - configMap: …";
  "write the path relative to /data/repo: load.js").
- Catalog e2e: the seven `directory` tools move from inline files to git
  (this repository at the commit under test, sparse path
  `test/catalog/cases/<tool>/repo`), as the samples already do; the
  single-file cases drop the `repo/` prefix; one case uses `testData`
  from a ConfigMap.
- Samples, docs examples (getting-started's hello-k6), the dev cluster's
  Tests (`playwright-smoke` → git or removed).

### Docs

- New `docs/test-data.md`: the ways to give a Test data — a parameter
  (few values, per run), a file in the repository, `testData` from a
  ConfigMap/Secret (per environment, secrets), large data (tarball URL,
  PVC), fetching in the test's setup (k6 `setup()`, Playwright
  `globalSetup`) — with examples.
- `docs/control-center.md` (Source, Test data), `docs/onboarding-a-tool.md`
  (`path: file|directory`), `docs/getting-started.md`.

### Acceptance

- Webhook sentinels (rules the schema can't express): git + files refused;
  `testData` path collisions and `mountPath`+`items` refused; platform
  directories refused; old `contentFrom` / `repo/` shapes refused with the
  rewrite.
- Content fetcher: a `mountPath` over a non-empty directory of the
  checkout fails with the files counted; an empty or missing one passes.
- CEL: two `path` parameters in a template refused (envtest).
- Resolver: inline main file = first file; explicit wins; `directory` +
  inline → error.
- Controller (envtest): `InlineNotSupported`; inline single-file Test
  without the parameter → `Resolved`.
- Compiler: `testData` volumes and mounts, read-only — default directory,
  `mountPath`, `items` as subPath mounts; `KUBETEST_TESTDATA_DIR`.
- Wizard: Source choice, Test data rows, no inline for project tools; a
  pasted k6 script creates a Test with no `script` in its config.
- Catalog e2e green: directory tools from git, single-file inline, one
  `testData` case.

---

## 20j — A wizard people can follow; details in the Test's Settings

> **Done in React (step 20R, R4).** What shipped differs from the notes
> below, which were written for the Go pages: the YAML everywhere is
> CodeMirror from the npm packages, code-split and size-budgeted (no
> server-side highlighter, no vendored bundle, no `<textarea>` fallback —
> the UI needs JavaScript); the live YAML is the Control Center API's
> preview; Settings has a YAML panel beside the sections (unsaved changes
> shown, "Edit YAML" for the whole Test) instead of a YAML section; saves
> are conditional on the definition (`metadata.generation`), so status
> writes don't refuse them.

20i's wizard put a dozen fields on one step (git, token, sparse paths,
test data with mountPath / key=path) and its jargon (`/data/repo`,
sparse, mountPath) up front. Testkube's dashboard creates a Test from two
fields and keeps the rest in the Test's *Settings*, split in sections.
We take that shape and keep what we do better (templates, inline scripts,
dry run, YAML for Git).

**Create: three short steps**
1. *What do you test?* — name, namespace (suggested), the tool from a
   **drop-down list** (the namespace's catalog, "Own image" last) — not
   tiles; one line under it says what the chosen tool is (its image, a
   short description). The code's source (git / inline / none) is a plain
   row of radio buttons, not cards.
2. *Where is the test?* — asked the tool's way:
   - single-file tools: two big choices, **Paste the script** (one editor,
     nothing else) or **From Git** (repository, path, branch);
   - project tools: git fields only, no choice shown;
   - own image: image and command (+ optional git).
   A private repository's token sits behind "Private repository?".
3. *Parameters* — the tool's parameters only; **Create test**; the YAML
   and **Download YAML** behind "Show YAML". The dry run still checks it.

**Everything else: the Test page's Settings tab** (GUI-managed Tests;
read-only view for Git-managed ones), sections as in Testkube:
- *General* — name/description, labels;
- *Source* — git (repository, revision, path, sparse paths, token) or
  inline files (editor per file, add/remove);
- *Test data* — "Add test data"; a row is kind + name; "Where" reveals one
  field only when needed (a directory; or key → path rows, not key=path
  text);
- *Parameters*, *Resources*, *Pod* (annotations, labels, service
  account), *Schedule*, *Timeout*;
- *YAML* — the full Test, editable (what the wizard's YAML mode does).
Each section saves on its own (a merge patch of that part, conditional on
the version shown — as the edit wizard does today).

**Layout: a narrow form, the YAML growing beside it**
- Two columns on wide screens: the form in a narrow column (~34rem — one
  field per line, nothing to scan sideways), and beside it a sticky panel
  with the Test's YAML, rebuilt as you type. Narrow screens: one column,
  the YAML in a collapsible panel under the form.
- Live: on input (debounced ~300 ms) the page posts the form to
  `…/tests/new/yaml` (and `…/edit/yaml`), which runs the same Go `build`
  as Create and returns the YAML — one implementation, no JavaScript copy
  of the rules that could drift. Problems show beside the field they
  belong to, the YAML keeps its last good state. The dry run through the
  webhooks stays on Create (and on demand: "Check").
- Without JavaScript: the YAML appears on the review step, as now.

**YAML that reads well**
- Formatted for people, not alphabetically: `apiVersion`, `kind`,
  `metadata` (name, namespace, labels), then `spec` in a fixed order —
  `use`, `config`, `content`, `container`, `pod`, `timeout`, `schedule`,
  the rest after. Multi-line strings (inline scripts) as `|` block
  scalars, not escaped one-liners; empty objects dropped (as now).
  Built on yaml.v3 nodes from the JSON encoding.
- Highlighted: keys, strings, numbers/booleans, comments, list dashes,
  block scalars — rendered server-side into `<span>`s (a small Go
  tokenizer for the YAML we emit; no CDN, CSP stays 'self'), themed for
  light and dark. The same component shows YAML everywhere: the wizard's
  panel, the Test page's Definition, Settings → YAML, the run's resolved
  spec. Copy and Download buttons on the panel.
- Editing YAML (Settings → YAML, the wizard's YAML mode, the inline
  script editor): **CodeMirror 6**, so it feels like an editor — line
  numbers, folding, bracket/indent guides, search, Tab = two spaces,
  undo history; YAML mode for Tests, JavaScript/Python/XML modes for
  inline scripts by file extension. Server errors (strict parse,
  admission) become line markers in the gutter. Later, completion of
  field names from the CRD schema (OpenAPI in the CRD) is possible.
  - Vendored, not from a CDN: `make vendor-codemirror` bundles pinned
    packages (`codemirror`, `@codemirror/lang-yaml`, `-lang-javascript`,
    `-lang-python`, `-lang-xml`, a light and a dark theme matching Control
    Center) with esbuild into `views/static/vendor/codemirror.min.js`
    (~150–250 KB), committed with its licenses (MIT) and the exact
    versions; a test fails if the bundle and the pinned versions drift.
  - CSP stays `default-src 'self'`: CodeMirror styles through
    constructable stylesheets (`adoptedStyleSheets`), which CSP doesn't
    block — verified in the browser check; if a browser falls back to
    `<style>` elements, `EditorView.cspNonce` with a per-response nonce.
  - Progressive: the page ships a plain `<textarea>` that CodeMirror
    replaces and keeps in sync, so the form posts the same field and
    still works without JavaScript.
- Read-only YAML (the live panel, Definition, resolved spec) stays the
  server-side highlighter: light, no script needed, same palette as the
  editor theme.

**Language:** no `/data/repo`, sparse or mountPath in the primary UI;
plain words ("the folder with your tests", "replace a file in the
repository"); the technical terms only in the advanced fields' help.

**Bug fixed with it (shipped ahead):** sections the scripts hide stayed
visible — layout CSS (`display: grid/flex`) beat the `hidden`
attribute; test-data rows showed every field.

**Acceptance**
- Creating an inline k6 Test: name, k6, paste, Create — three screens, no
  other field touched.
- Creating a Playwright Test from git: name, Playwright, repo + path,
  Create.
- Settings: each section saves alone and keeps the others; Git-managed
  Tests read-only with Duplicate.
- Page tests for every section; a browser check (headless Chrome over
  CDP, as for refresh.js) of the step flow and the hidden sections.
- CodeMirror in the browser check: loads under the CSP (no console
  violations), edits post back, a server error marks its line; the
  bundle-drift test.
- The YAML endpoint returns the same YAML as Create; key order and block
  scalars golden-tested; the highlighter's output for every token kind;
  the live panel updates in a browser check (type in the form → the
  YAML changes, an error shows by its field).

---

## Out of scope

- Live view for Locust (stays a backlog item; logs + the report after
  the run cover it).
- WebSockets for logs (polling is enough at this scale).
- Timeframes beyond 30 days on the Test page (Analytics covers history).
