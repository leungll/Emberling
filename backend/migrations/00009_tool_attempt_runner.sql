-- +goose Up
-- +goose StatementBegin

-- runner_id identifies the process instance that created a Tool Attempt: a random value
-- each Backend process generates once at startup. It is written once, by the INSERT that
-- creates the STARTED Attempt in the Action's claim transaction, and never updated. A
-- STARTED Attempt whose runner_id names another process has nobody executing its call.
-- Attempts created before this column existed keep NULL: which process wrote them is not
-- known, and it is never inferred.
--
-- uncertain records whether the external outcome of a Tool call that lost its process is
-- unknown. It is written once, in the transaction that moves such an Attempt to FAILED,
-- and every other failure path leaves it NULL. Only a FAILED Attempt may carry it.
ALTER TABLE tool_attempts
    ADD COLUMN runner_id TEXT,
    ADD COLUMN uncertain BOOLEAN,
    ADD CONSTRAINT tool_attempts_uncertain_failed_only CHECK (uncertain IS NULL OR status = 'FAILED');

-- The orphan scan reads only STARTED rows; the partial index keeps it independent of
-- the number of completed Attempts.
CREATE INDEX tool_attempts_started_runner_idx
    ON tool_attempts (started_at, id) WHERE status = 'STARTED';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS tool_attempts_started_runner_idx;
ALTER TABLE tool_attempts
    DROP CONSTRAINT IF EXISTS tool_attempts_uncertain_failed_only,
    DROP COLUMN IF EXISTS uncertain,
    DROP COLUMN IF EXISTS runner_id;
-- +goose StatementEnd
