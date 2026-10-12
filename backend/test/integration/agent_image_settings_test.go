//go:build integration

// Image settings tests: every image model shares one strict settings object, and the
// Runtime validates a generate_image Decision against it in the Action's claim
// transaction. Settings outside it fail the Action as INVALID_ACTION before any Tool
// Attempt exists, so no image model is ever asked to generate from them.
//
// They share the generation harness of agent_generation_limit_test.go.
package integration

import (
	"encoding/json"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
)

// TestAgentAction_SettingsViolateSchema_InvalidActionNoRequest drives a generate_image
// Decision whose settings break the shared schema - a member outside it, such as the
// retired settings-level mock control, a resolution other than 1K, a missing prompt. Each
// is rejected at claim: the Action fails with INVALID_ACTION and no Tool Attempt, the
// Executor is never called, the Mock Provider sees no generation request, and the Agent
// terminates as INVALID_ACTION.
func TestAgentAction_SettingsViolateSchema_InvalidActionNoRequest(t *testing.T) {
	for name, settings := range map[string]string{
		"member outside the schema": `{"prompt":"Warm film look","mock":"fail"}`,
		"resolution other than 1K":  `{"prompt":"Warm film look","resolution":"2K"}`,
		"missing prompt":            `{"aspectRatio":"1:1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := newGenerationProvider(t)
			h, executor := newGenerationHarness(t, p, nil)
			call := mockmodel.Scenario{ToolName: generateimage.ToolName, ToolArguments: json.RawMessage(
				`{"photoAssetId":"photo-a","settings":` + settings + `}`)}
			run := startGenerationRun(h, "wf-image-settings-invalid", nil, call)
			outcome := h.claimAgentNode(run.ID)
			h.execute(outcome)

			agentRunID := factAgentRunID(t, h, outcome)
			action := generationActionOfTurn(t, h, agentRunID, 1)
			if action.Status != domain.AgentActionFailed || action.Error == nil || action.Error.Code != "INVALID_ACTION" {
				t.Fatalf("action = %s error %+v, want FAILED with INVALID_ACTION", action.Status, action.Error)
			}
			if attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID); len(attempts) != 0 {
				t.Errorf("tool attempts = %+v, want none", attempts)
			}
			if got := executor.calls.Load(); got != 0 {
				t.Errorf("generate_image executor calls = %d, want 0", got)
			}
			if got := p.generations.Load(); got != 0 {
				t.Errorf("mock provider generations = %d, want 0", got)
			}
			assertAgentInvalidAction(t, h, run.ID, outcome)
		})
	}
}
