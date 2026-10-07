-- +goose Up
-- Finished TestRuns leave the cluster once they're in run history
-- (--finished-run-ttl), so links that carry a run's name (Control Center,
-- Test.status.latestRun) are resolved here by name.
CREATE INDEX test_runs_namespace_name_idx ON test_runs (namespace, name, finished_at DESC);

-- +goose Down
DROP INDEX IF EXISTS test_runs_namespace_name_idx;
