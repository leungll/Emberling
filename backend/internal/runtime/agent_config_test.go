package runtime

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestAgentNodeConfig_Parse_ValidConfig_Passes(t *testing.T) {
	raw := json.RawMessage(`{
		"instructions": "Answer using the lookup tool when needed.",
		"modelId": "text-model-v1",
		"modelConfig": {"temperature": 0.2},
		"allowedTools": ["lookup"],
		"maxTurns": 4,
		"timeoutMs": 120000
	}`)

	cfg, err := ParseAgentNodeConfig(raw)
	if err != nil {
		t.Fatalf("ParseAgentNodeConfig() unexpected error: %v", err)
	}
	if cfg.ModelID != "text-model-v1" {
		t.Errorf("ModelID = %q, want text-model-v1", cfg.ModelID)
	}
	if cfg.MaxTurns != 4 {
		t.Errorf("MaxTurns = %d, want 4", cfg.MaxTurns)
	}
	if cfg.TimeoutMs != 120000 {
		t.Errorf("TimeoutMs = %d, want 120000", cfg.TimeoutMs)
	}
	if len(cfg.AllowedTools) != 1 || cfg.AllowedTools[0] != "lookup" {
		t.Errorf("AllowedTools = %+v, want [lookup]", cfg.AllowedTools)
	}
}

func TestAgentNodeConfig_Parse_UnknownField_Fails(t *testing.T) {
	raw := json.RawMessage(`{
		"modelId": "text-model-v1",
		"maxTurns": 4,
		"timeoutMs": 120000,
		"notARealField": true
	}`)

	_, err := ParseAgentNodeConfig(raw)
	if err == nil {
		t.Fatal("ParseAgentNodeConfig() expected error, got nil")
	}
}

func TestAgentNodeConfig_Parse_MaxTurnsZero_Fails(t *testing.T) {
	raw := json.RawMessage(`{
		"modelId": "text-model-v1",
		"maxTurns": 0,
		"timeoutMs": 120000
	}`)

	_, err := ParseAgentNodeConfig(raw)
	if err == nil {
		t.Fatal("ParseAgentNodeConfig() expected error, got nil")
	}
	var ace *AgentConfigError
	if !errors.As(err, &ace) {
		t.Fatalf("expected *AgentConfigError, got %T: %v", err, err)
	}
	if ace.Field != "maxTurns" {
		t.Errorf("Field = %q, want maxTurns", ace.Field)
	}
}

func TestAgentNodeConfig_Parse_StateSchemaRejectsEmptyObject_Fails(t *testing.T) {
	raw := json.RawMessage(`{
		"modelId": "text-model-v1",
		"maxTurns": 4,
		"timeoutMs": 120000,
		"stateSchema": {"type": "object", "required": ["notes"], "properties": {"notes": {"type": "string"}}}
	}`)

	_, err := ParseAgentNodeConfig(raw)
	if err == nil {
		t.Fatal("ParseAgentNodeConfig() expected error, got nil")
	}
	var ace *AgentConfigError
	if !errors.As(err, &ace) {
		t.Fatalf("expected *AgentConfigError, got %T: %v", err, err)
	}
	if ace.Field != "stateSchema" {
		t.Errorf("Field = %q, want stateSchema", ace.Field)
	}
}

func TestAgentNodeConfig_Parse_OutputSchemaRejectsString_Fails(t *testing.T) {
	raw := json.RawMessage(`{
		"modelId": "text-model-v1",
		"maxTurns": 4,
		"timeoutMs": 120000,
		"outputSchema": {"type": "object", "required": ["answer"], "properties": {"answer": {"type": "string"}}}
	}`)

	_, err := ParseAgentNodeConfig(raw)
	if err == nil {
		t.Fatal("ParseAgentNodeConfig() expected error, got nil")
	}
	var ace *AgentConfigError
	if !errors.As(err, &ace) {
		t.Fatalf("expected *AgentConfigError, got %T: %v", err, err)
	}
	if ace.Field != "outputSchema" {
		t.Errorf("Field = %q, want outputSchema", ace.Field)
	}
}
