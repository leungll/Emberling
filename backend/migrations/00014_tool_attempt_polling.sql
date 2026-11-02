-- +goose Up
-- +goose StatementBegin

-- Provider polling scheduling facts for Tool Attempts, mirroring the Node Attempt ones.
--
-- next_poll_at is when a DISPATCHED Attempt of an asynchronous Tool that declares a poll
-- policy may next be queried; NULL means no poll is scheduled, either because the
-- registration declares no policy or because the poll bound has been reached. poll_count is
-- the number of polls claimed so far and bounds polling against the registered maximum.
-- Both are scheduling facts, not business state: claiming a poll writes no Event. Persisted
-- due polls are recoverable work; an in-process timer is only a latency optimisation.
--
-- operation_key is the Provider-side identity of an external operation whose outcome the
-- Runtime may have to query after the fact; it is unique so one operation can never be
-- attributed to two Attempts. outcome_unknown_since records when the Runtime stopped
-- knowing whether the operation took effect. Both are added here with their Store mapping
-- and are written only by the release Tool's dispatch path.
ALTER TABLE tool_attempts
    ADD COLUMN next_poll_at          TIMESTAMPTZ,
    ADD COLUMN poll_count            INTEGER NOT NULL DEFAULT 0 CHECK (poll_count >= 0),
    ADD COLUMN operation_key         TEXT UNIQUE,
    ADD COLUMN outcome_unknown_since TIMESTAMPTZ;

-- Discovery of due polls scans only Attempts that still have a poll scheduled.
CREATE INDEX tool_attempts_due_poll_idx ON tool_attempts (next_poll_at)
    WHERE next_poll_at IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS tool_attempts_due_poll_idx;
ALTER TABLE tool_attempts
    DROP COLUMN IF EXISTS outcome_unknown_since,
    DROP COLUMN IF EXISTS operation_key,
    DROP COLUMN IF EXISTS poll_count,
    DROP COLUMN IF EXISTS next_poll_at;
-- +goose StatementEnd
