# Step 04 — TestRun controller

## Goal
`internal/controller/testrun_controller.go`: reconcile TestRun end-to-end against Jobs. envtest-heavy step.

## Tasks
- On new TestRun: resolve Test, snapshot resolved spec into `status.resolvedSpec` (§15.5), call compiler, create Job, phase `queued`.
- Pod running → `running` + `startedAt`. Job succeeded/failed → read result (stub interface `ResultReader` until step 07), map to phase, persist status, THEN delete Job (§15.3); TTL 3600s as safety net only.
- Infra failures (§15.3): OOMKilled → `error` + message "OOMKilled"; ImagePullBackOff (pod condition/events) → `error` "infra: ImagePullBackOff <image>". Never `failed` for infra.
- Missing result.json → fallback to exit code + terminated state (§15.2), phase `error` if ambiguous.
- Orphan detection: `running`/`queued` TestRun without Job → `error` with reason (§15.3).
- Finalizer: delete during run kills Job + cleanup hook (§15.5).
- `concurrencyPolicy` enforcement per Test: Forbid → new run waits `queued`; Replace → abort previous.
- Requeue/idempotency: all writes via server-side apply or optimistic retry.

## Unit test requirements (envtest unless noted)
- Happy path: create Test+TestRun → Job exists with compiler-golden spec; fake Job success → TestRun `passed`, Job deleted, `resolvedSpec` populated and equals Test spec at creation time.
- Spec snapshot: mutate Test AFTER run started → `resolvedSpec` unchanged (asserts §15.5).
- Idempotency: force 3 reconciles of same state → exactly one Job (AlreadyExists tolerated), status stable.
- OOM path: patch pod status `terminated{exitCode:137,reason:OOMKilled}` → phase `error`, message contains "OOMKilled".
- Orphan: delete Job behind controller's back → TestRun flips to `error` within one reconcile.
- Finalizer: delete running TestRun → Job deleted first, finalizer removed, no stuck object.
- Concurrency table (pure unit, fake client OK): Allow/Forbid/Replace semantics.
- Deletion race (§15.3): Job deleted by TTL before status persisted → orphan path produces terminal phase, never eternal `running`.

## Acceptance
- envtest suite green with `-race`; no reconcile hot-loops (assert requeue counts bounded in tests).
