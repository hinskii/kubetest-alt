# Step 14 — Notification webhooks + Prometheus metrics

## Goal
Event webhooks on run lifecycle + `/metrics` (CLAUDE.md §6).

## Tasks
- Webhook CR (simple v1): url, events filter (queued/started/finished/failed), headers from secretRef, payload = ExecutionResult + run metadata; retries with backoff, timeout; failure never blocks reconcile.
- Prometheus: operator + apiserver `/metrics` — runs_total{executor,phase}, run_duration_seconds histogram{executor}, active_runs gauge, scheduler_fires_total, webhook_deliveries_total{code}, log/artifact bytes counters.
- Optional OTel traces behind flag (skip if time-boxed).

## Unit test requirements
- Webhook delivery (httptest server): payload schema golden; event filter table (subscribed vs not); retry on 500 then success → exactly 2 attempts; permanent 4xx → no retry; timeout → recorded failure; secret header present, secret value never logged (assert logs).
- Non-blocking: webhook endpoint hanging → reconcile completes within deadline (async dispatch test).
- Metrics: after simulated run lifecycle, gather registry and assert exact counter/histogram values + label sets; no high-cardinality labels (assert label names whitelist — no run IDs/test names in labels).

## Acceptance
- All 14 steps' gates green; full kind e2e: GitOps-applied Test + GUI-run TestRun + cron + trigger all coexist; VictoriaMetrics scrapes /metrics cleanly.
