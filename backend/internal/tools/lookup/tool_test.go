package lookup

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

func action(arguments string) registry.ToolAction {
	return registry.ToolAction{
		AgentRunID: "agentrun_1",
		TurnID:     "turn_1",
		ActionID:   "action_1",
		ToolName:   ToolName,
		AttemptNo:  1,
		Arguments:  json.RawMessage(arguments),
	}
}

func TestLookupTool_DeterministicResult(t *testing.T) {
	first, err := (Executor{}).Execute(context.Background(), action(`{"key":"ember"}`))
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if first.Kind != registry.ToolResultCompleted {
		t.Fatalf("Execute().Kind = %q, want %q for a SYNC Tool", first.Kind, registry.ToolResultCompleted)
	}
	if first.Result == nil {
		t.Fatal("Execute().Result = nil, want the completed Tool result")
	}
	if first.ExternalTask != nil {
		t.Fatalf("Execute().ExternalTask = %v, want nil for a SYNC Tool", first.ExternalTask)
	}

	second, err := (Executor{}).Execute(context.Background(), action(`{"key":"ember"}`))
	if err != nil {
		t.Fatalf("second Execute() error = %v, want nil", err)
	}
	if string(first.Result.Output) != string(second.Result.Output) {
		t.Fatalf("Execute() output = %s then %s, want the same result for the same arguments",
			first.Result.Output, second.Result.Output)
	}

	other, err := (Executor{}).Execute(context.Background(), action(`{"key":"kindling"}`))
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if string(other.Result.Output) == string(first.Result.Output) {
		t.Fatalf("Execute() returned %s for both keys, want a result derived from the arguments", other.Result.Output)
	}

	schema, err := registry.CompileSchema(Registration().Metadata.OutputSchema)
	if err != nil {
		t.Fatalf("CompileSchema(outputSchema) error = %v, want nil", err)
	}
	var decoded any
	if err := json.Unmarshal(first.Result.Output, &decoded); err != nil {
		t.Fatalf("decode result %s: %v", first.Result.Output, err)
	}
	if err := registry.ValidateValue(schema, decoded); err != nil {
		t.Fatalf("result %s does not satisfy the registered OutputSchema: %v", first.Result.Output, err)
	}
}

func TestLookupTool_ScriptedFailure_ReturnsToolError(t *testing.T) {
	result, err := (Executor{}).Execute(context.Background(), action(`{"key":"`+MissingKey+`"}`))
	if err == nil {
		t.Fatalf("Execute() error = nil, want a Tool error for the scripted failure key %q", MissingKey)
	}
	if result.Result != nil || result.ExternalTask != nil {
		t.Fatalf("Execute() result = %+v, want an empty result alongside the error", result)
	}
}

func TestLookupTool_RegistersWithMetadata(t *testing.T) {
	registration := Registration()
	if err := registry.NewToolRegistry().Register(registration); err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}
	if registration.Metadata.Name != ToolName {
		t.Fatalf("Metadata.Name = %q, want %q", registration.Metadata.Name, ToolName)
	}
	if registration.Metadata.ExecutionKind != domain.ToolExecutionSync {
		t.Fatalf("Metadata.ExecutionKind = %q, want %q", registration.Metadata.ExecutionKind, domain.ToolExecutionSync)
	}
	want := domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe}
	if registration.Metadata.SideEffect != want {
		t.Fatalf("Metadata.SideEffect = %+v, want %+v", registration.Metadata.SideEffect, want)
	}
}
