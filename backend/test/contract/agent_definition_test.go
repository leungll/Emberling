//go:build integration

package contract

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
)

// agentGraphRequest builds a text_input -> agent -> text_output POST /api/definitions
// body, parameterized on the agent node's own config so each test below can vary exactly
// the field its scenario needs (modelId, allowedTools or modelConfig) while keeping the
// rest of the graph identical to test/fixtures/definitions/agent_lookup.json.
func agentGraphRequest(agentConfig map[string]any) map[string]any {
	return map[string]any{
		"name":        "Agent Graph",
		"description": "text_input -> agent -> text_output",
		"nodes": []map[string]any{
			{
				"id": "node_input", "type": "text_input", "name": "Question",
				"position": map[string]any{"x": 0, "y": 0},
				"config":   map[string]any{"inputKey": "question", "label": "Question", "required": true},
			},
			{
				"id": "node_agent", "type": "agent", "name": "Answering Agent",
				"position": map[string]any{"x": 200, "y": 0},
				"config":   agentConfig,
			},
			{
				"id": "node_output", "type": "text_output", "name": "Answer",
				"position": map[string]any{"x": 400, "y": 0},
				"config":   map[string]any{},
			},
		},
		"edges": []map[string]any{
			{"id": "edge_input_agent", "source": "node_input", "sourceHandle": "text", "target": "node_agent", "targetHandle": "input"},
			{"id": "edge_agent_output", "source": "node_agent", "sourceHandle": "text", "target": "node_output", "targetHandle": "text"},
		},
	}
}

// baseAgentConfig is a config that satisfies runtime.ParseAgentNodeConfig and resolves
// cleanly against the fixture Backend's real Model and Tool Registries (mockmodel's
// "text-model-v1" and the lookup Tool), so each failing test below only needs to override
// the one field its scenario is about.
func baseAgentConfig(overrides map[string]any) map[string]any {
	cfg := map[string]any{
		"instructions": "Answer the question.",
		"modelId":      mockmodel.ModelID,
		"allowedTools": []string{"lookup"},
		"maxTurns":     4,
		"timeoutMs":    120000,
	}
	for k, v := range overrides {
		cfg[k] = v
	}
	return cfg
}

// TestDefinitions_Save_AgentUnknownModel_400 covers DefinitionService's
// resolveAgentRegistrations: an agent node's modelId that the Model Registry cannot
// resolve fails Save/Create with runtime.CodeModelNotFound, which (being absent from
// api's status422Codes allowlist) maps to HTTP 400, not the compiler's own 422-family
// structural codes.
func TestDefinitions_Save_AgentUnknownModel_400(t *testing.T) {
	env := newTestEnv(t)

	req := agentGraphRequest(baseAgentConfig(map[string]any{"modelId": "does-not-exist"}))
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /api/definitions (unknown modelId) status = %d, want %d, body=%s", resp.StatusCode, http.StatusBadRequest, body)
	}
	if code := errorCode(t, body); code != "MODEL_NOT_FOUND" {
		t.Errorf("POST /api/definitions (unknown modelId) error.code = %q, want %q", code, "MODEL_NOT_FOUND")
	}
}

// TestDefinitions_Save_AgentUnknownTool_400 covers the allowedTools half of
// resolveAgentRegistrations: a Tool name the Tool Registry cannot resolve fails with
// runtime.CodeToolNotFound, also HTTP 400.
func TestDefinitions_Save_AgentUnknownTool_400(t *testing.T) {
	env := newTestEnv(t)

	req := agentGraphRequest(baseAgentConfig(map[string]any{"allowedTools": []string{"not-a-real-tool"}}))
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /api/definitions (unknown tool) status = %d, want %d, body=%s", resp.StatusCode, http.StatusBadRequest, body)
	}
	if code := errorCode(t, body); code != "TOOL_NOT_FOUND" {
		t.Errorf("POST /api/definitions (unknown tool) error.code = %q, want %q", code, "TOOL_NOT_FOUND")
	}
}

// TestDefinitions_Save_AgentModelConfigViolatesSchema_400 covers freezeModelConfig's
// validation step: mockmodel's ConfigSchema bounds temperature to [0, 2]
// (internal/adapters/mockmodel/provider.go), so a submitted value outside that range
// fails with runtime.CodeModelConfigInvalid before any Definition version is persisted.
func TestDefinitions_Save_AgentModelConfigViolatesSchema_400(t *testing.T) {
	env := newTestEnv(t)

	req := agentGraphRequest(baseAgentConfig(map[string]any{
		"modelConfig": map[string]any{"temperature": 5},
	}))
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /api/definitions (modelConfig out of range) status = %d, want %d, body=%s", resp.StatusCode, http.StatusBadRequest, body)
	}
	if code := errorCode(t, body); code != "MODEL_CONFIG_INVALID" {
		t.Errorf("POST /api/definitions (modelConfig out of range) error.code = %q, want %q", code, "MODEL_CONFIG_INVALID")
	}
}

// TestDefinitions_Save_AgentModelConfigDefaultsFrozen covers the positive path of
// freezeModelConfig/withFrozenModelConfig: a Definition saved with no modelConfig at all
// gets mockmodel's ConfigSchema top-level default (temperature: 0.2) applied and frozen
// back into the persisted node config, per domain.ModelMetadata's doc comment ("defaults
// are resolved once before a Definition version is frozen and are never re-injected at
// Run or recovery time").
func TestDefinitions_Save_AgentModelConfigDefaultsFrozen(t *testing.T) {
	env := newTestEnv(t)

	req := agentGraphRequest(baseAgentConfig(nil))
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions (no modelConfig) status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[map[string]any](t, body)
	nodes, ok := created["nodes"].([]any)
	if !ok {
		t.Fatalf("POST /api/definitions (no modelConfig) response missing nodes: %s", body)
	}
	var agentConfig map[string]any
	for _, n := range nodes {
		node, _ := n.(map[string]any)
		if node["id"] == "node_agent" {
			agentConfig, _ = node["config"].(map[string]any)
		}
	}
	if agentConfig == nil {
		t.Fatalf("POST /api/definitions (no modelConfig) response missing node_agent: %s", body)
	}
	modelConfig, ok := agentConfig["modelConfig"].(map[string]any)
	if !ok {
		t.Fatalf("node_agent.config.modelConfig missing or not an object after freeze: %v", agentConfig)
	}
	if temp, _ := modelConfig["temperature"].(float64); temp != 0.2 {
		t.Errorf("node_agent.config.modelConfig.temperature = %v, want 0.2 (ConfigSchema default)", modelConfig["temperature"])
	}
}

// TestDefinitions_Save_AgentFixture_Succeeds is the positive control: the shipped
// agent_lookup.json fixture (text_input -> agent[allowedTools: lookup, modelId:
// text-model-v1] -> text_output) saves cleanly against the real Model and Tool
// Registries, with no schemas configured (contextSchema/stateSchema/outputSchema all
// absent, which ParseAgentNodeConfig accepts).
func TestDefinitions_Save_AgentFixture_Succeeds(t *testing.T) {
	env := newTestEnv(t)
	fx := loadAgentLookupFixture(t)

	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions (agent_lookup fixture) status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[map[string]any](t, body)
	if v, _ := created["version"].(float64); v != 1 {
		t.Errorf("POST /api/definitions (agent_lookup fixture) version = %v, want 1", created["version"])
	}
}

// TestCreateRun_AgentToolUnregistered_409_NoRunCreated covers ExecutionService's
// resolveAgentIdentifiers pre-check in CreateRun. Task brief note: the brief's own
// literal test name implies HTTP 400, but this scenario -- an already-saved, already
// Registry-validated Definition version whose allowedTools name the *current* process's
// Tool Registry can no longer resolve -- is exactly what service.RegistryResolutionError
// exists for (the same type the pre-existing document_processing modelId re-resolution
// check at CreateRun time already uses), and api/errors.go maps that type unconditionally
// to HTTP 409 RUNTIME_BINDING_UNAVAILABLE. This test asserts the code's actual documented
// behavior (409), not the brief's literal number, following the precedent set by
// TestAPI_CreateRun_InvalidInput_Returns400WithSchemaPaths in runs_test.go; see the final
// report's "undocumented decisions" section.
//
// The scenario is built with two Backends sharing one database (the same Pool-reuse
// pattern e2e_async_test.go's restart tests use): the first has "lookup" registered and
// saves the agent_lookup fixture; the second is a fresh process image over the same
// facts, but wired with SkipLookupTool so its Tool Registry cannot resolve "lookup" --
// reproducing "the Registry changed between Save and CreateRun" without any Unregister
// call, which does not exist in production.
func TestCreateRun_AgentToolUnregistered_409_NoRunCreated(t *testing.T) {
	env1 := newTestEnv(t)
	fx := loadAgentLookupFixture(t)

	resp, body := env1.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions (agent_lookup fixture, env1) status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ := created["workflowId"].(string)
	version, _ := created["version"].(float64)
	if workflowID == "" {
		t.Fatalf("POST /api/definitions (agent_lookup fixture, env1) missing workflowId: %s", body)
	}
	pool := env1.pool
	env1.stop()

	env2 := newTestEnvWithOptions(t, testEnvOptions{Pool: pool, SkipLookupTool: true})

	runResp, runBody := env2.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": int(version),
		"input": map[string]any{"question": "what is the answer?"},
	})
	if runResp.StatusCode != http.StatusConflict {
		t.Fatalf("POST /api/runs (lookup unregistered) status = %d, want %d, body=%s", runResp.StatusCode, http.StatusConflict, runBody)
	}
	if code := errorCode(t, runBody); code != "RUNTIME_BINDING_UNAVAILABLE" {
		t.Errorf("POST /api/runs (lookup unregistered) error.code = %q, want %q", code, "RUNTIME_BINDING_UNAVAILABLE")
	}

	var runCount int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM runs WHERE workflow_id = $1", workflowID).Scan(&runCount); err != nil {
		t.Fatalf("count runs for workflow %s: %v", workflowID, err)
	}
	if runCount != 0 {
		t.Errorf("runs persisted for workflow %s after rejected CreateRun = %d, want 0", workflowID, runCount)
	}
}

// agentNodeModelConfig extracts nodes[node_agent].config.modelConfig from a Definition
// response body, which is the persisted frozen snapshot every assertion below is about.
func agentNodeModelConfig(t *testing.T, body []byte) map[string]any {
	t.Helper()
	def := decodeBody[map[string]any](t, body)
	nodes, ok := def["nodes"].([]any)
	if !ok {
		t.Fatalf("definition response missing nodes: %s", body)
	}
	for _, n := range nodes {
		node, _ := n.(map[string]any)
		if node["id"] != "node_agent" {
			continue
		}
		config, _ := node["config"].(map[string]any)
		modelConfig, ok := config["modelConfig"].(map[string]any)
		if !ok {
			t.Fatalf("node_agent.config.modelConfig missing or not an object: %v", config)
		}
		return modelConfig
	}
	t.Fatalf("definition response missing node_agent: %s", body)
	return nil
}

// agentRunFrozenTemperature reads the one Agent Run of runID and returns its frozen
// model_config temperature. It is read straight from PostgreSQL because no API surface
// exposes an Agent Run's frozen model parameters, and PostgreSQL is the authority for
// exactly the fact this test is about (invariant #1).
func agentRunFrozenTemperature(t *testing.T, pool *pgxpool.Pool, runID string) float64 {
	t.Helper()
	var raw []byte
	err := pool.QueryRow(context.Background(), `
		SELECT ar.model_config
		FROM agent_runs ar
		JOIN node_runs nr ON nr.id = ar.node_run_id
		WHERE nr.run_id = $1`, runID).Scan(&raw)
	if err != nil {
		t.Fatalf("read frozen model_config of the agent run of %s: %v", runID, err)
	}
	var modelConfig struct {
		Temperature *float64 `json:"temperature"`
	}
	if err := json.Unmarshal(raw, &modelConfig); err != nil {
		t.Fatalf("decode frozen model_config %s: %v", raw, err)
	}
	if modelConfig.Temperature == nil {
		t.Fatalf("frozen model_config %s carries no temperature", raw)
	}
	return *modelConfig.Temperature
}

// agentCreateRunAndWait creates a Run of one Definition version and waits for it to reach
// a terminal status, failing the test if that status is not COMPLETED.
func agentCreateRunAndWait(t *testing.T, env *testEnv, workflowID string, version int) string {
	t.Helper()
	resp, body := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version,
		"input": map[string]any{"question": "what is the answer?"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[map[string]any](t, body)
	runID, _ := created["id"].(string)
	if runID == "" {
		t.Fatalf("POST /api/runs response missing id: %s", body)
	}
	snap := env.waitForTerminal(t, runID, e2eWait)
	run, _ := snap["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("run %s status = %v, want COMPLETED: %v", runID, status, run)
	}
	return runID
}

// TestCreateRun_AgentFrozenModelConfigIgnoresProviderDefaultChange covers docs/09 §3.3
// "创建 Run 或恢复 Agent 时 Provider 默认值发生变化": neither CreateRun nor a process
// restart may re-derive `modelConfig` from the Model Registration, so the Definition's
// frozen value and the Agent Run's frozen snapshot both survive unchanged.
//
// The "changed default" is represented by the one difference that is actually detectable:
// a Definition frozen to temperature 1.5 while the Backend's live ConfigSchema default is
// 0.2 (internal/adapters/mockmodel/provider.go). mockmodel's ConfigSchema is a
// compile-time constant and newTestEnvWithOptions registers that Provider
// unconditionally, so there is no way to re-register a variant default from a test; but
// the only mechanism by which a changed Provider default could ever leak into a frozen
// snapshot is re-injection of the ConfigSchema default at Run or recovery time, and a
// stored value that differs from the live default detects exactly that re-injection --
// including the "the Provider now defaults this field differently" case the row names.
//
// The second Backend over the same database is the restart half: it re-registered the
// Provider and re-read its defaults from scratch, then serves the already-frozen
// Definition version and creates a second Run against it.
func TestCreateRun_AgentFrozenModelConfigIgnoresProviderDefaultChange(t *testing.T) {
	const frozenTemperature = 1.5
	const providerDefaultTemperature = 0.2

	env1 := newTestEnv(t)

	req := agentGraphRequest(baseAgentConfig(map[string]any{
		"modelConfig": map[string]any{"temperature": frozenTemperature},
	}))
	resp, body := env1.doJSON(t, http.MethodPost, "/api/definitions", req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ := created["workflowId"].(string)
	versionFloat, _ := created["version"].(float64)
	version := int(versionFloat)
	if workflowID == "" || version == 0 {
		t.Fatalf("POST /api/definitions response missing workflowId/version: %s", body)
	}
	if got, _ := agentNodeModelConfig(t, body)["temperature"].(float64); got != frozenTemperature {
		t.Fatalf("saved modelConfig.temperature = %v, want the submitted %v", got, frozenTemperature)
	}
	if frozenTemperature == providerDefaultTemperature {
		t.Fatalf("the fixture froze the Provider's own default, which would make this test vacuous")
	}

	// CreateRun in the Backend that saved the Definition: the Agent Run freezes the
	// Definition's value, not the Registration's default.
	runID := agentCreateRunAndWait(t, env1, workflowID, version)
	if got := agentRunFrozenTemperature(t, env1.pool, runID); got != frozenTemperature {
		t.Errorf("agent run frozen model_config.temperature = %v, want the Definition's %v (not the ConfigSchema default %v)",
			got, frozenTemperature, providerDefaultTemperature)
	}

	// Restart: a new Backend image over the same PostgreSQL facts, whose Model Registry
	// re-read the Provider's defaults during its own registration.
	pool := env1.pool
	env1.stop()
	env2 := newTestEnvWithOptions(t, testEnvOptions{Pool: pool})

	versionResp, versionBody := env2.doJSON(t, http.MethodGet,
		"/api/definitions/"+workflowID+"/versions/"+strconv.Itoa(version), nil)
	if versionResp.StatusCode != http.StatusOK {
		t.Fatalf("GET definition version status = %d, want %d, body=%s", versionResp.StatusCode, http.StatusOK, versionBody)
	}
	if got, _ := agentNodeModelConfig(t, versionBody)["temperature"].(float64); got != frozenTemperature {
		t.Errorf("stored modelConfig.temperature after restart = %v, want the frozen %v: an immutable Definition version was rewritten",
			got, frozenTemperature)
	}

	// The Agent Run frozen before the restart is untouched by it.
	if got := agentRunFrozenTemperature(t, pool, runID); got != frozenTemperature {
		t.Errorf("agent run frozen model_config.temperature after restart = %v, want %v", got, frozenTemperature)
	}

	// And a Run created by the restarted Backend freezes the same Definition value, so a
	// Provider whose defaults were re-read still cannot reach an Agent Run.
	restartedRunID := agentCreateRunAndWait(t, env2, workflowID, version)
	if got := agentRunFrozenTemperature(t, pool, restartedRunID); got != frozenTemperature {
		t.Errorf("agent run frozen model_config.temperature of the post-restart Run = %v, want the Definition's %v (not the ConfigSchema default %v)",
			got, frozenTemperature, providerDefaultTemperature)
	}
}

// loadAgentLookupFixture reads test/fixtures/definitions/agent_lookup.json, reusing
// documentProcessingFixture's shape (loadDocumentProcessingFixture, harness_test.go)
// since both fixtures are the same name/description/nodes/edges wire shape.
func loadAgentLookupFixture(t *testing.T) documentProcessingFixture {
	t.Helper()
	raw, err := os.ReadFile("../fixtures/definitions/agent_lookup.json")
	if err != nil {
		t.Fatalf("read agent_lookup.json: %v", err)
	}
	var fx documentProcessingFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("unmarshal agent_lookup.json: %v", err)
	}
	return fx
}
