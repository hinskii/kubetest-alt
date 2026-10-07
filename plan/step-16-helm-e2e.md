# Step 16 — Helm chart + kind e2e in CI

## Goal
Make the platform installable and prove the WHOLE chain automatically. Two deliverables:
(1) `charts/kubetest-alt/` installing operator + apiserver + CRDs + webhook certs + RBAC, with
optional MinIO/Postgres subcharts for dev; (2) a CI job that installs the chart on kind and runs
real end-to-end scenarios as a gate. This closes the two biggest untested surfaces: full-chain
integration and production deploy.

## Tasks

### Helm chart
- Templates: operator Deployment (leader election on, flags from values: content-fetcher image,
  MinIO endpoint/secret, retention), apiserver Deployment + Service, CRDs (crds/ dir — install,
  never template), ValidatingWebhookConfiguration + cert-manager Certificate (values toggle:
  cert-manager vs helm-generated self-signed for dev), RBAC (roles from config/rbac as source of
  truth — generate, don't hand-copy), ServiceAccounts, NetworkPolicy (optional, values-gated).
- Subcharts (dev/e2e only, values-gated off by default): minio, postgresql (bitnami).
- Values documented in values.yaml comments; image tags pinned to chart appVersion.
- `helm lint` + `helm template` golden tests (unit): render with 2-3 values combos into
  testdata/golden-helm/, diff-compare — catches accidental template drift like our CRD goldens.

### kind e2e (CI job, separate workflow — not in make test)
- Script `test/e2e/run.sh` + Go test with build tag `e2e`: kind create → docker build operator +
  content-fetcher → kind load → helm install (with minio+postgres subcharts) → wait ready.
- Scenarios (each = apply CR, wait phase, assert):
  1. k6 passing (template from catalog) → phase passed, metrics in status, artifacts listed,
     logs retrievable via apiserver WS (curl/websocat or Go client).
  2. jmeter failing plan → phase FAILED despite tool exit 0 (verdictFrom: jtl — the flagship
     §15.2 assertion, now proven on a real cluster).
  3. Content fetch failure (bad git uri) → phase error, reason ContentFetchFailed.
  4. GitOps guard: Test labeled managed-by=gitops → apiserver PATCH returns 409.
  5. Cron: schedule "* * * * *", wait ≤70s → TestRun with source=cron exists (then remove
     schedule).
- Assert operator logs contain no ERROR-level entries during the run (catches silent reconcile
  loops).
- Teardown always (trap), kind cluster deleted; total budget ≤15 min or the job is too heavy
  for PR gate — if over, split: chart lint+golden on PR, full e2e on main/nightly.

## Unit test requirements
- helm golden render tests (2-3 values matrices), helm lint clean.
- RBAC parity test: rules in chart == rules generated in config/rbac (no drift).
- e2e scenarios tagged `e2e`, excluded from make test; CI workflow file is the acceptance.

## Acceptance
- Fresh kind + `helm install` from repo → all 5 scenarios green in CI, run linked in report.
- README quickstart section: 5 commands from zero to first passing TestRun.
- Report: mapping, CI run URL, gates + commit + push + git log.
