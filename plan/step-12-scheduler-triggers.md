# Step 12 — Cron scheduler + TestTrigger controller

## Goal
Built-in cron (NOT CronJob-per-test, CLAUDE.md §4) + TestTrigger (§5).

## Tasks
- Scheduler in operator: robfig/cron over Tests with `spec.schedule`; leader-elected; creates TestRuns with deterministic names `{test}-{unixScheduledTime}` (§15.6 idempotency), `source: cron`.
- Missed-fire policy on failover: fire-once-if-late within window, else skip (document).
- TestTrigger controller (§5): watch configured GVK via dynamic informer, resourceSelector match, conditionSpec gating (wait for conditions with timeout/delay), concurrencyPolicy, create TestRun `source: trigger`.

## Unit test requirements
- Scheduler (fake clock, mandatory — no sleeps): fires at cron boundaries; deterministic name collision (AlreadyExists) treated as success (double-fire test §15.6: two scheduler instances, same tick → exactly one TestRun); schedule edit reschedules; Test deletion unschedules.
- Failover: leadership lost/regained across a tick → no duplicate, missed-fire policy honored (table: late 10s → fires, late 2×interval → skips).
- Trigger (envtest): deployment modified matching selector + conditions met → TestRun created; conditions not met within timeout → no run + event recorded; concurrencyPolicy Forbid with active run → skipped.
- conditionSpec unit table: ttl expiry, reason mismatch, multi-condition AND semantics.
- Selector unit table: name vs nameRegex vs labelSelector precedence, namespaceRegex.

## Acceptance
- kind: cron Test fires on schedule; ArgoCD-style deployment patch fires trigger (manual smoke).
