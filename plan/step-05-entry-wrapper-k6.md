# Step 05 — /entry wrapper + k6 runner

## Goal
`cmd/entry` + `pkg/executor`: container-side wrapper implementing the contract (CLAUDE.md §11), with k6 as first Runner.

## Tasks
- Load `ExecutionRequest` from `/etc/kubetest/request.json`; run tool; stream stdout line-buffered; write `result.json` to `$KUBETEST_RESULTDIR`.
- Self-enforced timeout `TimeoutSeconds` (context deadline) — always fires before Job ADS (§15.3).
- SIGTERM trap: flush partial `result.json` (phase `aborted`) + trigger artifact scrape hook before exit (§15.3).
- k6 Runner: `k6 run --summary-export=summary.json`; exit-code normalization (§15.2): 0→passed, 99→failed (thresholds), other→error; parse summary metrics (p95, rps, checks).
- `executors/k6/Dockerfile` on `grafana/k6`.

## Unit test requirements
- Exit-code table (fake command via injected `execCommand`): {0→passed}, {99→failed + errorMessage mentions thresholds}, {107→error}, {1→error}, {SIGKILL/-1→error}.
- Timeout: fake tool sleeping > TimeoutSeconds → process killed, result.json written with phase `aborted`/`error`, wall time < TimeoutSeconds+2s.
- SIGTERM: send signal mid-run → partial result.json exists and is valid JSON (schema check).
- summary.json parsing: golden fixtures in `testdata/` (real k6 outputs: passing run, failed thresholds, empty/truncated file → error not panic).
- result.json schema: round-trips through `ExecutionResult` struct; unknown fields ignored.
- Missing request.json / malformed → exit non-zero with clear stderr, no panic.

## Acceptance
- `docker run` of k6 wrapper image with a sample script produces valid result.json + streams logs; e2e smoke in kind (manual OK this step).
