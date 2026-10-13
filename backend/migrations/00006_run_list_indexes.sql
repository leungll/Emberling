-- +goose Up
-- +goose StatementBegin

-- The Run list reads Runs newest first in the total order (started_at DESC, id DESC) and
-- pages with a keyset cursor over the same two values, so each filter the list accepts
-- has an index whose order matches it. The id column breaks ties between Runs that
-- started in the same microsecond, which is what keeps a page boundary stable.
CREATE INDEX runs_started_id_idx ON runs (started_at DESC, id DESC);
CREATE INDEX runs_status_started_id_idx ON runs (status, started_at DESC, id DESC);

-- Supersedes runs_workflow_started_idx: the same leading columns plus the tie-breaker,
-- so the latest-Run-per-Workflow read keeps an index while the list gains a stable order.
CREATE INDEX runs_workflow_started_id_idx ON runs (workflow_id, started_at DESC, id DESC);
DROP INDEX runs_workflow_started_idx;

-- Short-code lookup matches a prefix of the Run ID with byte-wise range comparison,
-- which needs the pattern operator class regardless of the database collation.
CREATE INDEX runs_id_pattern_idx ON runs (id text_pattern_ops);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS runs_id_pattern_idx;
CREATE INDEX IF NOT EXISTS runs_workflow_started_idx ON runs (workflow_id, started_at DESC);
DROP INDEX IF EXISTS runs_workflow_started_id_idx;
DROP INDEX IF EXISTS runs_status_started_id_idx;
DROP INDEX IF EXISTS runs_started_id_idx;
-- +goose StatementEnd
