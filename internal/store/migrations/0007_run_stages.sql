-- +goose Up
-- A leaf run's stages (step 20a): queued, fetch, run, collect, each with
-- its phase and times, so history shows a run's course after the TestRun
-- is gone. Null for composite and parallel runs, and for older runs.
ALTER TABLE test_runs
    ADD COLUMN stages jsonb;

-- +goose Down
ALTER TABLE test_runs
    DROP COLUMN IF EXISTS stages;
