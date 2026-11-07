package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
)

// Review model selection rules: a Tool that gives an independent second opinion makes the
// review model mandatory and distinct from the node's decision model, and every review Tool
// of the allowlist decides, through its registered metadata, which capability the selected
// review model must declare. The rules read registered metadata only, never a Tool Name.

const (
	selectionDecisionModel = "decision-model.1"
	selectionTextReviewer  = "text-reviewer.1"
	selectionVisionModel   = "vision-reviewer.1"
	selectionTextTool      = "text_review_tool"
	selectionVisionTool    = "vision_review_tool"
)

// selectionModels serves the registered models of these tests with their capabilities.
type selectionModels struct {
	registrations []registry.ModelRegistration
}

func (m selectionModels) Models(context.Context) ([]registry.ModelRegistration, error) {
	return m.registrations, nil
}

func (selectionModels) Generate(context.Context, registry.ModelRequest) (registry.ModelResponse, error) {
	return registry.ModelResponse{}, errors.New("selection tests ask no model")
}

type selectionNoopTool struct{}

func (selectionNoopTool) Execute(context.Context, registry.ToolAction) (registry.ToolExecutionResult, error) {
	return registry.ToolExecutionResult{Kind: registry.ToolResultCompleted, Result: &registry.ToolResult{Output: json.RawMessage(`{}`)}}, nil
}

func selectionModel(id string, capabilities ...string) registry.ModelRegistration {
	return registry.ModelRegistration{ModelMetadata: domain.ModelMetadata{
		ID: id, DisplayName: id, Capabilities: capabilities, ConfigSchema: json.RawMessage(`{"type":"object"}`),
	}}
}

func selectionRegistries(t *testing.T) (*registry.ModelRegistry, *registry.ToolRegistry) {
	t.Helper()
	models := registry.NewModelRegistry()
	if err := models.Register(context.Background(), selectionModels{registrations: []registry.ModelRegistration{
		selectionModel(selectionDecisionModel, domain.ModelCapabilityTextGeneration, domain.ModelCapabilityStructuredDecision),
		selectionModel(selectionTextReviewer, domain.ModelCapabilityTextGeneration, domain.ModelCapabilityStructuredDecision),
		selectionModel(selectionVisionModel, domain.ModelCapabilityVisionInput),
	}}); err != nil {
		t.Fatalf("register models: %v", err)
	}
	tools := registry.NewToolRegistry()
	reviewTool := func(name, capability string, independent bool) registry.ToolRegistration {
		return registry.ToolRegistration{
			Metadata: domain.ToolMetadata{
				Name:                  name,
				Description:           "Review with the node's review model",
				InputSchema:           json.RawMessage(`{"type":"object"}`),
				OutputSchema:          json.RawMessage(`{"type":"object"}`),
				SideEffect:            domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
				ExecutionKind:         domain.ToolExecutionSync,
				UsesReviewModel:       true,
				ReviewModelCapability: capability,
				RequiresReviewModel:   independent,
			},
			Executor: selectionNoopTool{},
		}
	}
	for _, reg := range []registry.ToolRegistration{
		reviewTool(selectionTextTool, domain.ModelCapabilityStructuredDecision, true),
		reviewTool(selectionVisionTool, domain.ModelCapabilityVisionInput, false),
	} {
		if err := tools.Register(reg); err != nil {
			t.Fatalf("register tool %s: %v", reg.Metadata.Name, err)
		}
	}
	return models, tools
}

func reviewSelectionError(t *testing.T, errs []runtime.ValidationError) (runtime.ValidationError, bool) {
	t.Helper()
	for _, err := range errs {
		if err.Path == "nodes[node_agent].config.reviewModelId" {
			return err, true
		}
	}
	return runtime.ValidationError{}, false
}

func TestAgentModelSelectionErrors_IndependentReviewToolWithoutReviewModel_Rejected(t *testing.T) {
	models, tools := selectionRegistries(t)
	errs := agentModelSelectionErrors(models, tools, "node_agent", runtime.AgentNodeConfig{
		ModelID: selectionDecisionModel, AllowedTools: []string{selectionTextTool},
	})
	got, found := reviewSelectionError(t, errs)
	if !found || got.Code != runtime.CodeSemanticValidationFailed || !strings.Contains(got.Message, selectionTextTool) {
		t.Fatalf("errors = %+v, want SEMANTIC_VALIDATION_FAILED at reviewModelId naming %s", errs, selectionTextTool)
	}
}

func TestAgentModelSelectionErrors_ReviewModelSameAsDecisionModel_Rejected(t *testing.T) {
	models, tools := selectionRegistries(t)
	errs := agentModelSelectionErrors(models, tools, "node_agent", runtime.AgentNodeConfig{
		ModelID: selectionDecisionModel, ReviewModelID: selectionDecisionModel, AllowedTools: []string{selectionTextTool},
	})
	got, found := reviewSelectionError(t, errs)
	if !found || got.Code != runtime.CodeSemanticValidationFailed || !strings.Contains(got.Message, "modelId") {
		t.Fatalf("errors = %+v, want SEMANTIC_VALIDATION_FAILED at reviewModelId requiring a model other than modelId", errs)
	}
}

func TestAgentModelSelectionErrors_ReviewModelLacksToolCapability_Rejected(t *testing.T) {
	models, tools := selectionRegistries(t)
	cases := map[string]struct {
		tool, model, missing string
	}{
		"text review with a vision-only model":  {selectionTextTool, selectionVisionModel, domain.ModelCapabilityStructuredDecision},
		"vision review with a text-only model":  {selectionVisionTool, selectionTextReviewer, domain.ModelCapabilityVisionInput},
		"vision review with an unregistered id": {selectionVisionTool, "vision-missing.1", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			errs := agentModelSelectionErrors(models, tools, "node_agent", runtime.AgentNodeConfig{
				ModelID: selectionDecisionModel, ReviewModelID: tc.model, AllowedTools: []string{tc.tool},
			})
			got, found := reviewSelectionError(t, errs)
			if !found {
				t.Fatalf("errors = %+v, want one at reviewModelId", errs)
			}
			if tc.missing == "" {
				if got.Code != runtime.CodeModelNotFound {
					t.Errorf("code = %s, want %s", got.Code, runtime.CodeModelNotFound)
				}
				return
			}
			if got.Code != runtime.CodeSemanticValidationFailed || !strings.Contains(got.Message, tc.missing) {
				t.Errorf("error = %+v, want SEMANTIC_VALIDATION_FAILED naming %s", got, tc.missing)
			}
		})
	}
}

func TestAgentModelSelectionErrors_IndependentReviewModelDistinct_Accepted(t *testing.T) {
	models, tools := selectionRegistries(t)
	for name, cfg := range map[string]runtime.AgentNodeConfig{
		"text review with a distinct text model": {
			ModelID: selectionDecisionModel, ReviewModelID: selectionTextReviewer, AllowedTools: []string{selectionTextTool},
		},
		"vision review without a review model falls back to the mock policy": {
			ModelID: selectionDecisionModel, AllowedTools: []string{selectionVisionTool},
		},
		"both review tools with a model declaring both capabilities": {
			ModelID: selectionDecisionModel, ReviewModelID: "dual-reviewer.1", AllowedTools: []string{selectionTextTool, selectionVisionTool},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if cfg.ReviewModelID == "dual-reviewer.1" {
				if err := models.Register(context.Background(), selectionModels{registrations: []registry.ModelRegistration{
					selectionModel("dual-reviewer.1", domain.ModelCapabilityStructuredDecision, domain.ModelCapabilityVisionInput),
				}}); err != nil {
					t.Fatalf("register dual reviewer: %v", err)
				}
			}
			if errs := agentModelSelectionErrors(models, tools, "node_agent", cfg); len(errs) != 0 {
				t.Fatalf("errors = %+v, want none", errs)
			}
		})
	}
}

func TestUnusableReviewModel_ChecksEveryCapabilityTheAllowlistNeeds(t *testing.T) {
	models, tools := selectionRegistries(t)
	capabilities := reviewModelCapabilities(tools, []string{selectionTextTool, selectionVisionTool})
	if len(capabilities) != 2 {
		t.Fatalf("capabilities = %v, want the two the allowlist needs", capabilities)
	}
	if unusable := unusableReviewModel(models, selectionTextReviewer, capabilities); unusable == nil || !strings.Contains(unusable.Message, domain.ModelCapabilityVisionInput) {
		t.Errorf("text reviewer for both tools = %+v, want it reported as lacking %s", unusable, domain.ModelCapabilityVisionInput)
	}
	if unusable := unusableReviewModel(models, selectionTextReviewer, reviewModelCapabilities(tools, []string{selectionTextTool})); unusable != nil {
		t.Errorf("text reviewer for the text tool = %+v, want usable", unusable)
	}
	if unusable := unusableReviewModel(models, "", capabilities); unusable == nil {
		t.Error("no frozen review model, want it reported as unusable")
	}
}
