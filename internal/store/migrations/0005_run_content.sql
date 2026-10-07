-- +goose Up
-- What the content fetcher checked out (step 18-2f): the git revision the
-- Test asked for and the commit it resolved to, so history shows which
-- code a run tested. Null without a git source.
ALTER TABLE test_runs
    ADD COLUMN git_revision text,
    ADD COLUMN git_commit   text;

-- +goose Down
ALTER TABLE test_runs
    DROP COLUMN IF EXISTS git_commit,
    DROP COLUMN IF EXISTS git_revision;
