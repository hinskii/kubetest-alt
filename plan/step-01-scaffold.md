# Step 01 — Scaffold

## Goal
Buildable repo skeleton matching CLAUDE.md §13 layout; manager binary starts.

## Tasks
- `kubebuilder init --domain kubetest.io --repo <module>`; restructure to §13 layout (`cmd/operator`, `cmd/apiserver`, `cmd/entry`, `cmd/cli` as empty mains).
- Makefile targets: `generate`, `manifests`, `lint`, `test`, `test-coverage`, `docker-build`, `run`.
- golangci-lint config (errcheck, govet, staticcheck, revive, gosec).
- CI pipeline: lint + test + build on PR.
- envtest setup helper in `test/envtest.go` (shared `TestMain` pattern, KUBEBUILDER_ASSETS via setup-envtest).

## Unit test requirements
- Smoke test: manager constructs with scheme containing corev1/batchv1 (no CRDs yet), `mgr.GetClient()` non-nil.
- CI proves `make test -race` runs green on empty suite.

## Acceptance
- `make run` starts manager against envtest/kind without panic.
- All gates green.
