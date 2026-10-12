//go:build integration

package contract

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
)

// imageOnlyModel registers one image Model ID that declares only the reference image
// generation capability, the way a configured real image model does. It provides no
// Decision, so Generate always fails; saving a Definition never calls it.
type imageOnlyModel struct{ id string }

func (m imageOnlyModel) Models(context.Context) ([]registry.ModelRegistration, error) {
	return []registry.ModelRegistration{{ModelMetadata: domain.ModelMetadata{
		ID:           m.id,
		DisplayName:  m.id,
		Capabilities: []string{domain.ModelCapabilityReferenceImageGeneration},
		ConfigSchema: json.RawMessage(`{"type":"object"}`),
	}}}, nil
}

func (imageOnlyModel) Generate(context.Context, registry.ModelRequest) (registry.ModelResponse, error) {
	return registry.ModelResponse{}, errors.New("imageOnlyModel: an image model provides no Decision")
}

// imageAgentConfig is an Agent config whose allowlist holds the image-model Tool.
func imageAgentConfig(overrides map[string]any) map[string]any {
	cfg := baseAgentConfig(map[string]any{"allowedTools": []string{generateimage.ToolName, "lookup"}})
	for k, v := range overrides {
		cfg[k] = v
	}
	return cfg
}

type definitionErrorDetail struct {
	Code   string `json:"code"`
	Path   string `json:"path"`
	NodeID string `json:"nodeId"`
}

// definitionErrors decodes the structured validation errors of a rejected Save or Create.
func definitionErrors(t *testing.T, body []byte) []definitionErrorDetail {
	t.Helper()
	envelope := decodeBody[struct {
		Error struct {
			Details struct {
				Errors []definitionErrorDetail `json:"errors"`
			} `json:"details"`
		} `json:"error"`
	}](t, body)
	return envelope.Error.Details.Errors
}

// assertDefinitionRejected proves POST /api/definitions refused the request with HTTP 400
// and exactly one error of the wanted code at the wanted path of the Agent node.
func assertDefinitionRejected(t *testing.T, env *testEnv, request map[string]any, wantCode, wantPath string) {
	t.Helper()
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", request)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /api/definitions status = %d, want %d, body=%s", resp.StatusCode, http.StatusBadRequest, body)
	}
	if code := errorCode(t, body); code != wantCode {
		t.Errorf("error.code = %q, want %q", code, wantCode)
	}
	errs := definitionErrors(t, body)
	if len(errs) != 1 || errs[0].Code != wantCode || errs[0].Path != wantPath || errs[0].NodeID != "node_agent" {
		t.Errorf("error details = %+v, want one %s at %s", errs, wantCode, wantPath)
	}
	var workflows int
	if err := env.pool.QueryRow(context.Background(), "SELECT count(*) FROM workflows").Scan(&workflows); err != nil {
		t.Fatalf("count workflows: %v", err)
	}
	if workflows != 0 {
		t.Errorf("workflows after the rejected Create = %d, want 0", workflows)
	}
}

const imageModelPath = "nodes[node_agent].config.imageModelId"

// TestDefinitionSave_ImageToolWithoutImageModel_RejectedWithPath covers an Agent whose
// allowlist holds a Tool that runs with the node's image model, saved without selecting
// one: nothing is persisted and the error points at the node's imageModelId.
func TestDefinitionSave_ImageToolWithoutImageModel_RejectedWithPath(t *testing.T) {
	env := newTestEnv(t)
	assertDefinitionRejected(t, env, agentGraphRequest(imageAgentConfig(nil)), "SEMANTIC_VALIDATION_FAILED", imageModelPath)
}

// TestDefinitionSave_ImageModelLacksCapability_Rejected covers an imageModelId that is not
// a usable image model: an unregistered ID is MODEL_NOT_FOUND, and a registered model that
// does not declare reference image generation is refused rather than accepted as the
// closest match.
func TestDefinitionSave_ImageModelLacksCapability_Rejected(t *testing.T) {
	env := newTestEnv(t)
	t.Run("unregistered", func(t *testing.T) {
		assertDefinitionRejected(t, env, agentGraphRequest(imageAgentConfig(map[string]any{"imageModelId": "image-model-missing"})),
			"MODEL_NOT_FOUND", imageModelPath)
	})
	t.Run("decision model", func(t *testing.T) {
		assertDefinitionRejected(t, env, agentGraphRequest(imageAgentConfig(map[string]any{"imageModelId": mockmodel.ModelID})),
			"SEMANTIC_VALIDATION_FAILED", imageModelPath)
	})
}

// TestDefinitionSave_ImageModelWithoutImageTool_Rejected covers an imageModelId on an
// Agent whose allowlist holds no Tool that would use it: a setting with no effect is not
// saved.
func TestDefinitionSave_ImageModelWithoutImageTool_Rejected(t *testing.T) {
	env := newTestEnv(t)
	request := agentGraphRequest(baseAgentConfig(map[string]any{"imageModelId": mockmodel.ImageModelID}))
	assertDefinitionRejected(t, env, request, "SEMANTIC_VALIDATION_FAILED", imageModelPath)
}

// TestDefinitionSave_AgentModelLacksStructuredDecision_Rejected covers an Agent modelId
// that resolves but cannot return a structured Decision, here an image model.
func TestDefinitionSave_AgentModelLacksStructuredDecision_Rejected(t *testing.T) {
	const imageOnlyID = "image-only-1.0"
	env := newTestEnvWithOptions(t, testEnvOptions{
		ExtraModelProviders: []registry.ModelProvider{imageOnlyModel{id: imageOnlyID}},
	})
	for _, modelID := range []string{mockmodel.ImageModelID, imageOnlyID} {
		t.Run(modelID, func(t *testing.T) {
			request := agentGraphRequest(baseAgentConfig(map[string]any{"modelId": modelID}))
			assertDefinitionRejected(t, env, request, "SEMANTIC_VALIDATION_FAILED", "nodes[node_agent].config.modelId")
		})
	}
}

// TestDefinitionSave_ImageModelSelected_SavedVerbatim covers the accepted shape: the
// selected image Model ID is saved unchanged with the Definition version.
func TestDefinitionSave_ImageModelSelected_SavedVerbatim(t *testing.T) {
	const imageOnlyID = "image-only-1.0"
	env := newTestEnvWithOptions(t, testEnvOptions{
		ExtraModelProviders: []registry.ModelProvider{imageOnlyModel{id: imageOnlyID}},
	})
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions",
		agentGraphRequest(imageAgentConfig(map[string]any{"imageModelId": imageOnlyID})))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[struct {
		Nodes []struct {
			ID     string         `json:"id"`
			Config map[string]any `json:"config"`
		} `json:"nodes"`
	}](t, body)
	for _, node := range created.Nodes {
		if node.ID == "node_agent" && node.Config["imageModelId"] != imageOnlyID {
			t.Errorf("saved imageModelId = %v, want %q", node.Config["imageModelId"], imageOnlyID)
		}
	}
}

// TestRunCreate_ImageModelRemovedAfterSave_Rejected covers an image model removed from
// configuration after a Definition saved it: the restarted Backend refuses to create a
// Run of that version and persists nothing, instead of substituting another image model.
func TestRunCreate_ImageModelRemovedAfterSave_Rejected(t *testing.T) {
	const removedID = "image-removed-1.0"
	env1 := newTestEnvWithOptions(t, testEnvOptions{
		ExtraModelProviders: []registry.ModelProvider{imageOnlyModel{id: removedID}},
	})
	resp, body := env1.doJSON(t, http.MethodPost, "/api/definitions",
		agentGraphRequest(imageAgentConfig(map[string]any{"imageModelId": removedID})))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	workflowID, _ := decodeBody[map[string]any](t, body)["workflowId"].(string)
	pool := env1.pool
	env1.stop()

	env2 := newTestEnvWithOptions(t, testEnvOptions{Pool: pool})
	runResp, runBody := env2.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": 1,
		"input": map[string]any{"question": "what is the answer?"},
	})
	if runResp.StatusCode != http.StatusConflict {
		t.Fatalf("POST /api/runs with removed %s: status = %d, want %d, body=%s", removedID, runResp.StatusCode, http.StatusConflict, runBody)
	}
	var runs int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM runs WHERE workflow_id = $1", workflowID).Scan(&runs); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runs != 0 {
		t.Errorf("runs after the rejected create = %d, want 0", runs)
	}
}

// TestNodeTypes_AgentImageModel_SelectorFilteredByCapability covers the Agent Node Type
// offering imageModelId as a Model Selector filtered by reference image generation, so
// Studio lists only image models for it.
func TestNodeTypes_AgentImageModel_SelectorFilteredByCapability(t *testing.T) {
	env := newTestEnv(t)
	resp, body := env.doJSON(t, http.MethodGet, "/api/node-types", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/node-types status = %d, body=%s", resp.StatusCode, body)
	}
	agentType := catalogItemsByKey(t, body, "type")["agent"]
	uiSchema := decodeBody[domain.NodeUISchema](t, agentType["uiSchema"])
	found := false
	for _, field := range uiSchema.Fields {
		if field.Path == "imageModelId" {
			found = true
			if field.Widget != domain.UIWidgetModelSelector || field.Capability != domain.ModelCapabilityReferenceImageGeneration {
				t.Errorf("agent imageModelId field = %+v, want a MODEL_SELECTOR filtered by reference_image_generation", field)
			}
		}
	}
	if !found {
		t.Errorf("agent uiSchema has no imageModelId field: %s", agentType["uiSchema"])
	}
}
