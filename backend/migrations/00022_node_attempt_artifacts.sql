-- +goose Up
-- +goose StatementBegin

-- Which Node Attempt of which Run declared which Execution Artifact. It mirrors
-- tool_attempt_artifacts for the second producer of artifacts: an asynchronous node, such
-- as the transcript node, writes text material to content storage and declares it with
-- its output. An artifact is content-addressed and may be shared by any number of Runs,
-- so this link is one of the two bases for reading an artifact through a Run; the other
-- is the Tool Attempt link, and both require the declaring Attempt to have SUCCEEDED.
--
-- A link commits in the result transaction of the Node Attempt that declared the
-- artifact, under the Run lock, together with the artifact's metadata row and the
-- Attempt's conditional update to SUCCEEDED. A completion that does not win that update
-- writes no link, so a link exists exactly when a committed result declared the artifact.
-- The link comes from the declaration the node returned with its output, never from
-- parsing the output. Links are immutable once written.
CREATE TABLE node_attempt_artifacts (
    node_attempt_id TEXT        NOT NULL REFERENCES node_attempts (id),
    artifact_id     TEXT        NOT NULL REFERENCES execution_artifacts (artifact_id),
    run_id          TEXT        NOT NULL REFERENCES runs (id),
    created_at      TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (node_attempt_id, artifact_id)
);

-- The Run-scoped content read looks up one artifact within one Run.
CREATE INDEX node_attempt_artifacts_run_artifact_idx ON node_attempt_artifacts (run_id, artifact_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS node_attempt_artifacts;
-- +goose StatementEnd
