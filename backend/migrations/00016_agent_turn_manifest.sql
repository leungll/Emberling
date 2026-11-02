-- +goose Up
-- +goose StatementBegin

-- One Context Manifest per Agent Turn: what material the model request of that Turn
-- carried, what it left out and where the original can be read. It is written once, in
-- the transaction that wins the Turn claim, and never updated; the PRIMARY KEY on turn_id
-- is what makes a second Manifest for the same Turn impossible.
CREATE TABLE agent_turn_manifests (
    turn_id      TEXT        PRIMARY KEY REFERENCES agent_turns (id),
    agent_run_id TEXT        NOT NULL REFERENCES agent_runs (id),
    items        JSONB       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL
);

-- The Agent Trace reads every Manifest of one Agent Run in one query.
CREATE INDEX agent_turn_manifests_agent_run_id_idx ON agent_turn_manifests (agent_run_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS agent_turn_manifests;
-- +goose StatementEnd
