# Step 09 — Postgres run store + retention

## Goal
`internal/store`: run history repo (write-on-finish from controller), retention (CLAUDE.md §9).

## Tasks
- Schema: `test_runs` partitioned monthly (§9); summary/steps as JSONB; pointers to MinIO only — no blobs.
- `RunStore` interface: `SaveFinished(run)`, `List(filter, page)`, `Get(id)`; controller writes on terminal phase (idempotent upsert by run UID).
- Retention job: drop partitions older than `retentionDays`; MinIO lifecycle documented (bucket rules, not code).
- Migrations (goose/atlas), applied on startup with lock.

## Unit test requirements
- Pure unit: partition math table — given now + retentionDays, exact partition names to create/drop (month boundaries, year rollover, DST-irrelevant UTC).
- Row mapping: ExecutionResult/TestRunStatus ⇄ row round-trip, including nulls and empty steps map.
- Idempotent upsert: same run UID saved twice → one row, last-write-wins on status.
- Integration (build tag `integration`, testcontainers-go postgres): migrations up from zero; partitioned insert lands in correct partition; List filters (test name, phase, time range) + pagination stable ordering; drop-partition removes rows and leaves others.
- Retention safety: partition holding runs newer than cutoff NEVER dropped (boundary test: run at cutoff-1s vs cutoff+1s).

## Acceptance
- `make test` green without Docker (unit only); `make test-integration` green with containers. Controller wired: finished run visible via store.
