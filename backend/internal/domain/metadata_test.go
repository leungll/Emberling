package domain

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestPortType_IsCompatibleWith_MatchesContract(t *testing.T) {
	cases := []struct {
		name   string
		source PortType
		target PortType
		want   bool
	}{
		{"identical text", PortTypeText, PortTypeText, true},
		{"identical image", PortTypeImage, PortTypeImage, true},
		{"identical json", PortTypeJSON, PortTypeJSON, true},
		{"identical any", PortTypeAny, PortTypeAny, true},
		{"text into image", PortTypeText, PortTypeImage, false},
		{"image into text", PortTypeImage, PortTypeText, false},
		{"json into text", PortTypeJSON, PortTypeText, false},
		{"any target accepts text", PortTypeText, PortTypeAny, true},
		{"any target accepts image", PortTypeImage, PortTypeAny, true},
		{"any target accepts json", PortTypeJSON, PortTypeAny, true},
		{"any source needs any target", PortTypeAny, PortTypeText, false},
		{"any source into image", PortTypeAny, PortTypeImage, false},
		{"any source into json", PortTypeAny, PortTypeJSON, false},
		{"unknown source", PortType("audio"), PortTypeAny, false},
		{"unknown target", PortTypeText, PortType("audio"), false},
		{"empty source", PortType(""), PortTypeText, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.source.IsCompatibleWith(tc.target); got != tc.want {
				t.Fatalf("PortType(%q).IsCompatibleWith(%q) = %v, want %v", tc.source, tc.target, got, tc.want)
			}
		})
	}
}

// validNodeMetadata returns a registration that passes Validate. Each rejection case
// mutates exactly one fact so the test proves which rule rejected it.
func validNodeMetadata() NodeMetadata {
	return NodeMetadata{
		Type:          "image_generation",
		DisplayName:   "Image Generation",
		Category:      NodeCategoryPromptAndModel,
		ExecutionKind: NodeExecutionAsync,
		Inputs:        []PortMetadata{{Name: "prompt", DataType: PortTypeText, Required: true}},
		Outputs:       []PortMetadata{{Name: "image", DataType: PortTypeImage, Required: true}},
		ConfigSchema:  json.RawMessage(`{"type":"object"}`),
		UISchema: NodeUISchema{Fields: []UIField{
			{Path: "modelId", Order: 10, Group: UIGroupModel, Widget: UIWidgetModelSelector, Capability: ModelCapabilityImageGeneration},
			{Path: "width", Order: 20, Group: UIGroupModelParameters, Widget: UIWidgetDefault},
		}},
		SideEffect: SideEffectPolicy{Kind: SideEffectExternal, Idempotency: IdempotencyKeyed},
	}
}

func TestNodeMetadata_Validate_AcceptsRegisteredContract(t *testing.T) {
	if err := validNodeMetadata().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestNodeMetadata_Validate_RejectsInvalidRegistration(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*NodeMetadata)
		wantMsg string
	}{
		{
			name:    "empty type",
			mutate:  func(m *NodeMetadata) { m.Type = "" },
			wantMsg: "type is empty",
		},
		{
			name:    "unknown execution kind",
			mutate:  func(m *NodeMetadata) { m.ExecutionKind = "STREAMING" },
			wantMsg: `executionKind "STREAMING"`,
		},
		{
			name:    "empty execution kind",
			mutate:  func(m *NodeMetadata) { m.ExecutionKind = "" },
			wantMsg: `executionKind ""`,
		},
		{
			name:    "unsupported input port type",
			mutate:  func(m *NodeMetadata) { m.Inputs[0].DataType = "audio" },
			wantMsg: `input port prompt: dataType "audio"`,
		},
		{
			name:    "unsupported output port type",
			mutate:  func(m *NodeMetadata) { m.Outputs[0].DataType = "" },
			wantMsg: `output port image: dataType ""`,
		},
		{
			name:    "empty port name",
			mutate:  func(m *NodeMetadata) { m.Inputs[0].Name = "" },
			wantMsg: "input port #0: name is empty",
		},
		{
			name: "duplicate input port name",
			mutate: func(m *NodeMetadata) {
				m.Inputs = append(m.Inputs, PortMetadata{Name: "prompt", DataType: PortTypeText})
			},
			wantMsg: `input port "prompt": duplicate name`,
		},
		{
			name: "duplicate output port name",
			mutate: func(m *NodeMetadata) {
				m.Outputs = append(m.Outputs, PortMetadata{Name: "image", DataType: PortTypeImage})
			},
			wantMsg: `output port "image": duplicate name`,
		},
		{
			name:    "missing config schema",
			mutate:  func(m *NodeMetadata) { m.ConfigSchema = nil },
			wantMsg: "configSchema is empty",
		},
		{
			name:    "malformed config schema",
			mutate:  func(m *NodeMetadata) { m.ConfigSchema = json.RawMessage(`{"type":`) },
			wantMsg: "configSchema is not valid JSON",
		},
		{
			name:    "unknown ui group",
			mutate:  func(m *NodeMetadata) { m.UISchema.Fields[1].Group = "ADVANCED" },
			wantMsg: `uiSchema field width: group "ADVANCED"`,
		},
		{
			name:    "unknown ui widget",
			mutate:  func(m *NodeMetadata) { m.UISchema.Fields[1].Widget = "SLIDER" },
			wantMsg: `uiSchema field width: widget "SLIDER"`,
		},
		{
			name:    "empty ui field path",
			mutate:  func(m *NodeMetadata) { m.UISchema.Fields[1].Path = "" },
			wantMsg: "uiSchema field #1: path is empty",
		},
		{
			name:    "capability without model selector",
			mutate:  func(m *NodeMetadata) { m.UISchema.Fields[1].Capability = ModelCapabilityTextGeneration },
			wantMsg: "uiSchema field width: capability is only allowed with widget MODEL_SELECTOR",
		},
		{
			name:    "unknown side effect kind",
			mutate:  func(m *NodeMetadata) { m.SideEffect.Kind = "INTERNAL" },
			wantMsg: `sideEffect.kind "INTERNAL"`,
		},
		{
			name:    "unknown idempotency mode",
			mutate:  func(m *NodeMetadata) { m.SideEffect.Idempotency = "" },
			wantMsg: `sideEffect.idempotency ""`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metadata := validNodeMetadata()
			tc.mutate(&metadata)

			err := metadata.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want rejection mentioning %q", tc.wantMsg)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("Validate() = %q, want it to mention %q", err, tc.wantMsg)
			}
		})
	}
}

func TestNodeMetadata_Validate_ReportsEveryViolationAtOnce(t *testing.T) {
	metadata := validNodeMetadata()
	metadata.Type = ""
	metadata.ExecutionKind = "STREAMING"
	metadata.ConfigSchema = nil

	err := metadata.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want rejection")
	}
	for _, want := range []string{"type is empty", `executionKind "STREAMING"`, "configSchema is empty"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate() = %q, want it to mention %q", err, want)
		}
	}
}

func TestToolMetadata_Validate_RejectsInvalidRegistration(t *testing.T) {
	valid := func() ToolMetadata {
		return ToolMetadata{
			Name:          "lookup",
			Description:   "Read a deterministic record",
			InputSchema:   json.RawMessage(`{"type":"object"}`),
			OutputSchema:  json.RawMessage(`{"type":"object"}`),
			SideEffect:    SideEffectPolicy{Kind: SideEffectNone, Idempotency: IdempotencySafe},
			ExecutionKind: ToolExecutionSync,
		}
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	cases := []struct {
		name    string
		mutate  func(*ToolMetadata)
		wantMsg string
	}{
		{"empty name", func(m *ToolMetadata) { m.Name = "" }, "name is empty"},
		{"unknown execution kind", func(m *ToolMetadata) { m.ExecutionKind = "MANAGED_AGENT" }, `executionKind "MANAGED_AGENT"`},
		{"missing input schema", func(m *ToolMetadata) { m.InputSchema = nil }, "inputSchema is empty"},
		{"malformed input schema", func(m *ToolMetadata) { m.InputSchema = json.RawMessage(`{`) }, "inputSchema is not valid JSON"},
		{"missing output schema", func(m *ToolMetadata) { m.OutputSchema = nil }, "outputSchema is empty"},
		{"malformed output schema", func(m *ToolMetadata) { m.OutputSchema = json.RawMessage(`nope`) }, "outputSchema is not valid JSON"},
		{"unknown side effect kind", func(m *ToolMetadata) { m.SideEffect.Kind = "" }, `sideEffect.kind ""`},
		{"unknown idempotency mode", func(m *ToolMetadata) { m.SideEffect.Idempotency = "BEST_EFFORT" }, `sideEffect.idempotency "BEST_EFFORT"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metadata := valid()
			tc.mutate(&metadata)

			err := metadata.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want rejection mentioning %q", tc.wantMsg)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("Validate() = %q, want it to mention %q", err, tc.wantMsg)
			}
		})
	}
}

// wireNodeMetadata is the Node Metadata Contract example served by GET /node-types.
const wireNodeMetadata = `{
  "type": "image_generation",
  "displayName": "Image Generation",
  "category": "Prompt & Model",
  "executionKind": "ASYNC",
  "inputs": [{"name": "prompt", "dataType": "text", "required": true}],
  "outputs": [{"name": "image", "dataType": "image", "required": true}],
  "configSchema": {
    "type": "object",
    "properties": {
      "modelId": {"type": "string"},
      "width": {"type": "integer", "minimum": 256}
    },
    "required": ["modelId", "width"]
  },
  "uiSchema": {
    "fields": [
      {
        "path": "modelId",
        "order": 10,
        "group": "MODEL",
        "widget": "MODEL_SELECTOR",
        "capability": "image_generation"
      },
      {
        "path": "width",
        "order": 20,
        "group": "MODEL_PARAMETERS",
        "widget": "DEFAULT"
      }
    ]
  },
  "sideEffect": {"kind": "EXTERNAL", "idempotency": "KEYED"},
  "factInputs": []
}`

// encodeWire serialises without HTML escaping so the result can be compared to the
// contract example verbatim. json.Marshal would write "Prompt & Model", which is the
// same wire value after decoding but not the same bytes as the documented example.
func encodeWire(t *testing.T, value any) []byte {
	t.Helper()
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func TestNodeMetadata_JSON_RoundTripsWireNames(t *testing.T) {
	fixture := validNodeMetadata()
	fixture.ConfigSchema = json.RawMessage(`{"type":"object","properties":{"modelId":{"type":"string"},"width":{"type":"integer","minimum":256}},"required":["modelId","width"]}`)
	fixture.FactInputs = []string{}

	var want bytes.Buffer
	if err := json.Compact(&want, []byte(wireNodeMetadata)); err != nil {
		t.Fatalf("compact contract example: %v", err)
	}

	encoded := encodeWire(t, fixture)
	if !bytes.Equal(encoded, want.Bytes()) {
		t.Fatalf("encode =\n%s\nwant\n%s", encoded, want.Bytes())
	}

	var decoded NodeMetadata
	if err := json.Unmarshal(want.Bytes(), &decoded); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	if reencoded := encodeWire(t, decoded); !bytes.Equal(reencoded, want.Bytes()) {
		t.Fatalf("round trip =\n%s\nwant\n%s", reencoded, want.Bytes())
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded.Validate() = %v, want nil", err)
	}
}

func TestModelMetadata_JSON_RoundTripsWireNames(t *testing.T) {
	const wire = `{"id":"text-model-v1","displayName":"Text Model v1","capabilities":["text_generation","structured_decision"],"configSchema":{"type":"object"}}`

	fixture := ModelMetadata{
		ID:           "text-model-v1",
		DisplayName:  "Text Model v1",
		Capabilities: []string{ModelCapabilityTextGeneration, ModelCapabilityStructuredDecision},
		ConfigSchema: json.RawMessage(`{"type":"object"}`),
	}
	encoded, err := json.Marshal(fixture)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	if string(encoded) != wire {
		t.Fatalf("Marshal() = %s, want %s", encoded, wire)
	}
}

func TestToolMetadata_JSON_RoundTripsWireNames(t *testing.T) {
	const wire = `{"name":"lookup","description":"Read a deterministic record","inputSchema":{},"outputSchema":{},"sideEffect":{"kind":"NONE","idempotency":"SAFE"},"executionKind":"SYNC","requires":[],"countsTowardGenerationLimit":false}`

	fixture := ToolMetadata{
		Name:          "lookup",
		Description:   "Read a deterministic record",
		InputSchema:   json.RawMessage(`{}`),
		OutputSchema:  json.RawMessage(`{}`),
		SideEffect:    SideEffectPolicy{Kind: SideEffectNone, Idempotency: IdempotencySafe},
		ExecutionKind: ToolExecutionSync,
		Requires:      []FactRequirement{},
	}
	encoded, err := json.Marshal(fixture)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	if string(encoded) != wire {
		t.Fatalf("Marshal() = %s, want %s", encoded, wire)
	}
}

func TestMetadata_PollPolicyValidOnAsync_Accepted(t *testing.T) {
	node := validNodeMetadata()
	node.Poll = &PollPolicy{IntervalMs: 1000, MaxPolls: 5}
	if err := node.Validate(); err != nil {
		t.Fatalf("NodeMetadata.Validate() = %v, want nil", err)
	}

	tool := ToolMetadata{
		Name:          "remote_lookup",
		InputSchema:   json.RawMessage(`{"type":"object"}`),
		OutputSchema:  json.RawMessage(`{"type":"object"}`),
		SideEffect:    SideEffectPolicy{Kind: SideEffectExternal, Idempotency: IdempotencyKeyed},
		ExecutionKind: ToolExecutionAsync,
		Poll:          &PollPolicy{IntervalMs: 1000, MaxPolls: 5},
	}
	if err := tool.Validate(); err != nil {
		t.Fatalf("ToolMetadata.Validate() = %v, want nil", err)
	}
}

func TestMetadata_PollPolicyInvalidBounds_Rejected(t *testing.T) {
	cases := []struct {
		name    string
		policy  PollPolicy
		wantMsg string
	}{
		{"zero interval", PollPolicy{IntervalMs: 0, MaxPolls: 3}, "poll.intervalMs must be greater than 0, got 0"},
		{"negative interval", PollPolicy{IntervalMs: -1, MaxPolls: 3}, "poll.intervalMs must be greater than 0, got -1"},
		{"zero max polls", PollPolicy{IntervalMs: 500, MaxPolls: 0}, "poll.maxPolls must be greater than 0, got 0"},
		{"negative max polls", PollPolicy{IntervalMs: 500, MaxPolls: -2}, "poll.maxPolls must be greater than 0, got -2"},
	}
	for _, tc := range cases {
		t.Run("node "+tc.name, func(t *testing.T) {
			metadata := validNodeMetadata()
			policy := tc.policy
			metadata.Poll = &policy
			err := metadata.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("NodeMetadata.Validate() = %v, want it to mention %q", err, tc.wantMsg)
			}
		})
		t.Run("tool "+tc.name, func(t *testing.T) {
			metadata := ToolMetadata{
				Name:          "remote_lookup",
				InputSchema:   json.RawMessage(`{"type":"object"}`),
				OutputSchema:  json.RawMessage(`{"type":"object"}`),
				SideEffect:    SideEffectPolicy{Kind: SideEffectExternal, Idempotency: IdempotencyKeyed},
				ExecutionKind: ToolExecutionAsync,
			}
			policy := tc.policy
			metadata.Poll = &policy
			err := metadata.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("ToolMetadata.Validate() = %v, want it to mention %q", err, tc.wantMsg)
			}
		})
	}
}

func TestMetadata_PollPolicyOnNonAsyncKind_Rejected(t *testing.T) {
	const wantMsg = "poll is only allowed with executionKind ASYNC"
	for _, kind := range []NodeExecutionKind{NodeExecutionSync, NodeExecutionManagedAgent} {
		t.Run("node "+string(kind), func(t *testing.T) {
			metadata := validNodeMetadata()
			metadata.ExecutionKind = kind
			metadata.Poll = &PollPolicy{IntervalMs: 1000, MaxPolls: 5}
			err := metadata.Validate()
			if err == nil || !strings.Contains(err.Error(), wantMsg) {
				t.Fatalf("NodeMetadata.Validate() = %v, want it to mention %q", err, wantMsg)
			}
		})
	}
	t.Run("tool SYNC", func(t *testing.T) {
		metadata := ToolMetadata{
			Name:          "lookup",
			InputSchema:   json.RawMessage(`{"type":"object"}`),
			OutputSchema:  json.RawMessage(`{"type":"object"}`),
			SideEffect:    SideEffectPolicy{Kind: SideEffectNone, Idempotency: IdempotencySafe},
			ExecutionKind: ToolExecutionSync,
			Poll:          &PollPolicy{IntervalMs: 1000, MaxPolls: 5},
		}
		err := metadata.Validate()
		if err == nil || !strings.Contains(err.Error(), wantMsg) {
			t.Fatalf("ToolMetadata.Validate() = %v, want it to mention %q", err, wantMsg)
		}
	})
}

func TestMetadata_PollPolicyJSON_SerialisedOnlyWhenDeclared(t *testing.T) {
	tool := ToolMetadata{
		Name:          "remote_lookup",
		Description:   "Look up a record asynchronously",
		InputSchema:   json.RawMessage(`{}`),
		OutputSchema:  json.RawMessage(`{}`),
		SideEffect:    SideEffectPolicy{Kind: SideEffectExternal, Idempotency: IdempotencyKeyed},
		ExecutionKind: ToolExecutionAsync,
		Poll:          &PollPolicy{IntervalMs: 2000, MaxPolls: 10},
		Requires:      []FactRequirement{},
	}
	const wantTool = `{"name":"remote_lookup","description":"Look up a record asynchronously","inputSchema":{},"outputSchema":{},"sideEffect":{"kind":"EXTERNAL","idempotency":"KEYED"},"executionKind":"ASYNC","poll":{"intervalMs":2000,"maxPolls":10},"requires":[],"countsTowardGenerationLimit":false}`
	encoded, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	if string(encoded) != wantTool {
		t.Fatalf("Marshal() = %s, want %s", encoded, wantTool)
	}

	node := validNodeMetadata()
	node.Poll = &PollPolicy{IntervalMs: 2000, MaxPolls: 10}
	encoded, err = json.Marshal(node)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	if !strings.Contains(string(encoded), `"poll":{"intervalMs":2000,"maxPolls":10}`) {
		t.Fatalf("Marshal() = %s, want a poll object", encoded)
	}
	var decoded NodeMetadata
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	if decoded.Poll == nil || *decoded.Poll != *node.Poll {
		t.Fatalf("decoded Poll = %+v, want %+v", decoded.Poll, node.Poll)
	}

	node.Poll = nil
	encoded, err = json.Marshal(node)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	if strings.Contains(string(encoded), `"poll"`) {
		t.Fatalf("Marshal() = %s, want no poll key for an undeclared policy", encoded)
	}
}
