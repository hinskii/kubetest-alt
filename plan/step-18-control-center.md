# Step 18 — Control Center (Go GUI) on top of kubetest

Replaces the deleted `web/` SPA (old steps 18–20). Control Center is a
**Go rewrite** of the in-house Django app "Testkube Control Center",
retargeted from Testkube OSS agents to kubetest. The Django code is a
**functional reference only** (kept outside this repo at
`~/Desktop/testkube-control-center`); no Python lands in the monorepo.

**Decisions (user, 2026-10-06):**
- Go, same module as kubetest (`cmd/control-center`, `internal/controlcenter`).
- Control Center talks to kubetest **through the kubetest apiserver**.
- **Full replacement** — no Testkube code paths.
- Must work for **every catalog tool**, not only k6.
- **Everything in English** — UI strings, code, comments, docs.
- **Control Center is stateless** (user, 2026-10-06; CLAUDE.md §0): no
  database of its own. Comments and audit live in the kubetest run store
  (apiserver endpoints, also usable from the CLI); one-shot scheduled runs
  are a TestRun field the operator honors; analytics are computed from run
  history. CC holds only rebuildable in-memory caches; preferences live in
  the browser, sessions in oauth2-proxy.

---

## 0. Target architecture

```
browser ──oauth2-proxy──► control-center (Go, admin cluster)
                              │  Bearer <cluster token>
                              ▼
               K8s API server of target cluster (RBAC-gated)
                 ├─ services/kubetest-apiserver:8080/proxy/...  ← CRs, history, logs, artifacts
                 └─ pods/<pod>:5665/proxy/...                   ← k6 live dashboard only
```

- kubetest apiserver stays **ClusterIP-only**; the only way in is the K8s
  API service proxy → authn/authz = Kubernetes RBAC (`services/proxy` on
  one Service). Closes fixes.md #1 without a custom auth layer.
- End-user identity (oauth2-proxy `X-Auth-Request-Email`) forwarded as
  `X-Kubetest-User` → `TestRun.spec.tags["kubetest.io/created-by"]`.
  Trust boundary documented: anyone with `services/proxy` can set it.
- Pod-level features (events, k6 dashboard :5665) use the K8s API directly.
- Shared wire types: Control Center decodes into `api/v1alpha1` structs and
  a new `pkg/apiclient` (also usable by the CLI) — compile-time contract
  instead of hand-parsed JSON.

### Stack
- `net/http` (Go 1.22+ mux patterns), `html/template` + `embed` for
  templates/static. No JS framework, no frontend build step; small vanilla
  JS for live logs / forms (same as the reference app).
- No database (stateless, see Decisions).
- Cluster auth providers: `gcp` (Workload Identity via
  `golang.org/x/oauth2/google`), `serviceaccount` (in-cluster),
  `kubeconfig` (local dev). TLS verified with the cluster CA (reference
  app had `verify_ssl=False`). Tokens cached until expiry (reference app
  minted N tokens per request).
- No background work in Control Center: schedules are TestRuns the
  operator holds until `notBefore`; any number of replicas.
- RBAC: viewer < developer < admin from oauth2-proxy email + config lists;
  local-dev override flag refused when `--env=production`.

### Identity: one run = one ID
Reference app had two IDs per run (CRD name vs Job hex ID) plus
minute-bucket grouping — its main bug source. kubetest gives **TestRun UID**
(stable, store PK) and **TestRun name** (== Job name). Control Center keys
all rows on `(cluster, run_uid)`; URLs use name for live runs, UID for
history. Minute grouping is dropped; composite Tests show parent + children.

---

## 1. Concept mapping

| Reference (Testkube)                     | kubetest replacement |
|------------------------------------------|----------------------|
| `TestWorkflow` list/get                  | `GET /tests?namespace=` + `/tests/{name}/resolved` |
| `TestWorkflowExecution` create           | `POST /runs` (`testRef`, `config`, `tags`) |
| stop = delete Job + pods + CR            | `POST /runs/{name}/abort` → `aborted`, persisted |
| agent DB raw SQL history                 | `GET /runs?test=&namespace=&phase=&after=&finishedAfter=` |
| `wait_for_new_job()` polling             | gone — run name known at create time |
| live logs: poll pod 1 s, diff last line  | apiserver WS `/runs/{name}/logs` relayed |
| end-of-run detection by log regex        | terminal `status.phase` |
| artifact archive via testkube-api:8088   | `GET /runs/{id}/artifacts` + `?stream=1` |
| `execution-output.log`                   | `GET /runs/{id}/logs.txt` |
| k6 `summary.json` parsed in Python       | `status.metrics` (Go perf parsers) + `status.testCounts` |
| Critical Path log regex                  | cucumber template → JUnit → `testCounts` |
| cleanup: DELETE agent DB rows + CRD      | `DELETE /runs/{uid}` (store + MinIO), admin-only |
| grouping by `testsGroup`/`market`        | configurable label keys, default `kubetest.io/tool` |

---

## 2. Sub-steps (one commit + push each; gates per plan/README.md)

### 18a — apiserver: multi-namespace, buckets, object keys
- fixes.md #2: empty `--namespace` = cluster-wide; name lookups take
  `?namespace=` (400 if missing in cluster-wide mode). Chart passes
  `--namespace` only when `apiserver.namespace` is set.
- fixes.md #3: operator + apiserver read `minio.logsBucket` /
  `minio.artifactsBucket` from one values source.
- MinIO keys → `<namespace>/<runUID>/…` (cross-namespace collision fix).
- Tests for every lookup path; e2e asserts operator-written logs are
  readable through the apiserver.

### 18b — apiserver: endpoints Control Center needs
- `POST /runs/{name}/abort` (new `spec.abort`): controller kills Job,
  writes `aborted`, persists to Postgres, fires webhook.
- `DELETE /runs/{uid}`: store row + MinIO prefix; refuses non-terminal.
- `GET /runs/{id}/artifacts` (list) + `?stream=1` byte streaming.
- `GET /runs/{id}/logs.txt`; `GET /tests/{name}/resolved`.
- `GET /runs`: `after` cursor, `source`, `finishedAfter`.
- `X-Kubetest-User` → `kubetest.io/created-by` tag.
- Apiserver ClusterRole narrowed (no templates/triggers/webhooks writes).
- Done along the way: `status.tool` (fixes.md #3), run context in history
  (effective config, tool, parent run — migration 0002), chart enables
  `--logs-enabled` (logs were never stored on default installs), e2e
  MinIO images moved to pinned Chainguard (upstream images removed).

### 18c — Metrics for every catalog tool + catalog e2e ✅
- `spec.metrics {from, path}` (Test + TestTemplate, merged like verdict):
  /entry parses the tool's report after it exits into `status.metrics`
  (`pkg/report`: k6Summary, jtl, locustCsv, gatlingStats, artilleryJson;
  shared vocabulary in `docs/metrics.md`). Previously no run ever had
  metrics — the wrapper never called a parser after step 11.
- Parsers tested on real tool output (`hack/report-fixtures.sh`).
- **Catalog e2e** (`test/catalog`, `.github/workflows/test-catalog.yml`):
  all 15 templates — artillery, cucumber, cypress, gatling, gradle,
  jmeter, k6, kubepug, locust, maven, newman, playwright, pytest, soapui,
  zap-baseline — run for real on kind with in-repo projects; verdict,
  JUnit counts, metrics and artifacts asserted per tool.
- Bugs it found and fixed: artifact globs of 12 templates never matched
  (wrong base dir); non-root tool images couldn't write into the fetched
  repo; k6/artillery need their output dir to exist; cypress `--spec`
  resolved against the wrong dir; zap needs `/zap/wrk` mounted; JUnit
  verdict and JUnit counts used different file discovery; sample repo
  referenced by `config/samples/tools` doesn't exist (follow-up).

### 18d — Control Center skeleton
- `pkg/apiclient`: typed Go client for the kubetest apiserver (direct URL
  or via the K8s service proxy), built together with its first consumer.
- `cmd/control-center/main.go`, `internal/controlcenter/{config,server,auth,clusters,views}`.
- Cluster registry from config file (YAML list, any number of clusters).
- No database. healthz; readyz (local only — a down cluster must not take the UI down; cluster health is on the page and in `controlcenter_cluster_up`); Prometheus `/metrics`.
- RBAC middleware + `requireRole`; CSRF protection on POST forms.
- Base layout, light/dark theme, English UI.
- Unit tests (httptest fake apiserver), CI job, lint clean.

### 18d2 — kubetest: state Control Center must not own ✅
- Run store migration 0003: `comment`, `comment_by`, `comment_at` on runs
  (deleted with the run) + append-only `audit_log` (actor from
  `X-Kubetest-User`: run created/aborted/deleted, cleanup).
- Apiserver: `PUT|DELETE /runs/{id}/comment`, `GET /audit`; apiclient methods.
- `TestRun.spec.notBefore`: operator keeps the run `queued` (no resolved
  spec, no Job) until then, then resolves the Test as it is at start. A
  waiting run is ignored by concurrencyPolicy (Forbid doesn't wait for it,
  Replace doesn't abort it). Cancel = abort (recorded in history).
- Comments only on finished runs (409 while live); audit is best-effort
  (a failed audit write is logged, the action stands). Audit retention
  lands with the retention job (fixes.md).

### 18e — Core views ✅
Done as: tests list (grouped by tool, filters, last run, read-only chip),
test page (typed run form incl. enum/boolean/pattern/required, optional
UTC start time → notBefore, history with cursor paging, merged
definition), run page (facts, parameters, metrics, steps/tries/workers in
natural order, log, artifacts proxied with CSP sandbox, comment, abort,
delete for admins). Live logs are polled (`/log?offset=N`, apiserver
`logs.txt?offset=`) rather than relayed over WebSocket: works through the
service proxy and any ingress, and the page reloads once the phase is
final. Pod events and composite child links remain for later.
- Clusters → Tests list (label grouping, tool chip, last phase, gitops lock).
- Test detail: typed params form (string/integer/number/boolean, enum →
  select, pattern hint), run history (cursor paging), schedule form, comments.
- Run page: phase + reason, steps, composite children, live logs (WS
  relay), artifacts list/download, pod events, abort.
- GitOps lock (§7): `managed-by=gitops` Tests read-only (Run allowed).
- All phases: queued, running, paused, passed, failed, aborted, error.

Order (user, 2026-10-07): **18-1f → 18-2f → 18f**. 18-1f and 18-2f
produce the data 18f's analytics are built on.

### 18-1f — Report parsers for ZAP and kubepug
The two catalog tools that only had a verdict get metrics like the load
tools (pkg/report, `spec.metrics` in their catalog templates, fixtures
from the catalog images' real output, docs/metrics.md vocabulary):
- ZAP (`zapJson`, `-J report.json`): `alerts_high`, `alerts_medium`,
  `alerts_low`, `alerts_info`, `alerts_total`;
- kubepug (`kubepugJson`, `--format json`): `deprecated_apis`,
  `deleted_apis`, `apis_total` (resources affected).
- Catalog e2e cases assert the metrics, like the load tools'.

### 18-2f — Functional tests: per-test-case results
Today JUnit gives only totals (`testCounts`). The reports carry every
test case (name, class/suite, status, duration, failure message):
1. **Failed tests on the run page** — name, message, stack excerpt,
   without downloading the XML.
2. **Per-test-case history** — e.g. "failed 4 of the last 20 runs,
   12 s on average, slower lately"; slowest tests of a Test.
3. **Flaky detection** — a test case that both passes and fails under
   the same configuration gets a flakiness score and a marker.
4. **New vs known failures** — comparing two runs: started failing,
   fixed, still failing.
5. **Screenshots and videos** of failed Cypress/Playwright tests shown
   on the run page instead of digging through artifacts.
6. **Commit and branch** of the test content on every run
   (`content.git` revision resolved by the fetcher), to tie a failure to
   a change.

Shape: the wrapper already parses JUnit — it adds the case list to
result.json (bounded: failures always, passing cases name + duration);
the run store gets a `test_cases` table (partitioned and expired with
`test_runs`); the apiserver serves `GET /runs/{id}/testcases` and
`GET /tests/{name}/testcases/{case}/history`; Control Center renders
1–5. Points 5 and 6 don't depend on the table.

### 18f — Analytics (generic)
**Decisions (user, 2026-10-07):** no legacy import — history starts fresh
with kubetest (the tool is about to be rolled out; the old Control
Center's k6 results are not carried over). No Critical Path parsing.
Analytics cover **every catalog tool**, not only k6.

- Computed from kubetest run history (`GET /runs` with `metrics`,
  `testCounts`, `config`, `tool`) and, for functional tools, the
  per-test-case data of 18-2f — no copy in Control Center.
- Tool-agnostic: per Test, trends of whatever the runs carry — pass rate
  and duration for all tools, `testCounts` and flakiness for
  JUnit-reporting tools, `metrics` (pkg/report vocabulary) for load
  tools and, after 18-1f, ZAP alerts and kubepug API findings;
  columns/series = metric keys present on the selected runs, never a
  per-tool layout.
- One comparison builder for table + Markdown export over selected runs.

### 18g — Schedules, cleanup, k6 extras
- One-shot schedules = TestRun with `spec.notBefore` (18d2); list =
  queued runs with a future notBefore. Recurring = kubetest
  `Test.spec.schedule` (editable only for `managed-by=ui`).
- Cleanup policy → `DELETE /runs/{uid}` (audited by kubetest); no-bound guard kept.
- k6 live dashboard reverse proxy (`httputil.ReverseProxy` over the K8s
  pod proxy), only for `tool=k6`; includes the xk6-dashboard#258 shim.
- Grafana link only when configured and tool=k6; compiler injects
  `KUBETEST_RUN_ID`, k6 template tags `testid` with it.

### 18h — Packaging + docs
- Chart component `controlCenter.enabled` (Deployment, Service, config,
  Secret refs); oauth2-proxy stays external (documented).
- Kind e2e smoke: list tests → start run → logs → result row.
- Root CLAUDE.md §13 layout + new `docs/control-center.md`.

---

## 3. Out of scope / follow-ups
- Remaining fixes.md items (Forbid deadlock, MissingResult fallback, tool
  label propagation, retention cron, …) — separate steps, ideally before
  18e ships to users.
- Multi-cluster federation inside kubetest — Control Center does it.
