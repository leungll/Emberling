-- +goose Up
-- +goose StatementBegin

-- runner_id identifies the process instance that created a Node Attempt: a random value
-- each Backend process generates once at startup. It is written once, by the INSERT that
-- creates the STARTED Attempt in the claim transaction, and never updated. A STARTED
-- Attempt with no deadline whose runner_id names another process has nobody executing its
-- call and nothing else to end it.
ALTER TABLE node_attempts ADD COLUMN runner_id TEXT;

-- Rows from before this column would otherwise stay stuck for good: with no deadline and
-- no recorded process, neither the timeout scan nor the orphan scan could ever end them.
-- Runtime execution is a single process, and migrations finish before it starts work, so
-- the process that created such a row is gone by the time this runs. The value names no
-- real process, so every process sees these rows as another's. Rows with a deadline keep
-- NULL because the timeout scan ends them, and only STARTED rows are touched because a
-- DISPATCHED Attempt does not depend on a process.
UPDATE node_attempts
   SET runner_id = 'before-node-attempt-runner-identity'
 WHERE status = 'STARTED' AND deadline_at IS NULL;

-- Dispatch now writes a maximum wait onto an Attempt with no deadline. Attempts dispatched
-- before that have nothing to end them if their answer never comes, so they get the same
-- bound once, counted from now because their wait until now was never promised a limit. A
-- deadline already there is kept. STARTED rows stay without one, which is what the orphan
-- scan and the index below select on; these rows were never in that index.
UPDATE node_attempts
   SET deadline_at = now() + interval '24 hours'
 WHERE status = 'DISPATCHED' AND deadline_at IS NULL;

-- The orphan scan reads only STARTED rows with a recorded process and no deadline; the
-- partial index keeps it independent of the number of completed Attempts.
CREATE INDEX node_attempts_orphan_started_idx
    ON node_attempts (started_at, id)
 WHERE status = 'STARTED' AND runner_id IS NOT NULL AND deadline_at IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- A deadline the backfill wrote stays: it cannot be told from one dispatch wrote, and an
-- older build reads any deadline through the same timeout scan.
DROP INDEX IF EXISTS node_attempts_orphan_started_idx;
ALTER TABLE node_attempts DROP COLUMN IF EXISTS runner_id;
-- +goose StatementEnd
