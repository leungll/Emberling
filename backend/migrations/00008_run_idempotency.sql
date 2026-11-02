-- +goose Up
-- +goose StatementBegin

-- A client-supplied submission key and the digest of the request it first accompanied.
-- Both are written once, in the transaction that creates the Run, and never updated: a
-- later submission with the same key in the same Workflow is answered with this Run when
-- its digest matches and rejected when it does not. Runs created without a key carry
-- neither value.
ALTER TABLE runs
    ADD COLUMN idempotency_key TEXT,
    ADD COLUMN idempotency_digest TEXT,
    ADD CONSTRAINT runs_idempotency_key_digest_paired
        CHECK ((idempotency_key IS NULL) = (idempotency_digest IS NULL));

-- The key space is per Workflow. Concurrent submissions with one key are decided here:
-- exactly one creation transaction commits, and every other one fails on this index and
-- rolls back whole, including the Events it appended.
CREATE UNIQUE INDEX runs_workflow_idempotency_key_uq
    ON runs (workflow_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS runs_workflow_idempotency_key_uq;
ALTER TABLE runs
    DROP CONSTRAINT IF EXISTS runs_idempotency_key_digest_paired,
    DROP COLUMN IF EXISTS idempotency_digest,
    DROP COLUMN IF EXISTS idempotency_key;
-- +goose StatementEnd
