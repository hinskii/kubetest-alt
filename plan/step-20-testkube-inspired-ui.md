# Step 20 — Control Center: what Testkube's dashboard does better

Reference: the last open-source Testkube dashboard (v1.16.15, agent chart
1.16.63) run against a real agent with tests, a suite and executions —
screenshots of every view. This step takes over what it does better and
keeps everything Control Center already does better (live view, report in
the page, wizard, test cases/flaky, analytics/compare, schedules, cleanup,
comments, Kubernetes events, multi-cluster, sign-in).

Not taken: their 2-field "Create a test" modal (our wizard), the in-browser
"API endpoint" prompt (we proxy server-side), telemetry/cookie and Pro
banners, a SPA. Control Center stays server-rendered html/template with
small same-origin scripts (CSP: no inline script), stateless (no database),
and every page keeps working without JavaScript.

Order: 20h (done) → 20i → 20a … 20g, one commit each, gates green before the next
(`make lint test test-coverage openapi-check`; e2e in CI).

---

## 20a — Run drawer

Opening a run from a list slides it in from the right over the list,
instead of leaving the page. The full run page stays at the same URL.

**Behaviour**
- Links marked `data-run-link` open the drawer: plain left click only.
  Ctrl/Cmd/Shift/middle click, "open in new tab", direct links, viewports
  under 900 px and no-JS keep today's full page.
- The drawer is ~85 % wide (`role="dialog"`, `aria-modal`, focus moved in
  and returned on close). Header: run name, status, "Open full page", ✕.
- The address bar shows the run's URL (`history.pushState`), so a copied
  link opens the full page. Back, Esc, ✕ or a click on the backdrop close
  it and restore the list's scroll position. Forward reopens it.
- ↑ / ↓ go to the previous / next run of the list it was opened from
  (e.g. stepping through failures), without closing.
- Content = the run page's own `#run-root` (title, Now:, facts, live
  view, report, failed cases, log, events, child runs, comment, actions),
  fetched as the full page and extracted with `DOMParser` — no partial
  templates, no second rendering path on the server.

**Run page script (`run.js`)**
- Becomes `KT.mountRun(root) → unmount()`: log polling, the 15 s queued
  refresh, the frame-colors observer all hang off `root` and stop on
  unmount. The full page mounts it on `#run-root` at load.
- A phase change re-fetches the run into the drawer instead of
  `location.reload()` (full page: unchanged behaviour).

**Actions inside the drawer** (Run again, Abort, Comment, Delete) post
with `fetch` (same origin, so `CrossOriginProtection` passes) and follow
the 303:
- to a run page (abort, comment, rerun → the new run): render it in the
  drawer, update the URL, show the notice from the redirect;
- anywhere else (delete → the Test page): close the drawer and refresh
  the list (refresh.js).

**Links to mark:** a Test's run history, a run's child runs, schedules,
test-case history (`case.html`), analytics, cleanup preview, the tests
list's "last run".

**refresh.js:** skips `[data-refresh]` parts while their content is
behind an open drawer only if they contain focus (unchanged rule); the
drawer itself is not a refresh part.

**Acceptance**
- Go tests: every listed template renders `data-run-link` on run links;
  the run page has `#run-root`; the full page is unchanged otherwise.
- Manual browser check (headless Chrome over CDP, as for refresh.js):
  open / ↑↓ / back / Esc / Cmd-click / Run again / Delete; log polling
  stops after close (no requests in the network log).
- `node --check` on the scripts.

---

## 20b — Tests list: statistics, search, status filter

**API — `GET /tests/stats?namespace=&since=<duration>&recent=<n>`**
(new): per Test over finished runs since `since` (default 7d, max 90d):
`total, passed, failed, errored, aborted, passRatio, p50Ms, p95Ms`, and
the `recent` (default 10, max 30) newest runs as
`{name, uid, phase, durationMs, finishedAt}`. One store query:
`row_number() over (partition by test …)` for the recent runs,
`percentile_cont` for p50/p95. Store interface `TestStats(ctx,
StatsFilter)`; Postgres integration test; fake for unit tests; apiclient
`TestStats`; OpenAPI. Without a store (no history) → 200 with empty
stats, the page shows "—".

**Page** — the table grouped by tool stays (denser than tiles); new
columns: *Pass rate (7d)*, *P50*, *Recent* — one coloured mark per recent
run, oldest → newest, each a run link (drawer). Plus:
- *Search*: by name; server-side `?q=` (works without JS), filtered as
  you type by the script;
- *Status*: last run passed / failed (failed, error, aborted) / never run;
- the count line says what the filters left.

**Acceptance:** store integration test (window and percentiles against a
seeded history), API handler tests, page tests for the columns and
filters, coverage gates.

---

## 20c — Test page header: tiles, timeframe, duration chart

Above the run history, for a timeframe `?range=24h|7d|30d` (default 7d):
- tiles: pass rate, P50, P95, failed, total (from `/tests/stats` for this
  Test);
- a bar chart of run durations (server-rendered SVG like the analytics
  sparklines): one bar per run, coloured by phase, dashed P50/P95 lines,
  each bar a run link (drawer); at most the last 100 runs of the range
  (`ListRuns` with `FinishedAfter`).

The Analytics page stays as the deep view (metrics trends, comparison);
the header links to it.

**Run history:** status filter (server-side, `phase=` — the API already
filters) and a search over run name, creator and comment on the shown
page.

**Acceptance:** page tests (tiles, range switch, bars link to runs,
filters keep paging), SVG has `<title>` per bar for accessibility.

---

## 20d — Log viewer

On the run page / drawer log:
- *Search*: highlights matches in the loaded text, n of m, Enter / ⇧Enter
  to step; works while the log is still growing;
- *Wrap lines* toggle, *Full screen* (the log fills the viewport, Esc
  leaves), *Copy* (whole log as loaded), *Download* stays.

Client-side only; the server already sends plain text.

**Acceptance:** manual browser check; no change to the log endpoint.

---

## 20e — CLI commands

A collapsible *CLI* panel on the Test page and the run page with ready
commands and copy buttons, filled with the namespace, Test and run:
`kubectl kubetest run <test> -n <ns> --follow`, `… --config k=v`,
`runs --test`, `logs <run> -f`, `artifacts <run> --download ./out`,
`abort <run>`. A note line: the kube context is the user's
(`--context`), the token comes from `$KUBETEST_API_TOKEN` or the chart
Secret (docs/cli.md).

**Acceptance:** page tests for the commands (escaped names).

---

## 20f — Labels

A Test's labels as chips on the tests list and the Test page — without
the platform's own (`app.kubernetes.io/managed-by`, `kubetest.io/*`;
the tool already has its chip). Clicking one filters the list
(`?label=k=v`, repeatable, AND); the filter shows as a removable chip.

**Acceptance:** page tests (system labels hidden, filter, escaping).

---

## 20g — About / versions

- API server: `GET /version` → `{version, commit, namespace}` (build info
  via `-ldflags`, as the CLI); OpenAPI.
- Chart passes its version to every component (`KUBETEST_CHART_VERSION`);
  the API reports it too.
- Control Center: an *About* page listing per cluster the API server,
  chart and namespace, plus its own version; the home page's cluster
  cards show the API version.

**Acceptance:** API test, helm test (env present), page test.

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

## 20i — Content: git or inline, never both; projects only from git

Follow-up to 20h. Inline files are text typed into the Test itself; git is
a repository. They answer different needs, so a Test uses one or the
other, and only git asks where the tests are.

**Templates say what their main path is** — a new optional field on
`Parameter`:

```yaml
config:
  script:
    type: string
    path: file        # file | directory — this is the main path parameter
    description: "The k6 script, relative to the repository root."
```

- `path` marks the main path parameter explicitly; `resolver.MainPathParam`
  reads it instead of scanning the container for `/data/repo/{{ config.X }}`
  (that scan goes). At most one parameter per template may set it
  (TestTemplate webhook). CRD change, additive.
- Catalog: `path: file` — k6 `script`, JMeter `plan`, newman `collection`,
  Locust `locustfile`, Artillery `scenario`, kubepug `inputFile`, SoapUI
  `project`; `path: directory` — Playwright, Cypress, Gradle, Maven
  `projectDir`, pytest `testsDir`, Cucumber `featuresDir`, Gatling
  `simulationsFolder`.

**Projects come from git only.** A template whose main path is a
`directory` runs a project (package.json + config + specs + lockfile,
pom.xml, …) — something that belongs in a repository: reviewed, run
locally, versioned with the app, and too big for the 512 KB inline cap.
- Test webhook: no inline files with a template whose main path is a
  directory — needs the template, so it is checked where templates are
  known: the operator's Ready condition (`False/InlineNotSupported`, "this
  tool runs a project: put it in git") and the wizard, which doesn't offer
  inline files for such a template. Admission stays template-agnostic.
- Single-file tools (`path: file`) take git or inline.

**Git or inline, never both.**
- Test webhook (no template needed): `content.git` together with inline
  files (`content.files[]` with `content`) is refused — "a Test's files
  come from git or are written inline, not both".
- Open question: files with `contentFrom` (ConfigMap/Secret) are test
  data, not test code — proposed to stay allowed next to git (e.g. a list
  of product IDs per environment). To confirm before implementing.
- Wizard: *Source* is a choice — Git | Inline files — not two checkboxes.

**Inline: no path, ever (YAML and GUI).** With inline files and the main
path parameter unset, the resolver sets it to the first inline file
(path relative to /data/repo). Nothing to type: `content.files` is the
whole story. Setting the parameter explicitly still wins (e.g. a data
file listed first). The Ready condition counts this as set.

**Git: unchanged from 20h** — the path is required (`ParameterMissing`
without it); the wizard's *Path in the repository* fills it.

**Migration**
- Catalog e2e cases of the seven directory tools (playwright, cypress,
  gradle, maven, pytest, cucumber, gatling) move from inline files to git:
  this repository at the commit under test, sparse path
  `test/catalog/cases/<tool>/repo` — as the samples already do. E2E then
  needs network for those (the samples already do).
- Tests on clusters that combine git and inline text files are refused on
  their next update; inline projects of directory tools report
  `InlineNotSupported`. Upgrade note in docs/control-center.md.
- The dev cluster's `playwright-smoke` (inline) goes to git or away.

**Acceptance**
- Resolver: inline main file = first file; explicit value wins; directory
  template + inline → error.
- Webhook sentinel: git + inline text files refused (rule not
  expressible in the schema); TestTemplate with two `path` parameters
  refused.
- Controller: `InlineNotSupported`; inline single-file Test without the
  parameter → Resolved.
- Wizard: Source choice; no inline for directory tools; inline k6 with
  only a pasted script creates a Test with no `script` in its config.
- Catalog e2e green (directory tools from git).

---

## Out of scope

- Live view for Locust (stays a backlog item; logs + the report after
  the run cover it).
- WebSockets for logs (polling is enough at this scale).
- Editing labels from the UI (the wizard's YAML mode does it).
- Timeframes beyond 30 days on the Test page (Analytics covers history).
