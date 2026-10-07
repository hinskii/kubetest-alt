-- +goose Up
-- Individual JUnit test cases of finished runs (step 18-2f): the failed
-- tests on a run's page, per-test-case history and flakiness. Partitioned
-- like test_runs, with the same month boundaries and partition suffix
-- (test_cases_YYYY_MM), so retention drops a month of both together.
-- Bounded per run by the wrapper (5000 cases, trimmed failure text).
CREATE TABLE test_cases (
    run_uid     uuid        NOT NULL,
    finished_at timestamptz NOT NULL, -- the run's, for partitioning
    idx         integer     NOT NULL, -- position in the run's reports
    namespace   text        NOT NULL,
    test_ref    text        NOT NULL,
    case_key    text        NOT NULL, -- class (or suite) › name
    suite       text,
    class       text,
    name        text        NOT NULL,
    status      text        NOT NULL, -- passed | failed | error | skipped
    duration_ms bigint,
    message     text,
    details     text,
    file        text,
    PRIMARY KEY (run_uid, idx, finished_at)
) PARTITION BY RANGE (finished_at);

-- History of one case of one Test, newest first; and a run's cases.
CREATE INDEX test_cases_history_idx ON test_cases (namespace, test_ref, case_key, finished_at DESC);
CREATE INDEX test_cases_test_finished_idx ON test_cases (namespace, test_ref, finished_at DESC);
CREATE INDEX test_cases_run_idx ON test_cases (run_uid);

-- +goose Down
DROP TABLE IF EXISTS test_cases;
