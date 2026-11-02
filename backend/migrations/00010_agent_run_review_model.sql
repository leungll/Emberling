-- +goose Up
-- +goose StatementBegin

-- The review Model ID an Agent node selected for its Tools that review with a vision
-- model, frozen into the Agent Run by the initialisation transaction and never updated.
-- Claims of such a Tool resolve this value against the current Model Registry; a value
-- that no longer resolves fails the Action explicitly and is never replaced by another
-- model, nor by the deterministic mock review policy.
--
-- NULL means the Agent node selected no review model, and its review Tools apply the
-- deterministic mock review policy. An Agent Run created before this column existed is
-- NULL as well, which is the policy it ran with.
ALTER TABLE agent_runs
    ADD COLUMN review_model_id TEXT;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agent_runs
    DROP COLUMN IF EXISTS review_model_id;
-- +goose StatementEnd
