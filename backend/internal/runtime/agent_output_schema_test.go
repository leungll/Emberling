package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// photoSetFinalSchema is the FINAL shape of the photo effect-template scenario: one
// reviewed example per photo and the digest of the shared settings.
const photoSetFinalSchema = `{
	"type": "object",
	"properties": {
		"examples": {
			"type": "array",
			"items": {
				"type": "object",
				"properties": {
					"photoAssetId": {"type": "string"},
					"assetRef": {"type": "string"}
				},
				"required": ["photoAssetId", "assetRef"],
				"additionalProperties": false
			}
		},
		"settingsDigest": {"type": "string"}
	},
	"required": ["examples", "settingsDigest"],
	"additionalProperties": false
}`

func parseWithOutputSchema(schema string) (AgentNodeConfig, error) {
	raw := `{"modelId": "text-model-v1", "maxTurns": 4, "timeoutMs": 120000, "outputSchema": ` + schema + `}`
	return ParseAgentNodeConfig(json.RawMessage(raw))
}

// outputSchemaRejection parses schema and returns the AgentConfigError it must produce.
func outputSchemaRejection(t *testing.T, schema string) *AgentConfigError {
	t.Helper()
	_, err := parseWithOutputSchema(schema)
	if err == nil {
		t.Fatalf("ParseAgentNodeConfig(outputSchema %s) error = nil, want a rejection", schema)
	}
	var ace *AgentConfigError
	if !errors.As(err, &ace) {
		t.Fatalf("ParseAgentNodeConfig() error = %T %v, want *AgentConfigError", err, err)
	}
	if ace.Field != "outputSchema" {
		t.Errorf("Field = %q, want outputSchema", ace.Field)
	}
	return ace
}

func TestParseAgentNodeConfig_ObjectOutputSchemaInProfile_Accepted(t *testing.T) {
	cfg, err := parseWithOutputSchema(photoSetFinalSchema)
	if err != nil {
		t.Fatalf("ParseAgentNodeConfig() error = %v, want the photo-set FINAL Schema accepted", err)
	}
	if cfg.OutputForm != AgentOutputObject {
		t.Errorf("OutputForm = %q, want %q", cfg.OutputForm, AgentOutputObject)
	}
}

func TestParseAgentNodeConfig_ObjectSchemaNullableAndScalarEnum_Accepted(t *testing.T) {
	schema := `{"type":"object","title":"Verdict","description":"d","properties":{
		"note":{"type":["string","null"]},
		"grade":{"type":"string","enum":["a","b",null]},
		"score":{"type":["null","number"]},
		"count":{"type":"integer"},
		"ok":{"type":"boolean"},
		"tags":{"type":["array","null"],"items":{"type":"string"}},
		"nested":{"type":["object","null"],"properties":{"x":{"type":"integer"}},"required":["x"],"additionalProperties":false}
	},"required":["note","grade","score","count","ok","tags","nested"],"additionalProperties":false}`
	cfg, err := parseWithOutputSchema(schema)
	if err != nil {
		t.Fatalf("ParseAgentNodeConfig() error = %v, want a nullable-union object Schema accepted", err)
	}
	if cfg.OutputForm != AgentOutputObject {
		t.Errorf("OutputForm = %q, want %q", cfg.OutputForm, AgentOutputObject)
	}
}

func TestParseAgentNodeConfig_ObjectSchemaOptionalProperty_Rejected(t *testing.T) {
	schema := `{"type":"object","properties":{
		"examples":{"type":"array","items":{"type":"object",
			"properties":{"photoAssetId":{"type":"string"},"assetRef":{"type":"string"}},
			"required":["photoAssetId"],"additionalProperties":false}},
		"settingsDigest":{"type":"string"}
	},"required":["examples","settingsDigest"],"additionalProperties":false}`
	ace := outputSchemaRejection(t, schema)
	if !strings.Contains(ace.Message, `"/properties/examples/items/required"`) {
		t.Errorf("message = %q, want the JSON Pointer of the incomplete required list", ace.Message)
	}
	if !strings.Contains(ace.Message, "every property") {
		t.Errorf("message = %q, want it to name the rule: required lists every property", ace.Message)
	}
}

func TestParseAgentNodeConfig_ObjectSchemaUnsupportedKeyword_Rejected(t *testing.T) {
	deep := `{"type":"string"}`
	for i := 0; i < 5; i++ {
		deep = `{"type":"object","properties":{"a":` + deep + `},"required":["a"],"additionalProperties":false}`
	}
	for _, tc := range []struct {
		name    string
		schema  string
		pointer string
		rule    string
	}{
		{"oneOf", `{"type":"object","properties":{"a":{"oneOf":[{"type":"string"},{"type":"integer"}]}},"required":["a"],"additionalProperties":false}`,
			`"/properties/a/oneOf"`, "outside the strict output subset"},
		{"$ref", `{"type":"object","properties":{"a":{"$ref":"#/$defs/x"}},"required":["a"],"additionalProperties":false,"$defs":{"x":{"type":"string"}}}`,
			`"/$defs"`, "outside the strict output subset"},
		{"pattern", `{"type":"object","properties":{"a":{"type":"string","pattern":"^x$"}},"required":["a"],"additionalProperties":false}`,
			`"/properties/a/pattern"`, "outside the strict output subset"},
		{"additionalProperties true", `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"],"additionalProperties":true}`,
			`"/additionalProperties"`, "additionalProperties: false"},
		{"additionalProperties missing", `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`,
			`""`, "additionalProperties: false"},
		{"too deep", deep, `"/properties/a/properties/a/properties/a/properties/a/properties/a"`, "deeper than 5"},
		{"untyped member", `{"type":"object","properties":{"a":{}},"required":["a"],"additionalProperties":false}`,
			`"/properties/a"`, "type"},
		{"union of two non-null types", `{"type":"object","properties":{"a":{"type":["string","integer"]}},"required":["a"],"additionalProperties":false}`,
			`"/properties/a/type"`, `"null"`},
		{"empty properties", `{"type":"object","properties":{},"required":[],"additionalProperties":false}`,
			`"/properties"`, "at least one property"},
		{"tuple items", `{"type":"object","properties":{"a":{"type":"array","items":[{"type":"string"}]}},"required":["a"],"additionalProperties":false}`,
			`"/properties/a/items"`, "single Schema"},
		{"object enum value", `{"type":"object","properties":{"a":{"type":"string","enum":[{"x":1}]}},"required":["a"],"additionalProperties":false}`,
			`"/properties/a/enum/0"`, "scalar"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ace := outputSchemaRejection(t, tc.schema)
			if !strings.Contains(ace.Message, tc.pointer) {
				t.Errorf("message = %q, want JSON Pointer %s", ace.Message, tc.pointer)
			}
			if !strings.Contains(ace.Message, tc.rule) {
				t.Errorf("message = %q, want it to name the violated rule (%q)", ace.Message, tc.rule)
			}
		})
	}
}

func TestParseAgentNodeConfig_ObjectSchemaOverPropertyLimit_Rejected(t *testing.T) {
	names := make([]string, 101)
	props := make([]string, 101)
	for i := range names {
		names[i] = fmt.Sprintf("%q", fmt.Sprintf("p%03d", i))
		props[i] = names[i] + `:{"type":"string"}`
	}
	schema := `{"type":"object","properties":{` + strings.Join(props, ",") + `},"required":[` +
		strings.Join(names, ",") + `],"additionalProperties":false}`
	ace := outputSchemaRejection(t, schema)
	if !strings.Contains(ace.Message, "100 properties") {
		t.Errorf("message = %q, want it to name the 100-property limit", ace.Message)
	}
}

func TestParseAgentNodeConfig_ObjectSchemaOverSizeLimit_Rejected(t *testing.T) {
	long := strings.Repeat("x", 17*1024)
	schema := `{"type":"object","description":"` + long + `","properties":{"a":{"type":"string"}},"required":["a"],"additionalProperties":false}`
	ace := outputSchemaRejection(t, schema)
	if !strings.Contains(ace.Message, "16 KiB") {
		t.Errorf("message = %q, want it to name the 16 KiB limit", ace.Message)
	}
	if strings.Contains(ace.Message, long[:64]) {
		t.Errorf("message repeats the Schema's content")
	}
}

func TestParseAgentNodeConfig_ObjectSchemaRejection_NeverQuotesSchemaContent(t *testing.T) {
	const marker = "author-written-description"
	schema := `{"type":"object","description":"` + marker + `","properties":{"a":{"type":"string","format":"` + marker + `"}},"required":["a"],"additionalProperties":false}`
	ace := outputSchemaRejection(t, schema)
	if strings.Contains(ace.Error(), marker) {
		t.Errorf("error = %q, want no Schema content beyond the JSON Pointer", ace.Error())
	}
}

func TestParseAgentNodeConfig_StringAcceptingSchema_RemainsTextForm(t *testing.T) {
	for _, schema := range []string{
		`{}`,
		`{"type":"string"}`,
		`{"type":"string","enum":["","approved"]}`,
	} {
		t.Run(schema, func(t *testing.T) {
			cfg, err := parseWithOutputSchema(schema)
			if err != nil {
				t.Fatalf("ParseAgentNodeConfig() error = %v, want a string-accepting Schema accepted", err)
			}
			if cfg.OutputForm != AgentOutputText {
				t.Errorf("OutputForm = %q, want %q", cfg.OutputForm, AgentOutputText)
			}
		})
	}

	cfg, err := ParseAgentNodeConfig(json.RawMessage(`{"modelId": "text-model-v1", "maxTurns": 4, "timeoutMs": 120000}`))
	if err != nil {
		t.Fatalf("ParseAgentNodeConfig(no outputSchema) error = %v", err)
	}
	if cfg.OutputForm != AgentOutputText {
		t.Errorf("OutputForm without outputSchema = %q, want %q", cfg.OutputForm, AgentOutputText)
	}
}

func TestParseAgentNodeConfig_SchemaNeitherForm_Rejected(t *testing.T) {
	for _, schema := range []string{
		`{"type":"array","items":{"type":"string"}}`,
		`{"type":"integer"}`,
		`{"type":["object","null"],"properties":{"a":{"type":"string"}},"required":["a"],"additionalProperties":false}`,
	} {
		t.Run(schema, func(t *testing.T) {
			ace := outputSchemaRejection(t, schema)
			if !strings.Contains(ace.Message, "text form") || !strings.Contains(ace.Message, "object form") {
				t.Errorf("message = %q, want it to name both accepted forms", ace.Message)
			}
		})
	}
}
