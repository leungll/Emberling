-- +goose Up
-- +goose StatementBegin

-- Which Tool Attempt of which Run declared which Execution Artifact. An artifact is
-- content-addressed and may be shared by any number of Runs, so execution_artifacts has
-- no Run of its own; this link is the only basis for reading an artifact through a Run.
--
-- A link commits in the result transaction of the Tool Attempt that declared the
-- artifact, under the Run lock, after the artifact's metadata row and before the
-- Attempt's conditional update to SUCCEEDED. A result that loses that update rolls back
-- together with its link, so a link exists exactly when a committed result declared the
-- artifact. The link comes from the declaration the Tool returned with its result, never
-- from parsing the result. Links are immutable once written.
CREATE TABLE tool_attempt_artifacts (
    tool_attempt_id TEXT        NOT NULL REFERENCES tool_attempts (id),
    artifact_id     TEXT        NOT NULL REFERENCES execution_artifacts (artifact_id),
    run_id          TEXT        NOT NULL REFERENCES runs (id),
    created_at      TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tool_attempt_id, artifact_id)
);

-- The Run-scoped content read looks up one artifact within one Run.
CREATE INDEX tool_attempt_artifacts_run_artifact_idx ON tool_attempt_artifacts (run_id, artifact_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS tool_attempt_artifacts;
-- +goose StatementEnd
