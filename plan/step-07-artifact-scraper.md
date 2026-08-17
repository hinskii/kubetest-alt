# Step 07 — Artifact scraper + result parsers

## Goal
`internal/scraper`: glob → MinIO upload; JUnit XML + perf report auto-parse (CLAUDE.md §6, §12). Implements `ResultReader` stubbed in step 04.

## Tasks
- doublestar glob over `artifacts.paths`; upload behind `Uploader` interface (real: minio-go; fake in tests); layout `kubetest-artifacts/<runID>/<relpath>`; optional compress.
- JUnit scan: every uploaded `.xml` validated as JUnit; parsed into counts (total/passed/failed/skipped) merged into ExecutionResult.
- Perf ingest: k6 summary.json (already step 05), JMeter JTL (CSV), locust CSV, newman JSON — shared parser registry keyed by executor type (full set lands in step 11; registry + k6 here).
- Wrapper integration: scrape runs post-tool AND from SIGTERM hook.

## Unit test requirements
- Glob table: `**/*.xml`, `out/**`, absolute path rejected, no matches → empty (not error), >10k files → capped with warning.
- Uploader fake asserts: object keys, per-run prefix isolation, upload retry on transient error (fake returns 500 once), permanent failure → artifacts partial + error recorded, run result still produced.
- JUnit: golden fixtures — valid single suite, nested suites, malformed XML (→ skipped with warning, NO panic/NO fail), huge file (size cap).
- Non-JUnit XML (e.g. random config) → correctly ignored, not counted.
- Compress: archive contains exactly matched files, deterministic paths.

## Acceptance
- Coverage `internal/scraper` >= 85%. envtest e2e: run with artifacts produces objects in fake uploader + counts in status.
