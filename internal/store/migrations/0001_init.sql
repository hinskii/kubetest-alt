-- +goose Up
-- test_runs is monthly-partitioned by finished_at (UTC). §9 forbids blobs in
-- the DB: logs + artifacts live in MinIO, this table stores only metadata +
-- JSONB summaries + object-store pointers. Retention drops whole partitions
-- rather than DELETEing rows — see internal/store/partition.go.
--
-- Primary key MUST include the partition key (Postgres constraint): (uid,
-- finished_at). uid alone is unique in practice (TestRun UID is a k8s UUID),
-- but the composite lets Postgres route rows and enforce upserts per (uid,
-- finished_at) — collisions across partitions are prevented by uid uniqueness
-- at the app layer (see idempotent-upsert test).
CREATE TABLE test_runs (
    uid           uuid        NOT NULL,
    name          text        NOT NULL,
    namespace     text        NOT NULL,
    test_ref      text        NOT NULL,
    phase         text        NOT NULL,
    source        text,
    queued_at     timestamptz,
    started_at    timestamptz,
    finished_at   timestamptz NOT NULL,
    duration_ms   bigint,
    resolved_spec jsonb,       -- Test.Spec snapshot (§15.5)
    steps         jsonb,       -- map[string]StepResult
    metrics       jsonb,       -- map[string]float64 (string→float parsed at write)
    test_counts   jsonb,       -- {total,passed,failed,skipped} or null
    artifact_refs jsonb,       -- []ArtifactRef pointers into MinIO
    logs_ref      text,        -- MinIO prefix / key for streamed logs (step 08)
    message       text,
    tags          jsonb,       -- map[string]string from TestRun.Spec.Tags
    PRIMARY KEY (uid, finished_at)
) PARTITION BY RANGE (finished_at);

-- Query index for the UI's dominant read path: "recent runs of test X."
-- DESC on finished_at so the API server's paginated list is index-only.
CREATE INDEX test_runs_test_ref_finished_at_idx
    ON test_runs (test_ref, finished_at DESC);

-- Query index for "recent runs in namespace X" (multi-team clusters).
CREATE INDEX test_runs_namespace_finished_at_idx
    ON test_runs (namespace, finished_at DESC);

-- Query index for "recent failed runs" — SRE dashboard use case.
CREATE INDEX test_runs_phase_finished_at_idx
    ON test_runs (phase, finished_at DESC);

-- +goose Down
DROP TABLE IF EXISTS test_runs;
