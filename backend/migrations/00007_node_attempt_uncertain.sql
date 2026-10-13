-- +goose Up
-- +goose StatementBegin

-- Whether a failed Node Attempt's external outcome was uncertain when it failed: true when
-- the call reached its deadline or the Adapter reported the result as uncertain, false for
-- a failure known not to have taken effect. The value is written once, in the transaction
-- that moves the Attempt to FAILED and appends its NODE_RETRYING or NODE_FAILED Event, and
-- is never updated. It is a record for Trace and audit; the retry decision is taken from
-- the same determination inside that transaction and never reads this column back.
--
-- NULL on every Attempt that has not failed, and on Attempts that failed before this
-- column existed: an unrecorded determination is never inferred.
ALTER TABLE node_attempts
    ADD COLUMN uncertain BOOLEAN,
    ADD CONSTRAINT node_attempts_uncertain_failed_only CHECK (uncertain IS NULL OR status = 'FAILED');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE node_attempts
    DROP CONSTRAINT IF EXISTS node_attempts_uncertain_failed_only,
    DROP COLUMN IF EXISTS uncertain;
-- +goose StatementEnd
