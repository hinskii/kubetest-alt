# Implementation Plan — Kubetest-alt

Step files for Claude Code. Work through them **in order** — each step assumes the previous ones are merged.

## How to use with Claude Code

1. `CLAUDE.md` (repo root) is the spec — always in context. Section references (§N) in steps point there.
2. One step = one session/PR. Open the step file, implement, run gates, check off.
3. Do NOT start a step if the previous step's acceptance criteria fail.

## Global conventions (apply to every step)

- Go 1.23+, kubebuilder v4 scaffolding, controller-runtime.
- Table-driven tests, `testify` (`require` for setup, `assert` for checks).
- Controllers/webhooks tested with **envtest** (real API server). Pure logic (compiler, parsers, expr) tested with plain unit tests — no cluster.
- Every webhook needs at least one **sentinel test** asserting a rule not expressible in OpenAPI schema (e.g. aggregate byte-size, cron parsing, cross-field predicates) and asserting an error substring that only the webhook code produces. Rules covered by `+kubebuilder:validation:*` markers pass regardless of whether the webhook is wired — the sentinel is what proves the webhook is on the request path.
- Fixtures in `testdata/` next to the package. Golden files for generated Job specs and parsed results; update via `go test -update` flag pattern.
- No hand-rolled mocks of k8s clients. Use envtest or `fake.NewClientBuilder()`.
- External systems behind small interfaces (`Uploader`, `RunStore`, `Clock`) — fake implementations in tests, real ones wired in main.

## Gates (every step, non-negotiable)

```
make generate manifests   # zero diff after commit
make lint                 # golangci-lint clean
make test                 # go test -race ./... green
make test-coverage        # core logic packages (compiler, executor, scraper, expr, store) >= 80%
```

## Steps

| # | File | Scope |
|---|------|-------|
| 01 | step-01-scaffold.md | Repo, kubebuilder init, CI, Makefile |
| 02 | step-02-crds.md | CRD types + validation/defaulting webhooks |
| 03 | step-03-compiler.md | Test+TestRun → Job spec (pure functions) |
| 04 | step-04-testrun-controller.md | Reconciler: Job lifecycle, status, finalizer |
| 05 | step-05-entry-wrapper-k6.md | /entry wrapper contract + k6 runner |
| 06 | step-06-content-fetcher.md | git/files/tarball init container |
| 07 | step-07-artifact-scraper.md | Glob scrape → MinIO, JUnit/perf parsers |
| 08 | step-08-logstream.md | Pod log tail → websocket + MinIO |
| 09 | step-09-postgres-store.md | Run history repo + retention |
| 10 | step-10-apiserver.md | REST/WS API for GUI, managed-by enforcement |
| 11 | step-11-executors-remaining.md | cypress/newman/locust/jmeter wrappers |
| 12 | step-12-scheduler-triggers.md | Cron scheduler + TestTrigger controller |
| 13 | step-13-templates-expr.md | TestTemplate, config params, {{ }} engine |
| 14 | step-14-webhooks-metrics.md | Notifications + Prometheus metrics |
