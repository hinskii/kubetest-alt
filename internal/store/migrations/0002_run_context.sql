-- +goose Up
-- Run context Control Center needs for history and analytics without
-- re-reading the CR (which is gone for archived runs):
--   config      effective parameters (Test/template defaults overlaid with
--               TestRun.spec.config) — what the run actually ran with;
--   tool        kubetest.io/tool identity (TestRun.status.tool);
--   parent_run  composite parent's run name, null for top-level runs.
-- Added on the partitioned parent table, so every partition gets them.
ALTER TABLE test_runs
    ADD COLUMN config     jsonb,
    ADD COLUMN tool       text,
    ADD COLUMN parent_run text;

-- "Recent runs of test X in namespace Y" — the Control Center history page.
CREATE INDEX test_runs_namespace_test_ref_finished_at_idx
    ON test_runs (namespace, test_ref, finished_at DESC);

-- +goose Down
DROP INDEX IF EXISTS test_runs_namespace_test_ref_finished_at_idx;
ALTER TABLE test_runs
    DROP COLUMN IF EXISTS parent_run,
    DROP COLUMN IF EXISTS tool,
    DROP COLUMN IF EXISTS config;
