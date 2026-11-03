package runtime

import (
	"encoding/json"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

func taskInputDefinition(config string, extra ...domain.Node) domain.Definition {
	nodes := []domain.Node{{ID: "node_task", Type: NodeTypeTaskInput, Config: json.RawMessage(config)}}
	return domain.Definition{Nodes: append(nodes, extra...)}
}

// TestRunInputSchema_TaskInput_DefaultRequiresAnObject pins the property a task_input
// without a configured schema contributes: any JSON object, nothing more.
func TestRunInputSchema_TaskInput_DefaultRequiresAnObject(t *testing.T) {
	got, errs := buildRunInputSchema(taskInputDefinition(`{"inputKey":"task","required":true}`))
	if len(errs) > 0 {
		t.Fatalf("buildRunInputSchema() errors = %+v, want none", errs)
	}
	want := `{"type":"object","additionalProperties":false,"properties":{"task":{"type":"object"}},"required":["task"]}`
	if string(got) != want {
		t.Fatalf("RunInputSchema =\n%s\nwant\n%s", got, want)
	}
}

// TestRunInputSchema_TaskInput_ConfiguredSchemaIsCanonicalAndTyped embeds the configured
// task schema with sorted keys and an injected object type, so equal Definitions compile
// to byte-identical schemas.
func TestRunInputSchema_TaskInput_ConfiguredSchemaIsCanonicalAndTyped(t *testing.T) {
	config := `{"inputKey":"task","required":true,"schema":{"required":["goal"],"properties":{"goal":{"type":"string","enum":["PREPARE_ONLY","FIX_AND_DEPLOY"]},"advisory":{"type":"string"}}}}`
	got, errs := buildRunInputSchema(taskInputDefinition(config))
	if len(errs) > 0 {
		t.Fatalf("buildRunInputSchema() errors = %+v, want none", errs)
	}
	want := `{"type":"object","additionalProperties":false,"properties":{"task":{` +
		`"properties":{"advisory":{"type":"string"},"goal":{"enum":["PREPARE_ONLY","FIX_AND_DEPLOY"],"type":"string"}},` +
		`"required":["goal"],"type":"object"}},"required":["task"]}`
	if string(got) != want {
		t.Fatalf("RunInputSchema =\n%s\nwant\n%s", got, want)
	}
}

// TestRunInputSchema_TaskInput_RejectsNonObjectAtRunCreation proves the frozen schema
// turns a task that is not an object, or that misses a required member, away before a
// Run exists.
func TestRunInputSchema_TaskInput_RejectsNonObjectAtRunCreation(t *testing.T) {
	schema, errs := buildRunInputSchema(taskInputDefinition(`{"inputKey":"task","required":true,"schema":{"required":["goal"]}}`))
	if len(errs) > 0 {
		t.Fatalf("buildRunInputSchema() errors = %+v, want none", errs)
	}
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "object with goal", input: `{"task":{"goal":"PREPARE_ONLY","advisory":"GO-2025-3553"}}`},
		{name: "string", input: `{"task":"PREPARE_ONLY"}`, wantErr: true},
		{name: "array", input: `{"task":[]}`, wantErr: true},
		{name: "missing required member", input: `{"task":{}}`, wantErr: true},
		{name: "absent", input: `{}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateRunInput(schema, json.RawMessage(tc.input))
			if tc.wantErr && len(errs) == 0 {
				t.Fatal("ValidateRunInput() errors = none, want a rejection")
			}
			if !tc.wantErr && len(errs) > 0 {
				t.Fatalf("ValidateRunInput() errors = %+v, want none", errs)
			}
		})
	}
}

func TestRunInputSchema_TaskInput_EmptyInputKey_Fails(t *testing.T) {
	_, errs := buildRunInputSchema(taskInputDefinition(`{"inputKey":"","required":true}`))
	if !containsCode(errs, CodeInputKeyRequired) {
		t.Fatalf("buildRunInputSchema() errors = %+v, want %s", errs, CodeInputKeyRequired)
	}
}

func TestRunInputSchema_TaskInput_KeySharedWithTextInput_FailsOnBoth(t *testing.T) {
	_, errs := buildRunInputSchema(taskInputDefinition(`{"inputKey":"task","required":true}`, domain.Node{
		ID: "node_text", Type: NodeTypeTextInput, Config: json.RawMessage(`{"inputKey":"task","required":true}`),
	}))
	var nodes []string
	for _, err := range errs {
		if err.Code == CodeDuplicateInputKey {
			nodes = append(nodes, err.NodeID)
		}
	}
	if len(nodes) != 2 {
		t.Fatalf("duplicate-key errors on %v, want both nodes", nodes)
	}
}
