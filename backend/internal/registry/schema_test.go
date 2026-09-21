package registry

import (
	"encoding/json"
	"testing"
)

func TestApplyTopLevelDefaults_MissingProperty_FillsDefault(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"temperature": {"type": "number", "default": 0.2}}
	}`)

	got, err := ApplyTopLevelDefaults(schema, map[string]any{})
	if err != nil {
		t.Fatalf("ApplyTopLevelDefaults() unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("ApplyTopLevelDefaults() returned nil map, want a non-nil empty-or-filled map")
	}
	if got["temperature"] != 0.2 {
		t.Errorf("temperature = %v, want 0.2", got["temperature"])
	}
}

func TestApplyTopLevelDefaults_PropertyAlreadySet_KeepsSubmittedValue(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"temperature": {"type": "number", "default": 0.2}}
	}`)

	got, err := ApplyTopLevelDefaults(schema, map[string]any{"temperature": 0.9})
	if err != nil {
		t.Fatalf("ApplyTopLevelDefaults() unexpected error: %v", err)
	}
	if got["temperature"] != 0.9 {
		t.Errorf("temperature = %v, want 0.9 (submitted value must not be overwritten)", got["temperature"])
	}
}

func TestApplyTopLevelDefaults_EmptySchemaAndValue_ReturnsNonNilEmptyMap(t *testing.T) {
	got, err := ApplyTopLevelDefaults(json.RawMessage(``), map[string]any{})
	if err != nil {
		t.Fatalf("ApplyTopLevelDefaults() unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("ApplyTopLevelDefaults() returned nil, want a non-nil empty map")
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if string(raw) != "{}" {
		t.Errorf("marshalled result = %s, want {}", raw)
	}
}
