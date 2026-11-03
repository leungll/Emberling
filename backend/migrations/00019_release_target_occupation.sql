-- +goose Up
-- +goose StatementBegin

-- Release target occupation, human waits and the release confirmation facts of a Tool
-- Attempt.
--
-- release_occupations: one row per release Action that was approved onto a target. The
-- target_key is '<service>/<environment>'. At most one occupation of a target is ACTIVE at
-- any time; the partial unique index below is the only arbiter of that, so two approval
-- transactions racing for the same target are decided by INSERT ... ON CONFLICT DO NOTHING
-- and the loser sees zero affected rows. operation_key names the release operation that
-- currently holds the target and is updated when the Action re-releases after a void.
-- A RELEASED row records why the target was given back: the operation deployed, was
-- voided, was refused by production, or was never deployed after the retries ran out.
-- next_query_at, query_count and deployed_unchecked_version are created here for the
-- post-task reconciliation path, which does not write them yet.
CREATE TABLE release_occupations (
    id                         TEXT        PRIMARY KEY,
    target_key                 TEXT        NOT NULL,
    run_id                     TEXT        NOT NULL REFERENCES runs (id),
    action_id                  TEXT        NOT NULL REFERENCES agent_actions (id),
    operation_key              TEXT        NOT NULL,
    status                     TEXT        NOT NULL CHECK (status IN ('ACTIVE', 'RELEASED')),
    acquired_at                TIMESTAMPTZ NOT NULL,
    released_at                TIMESTAMPTZ,
    release_reason             TEXT        CHECK (release_reason IN ('DEPLOYED', 'VOIDED', 'REFUSED', 'NOT_DEPLOYED')),
    next_query_at              TIMESTAMPTZ,
    query_count                INTEGER     NOT NULL DEFAULT 0 CHECK (query_count >= 0),
    deployed_unchecked_version TEXT,
    CONSTRAINT release_occupations_released_columns CHECK (
        status <> 'RELEASED' OR (released_at IS NOT NULL AND release_reason IS NOT NULL)
    )
);

CREATE UNIQUE INDEX release_occupations_active_target_key
    ON release_occupations (target_key) WHERE status = 'ACTIVE';
CREATE INDEX release_occupations_action_idx ON release_occupations (action_id);

-- human_waits: one wait per release Action whose production confirmation did not arrive
-- before the confirm deadline. It is opened OPEN and closed once: RESOLVED when production
-- gives a definite answer, STOPPED when a person stops the release, EXPIRED when the human
-- wait deadline passes. The latter two are written by the human-stop path, not here.
CREATE TABLE human_waits (
    id        TEXT        PRIMARY KEY,
    action_id TEXT        NOT NULL UNIQUE REFERENCES agent_actions (id),
    run_id    TEXT        NOT NULL REFERENCES runs (id),
    status    TEXT        NOT NULL CHECK (status IN ('OPEN', 'STOPPED', 'EXPIRED', 'RESOLVED')),
    opened_at TIMESTAMPTZ NOT NULL,
    deadline  TIMESTAMPTZ NOT NULL,
    closed_at TIMESTAMPTZ,
    closed_by TEXT,
    outcome   TEXT,
    CONSTRAINT human_waits_closed_columns CHECK (
        (status = 'OPEN' AND closed_at IS NULL) OR (status <> 'OPEN' AND closed_at IS NOT NULL)
    )
);

-- confirm_deadline is when a release Attempt without a definite production answer hands
-- the Action over to a human wait; void_requested_at is when the Runtime first asked
-- production to void the operation. Both are written only for release Attempts.
ALTER TABLE tool_attempts
    ADD COLUMN confirm_deadline  TIMESTAMPTZ,
    ADD COLUMN void_requested_at TIMESTAMPTZ;

-- Confirm-deadline discovery scans only Attempts that still carry a deadline.
CREATE INDEX tool_attempts_confirm_deadline_idx ON tool_attempts (confirm_deadline)
    WHERE confirm_deadline IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS tool_attempts_confirm_deadline_idx;
ALTER TABLE tool_attempts
    DROP COLUMN IF EXISTS void_requested_at,
    DROP COLUMN IF EXISTS confirm_deadline;
DROP TABLE IF EXISTS human_waits;
DROP INDEX IF EXISTS release_occupations_action_idx;
DROP INDEX IF EXISTS release_occupations_active_target_key;
DROP TABLE IF EXISTS release_occupations;
-- +goose StatementEnd
