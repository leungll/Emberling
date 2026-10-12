-- +goose Up
-- +goose StatementBegin

-- The image Model ID an Agent node selected for its Tools that run with an image model,
-- frozen into the Agent Run by the initialisation transaction and never updated. Claims of
-- such a Tool resolve this value against the current Model Registry; a value that no
-- longer resolves fails the Action explicitly and is never replaced by another model.
--
-- NULL means the Agent node selected no image model. An Agent Run created before this
-- column existed is NULL as well; if its allowlist holds a Tool that uses an image model,
-- the claim of that Tool fails explicitly instead of guessing a model.
ALTER TABLE agent_runs
    ADD COLUMN image_model_id TEXT;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agent_runs
    DROP COLUMN IF EXISTS image_model_id;
-- +goose StatementEnd
