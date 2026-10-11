//go:build integration

package contract

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/openaimodel"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// configuredModel builds one OpenAI-compatible Provider registered as id, the way the
// process registers one Provider per configured model. Its endpoint fails the test if it
// is ever called: saving a Definition and creating a Run never reach a model.
func configuredModel(t *testing.T, id string) registry.ModelProvider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("model %s was called; no model call is expected", id)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	provider, err := openaimodel.New(openaimodel.Config{
		ModelID:        id,
		DisplayName:    id,
		BaseURL:        server.URL,
		APIKey:         "sk-contract-model",
		Model:          "vendor/" + id,
		HTTPClient:     server.Client(),
		DecisionSchema: registry.DecisionSchema(),
	})
	if err != nil {
		t.Fatalf("openaimodel.New(%s): %v", id, err)
	}
	return provider
}

// TestDefinitionSave_RemovedModelID_RejectedWithPath covers a Model ID removed from
// configuration after a Definition saved it. The restarted Backend still opens the saved
// version, but saving it again creates no new version and reports the Agent node's
// modelId path, and a Run of the saved version is refused before anything is persisted.
// No other registered model is substituted.
func TestDefinitionSave_RemovedModelID_RejectedWithPath(t *testing.T) {
	const removedID = "removed-1.0"
	env1 := newTestEnvWithOptions(t, testEnvOptions{
		ExtraModelProviders: []registry.ModelProvider{configuredModel(t, removedID), configuredModel(t, "kept-1.0")},
	})
	request := agentGraphRequest(baseAgentConfig(map[string]any{"modelId": removedID}))
	resp, body := env1.doJSON(t, http.MethodPost, "/api/definitions", request)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions with %s registered: status = %d, want %d, body=%s", removedID, resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ := created["workflowId"].(string)
	if workflowID == "" {
		t.Fatalf("POST /api/definitions missing workflowId: %s", body)
	}
	pool := env1.pool
	env1.stop()

	env2 := newTestEnvWithOptions(t, testEnvOptions{
		Pool:                pool,
		ExtraModelProviders: []registry.ModelProvider{configuredModel(t, "kept-1.0")},
	})

	if resp, body := env2.doJSON(t, http.MethodGet, "/api/definitions/"+workflowID+"/versions/1", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("GET saved version after the model was removed: status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}

	request["baseVersion"] = 1
	resp, body = env2.doJSON(t, http.MethodPut, "/api/definitions/"+workflowID, request)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT /api/definitions with removed %s: status = %d, want %d, body=%s", removedID, resp.StatusCode, http.StatusBadRequest, body)
	}
	if code := errorCode(t, body); code != "MODEL_NOT_FOUND" {
		t.Errorf("PUT error.code = %q, want MODEL_NOT_FOUND", code)
	}
	envelope := decodeBody[struct {
		Error struct {
			Details struct {
				Errors []struct {
					Code   string `json:"code"`
					Path   string `json:"path"`
					NodeID string `json:"nodeId"`
				} `json:"errors"`
			} `json:"details"`
		} `json:"error"`
	}](t, body)
	errs := envelope.Error.Details.Errors
	if len(errs) != 1 || errs[0].Code != "MODEL_NOT_FOUND" || errs[0].Path != "nodes[node_agent].config.modelId" || errs[0].NodeID != "node_agent" {
		t.Errorf("PUT error details = %+v, want one MODEL_NOT_FOUND at nodes[node_agent].config.modelId", errs)
	}

	var versions int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM workflow_definitions WHERE workflow_id = $1", workflowID).Scan(&versions); err != nil {
		t.Fatalf("count definition versions: %v", err)
	}
	if versions != 1 {
		t.Errorf("definition versions after the rejected Save = %d, want 1", versions)
	}

	runResp, runBody := env2.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": 1,
		"input": map[string]any{"question": "what is the answer?"},
	})
	if runResp.StatusCode != http.StatusConflict {
		t.Fatalf("POST /api/runs with removed %s: status = %d, want %d, body=%s", removedID, runResp.StatusCode, http.StatusConflict, runBody)
	}
	if code := errorCode(t, runBody); code != "RUNTIME_BINDING_UNAVAILABLE" {
		t.Errorf("POST /api/runs error.code = %q, want RUNTIME_BINDING_UNAVAILABLE", code)
	}
	var runs int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM runs WHERE workflow_id = $1", workflowID).Scan(&runs); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runs != 0 {
		t.Errorf("runs persisted after the rejected CreateRun = %d, want 0", runs)
	}
}
