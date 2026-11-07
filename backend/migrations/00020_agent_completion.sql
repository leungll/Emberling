-- +goose Up
-- +goose StatementBegin

-- Completion evaluation: when an Agent Run with a frozen task goal ends, the Runtime checks
-- the recorded facts against that goal and writes one verdict. The row commits in the same
-- transaction as the Agent Run's termination (the FINAL completion or the failure path that
-- ended it) and is never updated: a second evaluation of the same Agent Run is a programming
-- error, and the primary key makes it lose in the database.
--
-- goal is the goal the verdict was evaluated against, copied from the Agent Run so the row
-- is self-describing. outcome is one of the five task results. checks is the JSON array of
-- {name, passed, refs} items the evaluation went through, refs naming the facts, records
-- and patches each check rested on; missing is the JSON array of {code, description} items
-- an INCOMPLETE verdict still lacks, empty for every other outcome.
CREATE TABLE agent_completions (
    agent_run_id TEXT        PRIMARY KEY REFERENCES agent_runs (id),
    goal         TEXT        NOT NULL CHECK (goal IN ('PREPARE_ONLY', 'FIX_AND_DEPLOY')),
    outcome      TEXT        NOT NULL CHECK (outcome IN (
        'READY_FOR_APPROVAL', 'DEPLOYED', 'DEPLOYED_CHECK_FAILED', 'RELEASE_UNKNOWN', 'INCOMPLETE'
    )),
    checks       JSONB       NOT NULL CHECK (jsonb_typeof(checks) = 'array'),
    missing      JSONB       NOT NULL CHECK (jsonb_typeof(missing) = 'array'),
    evaluated_at TIMESTAMPTZ NOT NULL
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS agent_completions;
-- +goose StatementEnd
