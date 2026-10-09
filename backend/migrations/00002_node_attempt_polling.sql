-- +goose Up
-- +goose StatementBegin

-- Provider polling scheduling facts for Node Attempts.
--
-- next_poll_at is when a DISPATCHED Attempt may next be polled; NULL means no poll is
-- scheduled, either because the registration declares no poll policy or because the
-- poll bound has been reached. poll_count is the number of polls claimed so far and
-- bounds polling against the registered maximum. Both are scheduling facts, not
-- business state: claiming a poll writes no Event and never changes deadline_at.
-- Persisted due polls are recoverable work; an in-process timer is only a latency
-- optimisation.
ALTER TABLE node_attempts
    ADD COLUMN next_poll_at TIMESTAMPTZ,
    ADD COLUMN poll_count   INTEGER NOT NULL DEFAULT 0 CHECK (poll_count >= 0);

-- Discovery of due polls scans only DISPATCHED Attempts that still have a poll scheduled.
CREATE INDEX node_attempts_due_poll_idx ON node_attempts (next_poll_at)
    WHERE status = 'DISPATCHED' AND next_poll_at IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS node_attempts_due_poll_idx;
ALTER TABLE node_attempts
    DROP COLUMN IF EXISTS poll_count,
    DROP COLUMN IF EXISTS next_poll_at;
-- +goose StatementEnd
