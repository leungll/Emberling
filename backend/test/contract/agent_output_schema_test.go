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
