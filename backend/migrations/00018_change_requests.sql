-- +goose Up
-- +goose StatementBegin

-- Change requests: the execution fact that binds the Agent Run's final patch to the
-- verification it passed and to the decision basis the Agent cites. A row is written once,
-- in the Tool result transaction that accepted the change-request result, and never
-- updated; the rendered content lives in a text Execution Artifact and the row names it by
-- id together with the digest of those bytes.
--
-- UNIQUE (agent_run_id, content_digest) is what makes a repeated request with the same
-- content converge on one record: the result transaction that finds the row already
-- present returns it instead of creating a second one. decision_basis is the JSON array of
-- {statement, turnNo, manifestRef} items the Runtime checked against the Turns' Context
-- Manifests before the Tool ran.
CREATE TABLE change_requests (
    id                   TEXT        PRIMARY KEY,
    agent_run_id         TEXT        NOT NULL REFERENCES agent_runs (id),
    patch_digest         TEXT        NOT NULL,
    verification_fact_id TEXT        NOT NULL REFERENCES execution_facts (id),
    content_artifact_id  TEXT        NOT NULL REFERENCES execution_artifacts (artifact_id),
    content_digest       TEXT        NOT NULL,
    decision_basis       JSONB       NOT NULL CHECK (jsonb_typeof(decision_basis) = 'array'),
    tool_attempt_id      TEXT        NOT NULL REFERENCES tool_attempts (id),
    created_at           TIMESTAMPTZ NOT NULL,
    CONSTRAINT change_requests_agent_run_content_key UNIQUE (agent_run_id, content_digest)
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS change_requests;
-- +goose StatementEnd
