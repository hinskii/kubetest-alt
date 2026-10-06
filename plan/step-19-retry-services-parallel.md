# Step 19 — retry, services, parallel (fixes.md #16, #17)

The CRD accepted `spec.retry`, `spec.services`, `spec.parallel` and
`steps[].retry`, and nothing executed them. User decision (2026-10-06):
implement all of them now. Security of `spec.pod` / API server (#1) is
deferred — recorded as a known risk in docs/security.md.

One commit + push per sub-step; gates per plan/README.md.

## 19a — retry ✅

**Leaf runs (`Test.spec.retry`)** — retried inside the wrapper, like a
TestWorkflow step retry: `/entry` runs the tool, computes the verdict, and
while it isn't `passed` and attempts remain, prints a separator line and
runs the tool again. One pod, one log stream, one artifact scrape (after
the last attempt), one `result.json` with the last attempt's verdict plus
`attempts[]`. The operator records attempts in `status.steps`
(`attempt-1`, `attempt-2`, …) so history and the GUI show them.

- Retry on `failed` and `error` from the tool/processors; never on timeout
  or abort (the wrapper's budget `TimeoutSeconds` covers all attempts).
- `until`: only `passed` (the default) is supported; the webhook rejects
  anything else instead of accepting and ignoring it.
- Stale reports: a processor may read a report left by a previous
  attempt; previous attempts were not `passed`, so a stale report can
  only make the verdict fail, never pass.
- Pod-level failures (OOM, eviction) are not retried — same as Testkube.

**Composite steps (`steps[].retry`)** — when a child's outcome (after
`negative`) is a failure and retries remain, a new child
`<child>-r<N>` is created; the step aggregates the latest attempt of each
expected child. Aborted children are not retried.

## 19b — services ✅

Dependent pods started before the test, reachable by DNS:

- per service, `count` replicas (default 1) or one per `matrix`
  combination, as Pods `<run>-<svc>-<i>` with hostname/subdomain, behind a
  headless Service `<run>-<svc>` (names bounded to 63);
- the pod spec reuses `Test.spec.pod` (annotations, SA, scheduling) plus
  the service's image, command, args, env, resources, readinessProbe;
- the operator creates them at setup, waits until every replica is Ready
  (or `timeout`, default 5m → run `error`, reason ServiceNotReady), then
  creates the test Job; services are deleted at the terminal transition
  (owner reference as a safety net);
- the test gets `KUBETEST_SERVICE_<NAME>_HOST` and expressions
  `{{ services.<name> }}` (deterministic DNS name, resolvable at setup;
  the expression language has no nested keys, so not `.host`);
- `logs: true` tails each replica's log to
  `runs/<ns>/<uid>/services/<svc>-<i>/logs/`;
- `shards` / `maxCount` on a service: rejected by the webhook (no meaning
  for a dependency).

## 19c — parallel ✅

N workers of the same Test, each its own Job/Pod:

- workers: `count` per `matrix` combination; with `shards` and no
  `count`, one per value of the longest shard list, capped by `maxCount`
  (Testkube semantics); `shards` are now `map[string][]string`, each list
  split into contiguous near-equal parts (`shard.<key>` comma-joined);
- expressions `{{ matrix.<k> }}`, `{{ shard.<k> }}`, `{{ worker.index }}`,
  `{{ worker.count }}` — checked at setup (resolver), filled per worker at
  compile time (`expr.SubstituteWorker`); also env
  `KUBETEST_WORKER_INDEX/COUNT`, `KUBETEST_MATRIX_<K>`, `KUBETEST_SHARD_<K>`;
- worker i writes to `runs/<ns>/<uid>/artifacts/workers/<i>/` — inside the
  run's artifacts, so the API serves worker logs, result.json and
  artifacts with no API change (`workers/<i>/logs/…`,
  `workers/<i>/artifacts/…`); retention and delete cover them. Service
  logs moved the same way (`artifacts/services/<pod>/`);
- verdict: any error → error, else any failure/abort → failed, else
  passed; per-worker phases in `status.steps["worker-<i>"]`, test counts
  summed, artifact refs merged with their `workers/<i>/artifacts/` path;
- a worker's Job is deleted only after its verdict is persisted (a lost
  status write must not turn into "Job missing");
- `transfer`, `fetch`, `logs` removed from the API: every worker fetches
  the content itself, its output is already under the run, and logs are
  always stored. Webhook: count xor maxCount, maxCount only with shards,
  identifier keys, ≤ 50 workers;
- with `services`, the services start first and serve every worker.

## Out of scope here
- GUI for attempts/services/workers (18e).
