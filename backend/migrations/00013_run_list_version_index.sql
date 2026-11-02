-- +goose Up
-- +goose StatementBegin

-- The Run list can be narrowed to one Definition version of a Workflow. The index leads
-- with the two filter columns and continues in the list's total order (started_at DESC,
-- id DESC), the same order the keyset cursor pages in, so a version-filtered page is
-- read in index order without a sort.
CREATE INDEX runs_workflow_version_started_id_idx
    ON runs (workflow_id, definition_version, started_at DESC, id DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS runs_workflow_version_started_id_idx;
-- +goose StatementEnd
