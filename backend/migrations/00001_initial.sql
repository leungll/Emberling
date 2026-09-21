-- +goose Up
-- +goose StatementBegin

-- Emberling MVP execution schema.
--
-- Invariant #1: PostgreSQL is the single persistence authority for Emberling-owned
-- execution facts. Every table below stores a fact that must survive a Backend restart.
--
-- Status CHECK constraints intentionally exclude the Phase 2 values Run `CANCELLED`,
-- NodeRun `SKIPPED` and NodeRun `CANCELLED`. Adding them requires an explicit forward
-- migration when the accepted scope changes.

-- --------------------------------------------------------------------------
-- Definition
-- --------------------------------------------------------------------------

CREATE TABLE workflows (
    workflow_id    TEXT PRIMARY KEY,
    name           TEXT        NOT NULL,
    latest_version INTEGER     NOT NULL DEFAULT 0 CHECK (latest_version >= 0),
    created_at     TIMESTAMPTZ NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL
);

-- A stored Definition version is immutable. There is no UPDATE path in the Store;
-- editing a Workflow creates a new row (invariant #8).
CREATE TABLE workflow_definitions (
    workflow_id      TEXT        NOT NULL REFERENCES workflows (workflow_id),
    version          INTEGER     NOT NULL CHECK (version >= 1),
    name             TEXT        NOT NULL,
    description      TEXT        NOT NULL DEFAULT '',
    nodes            JSONB       NOT NULL,
    edges            JSONB       NOT NULL,
    run_input_schema JSONB       NOT NULL,
    validation       JSONB       NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (workflow_id, version)
);

-- --------------------------------------------------------------------------
-- Asset and Execution Artifact metadata
-- --------------------------------------------------------------------------

CREATE TABLE assets (
    asset_id    TEXT PRIMARY KEY,
    media_type  TEXT        NOT NULL,
    size_bytes  BIGINT      NOT NULL CHECK (size_bytes >= 0),
    sha256      TEXT        NOT NULL,
    -- storage_key is a system secret: it must never reach Event, Trace or API responses.
    storage_key TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL
);

CREATE TABLE execution_artifacts (
    artifact_id TEXT PRIMARY KEY,
    media_type  TEXT        NOT NULL,
    size_bytes  BIGINT      NOT NULL CHECK (size_bytes >= 0),
    sha256      TEXT        NOT NULL,
    storage_key TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL
);

-- --------------------------------------------------------------------------
-- Run
-- --------------------------------------------------------------------------

-- A Run is the concurrency aggregate root. Every transaction that touches execution
-- facts of this Run takes `SELECT ... FOR UPDATE` on this row first, allocates Event
-- `seq` from `last_seq` and writes `last_seq` back in the same transaction.
CREATE TABLE runs (
    id                 TEXT PRIMARY KEY,
    workflow_id        TEXT        NOT NULL,
    definition_version INTEGER     NOT NULL,
    status             TEXT        NOT NULL CHECK (status IN ('RUNNING', 'PAUSED', 'COMPLETED', 'FAILED')),
    input              JSONB       NOT NULL,
    output             JSONB,
    error              JSONB,
    last_seq           BIGINT      NOT NULL DEFAULT 0 CHECK (last_seq >= 0),
    started_at         TIMESTAMPTZ NOT NULL,
    completed_at       TIMESTAMPTZ,
    updated_at         TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (workflow_id, definition_version) REFERENCES workflow_definitions (workflow_id, version)
);

CREATE INDEX runs_workflow_started_idx ON runs (workflow_id, started_at DESC);

-- --------------------------------------------------------------------------
-- NodeRun and Node Attempt
-- --------------------------------------------------------------------------

-- `(run_id, node_id)` uniqueness is what stops immediate advancement and the
-- Reconciler from creating a second NodeRun for the same Definition node.
CREATE TABLE node_runs (
    id              TEXT PRIMARY KEY,
    run_id          TEXT        NOT NULL REFERENCES runs (id),
    node_id         TEXT        NOT NULL,
    node_type       TEXT        NOT NULL,
    status          TEXT        NOT NULL CHECK (status IN ('READY', 'RUNNING', 'WAITING_CALLBACK', 'SUCCEEDED', 'FAILED')),
    input           JSONB       NOT NULL,
    output          JSONB,
    error           JSONB,
    attempt_count   INTEGER     NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at TIMESTAMPTZ,
    ready_at        TIMESTAMPTZ NOT NULL,
    started_at      TIMESTAMPTZ,
    waiting_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    latency_ms      BIGINT CHECK (latency_ms >= 0),
    token_usage     JSONB,
    updated_at      TIMESTAMPTZ NOT NULL,
    UNIQUE (run_id, node_id)
);

-- Reconciler rediscovery of persisted READY work and of retries whose backoff elapsed.
CREATE INDEX node_runs_status_next_attempt_idx ON node_runs (status, next_attempt_at);
-- Run aggregation and Snapshot read the NodeRuns of one Run.
CREATE INDEX node_runs_run_status_idx ON node_runs (run_id, status);

CREATE TABLE node_attempts (
    id                  TEXT PRIMARY KEY,
    node_run_id         TEXT        NOT NULL REFERENCES node_runs (id),
    attempt_no          INTEGER     NOT NULL CHECK (attempt_no >= 1),
    status              TEXT        NOT NULL CHECK (status IN ('STARTED', 'DISPATCHED', 'SUCCEEDED', 'FAILED')),
    input               JSONB       NOT NULL,
    result              JSONB,
    -- Only the hash is persisted. The plaintext callback token leaves the process once,
    -- with the dispatch request, and never enters state, Event, Trace or logs.
    callback_token_hash TEXT,
    started_at          TIMESTAMPTZ NOT NULL,
    deadline_at         TIMESTAMPTZ,
    dispatched_at       TIMESTAMPTZ,
    completed_at        TIMESTAMPTZ,
    error               JSONB,
    UNIQUE (node_run_id, attempt_no)
);

-- Reconciler scans for abandoned STARTED/DISPATCHED Attempts past their absolute deadline.
CREATE INDEX node_attempts_status_deadline_idx ON node_attempts (status, deadline_at);

-- --------------------------------------------------------------------------
-- Event
-- --------------------------------------------------------------------------

-- Events are append-only. `(run_id, seq)` uniqueness is the database-level guarantee
-- that seq allocation under the Run aggregate lock cannot fork (invariant #4).
CREATE TABLE events (
    id          TEXT PRIMARY KEY,
    run_id      TEXT   NOT NULL REFERENCES runs (id),
    node_run_id TEXT REFERENCES node_runs (id),
    type        TEXT   NOT NULL CHECK (type IN (
        'RUN_CREATED', 'RUN_PAUSED', 'RUN_RESUMED', 'RUN_COMPLETED', 'RUN_FAILED',
        'NODE_READY', 'NODE_STARTED', 'NODE_RETRYING', 'NODE_DISPATCHED',
        'NODE_CALLBACK_RECEIVED', 'NODE_COMPLETED', 'NODE_FAILED',
        'AGENT_STARTED', 'AGENT_TURN_READY', 'AGENT_TURN_STARTED',
        'AGENT_DECISION_COMMITTED', 'AGENT_ACTION_STARTED', 'AGENT_ACTION_WAITING',
        'AGENT_ACTION_COMPLETED', 'AGENT_ACTION_FAILED', 'AGENT_STATE_UPDATED',
        'AGENT_COMPLETED', 'AGENT_FAILED'
    )),
    seq         BIGINT NOT NULL CHECK (seq >= 1),
    occurred_at TIMESTAMPTZ NOT NULL,
    payload     JSONB  NOT NULL,
    UNIQUE (run_id, seq)
);

-- --------------------------------------------------------------------------
-- Async recovery routing
-- --------------------------------------------------------------------------

-- Callback Binding is the only authoritative route from an external task identity back
-- to the originating Attempt. Handlers query it; they never infer a target from ID shape
-- or from Event payloads (invariant #5).
CREATE TABLE callback_bindings (
    id               TEXT PRIMARY KEY,
    provider_id      TEXT        NOT NULL,
    external_task_id TEXT        NOT NULL UNIQUE,
    target_type      TEXT        NOT NULL CHECK (target_type IN ('NODE_ATTEMPT', 'TOOL_ATTEMPT')),
    target_id        TEXT        NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL
);

CREATE INDEX callback_bindings_target_idx ON callback_bindings (target_type, target_id);

-- Holds authenticated callbacks that arrived before their binding committed.
CREATE TABLE pending_callbacks (
    external_task_id    TEXT PRIMARY KEY,
    payload             JSONB       NOT NULL,
    payload_hash        TEXT        NOT NULL,
    callback_token_hash TEXT        NOT NULL,
    received_at         TIMESTAMPTZ NOT NULL,
    expires_at          TIMESTAMPTZ NOT NULL,
    consumed_at         TIMESTAMPTZ,
    duplicate_count     INTEGER     NOT NULL DEFAULT 0 CHECK (duplicate_count >= 0)
);

-- Operational cleanup of expired, never-matched records.
CREATE INDEX pending_callbacks_expires_idx ON pending_callbacks (expires_at) WHERE consumed_at IS NULL;

-- --------------------------------------------------------------------------
-- Agent execution facts
-- --------------------------------------------------------------------------

-- One Agent NodeRun has at most one Agent Run. The configuration columns are frozen at
-- creation: recovery must not re-read current model parameters, Tool allowlist or Schemas.
CREATE TABLE agent_runs (
    id                      TEXT PRIMARY KEY,
    node_run_id             TEXT        NOT NULL UNIQUE REFERENCES node_runs (id),
    instructions            TEXT        NOT NULL,
    model_id                TEXT        NOT NULL,
    model_config            JSONB       NOT NULL,
    allowed_tools           JSONB       NOT NULL,
    context_schema          JSONB       NOT NULL,
    state_schema            JSONB       NOT NULL,
    output_schema           JSONB       NOT NULL,
    max_turns               INTEGER     NOT NULL CHECK (max_turns >= 1),
    current_turn_no         INTEGER     NOT NULL CHECK (current_turn_no >= 1),
    current_context_version INTEGER     NOT NULL CHECK (current_context_version >= 0),
    current_state_version   INTEGER     NOT NULL CHECK (current_state_version >= 0),
    started_at              TIMESTAMPTZ NOT NULL,
    deadline                TIMESTAMPTZ NOT NULL,
    terminated_at           TIMESTAMPTZ,
    termination             TEXT CHECK (termination IN (
        'FINAL_RESPONSE', 'MAX_TURNS', 'TIMEOUT', 'MODEL_ERROR', 'TOOL_ERROR', 'INVALID_ACTION'
    )),
    error                   JSONB
);

CREATE TABLE agent_turns (
    id           TEXT PRIMARY KEY,
    agent_run_id TEXT    NOT NULL REFERENCES agent_runs (id),
    turn_no      INTEGER NOT NULL CHECK (turn_no >= 1),
    status       TEXT    NOT NULL CHECK (status IN ('READY', 'RUNNING', 'COMPLETED', 'FAILED')),
    request      JSONB   NOT NULL,
    response     JSONB,
    token_usage  JSONB,
    started_at   TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    error        JSONB,
    UNIQUE (agent_run_id, turn_no)
);

-- Reconciler rediscovery of persisted READY Turns (invariant #7).
CREATE INDEX agent_turns_status_idx ON agent_turns (status);

-- A committed Decision is immutable: recovery never regenerates or overwrites it.
CREATE TABLE agent_decisions (
    id               TEXT PRIMARY KEY,
    turn_id          TEXT        NOT NULL UNIQUE REFERENCES agent_turns (id),
    kind             TEXT        NOT NULL CHECK (kind IN ('TOOL_CALL', 'FINAL')),
    tool_name        TEXT,
    arguments        JSONB,
    output           JSONB,
    state_patch      JSONB,
    response_summary JSONB,
    created_at       TIMESTAMPTZ NOT NULL,
    CHECK (
        (kind = 'TOOL_CALL' AND tool_name IS NOT NULL AND arguments IS NOT NULL AND output IS NULL)
        OR
        (kind = 'FINAL' AND tool_name IS NULL AND arguments IS NULL AND output IS NOT NULL)
    )
);

CREATE TABLE agent_actions (
    id           TEXT        PRIMARY KEY,
    turn_id      TEXT        NOT NULL UNIQUE REFERENCES agent_turns (id),
    decision_id  TEXT        NOT NULL UNIQUE REFERENCES agent_decisions (id),
    type         TEXT        NOT NULL CHECK (type IN ('TOOL_CALL', 'FINAL')),
    status       TEXT        NOT NULL CHECK (status IN ('READY', 'RUNNING', 'WAITING_CALLBACK', 'SUCCEEDED', 'FAILED')),
    created_at   TIMESTAMPTZ NOT NULL,
    started_at   TIMESTAMPTZ,
    waiting_at   TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    error        JSONB
);

-- Reconciler rediscovery of persisted READY Actions (invariant #7).
CREATE INDEX agent_actions_status_idx ON agent_actions (status);

CREATE TABLE tool_attempts (
    id                  TEXT        PRIMARY KEY,
    action_id           TEXT        NOT NULL REFERENCES agent_actions (id),
    attempt_no          INTEGER     NOT NULL CHECK (attempt_no >= 1),
    tool_name           TEXT        NOT NULL,
    status              TEXT        NOT NULL CHECK (status IN ('STARTED', 'DISPATCHED', 'SUCCEEDED', 'FAILED')),
    input               JSONB       NOT NULL,
    result              JSONB,
    callback_token_hash TEXT,
    started_at          TIMESTAMPTZ NOT NULL,
    dispatched_at       TIMESTAMPTZ,
    completed_at        TIMESTAMPTZ,
    error               JSONB,
    UNIQUE (action_id, attempt_no)
);

-- Immutable version chains. The Agent Run pointer columns decide where recovery resumes.
CREATE TABLE agent_context_versions (
    id             TEXT        PRIMARY KEY,
    agent_run_id   TEXT        NOT NULL REFERENCES agent_runs (id),
    version        INTEGER     NOT NULL CHECK (version >= 0),
    source_turn_id TEXT REFERENCES agent_turns (id),
    messages       JSONB       NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL,
    UNIQUE (agent_run_id, version)
);

CREATE TABLE agent_state_versions (
    id             TEXT        PRIMARY KEY,
    agent_run_id   TEXT        NOT NULL REFERENCES agent_runs (id),
    version        INTEGER     NOT NULL CHECK (version >= 0),
    source_turn_id TEXT REFERENCES agent_turns (id),
    value          JSONB       NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL,
    UNIQUE (agent_run_id, version)
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS agent_state_versions;
DROP TABLE IF EXISTS agent_context_versions;
DROP TABLE IF EXISTS tool_attempts;
DROP TABLE IF EXISTS agent_actions;
DROP TABLE IF EXISTS agent_decisions;
DROP TABLE IF EXISTS agent_turns;
DROP TABLE IF EXISTS agent_runs;
DROP TABLE IF EXISTS pending_callbacks;
DROP TABLE IF EXISTS callback_bindings;
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS node_attempts;
DROP TABLE IF EXISTS node_runs;
DROP TABLE IF EXISTS runs;
DROP TABLE IF EXISTS execution_artifacts;
DROP TABLE IF EXISTS assets;
DROP TABLE IF EXISTS workflow_definitions;
DROP TABLE IF EXISTS workflows;
-- +goose StatementEnd
