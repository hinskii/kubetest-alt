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
- Own Postgres (results archive, comments, schedules, audit log) via
  `pgx` + `goose`, same conventions as `internal/store`.
- Cluster auth providers: `gcp` (Workload Identity via
  `golang.org/x/oauth2/google`), `serviceaccount` (in-cluster),
  `kubeconfig` (local dev). TLS verified with the cluster CA (reference
  app had `verify_ssl=False`). Tokens cached until expiry (reference app
  minted N tokens per request).
- Background work in-process: one-shot schedule runner + results importer
  as goroutines; multi-replica safe via atomic `UPDATE … WHERE status=…`
  claims (no CronJobs needed).
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
- `cmd/control-center/main.go`, `internal/controlcenter/{server,auth,clusters,store,views,jobs}`.
- Cluster registry from config file (YAML list, any number of clusters).
- Postgres schema + goose migrations; healthz/readyz; Prometheus `/metrics`.
- RBAC middleware + `requireRole`; CSRF protection on POST forms.
- Base layout, light/dark theme, English UI.
- Unit tests (httptest fake apiserver), CI job, lint clean.

### 18e — Core views
- Clusters → Tests list (label grouping, tool chip, last phase, gitops lock).
- Test detail: typed params form (string/integer/number/boolean, enum →
  select, pattern hint), run history (cursor paging), schedule form, comments.
- Run page: phase + reason, steps, composite children, live logs (WS
  relay), artifacts list/download, pod events, abort.
- GitOps lock (§7): `managed-by=gitops` Tests read-only (Run allowed).
- All phases: queued, running, paused, passed, failed, aborted, error.

### 18f — Analytics (generic)
- `run_results` table: cluster, namespace, test, run_uid, run_name, tool,
  phase, timestamps, duration_ms, config, test_counts, metrics (JSONB),
  message, soft-delete fields.
- Legacy import: one-off command reads the reference app's
  `dashboard_k6_analytics_results` table (if present) and maps k6 fields
  to the metric vocabulary, critical-path → test_counts;
  `legacy_source=testkube`.
- Import on terminal phase + periodic catch-up via `finishedAfter`
  cursor; soft-deleted rows are never re-imported (unless `--force-deleted`).
- One comparison builder for table + Markdown export; columns = metric
  keys present on the selected runs.

### 18g — Schedules, cleanup, k6 extras
- One-shot schedules (pending → running → completed|failed|cancelled,
  stuck-running recovery, atomic claim). Recurring = kubetest
  `Test.spec.schedule` (editable only for `managed-by=ui`).
- Cleanup policy → `DELETE /runs/{uid}`; audit log + no-bound guard kept.
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
