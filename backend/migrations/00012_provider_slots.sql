-- +goose Up
-- +goose StatementBegin

-- Provider capacity: an asynchronous Provider runs at most a configured number of tasks
-- at once. Occupancy is a persisted fact so that a crash can neither leak a slot nor
-- hand out one too many; the in-process queue only wakes work up.

-- One row per governed Provider, seeded at startup and never deleted. Admission takes
-- this row FOR UPDATE, after the Run aggregate lock and never before it, so every count of
-- held slots sees every earlier admission that committed.
CREATE TABLE provider_slot_locks (
    provider_id TEXT PRIMARY KEY
);

-- One row per NodeRun that holds a slot. It is inserted in the transaction that claims
-- the NodeRun READY -> RUNNING and deleted in the transaction that moves the NodeRun to
-- SUCCEEDED or terminal FAILED; a retry keeps the row.
CREATE TABLE provider_slots (
    node_run_id TEXT PRIMARY KEY REFERENCES node_runs (id),
    provider_id TEXT        NOT NULL,
    acquired_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX provider_slots_provider_idx ON provider_slots (provider_id);

-- A READY NodeRun refused a slot records when it first queued and for which Provider.
-- Both are written together on the first refusal and never changed; a later admission
-- keeps them as the record that the NodeRun once waited.
ALTER TABLE node_runs
    ADD COLUMN capacity_deferred_at TIMESTAMPTZ,
    ADD COLUMN capacity_provider_id TEXT,
    ADD CONSTRAINT node_runs_capacity_deferral_all_or_none CHECK (
        (capacity_deferred_at IS NULL) = (capacity_provider_id IS NULL)
    );

-- Queued READY NodeRuns of one Provider in first-come order, for the first-come check and
-- the handoff after a slot is released.
CREATE INDEX node_runs_capacity_deferred_idx
    ON node_runs (capacity_provider_id, ready_at, id)
    WHERE status = 'READY' AND capacity_deferred_at IS NOT NULL;

-- events.type enumerates every Event type inline, so the list is replaced to admit
-- NODE_WAITING_CAPACITY, the Event a first refusal appends.
ALTER TABLE events DROP CONSTRAINT events_type_check;
ALTER TABLE events ADD CONSTRAINT events_type_check CHECK (type IN (
    'RUN_CREATED', 'RUN_PAUSED', 'RUN_RESUMED', 'RUN_COMPLETED', 'RUN_FAILED',
    'NODE_READY', 'NODE_STARTED', 'NODE_RETRYING', 'NODE_DISPATCHED',
    'NODE_CALLBACK_RECEIVED', 'NODE_COMPLETED', 'NODE_FAILED',
    'AGENT_STARTED', 'AGENT_TURN_READY', 'AGENT_TURN_STARTED',
    'AGENT_DECISION_COMMITTED', 'AGENT_ACTION_STARTED', 'AGENT_ACTION_WAITING',
    'AGENT_ACTION_COMPLETED', 'AGENT_ACTION_FAILED', 'AGENT_STATE_UPDATED',
    'AGENT_COMPLETED', 'AGENT_FAILED',
    'NODE_WAITING_CAPACITY'
));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE events DROP CONSTRAINT events_type_check;
ALTER TABLE events ADD CONSTRAINT events_type_check CHECK (type IN (
    'RUN_CREATED', 'RUN_PAUSED', 'RUN_RESUMED', 'RUN_COMPLETED', 'RUN_FAILED',
    'NODE_READY', 'NODE_STARTED', 'NODE_RETRYING', 'NODE_DISPATCHED',
    'NODE_CALLBACK_RECEIVED', 'NODE_COMPLETED', 'NODE_FAILED',
    'AGENT_STARTED', 'AGENT_TURN_READY', 'AGENT_TURN_STARTED',
    'AGENT_DECISION_COMMITTED', 'AGENT_ACTION_STARTED', 'AGENT_ACTION_WAITING',
    'AGENT_ACTION_COMPLETED', 'AGENT_ACTION_FAILED', 'AGENT_STATE_UPDATED',
    'AGENT_COMPLETED', 'AGENT_FAILED'
));
DROP INDEX IF EXISTS node_runs_capacity_deferred_idx;
ALTER TABLE node_runs
    DROP CONSTRAINT IF EXISTS node_runs_capacity_deferral_all_or_none,
    DROP COLUMN IF EXISTS capacity_provider_id,
    DROP COLUMN IF EXISTS capacity_deferred_at;
DROP TABLE IF EXISTS provider_slots;
DROP TABLE IF EXISTS provider_slot_locks;
-- +goose StatementEnd
