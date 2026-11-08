-- +goose Up
-- +goose StatementBegin

-- Clip delivery joins the goal-based completion check. An Agent Run may freeze the
-- DELIVER_CLIP goal; such a task ends as CLIP_DELIVERED when the committed facts show the
-- declared clip was rendered and passed its checks (and its review, when the Agent could
-- review), as CANNOT_SATISFY when the Agent declared with a reason that the request cannot
-- be met, or as INCOMPLETE. The three CHECK constraints are replaced; the rows written
-- under the earlier sets are unchanged.
ALTER TABLE agent_runs DROP CONSTRAINT agent_runs_goal_check;
ALTER TABLE agent_runs
    ADD CONSTRAINT agent_runs_goal_check
    CHECK (goal IN ('PREPARE_ONLY', 'FIX_AND_DEPLOY', 'DELIVER_CLIP'));

ALTER TABLE agent_completions DROP CONSTRAINT agent_completions_goal_check;
ALTER TABLE agent_completions
    ADD CONSTRAINT agent_completions_goal_check
    CHECK (goal IN ('PREPARE_ONLY', 'FIX_AND_DEPLOY', 'DELIVER_CLIP'));

ALTER TABLE agent_completions DROP CONSTRAINT agent_completions_outcome_check;
ALTER TABLE agent_completions
    ADD CONSTRAINT agent_completions_outcome_check
    CHECK (outcome IN (
        'READY_FOR_APPROVAL', 'DEPLOYED', 'DEPLOYED_CHECK_FAILED', 'RELEASE_UNKNOWN', 'INCOMPLETE',
        'CLIP_DELIVERED', 'CANNOT_SATISFY'
    ));

-- The optional revision limits frozen from the Agent configuration when the Agent Run is
-- created: an object from fact type to the number of failed verdicts of that type the
-- Agent may collect before further counted calls are refused. NULL means no limit. The
-- failures already collected are not stored: they are counted under the Run lock from the
-- committed facts, so there is no second source of truth to drift.
ALTER TABLE agent_runs
    ADD COLUMN max_failed_facts JSONB CHECK (max_failed_facts IS NULL OR jsonb_typeof(max_failed_facts) = 'object');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agent_runs DROP COLUMN IF EXISTS max_failed_facts;
ALTER TABLE agent_completions DROP CONSTRAINT agent_completions_outcome_check;
ALTER TABLE agent_completions
    ADD CONSTRAINT agent_completions_outcome_check
    CHECK (outcome IN ('READY_FOR_APPROVAL', 'DEPLOYED', 'DEPLOYED_CHECK_FAILED', 'RELEASE_UNKNOWN', 'INCOMPLETE'));
ALTER TABLE agent_completions DROP CONSTRAINT agent_completions_goal_check;
ALTER TABLE agent_completions
    ADD CONSTRAINT agent_completions_goal_check
    CHECK (goal IN ('PREPARE_ONLY', 'FIX_AND_DEPLOY'));
ALTER TABLE agent_runs DROP CONSTRAINT agent_runs_goal_check;
ALTER TABLE agent_runs
    ADD CONSTRAINT agent_runs_goal_check
    CHECK (goal IN ('PREPARE_ONLY', 'FIX_AND_DEPLOY'));
-- +goose StatementEnd
