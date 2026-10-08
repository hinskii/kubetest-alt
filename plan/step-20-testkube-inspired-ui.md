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

Order: 20a → 20h, one commit each, gates green before the next
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

## 20h — The test's path: required, stated once, nothing hardcoded

Today the location of a Test's files is partly hardcoded, partly
defaulted, and stated twice:
- the templates hardcode the checkout's place — `/data/repo/{{ config.script }}`,
  `workingDir: /data/repo/{{ config.projectDir }}`, … — so a Test that sets
  `content.git.mountPath` (which the fetcher honours) breaks every
  template;
- several templates default the path: `projectDir: "."` (playwright,
  cypress, gradle, maven), `testsDir: "."` (pytest), `featuresDir: "."`
  (cucumber), `locustfile: locustfile.py`, gatling's simulations folder —
  so a git Test "works" without saying where its tests are, and runs
  whatever sits at the repository's root;
- the same directory goes into `content.git.paths` (sparse checkout) and
  into the template's main parameter — the samples repeat it
  (`paths: [test/catalog/cases/playwright/repo]` +
  `projectDir: test/catalog/cases/playwright/repo`), the wizard asks for it
  twice, and a mismatch fails only at run time.

**The rule:** a Test says where its tests are — the template's main path
parameter has no default, ever. Where the checkout sits in the pod
(`/data/repo`) stays the platform's business.

**Templates (catalog)**
- The main path parameter of every template — `script`, `plan`,
  `collection`, `projectDir`, `testsDir`, `featuresDir`, `locustfile`,
  `simulationsFolder`, `scenario`, `inputFile`, … — loses its default and
  gets a `description` saying what to put there (relative to the
  repository root, or to /data/repo for inline files).
- A new expression `{{ content.repo }}`: where the git checkout is, from
  `content.git.mountPath` (relative to /data or absolute inside it), else
  `/data/repo`. Every template uses it instead of the literal, so a custom
  mountPath works; results stay under `{{ content.repo }}/results`.
- Lint test over config/templates: no `/data/repo` literal, and the
  parameter each template puts after `{{ content.repo }}/` has no default.

**Operator**
- A Test whose resolved spec leaves a required parameter empty gets
  `status.conditions` `Ready=False`, reason `ParameterMissing`, message
  "set spec.config.script (the k6 script, relative to the repository)".
  Control Center shows it on the Test page and in the list (a warning
  chip). Admission stays template-agnostic — no template lookups in the
  webhook (ArgoCD may sync a Test before its template).
- A run of such a Test still starts only with the value given at run
  time (today's required-parameter rule); without it, it fails at once
  with the same message — never runs the repository root.

**Wizard (CC)**
- Git source: one required field, *Path in the repository* (a file or a
  directory, e.g. `perf/checkout.js`, `e2e/web`). It sets the template's
  main parameter (found from the template's arguments, as the wizard
  already does for inline files) and the sparse checkout (the directory,
  or a file's directory). Review refuses to continue without it.
- *Sparse paths* stays as an advanced field (more paths, or "." for the
  whole repository — explicit, never implied).
- Inline files: the main parameter is bound to the first file as today;
  with no file and no git, the review says which parameter is missing.
- Review warns when the main parameter points outside the sparse paths
  ("k6 will look for perf/x.js, which the checkout leaves out").
- Edit shows the path back (the parameter).

**Samples, catalog e2e, docs**
- `config/samples/tools/*.yaml` and the catalog cases set the parameter
  explicitly; one sample sets a custom `mountPath` to keep
  `{{ content.repo }}` honest in the catalog e2e.
- Release note: Tests that relied on a removed default (`projectDir: .`,
  `locustfile.py`, …) now report `ParameterMissing` — set the parameter.
- docs/onboarding-a-tool.md: templates use `{{ content.repo }}` and never
  default the main path; docs/control-center.md: the wizard's git path.

**Acceptance**
- Resolver tests: `{{ content.repo }}` default, relative and absolute
  mountPath.
- Template lint test (above).
- Controller test: `ParameterMissing` set and cleared.
- Wizard tests: git path required; one path → parameter + sparse path;
  typed parameter kept; the outside-the-checkout warning.
- Catalog e2e green (every tool), including the custom-mountPath sample.

---

## Out of scope

- Live view for Locust (stays a backlog item; logs + the report after
  the run cover it).
- WebSockets for logs (polling is enough at this scale).
- Editing labels from the UI (the wizard's YAML mode does it).
- Timeframes beyond 30 days on the Test page (Analytics covers history).
