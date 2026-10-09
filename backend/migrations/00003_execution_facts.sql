-- +goose Up
-- +goose StatementBegin

-- Execution facts: trusted facts a successful Tool Attempt produced under its
-- registered declaration, for example "this image was generated from that photo with
-- these settings" or "this image was reviewed and passed". Protected Tool preconditions
-- and delivery checks read only these committed rows; they never reinterpret historical
-- Tool results or derive facts from Events, which are projections.
--
-- A fact commits in the same transaction as the Tool Attempt result, the Action
-- completion and its Event. fact_type is a registered string validated by the Registry
-- at startup, not a database enum. binding holds bounded key-value references and
-- digests only; credentials, callback tokens and signed URLs never belong here. A fact
-- is immutable once written and has no manual source: every fact names the Tool
-- Attempt that produced it. basis_fact_id links a review fact to the generation fact it
-- reviewed, forming the provenance chain.
--
-- UNIQUE (tool_attempt_id, fact_type) makes a duplicated result transaction fail
-- instead of recording the same fact twice; the writer rolls back rather than ignoring
-- the conflict.
CREATE TABLE execution_facts (
    id              TEXT        PRIMARY KEY,
    run_id          TEXT        NOT NULL REFERENCES runs (id),
    agent_run_id    TEXT        NOT NULL REFERENCES agent_runs (id),
    tool_attempt_id TEXT        NOT NULL REFERENCES tool_attempts (id),
    fact_type       TEXT        NOT NULL,
    subject_ref     TEXT        NOT NULL,
    binding         JSONB       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(binding) = 'object'),
    verdict         BOOLEAN,
    basis_fact_id   TEXT        REFERENCES execution_facts (id),
    created_at      TIMESTAMPTZ NOT NULL,
    CONSTRAINT execution_facts_tool_attempt_fact_type_key UNIQUE (tool_attempt_id, fact_type)
);

-- Precondition checks and basis resolution look up the newest fact of one type about
-- one subject within a Run.
CREATE INDEX execution_facts_run_type_subject_idx
    ON execution_facts (run_id, fact_type, subject_ref, created_at DESC, id DESC);

-- Trace projection and per-Agent Run reads.
CREATE INDEX execution_facts_agent_run_type_idx ON execution_facts (agent_run_id, fact_type);

-- The optional generation limit frozen from the Agent configuration when the Agent Run
-- is created. NULL means no limit. The number of generation calls already made is not
-- stored: it is derived under the Run lock from the persisted Tool Attempts, so there
-- is no second source of truth to drift.
ALTER TABLE agent_runs
    ADD COLUMN max_generation_calls INTEGER CHECK (max_generation_calls >= 0);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agent_runs DROP COLUMN IF EXISTS max_generation_calls;
DROP TABLE IF EXISTS execution_facts;
-- +goose StatementEnd
