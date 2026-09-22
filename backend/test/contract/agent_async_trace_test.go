//go:build integration

package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
	"github.com/leungll/Emberling/backend/internal/tools/remotelookup"
)

// TestAPI_AgentTrace_DispatchedToolAttempt_ShowsSafeBindingNoTokenHash drives an Agent
// through the real Mock Provider with an ASYNC Tool whose callback is lost, so the Agent
// NodeRun stays WAITING_CALLBACK. The Agent Trace must show the DISPATCHED Tool Attempt
// with its Callback Binding summarised as provider and external task identity only: the
// callback token and its persisted hash never leave the Backend (08 §3.4).
func TestAPI_AgentTrace_DispatchedToolAttempt_ShowsSafeBindingNoTokenHash(t *testing.T) {
	fixture := newProviderFixture(t)
	env := fixture.startBackend(t, nil)
	env.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		return &mockmodel.Scenario{
			Kind:          mockmodel.ScenarioToolCall,
			ToolName:      remotelookup.ToolName,
			ToolArguments: json.RawMessage(`{"key":"mock:lost"}`),
		}
	}

	fx := loadAgentLookupFixture(t)
	for i, node := range fx.Nodes {
		if node.ID == "node_agent" {
			fx.Nodes[i].Config = json.RawMessage(strings.Replace(string(node.Config),
				`["`+lookup.ToolName+`"]`, `["`+remotelookup.ToolName+`"]`, 1))
		}
	}
	resp, raw := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, raw)
	}
	created := decodeBody[map[string]any](t, raw)
	workflowID, _ := created["workflowId"].(string)
	version, _ := created["version"].(float64)
	runID := createAgentRun(t, env, workflowID, int(version), "what is the answer?")

	agentNodeRun := env.waitForNodeRunStatus(t, runID, "node_agent", "WAITING_CALLBACK", agentTraceWait)
	if status := env.runStatus(t, runID); status != "PAUSED" {
		t.Errorf("run status = %s, want PAUSED while its only running NodeRun waits for a callback", status)
	}

	agentNodeRunID, _ := agentNodeRun["id"].(string)
	status, body := getAgentTrace(t, env, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, want %d, body=%s", status, http.StatusOK, body)
	}
	var trace struct {
		Turns []struct {
			Action *struct {
				Status string `json:"status"`
			} `json:"action"`
			ToolAttempts []struct {
				ToolName        string  `json:"toolName"`
				Status          string  `json:"status"`
				DispatchedAt    *string `json:"dispatchedAt"`
				CallbackBinding *struct {
					ID             string `json:"id"`
					ProviderID     string `json:"providerId"`
					ExternalTaskID string `json:"externalTaskId"`
				} `json:"callbackBinding"`
			} `json:"toolAttempts"`
		} `json:"turns"`
	}
	if err := json.Unmarshal(body, &trace); err != nil {
		t.Fatalf("decode agent trace: %v body=%s", err, body)
	}
	if len(trace.Turns) != 1 || trace.Turns[0].Action == nil || len(trace.Turns[0].ToolAttempts) != 1 {
		t.Fatalf("agent trace = %s, want one Turn with one Action and one Tool Attempt", body)
	}
	if got := trace.Turns[0].Action.Status; got != "WAITING_CALLBACK" {
		t.Errorf("action status = %s, want WAITING_CALLBACK", got)
	}
	attempt := trace.Turns[0].ToolAttempts[0]
	if attempt.ToolName != remotelookup.ToolName || attempt.Status != "DISPATCHED" || attempt.DispatchedAt == nil {
		t.Errorf("tool attempt = %+v, want %s DISPATCHED with dispatchedAt", attempt, remotelookup.ToolName)
	}
	if attempt.CallbackBinding == nil || attempt.CallbackBinding.ID == "" ||
		attempt.CallbackBinding.ProviderID != remotelookup.ProviderID || attempt.CallbackBinding.ExternalTaskID == "" {
		t.Errorf("callback binding = %+v, want id, provider %q and an external task id", attempt.CallbackBinding, remotelookup.ProviderID)
	}

	dispatches := fixture.tasks.dispatches()
	if len(dispatches) != 1 {
		t.Fatalf("provider received %d dispatches, want 1", len(dispatches))
	}
	var sent struct {
		CallbackToken string `json:"callbackToken"`
	}
	if err := json.Unmarshal(dispatches[0], &sent); err != nil || sent.CallbackToken == "" {
		t.Fatalf("dispatch carried no callback token: %v", err)
	}
	sum := sha256.Sum256([]byte(sent.CallbackToken))
	text := string(body)
	for _, forbidden := range []string{sent.CallbackToken, hex.EncodeToString(sum[:]), "callbackTokenHash", "callbackToken"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("agent trace leaks %q: %s", forbidden, text)
		}
	}
}
