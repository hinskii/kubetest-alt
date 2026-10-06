-- +goose Up
-- Kubetest owns the state its GUI shows; Control Center is stateless.
--
-- A run's comment lives on its history row, so it is deleted with the run
-- (DELETE /runs/{id}, retention partition drops) and never orphaned.
-- SaveFinished's upsert lists its columns explicitly and leaves these alone.
ALTER TABLE test_runs
    ADD COLUMN comment    text,
    ADD COLUMN comment_by text,
    ADD COLUMN comment_at timestamptz;

-- Who did what through the API server: runs created/aborted/deleted,
-- comments, Test definitions changed. Append-only; low volume (one row per
-- user action), so not partitioned.
CREATE TABLE audit_log (
    id        bigserial   PRIMARY KEY,
    at        timestamptz NOT NULL DEFAULT now(),
    actor     text        NOT NULL DEFAULT '',
    action    text        NOT NULL,
    namespace text        NOT NULL DEFAULT '',
    target    text        NOT NULL DEFAULT '',
    details   jsonb
);
CREATE INDEX audit_log_namespace_id_idx ON audit_log (namespace, id DESC);
CREATE INDEX audit_log_actor_id_idx ON audit_log (actor, id DESC);

-- +goose Down
DROP TABLE IF EXISTS audit_log;
ALTER TABLE test_runs
    DROP COLUMN IF EXISTS comment_at,
    DROP COLUMN IF EXISTS comment_by,
    DROP COLUMN IF EXISTS comment;
