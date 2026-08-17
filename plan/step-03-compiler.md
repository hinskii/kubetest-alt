# Step 03 — Compiler (Test+TestRun → Job)

## Goal
`internal/compiler`: pure function `Compile(test, run, opts) (*batchv1.Job, []client.Object, error)`. No k8s calls. This is the highest-value unit-test target in the repo.

## Tasks
- Build Job per CLAUDE.md §11/§12/§15: init container (content-fetcher), main container (wrapper), volumes (`/data` emptyDir, `$KUBETEST_RESULTDIR`), env, `ExecutionRequest` JSON into projected volume.
- `automountServiceAccountToken: false`, `restartPolicy: Never`, `backoffLimit: 0`.
- `activeDeadlineSeconds = spec.timeout + 60s` (§15.3); wrapper `TimeoutSeconds = spec.timeout`.
- Pod metadata merge (§8): Test.spec.pod → TestRun.spec.pod (TestRun wins per key) + reserved operator labels `kubetest.io/run-id`, `app.kubernetes.io/managed-by`. User values NEVER dropped or rewritten; attempts to set reserved `kubetest.io/*` keys are ignored (operator value wins).
- Cypress executor type: memory-backed emptyDir at `/dev/shm` (§15.3).
- ownerRef Job→TestRun; NO ownerRef Test→TestRun (§15.5).

## Unit test requirements
- Golden Job YAML per executor type (k6/cypress/newman/locust/jmeter) in `testdata/golden/` — full object compare.
- Annotation passthrough table (§8 regression suite):
  - arbitrary annotations (`sidecar.istio.io/inject: "false"`, `foo/bar: baz`, empty map, nil) land verbatim on pod template;
  - assert compiler output contains ZERO annotations not present in input (no hardcoded injection);
  - merge: same key in Test+TestRun → TestRun value; reserved `kubetest.io/run-id` set by user → overwritten by operator value.
- Deadline math table: timeout nil → default applied; timeout 10m → ADS 660s; wrapper TimeoutSeconds always < ADS.
- ownerRef test: exactly one ownerRef, kind=TestRun, controller=true.
- Cypress `/dev/shm` present with `medium: Memory`; absent for other executors.
- Error cases: unknown executor type, nil test → typed errors.

## Acceptance
- Coverage of `internal/compiler` >= 90%.
