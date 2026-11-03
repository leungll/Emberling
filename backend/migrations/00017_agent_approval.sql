-- +goose Up
-- +goose StatementBegin

-- Per-Action authorization, human approval and the pause that stops the Agent clock while
-- a person decides.
--
-- agent_runs.goal is the task goal the Agent Run was frozen to when it started, read once
-- from the node input and never re-read; NULL when the node configures no goal pointer. The
-- authorization decision of a production-risk Tool reads it.
--
-- paused_at and pause_reason record that the Agent Run is waiting on something outside its
-- own execution: a human decision (APPROVAL), a release the Runtime reconciles (RELEASE)
-- or a human wait (HUMAN). While paused, the deadline is not compared against the clock;
-- resuming moves the deadline later by the paused duration, so a human wait never consumes
-- the Agent's execution time. The two columns are written and cleared together.
ALTER TABLE agent_runs
    ADD COLUMN goal         TEXT CHECK (goal IN ('PREPARE_ONLY', 'FIX_AND_DEPLOY')),
    ADD COLUMN paused_at    TIMESTAMPTZ,
    ADD COLUMN pause_reason TEXT CHECK (pause_reason IN ('APPROVAL', 'RELEASE', 'HUMAN')),
    ADD CONSTRAINT agent_runs_pause_all_or_none CHECK (
        (paused_at IS NULL) = (pause_reason IS NULL)
    );

-- HUMAN_STOPPED is the termination a human stop writes; only the constraint is widened
-- here, no path writes it yet.
ALTER TABLE agent_runs DROP CONSTRAINT agent_runs_termination_check;
ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_termination_check CHECK (termination IN (
    'FINAL_RESPONSE', 'MAX_TURNS', 'TIMEOUT', 'MODEL_ERROR', 'TOOL_ERROR', 'INVALID_ACTION',
    'HUMAN_STOPPED'
));

-- An Action waiting on a person: WAITING_APPROVAL before its Tool runs, WAITING_HUMAN for a
-- human fact after a Tool reported. authorization_decision is the decision the claim made
-- for this Action (ALLOW, ASK or DENY) and authorization_reason explains a DENY. The column
-- is not named authorization because that word is reserved in SQL.
ALTER TABLE agent_actions DROP CONSTRAINT agent_actions_status_check;
ALTER TABLE agent_actions ADD CONSTRAINT agent_actions_status_check CHECK (status IN (
    'READY', 'RUNNING', 'WAITING_CALLBACK', 'WAITING_APPROVAL', 'WAITING_HUMAN', 'SUCCEEDED', 'FAILED'
));
ALTER TABLE agent_actions
    ADD COLUMN authorization_decision TEXT CHECK (authorization_decision IN ('ALLOW', 'ASK', 'DENY')),
    ADD COLUMN authorization_reason   TEXT;

CREATE INDEX agent_actions_waiting_human_idx ON agent_actions (status)
    WHERE status IN ('WAITING_APPROVAL', 'WAITING_HUMAN');

-- Approvals: one human decision per Action whose authorization was ASK. The row is created
-- PENDING in the claim transaction that suspended the Action and decided exactly once by a
-- conditional update from PENDING; approve, reject and expiry compete on that update, so
-- the database decides the single winner. UNIQUE (action_id) ties an approval to one Action
-- with its arguments already committed: an approval never covers another call.
CREATE TABLE approvals (
    id           TEXT        PRIMARY KEY,
    action_id    TEXT        NOT NULL UNIQUE REFERENCES agent_actions (id),
    run_id       TEXT        NOT NULL REFERENCES runs (id),
    agent_run_id TEXT        NOT NULL REFERENCES agent_runs (id),
    status       TEXT        NOT NULL CHECK (status IN ('PENDING', 'APPROVED', 'REJECTED', 'EXPIRED')),
    requested_at TIMESTAMPTZ NOT NULL,
    deadline     TIMESTAMPTZ NOT NULL,
    decided_at   TIMESTAMPTZ,
    decided_by   TEXT,
    reason       TEXT,
    CONSTRAINT approvals_decision_columns CHECK (
        (status = 'PENDING' AND decided_at IS NULL AND decided_by IS NULL AND reason IS NULL)
        OR (status <> 'PENDING' AND decided_at IS NOT NULL)
    )
);

-- Expiry discovery scans only undecided approvals.
CREATE INDEX approvals_pending_deadline_idx ON approvals (deadline) WHERE status = 'PENDING';

-- events.type enumerates every Event type inline, so the list is replaced once to admit
-- every Event the approval, pause, release and human-wait paths append. Later changes write
-- these types; the constraint is not widened again.
ALTER TABLE events DROP CONSTRAINT events_type_check;
ALTER TABLE events ADD CONSTRAINT events_type_check CHECK (type IN (
    'RUN_CREATED', 'RUN_PAUSED', 'RUN_RESUMED', 'RUN_COMPLETED', 'RUN_FAILED',
    'NODE_READY', 'NODE_STARTED', 'NODE_RETRYING', 'NODE_DISPATCHED',
    'NODE_CALLBACK_RECEIVED', 'NODE_COMPLETED', 'NODE_FAILED',
    'AGENT_STARTED', 'AGENT_TURN_READY', 'AGENT_TURN_STARTED',
    'AGENT_DECISION_COMMITTED', 'AGENT_ACTION_STARTED', 'AGENT_ACTION_WAITING',
    'AGENT_ACTION_COMPLETED', 'AGENT_ACTION_FAILED', 'AGENT_STATE_UPDATED',
    'AGENT_COMPLETED', 'AGENT_FAILED',
    'NODE_WAITING_CAPACITY',
    'AGENT_ACTION_WAITING_APPROVAL', 'APPROVAL_DECIDED', 'AGENT_PAUSED', 'AGENT_RESUMED',
    'RELEASE_OUTCOME_UNKNOWN', 'RELEASE_VOIDED', 'AGENT_ACTION_WAITING_HUMAN',
    'AGENT_COMPLETION_EVALUATED'
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
    'AGENT_COMPLETED', 'AGENT_FAILED',
    'NODE_WAITING_CAPACITY'
));
DROP INDEX IF EXISTS approvals_pending_deadline_idx;
DROP TABLE IF EXISTS approvals;
DROP INDEX IF EXISTS agent_actions_waiting_human_idx;
ALTER TABLE agent_actions
    DROP COLUMN IF EXISTS authorization_reason,
    DROP COLUMN IF EXISTS authorization_decision;
ALTER TABLE agent_actions DROP CONSTRAINT agent_actions_status_check;
ALTER TABLE agent_actions ADD CONSTRAINT agent_actions_status_check CHECK (status IN (
    'READY', 'RUNNING', 'WAITING_CALLBACK', 'SUCCEEDED', 'FAILED'
));
ALTER TABLE agent_runs DROP CONSTRAINT agent_runs_termination_check;
ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_termination_check CHECK (termination IN (
    'FINAL_RESPONSE', 'MAX_TURNS', 'TIMEOUT', 'MODEL_ERROR', 'TOOL_ERROR', 'INVALID_ACTION'
));
ALTER TABLE agent_runs
    DROP CONSTRAINT IF EXISTS agent_runs_pause_all_or_none,
    DROP COLUMN IF EXISTS pause_reason,
    DROP COLUMN IF EXISTS paused_at,
    DROP COLUMN IF EXISTS goal;
-- +goose StatementEnd
