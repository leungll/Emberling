//go:build integration

package contract

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// objectOutputSchemaInProfile is the photo effect-template FINAL shape: an object-form
// outputSchema inside the strict output subset.
const objectOutputSchemaInProfile = `{
	"type": "object",
	"properties": {
		"examples": {
			"type": "array",
			"items": {
				"type": "object",
				"properties": {"photoAssetId": {"type": "string"}, "assetRef": {"type": "string"}},
				"required": ["photoAssetId", "assetRef"],
				"additionalProperties": false
			}
		},
		"settingsDigest": {"type": "string"}
	},
	"required": ["examples", "settingsDigest"],
	"additionalProperties": false
}`

func decodeJSONValue(t *testing.T, raw string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return value
}

// TestDefinitionSave_ObjectOutputSchemaOutsideProfile_Rejected saves Agents whose
// object-form outputSchema leaves the strict output subset. Each Save is refused with the
// error at the Agent's outputSchema, a message naming the rule and its JSON Pointer inside
// the Schema, and no Definition version is created.
func TestDefinitionSave_ObjectOutputSchemaOutsideProfile_Rejected(t *testing.T) {
	const outputSchemaPath = "nodes[node_agent].config.outputSchema"
	for _, tc := range []struct {
		name    string
		schema  string
		pointer string
	}{
		{"optional property", `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a"],"additionalProperties":false}`,
			`"/required"`},
		{"unsupported keyword", `{"type":"object","properties":{"a":{"type":"string","maxLength":8}},"required":["a"],"additionalProperties":false}`,
			`"/properties/a/maxLength"`},
		{"open object", `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"],"additionalProperties":true}`,
			`"/additionalProperties"`},
		{"contentEncoding", `{"type":"object","properties":{"a":{"type":"string","contentEncoding":"base64"}},"required":["a"],"additionalProperties":false}`,
			`"/properties/a/contentEncoding"`},
		{"contentMediaType on a non-string member", `{"type":"object","properties":{"a":{"type":"integer","contentMediaType":"image/png"}},"required":["a"],"additionalProperties":false}`,
			`"/properties/a/contentMediaType"`},
		{"malformed contentMediaType", `{"type":"object","properties":{"a":{"type":"string","contentMediaType":"image"}},"required":["a"],"additionalProperties":false}`,
			`"/properties/a/contentMediaType"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			request := agentGraphRequest(baseAgentConfig(map[string]any{"outputSchema": decodeJSONValue(t, tc.schema)}))
			resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", request)
			// A config value the Agent node refuses is a semantic config failure, which the
			// error mapping reports as 400 like every other Agent config violation.
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("POST /api/definitions status = %d, want %d, body=%s", resp.StatusCode, http.StatusBadRequest, body)
			}
			if code := errorCode(t, body); code != "SEMANTIC_VALIDATION_FAILED" {
				t.Errorf("error.code = %q, want SEMANTIC_VALIDATION_FAILED", code)
			}
			envelope := decodeBody[struct {
				Error struct {
					Details struct {
						Errors []struct {
							Code    string `json:"code"`
							Path    string `json:"path"`
							NodeID  string `json:"nodeId"`
							Message string `json:"message"`
						} `json:"errors"`
					} `json:"details"`
				} `json:"error"`
			}](t, body)
			errs := envelope.Error.Details.Errors
			if len(errs) != 1 || errs[0].Path != outputSchemaPath || errs[0].NodeID != "node_agent" {
				t.Fatalf("error details = %+v, want one error at %s", errs, outputSchemaPath)
			}
			if !strings.Contains(errs[0].Message, "JSON Pointer "+tc.pointer) {
				t.Errorf("error message = %q, want the JSON Pointer %s", errs[0].Message, tc.pointer)
			}
			var workflows int
			if err := env.pool.QueryRow(context.Background(), "SELECT count(*) FROM workflows").Scan(&workflows); err != nil {
				t.Fatalf("count workflows: %v", err)
			}
			if workflows != 0 {
				t.Errorf("workflows after the rejected Save = %d, want 0", workflows)
			}
		})
	}
}

// TestDefinitionSave_ObjectOutputSchemaInProfile_SavesVersion saves an Agent with an
// object-form outputSchema inside the strict output subset and reads the version back:
// the stored Schema is the one that was saved.
func TestDefinitionSave_ObjectOutputSchemaInProfile_SavesVersion(t *testing.T) {
	env := newTestEnv(t)
	want := decodeJSONValue(t, objectOutputSchemaInProfile)
	request := agentGraphRequest(baseAgentConfig(map[string]any{"outputSchema": want}))
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", request)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ := created["workflowId"].(string)
	version, _ := created["version"].(float64)

	getResp, getBody := env.doJSON(t, http.MethodGet,
		"/api/definitions/"+workflowID+"/versions/"+strconv.Itoa(int(version)), nil)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET definition version status = %d, body=%s", getResp.StatusCode, getBody)
	}
	nodes, _ := decodeBody[map[string]any](t, getBody)["nodes"].([]any)
	for _, n := range nodes {
		node, _ := n.(map[string]any)
		if node["id"] != "node_agent" {
			continue
		}
		config, _ := node["config"].(map[string]any)
		if !reflect.DeepEqual(config["outputSchema"], want) {
			t.Errorf("stored outputSchema = %v, want the saved Schema %v", config["outputSchema"], want)
		}
		return
	}
	t.Fatalf("definition version has no node_agent: %s", getBody)
}

// storedAgentOutputSchema saves request and reads the Agent's outputSchema back from the
// created Definition version.
func storedAgentOutputSchema(t *testing.T, env *testEnv, request map[string]any) any {
	t.Helper()
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", request)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ := created["workflowId"].(string)
	version, _ := created["version"].(float64)
	getResp, getBody := env.doJSON(t, http.MethodGet,
		"/api/definitions/"+workflowID+"/versions/"+strconv.Itoa(int(version)), nil)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET definition version status = %d, body=%s", getResp.StatusCode, getBody)
	}
	nodes, _ := decodeBody[map[string]any](t, getBody)["nodes"].([]any)
	for _, n := range nodes {
		node, _ := n.(map[string]any)
		if node["id"] == "node_agent" {
			config, _ := node["config"].(map[string]any)
			return config["outputSchema"]
		}
	}
	t.Fatalf("definition version has no node_agent: %s", getBody)
	return nil
}

// TestDefinitionSave_OutputSchemaContentMediaType_SavedVerbatim saves an object-form
// outputSchema whose string members carry contentMediaType, a display-only annotation
// for Studio: the Save is accepted and the stored Schema still carries the keyword, so
// the Snapshot that Studio renders from can read it.
func TestDefinitionSave_OutputSchemaContentMediaType_SavedVerbatim(t *testing.T) {
	env := newTestEnv(t)
	want := decodeJSONValue(t, `{"type":"object","properties":{
		"poster":{"type":["string","null"],"contentMediaType":"image/png"},
		"frames":{"type":"array","items":{"type":"string","contentMediaType":"image/*"}}
	},"required":["poster","frames"],"additionalProperties":false}`)
	got := storedAgentOutputSchema(t, env, agentGraphRequest(baseAgentConfig(map[string]any{"outputSchema": want})))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stored outputSchema = %v, want the saved Schema with contentMediaType %v", got, want)
	}
}

// TestDefinitionSave_TaskInputSchemaContentMediaType_SurvivesAndRunAccepted saves a
// task_input whose task schema annotates a string with contentMediaType. The keyword is
// carried into the frozen runInputSchema, and a Run whose string is not media is still
// created: the annotation is for display and never validated against a value.
func TestDefinitionSave_TaskInputSchemaContentMediaType_SurvivesAndRunAccepted(t *testing.T) {
	env := newTestEnv(t)
	env.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: "done"}
	}
	request := map[string]any{
		"name": "Task Graph", "description": "task_input -> agent -> text_output",
		"nodes": []map[string]any{
			{"id": "node_task", "type": "task_input", "name": "Task", "position": map[string]any{"x": 0, "y": 0},
				"config": map[string]any{"inputKey": "task", "required": true, "schema": decodeJSONValue(t,
					`{"required":["poster"],"properties":{"poster":{"type":"string","contentMediaType":"image/png"}}}`)}},
			{"id": "node_agent", "type": "agent", "name": "Agent", "position": map[string]any{"x": 200, "y": 0},
				"config": baseAgentConfig(nil)},
			{"id": "node_output", "type": "text_output", "name": "Answer", "position": map[string]any{"x": 400, "y": 0},
				"config": map[string]any{}},
		},
		"edges": []map[string]any{
			{"id": "e1", "source": "node_task", "sourceHandle": "task", "target": "node_agent", "targetHandle": "input"},
			{"id": "e2", "source": "node_agent", "sourceHandle": "text", "target": "node_output", "targetHandle": "text"},
		},
	}
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", request)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	created := decodeBody[map[string]any](t, body)
	schema, _ := json.Marshal(created["runInputSchema"])
	if !strings.Contains(string(schema), `"contentMediaType":"image/png"`) {
		t.Errorf("runInputSchema = %s, want the task schema's contentMediaType carried through", schema)
	}

	resp, body = env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": created["workflowId"], "definitionVersion": created["version"],
		"input": map[string]any{"task": map[string]any{"poster": "plain words, not an image"}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, want %d (contentMediaType must not be enforced), body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)
	snapshot := env.waitForTerminal(t, runID, 10*time.Second)
	if run, _ := snapshot["run"].(map[string]any); run["status"] != "COMPLETED" {
		t.Errorf("Run status = %v, want COMPLETED", run["status"])
	}
}
