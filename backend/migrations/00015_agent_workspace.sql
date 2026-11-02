-- +goose Up
-- +goose StatementBegin

-- Agent workspaces: the repository binding an Agent Run is frozen to when it starts. The
-- row is written in the same transaction as the Agent Run and never changes afterwards:
-- a running Agent reads its repository, base commit, allowed path prefixes and deploy
-- target from here, never from the Definition's current configuration. The workspace
-- itself is the base commit plus the sequence of cumulative patches below; no checkout is
-- ever persisted.
CREATE TABLE agent_workspaces (
    agent_run_id  TEXT        PRIMARY KEY REFERENCES agent_runs (id),
    repository_id TEXT        NOT NULL,
    base_commit   TEXT        NOT NULL,
    allowed_paths JSONB       NOT NULL CHECK (jsonb_typeof(allowed_paths) = 'array'),
    deploy_target JSONB       CHECK (deploy_target IS NULL OR jsonb_typeof(deploy_target) = 'object'),
    created_at    TIMESTAMPTZ NOT NULL
);

-- Workspace patches: one row per successful write to the workspace. Each row holds the
-- whole cumulative unified diff relative to the frozen base commit, so the newest seq is
-- the current workspace and an execution environment is rebuilt from the base commit and
-- one patch, never from an earlier row. A row commits in the Tool result transaction that
-- accepted it and is immutable afterwards.
--
-- UNIQUE (agent_run_id, seq) makes a duplicated result transaction lose on the sequence it
-- allocated; UNIQUE (tool_attempt_id) makes one Tool Attempt record at most one patch. The
-- length CHECK bounds the patch text the Runtime stores.
CREATE TABLE workspace_patches (
    id              TEXT        PRIMARY KEY,
    agent_run_id    TEXT        NOT NULL REFERENCES agent_runs (id),
    seq             INTEGER     NOT NULL CHECK (seq >= 1),
    patch           TEXT        NOT NULL CONSTRAINT workspace_patches_patch_size_check CHECK (octet_length(patch) <= 262144),
    patch_digest    TEXT        NOT NULL,
    touched_paths   JSONB       NOT NULL CHECK (jsonb_typeof(touched_paths) = 'array'),
    tool_attempt_id TEXT        NOT NULL UNIQUE REFERENCES tool_attempts (id),
    created_at      TIMESTAMPTZ NOT NULL,
    CONSTRAINT workspace_patches_agent_run_seq_key UNIQUE (agent_run_id, seq)
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS workspace_patches;
DROP TABLE IF EXISTS agent_workspaces;
-- +goose StatementEnd
