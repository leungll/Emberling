-- +goose Up
-- +goose StatementBegin

-- The charge a Provider reported for one external call, recorded once beside the call's
-- result in the transaction that commits that result, and never updated afterwards.
-- cost_amount is the decimal amount as reported, cost_currency the ISO 4217 code the
-- Provider bills in, and cost_source the stable Model ID of the call, so the figure traces
-- back to the Provider and price list that produced it.
--
-- All three are NULL together or set together. NULL means the charge is not known: the
-- Provider reported none, the call failed or its outcome is uncertain, or the row predates
-- these columns. A reported zero is stored as 0, a known charge, and is never confused
-- with NULL. Nothing here is ever estimated from a price list.
ALTER TABLE node_attempts
    ADD COLUMN cost_amount NUMERIC(14,6),
    ADD COLUMN cost_currency TEXT,
    ADD COLUMN cost_source TEXT,
    ADD CONSTRAINT node_attempts_cost_all_or_none CHECK (
        (cost_amount IS NULL AND cost_currency IS NULL AND cost_source IS NULL)
        OR (cost_amount IS NOT NULL AND cost_currency IS NOT NULL AND cost_source IS NOT NULL)
    );

ALTER TABLE tool_attempts
    ADD COLUMN cost_amount NUMERIC(14,6),
    ADD COLUMN cost_currency TEXT,
    ADD COLUMN cost_source TEXT,
    ADD CONSTRAINT tool_attempts_cost_all_or_none CHECK (
        (cost_amount IS NULL AND cost_currency IS NULL AND cost_source IS NULL)
        OR (cost_amount IS NOT NULL AND cost_currency IS NOT NULL AND cost_source IS NOT NULL)
    );

ALTER TABLE agent_turns
    ADD COLUMN cost_amount NUMERIC(14,6),
    ADD COLUMN cost_currency TEXT,
    ADD COLUMN cost_source TEXT,
    ADD CONSTRAINT agent_turns_cost_all_or_none CHECK (
        (cost_amount IS NULL AND cost_currency IS NULL AND cost_source IS NULL)
        OR (cost_amount IS NOT NULL AND cost_currency IS NOT NULL AND cost_source IS NOT NULL)
    );
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agent_turns
    DROP CONSTRAINT IF EXISTS agent_turns_cost_all_or_none,
    DROP COLUMN IF EXISTS cost_source,
    DROP COLUMN IF EXISTS cost_currency,
    DROP COLUMN IF EXISTS cost_amount;
ALTER TABLE tool_attempts
    DROP CONSTRAINT IF EXISTS tool_attempts_cost_all_or_none,
    DROP COLUMN IF EXISTS cost_source,
    DROP COLUMN IF EXISTS cost_currency,
    DROP COLUMN IF EXISTS cost_amount;
ALTER TABLE node_attempts
    DROP CONSTRAINT IF EXISTS node_attempts_cost_all_or_none,
    DROP COLUMN IF EXISTS cost_source,
    DROP COLUMN IF EXISTS cost_currency,
    DROP COLUMN IF EXISTS cost_amount;
-- +goose StatementEnd
