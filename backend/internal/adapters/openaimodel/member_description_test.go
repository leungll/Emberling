package openaimodel_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/adapters/openaimodel"
)

// structureWithoutDescriptions is the strict Decision schema of a text-form request with
// every description removed: the member descriptions may explain the per-kind rules, but
// the types, nullability and required list stay exactly as before.
const structureWithoutDescriptions = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "kind": {"type": "string", "enum": ["TOOL_CALL", "FINAL"]},
    "tool": {"type": ["string", "null"]},
    "arguments": {"type": ["string", "null"]},
    "output": {"type": ["string", "null"]},
    "statePatch": {"type": ["string", "null"]}
  },
  "required": ["kind", "arguments", "output", "statePatch", "tool"]
}`

// memberDescriptions returns the description of every top-level member of the strict
// Decision schema, keyed by member name.
func memberDescriptions(t *testing.T, schema json.RawMessage) map[string]string {
	t.Helper()
	var decoded struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &decoded); err != nil {
		t.Fatalf("strict Decision schema is not an object: %v", err)
	}
	descriptions := map[string]string{}
	for name, member := range decoded.Properties {
		descriptions[name] = member.Description
	}
	return descriptions
}

// withoutDescriptions removes every top-level member description.
func withoutDescriptions(t *testing.T, schema json.RawMessage) any {
	t.Helper()
	value := decode(t, schema)
	properties, _ := value.(map[string]any)["properties"].(map[string]any)
	for _, member := range properties {
		if object, ok := member.(map[string]any); ok {
			delete(object, "description")
		}
	}
	return value
}

func TestProvider_Generate_StrictDecision_DescribesWhichKindRequiresEachMember(t *testing.T) {
	body := sendWithOutputSchema(t, nil)
	schema := body.ResponseFormat.JSONSchema.Schema
	descriptions := memberDescriptions(t, schema)

	for name, want := range map[string][]string{
		"tool":       {"Required (non-null) when kind is TOOL_CALL", "null when kind is FINAL"},
		"arguments":  {"JSON-encoded object", "Required (non-null) when kind is TOOL_CALL", "null when kind is FINAL"},
		"output":     {"Required (non-null) when kind is FINAL", "null when kind is TOOL_CALL"},
		"statePatch": {"JSON-encoded", "Optional when kind is TOOL_CALL or FINAL"},
		"kind":       {"TOOL_CALL requires non-null tool and arguments", "FINAL requires non-null output"},
	} {
		for _, part := range want {
			if !strings.Contains(descriptions[name], part) {
				t.Errorf("%s description = %q, want it to contain %q", name, descriptions[name], part)
			}
		}
	}
	if diff := cmp.Diff(decode(t, []byte(structureWithoutDescriptions)), withoutDescriptions(t, schema)); diff != "" {
		t.Errorf("strict Decision structure changed (-want +got):\n%s", diff)
	}
}

func TestProvider_Generate_StrictDecision_DescriptionsFollowTheRegisteredSchema(t *testing.T) {
	// A Decision Schema whose kinds and per-kind required members differ from the
	// built-in one: the descriptions must follow it rather than fixed member rules.
	decisionSchema := `{"oneOf":[
	  {"properties":{"kind":{"const":"CALL"},"tool":{"type":"string"},"arguments":{}},"required":["kind","tool","arguments"]},
	  {"properties":{"kind":{"const":"DONE"},"output":{},"statePatch":{"type":"object"}},"required":["kind","output","statePatch"]}
	]}`
	content := `{"kind":"DONE","tool":null,"arguments":null,"output":"ok","statePatch":"{}"}`
	server, seen := fakeProvider(t, http.StatusOK, completion(t, content, ""))
	provider, err := openaimodel.New(openaimodel.Config{
		ModelID: testModelID, DisplayName: testDisplayName,
		BaseURL: server.URL + "/v1/", APIKey: testKey, Model: "gpt-test",
		HTTPClient: server.Client(), DecisionSchema: raw(decisionSchema),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request := agentRequest()
	request.DecisionSchema = raw(decisionSchema)
	if _, err := provider.Generate(context.Background(), request); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	var body sentRequest
	if err := json.Unmarshal(seen.body, &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	descriptions := memberDescriptions(t, body.ResponseFormat.JSONSchema.Schema)

	for name, want := range map[string]string{
		"tool":       "Required (non-null) when kind is CALL. Must be null when kind is DONE.",
		"statePatch": "Required (non-null) when kind is DONE. Must be null when kind is CALL.",
		"output":     "Required (non-null) when kind is DONE. Must be null when kind is CALL.",
		"kind":       "CALL requires non-null tool and arguments. DONE requires non-null output and statePatch.",
	} {
		if !strings.Contains(descriptions[name], want) {
			t.Errorf("%s description = %q, want it to contain %q", name, descriptions[name], want)
		}
	}
	for name, description := range descriptions {
		if strings.Contains(description, "TOOL_CALL") || strings.Contains(description, "FINAL") {
			t.Errorf("%s description = %q names a kind the registered schema does not declare", name, description)
		}
	}
}

func TestNew_DecisionSchemaRequiringUndeclaredMember_Fails(t *testing.T) {
	schema := `{"oneOf":[{"properties":{"kind":{"const":"FINAL"},"output":{}},"required":["kind","output","tool"]}]}`
	_, err := openaimodel.New(openaimodel.Config{
		ModelID: testModelID, DisplayName: testDisplayName,
		BaseURL: "https://llm.example", APIKey: testKey, Model: "m", DecisionSchema: raw(schema),
	})
	if err == nil || !strings.Contains(err.Error(), "tool") {
		t.Fatalf("New() error = %v, want a refusal naming the undeclared required member", err)
	}
}

func TestNew_DecisionSchemaRepeatingKind_Fails(t *testing.T) {
	// Two branches for one kind would give that kind two different required lists, so
	// the member descriptions could not say which members it requires.
	schema := `{"oneOf":[
	  {"properties":{"kind":{"const":"FINAL"},"output":{}},"required":["kind","output"]},
	  {"properties":{"kind":{"const":"FINAL"},"output":{},"tool":{"type":"string"}},"required":["kind","tool"]}
	]}`
	_, err := openaimodel.New(openaimodel.Config{
		ModelID: testModelID, DisplayName: testDisplayName,
		BaseURL: "https://llm.example", APIKey: testKey, Model: "m", DecisionSchema: raw(schema),
	})
	if err == nil || !strings.Contains(err.Error(), "FINAL") {
		t.Fatalf("New() error = %v, want a refusal naming the repeated kind", err)
	}
}
