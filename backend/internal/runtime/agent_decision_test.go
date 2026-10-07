package runtime

import (
	"encoding/json"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

func toolName(name string) *string { return &name }

func lookupMetadata() domain.ToolMetadata {
	return domain.ToolMetadata{
		Name:          "lookup",
		Description:   "Read a deterministic record",
		InputSchema:   json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"}},"required":["key"],"additionalProperties":false}`),
		OutputSchema:  json.RawMessage(`{"type":"object"}`),
		SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		ExecutionKind: domain.ToolExecutionSync,
	}
}

func TestParseModelDecision_ToolCallMissingArguments_Fails(t *testing.T) {
	_, err := ParseModelDecision(DecisionEnvelope{Kind: domain.DecisionToolCall, ToolName: toolName("lookup")})
	if err == nil {
		t.Fatal("ParseModelDecision() error = nil, want error for a TOOL_CALL without arguments")
	}
	if _, ok := AsInvalidActionError(err); !ok {
		t.Fatalf("ParseModelDecision() error = %v, want an *InvalidActionError", err)
	}
}

func TestParseModelDecision_FinalWithToolFields_Fails(t *testing.T) {
	_, err := ParseModelDecision(DecisionEnvelope{
		Kind:      domain.DecisionFinal,
		Output:    json.RawMessage(`"done"`),
		ToolName:  toolName("lookup"),
		Arguments: json.RawMessage(`{"key":"ember"}`),
	})
	if err == nil {
		t.Fatal("ParseModelDecision() error = nil, want error for a FINAL carrying Tool fields")
	}
	if _, ok := AsInvalidActionError(err); !ok {
		t.Fatalf("ParseModelDecision() error = %v, want an *InvalidActionError", err)
	}
}

func TestParseModelDecision_ValidToolCall_Passes(t *testing.T) {
	decision, err := ParseModelDecision(DecisionEnvelope{
		Kind:       domain.DecisionToolCall,
		ToolName:   toolName("lookup"),
		Arguments:  json.RawMessage(`{"key":"ember"}`),
		StatePatch: json.RawMessage(`{"lookups":1}`),
	})
	if err != nil {
		t.Fatalf("ParseModelDecision() error = %v, want nil", err)
	}
	if decision.Kind != domain.DecisionToolCall {
		t.Fatalf("Kind = %q, want %q", decision.Kind, domain.DecisionToolCall)
	}
	if decision.ToolName == nil || *decision.ToolName != "lookup" {
		t.Fatalf("ToolName = %v, want \"lookup\"", decision.ToolName)
	}
	if string(decision.Arguments) != `{"key":"ember"}` {
		t.Fatalf("Arguments = %s, want the model's arguments unchanged", decision.Arguments)
	}
	if string(decision.StatePatch) != `{"lookups":1}` {
		t.Fatalf("StatePatch = %s, want the model's patch unchanged", decision.StatePatch)
	}
	if len(decision.Output) != 0 {
		t.Fatalf("Output = %s, want empty for a TOOL_CALL", decision.Output)
	}
}

func TestParseModelDecision_StatePatchNotObject_Fails(t *testing.T) {
	_, err := ParseModelDecision(DecisionEnvelope{
		Kind:       domain.DecisionFinal,
		Output:     json.RawMessage(`"done"`),
		StatePatch: json.RawMessage(`[1,2]`),
	})
	if err == nil {
		t.Fatal("ParseModelDecision() error = nil, want error for a non-object statePatch")
	}
	if _, ok := AsInvalidActionError(err); !ok {
		t.Fatalf("ParseModelDecision() error = %v, want an *InvalidActionError", err)
	}
}

func TestValidateToolCall_ToolNotInAllowlist_Fails(t *testing.T) {
	decision := domain.AgentDecision{
		Kind:      domain.DecisionToolCall,
		ToolName:  toolName("lookup"),
		Arguments: json.RawMessage(`{"key":"ember"}`),
	}
	err := ValidateToolCall(decision, []string{"other_tool"}, lookupMetadata())
	if err == nil {
		t.Fatal("ValidateToolCall() error = nil, want error for a Tool outside the frozen allowlist")
	}
	if _, ok := AsInvalidActionError(err); !ok {
		t.Fatalf("ValidateToolCall() error = %v, want an *InvalidActionError", err)
	}
}

func TestValidateToolCall_ArgumentsViolateSchema_Fails(t *testing.T) {
	decision := domain.AgentDecision{
		Kind:      domain.DecisionToolCall,
		ToolName:  toolName("lookup"),
		Arguments: json.RawMessage(`{"unknown":"ember"}`),
	}
	err := ValidateToolCall(decision, []string{"lookup"}, lookupMetadata())
	if err == nil {
		t.Fatal("ValidateToolCall() error = nil, want error for arguments the Tool InputSchema rejects")
	}
	if _, ok := AsInvalidActionError(err); !ok {
		t.Fatalf("ValidateToolCall() error = %v, want an *InvalidActionError", err)
	}
}

func TestValidateToolCall_AllowedToolWithValidArguments_Passes(t *testing.T) {
	decision := domain.AgentDecision{
		Kind:      domain.DecisionToolCall,
		ToolName:  toolName("lookup"),
		Arguments: json.RawMessage(`{"key":"ember"}`),
	}
	if err := ValidateToolCall(decision, []string{"lookup"}, lookupMetadata()); err != nil {
		t.Fatalf("ValidateToolCall() error = %v, want nil", err)
	}
}

func TestValidateFinalOutput_NoSchema_RequiresString(t *testing.T) {
	if err := ValidateFinalOutput(json.RawMessage(`"the answer"`), nil); err != nil {
		t.Fatalf("ValidateFinalOutput(string, no schema) error = %v, want nil", err)
	}
	err := ValidateFinalOutput(json.RawMessage(`{"answer":"42"}`), nil)
	if err == nil {
		t.Fatal("ValidateFinalOutput(object, no schema) error = nil, want error: the MVP agent output port is text")
	}
	if _, ok := AsInvalidActionError(err); !ok {
		t.Fatalf("ValidateFinalOutput() error = %v, want an *InvalidActionError", err)
	}
}

func TestValidateFinalOutput_SchemaViolation_Fails(t *testing.T) {
	schema := json.RawMessage(`{"type":"string","minLength":3}`)
	if err := ValidateFinalOutput(json.RawMessage(`"long enough"`), schema); err != nil {
		t.Fatalf("ValidateFinalOutput() error = %v, want nil for an output the schema accepts", err)
	}
	err := ValidateFinalOutput(json.RawMessage(`"no"`), schema)
	if err == nil {
		t.Fatal("ValidateFinalOutput() error = nil, want error for an output the frozen OutputSchema rejects")
	}
	if _, ok := AsInvalidActionError(err); !ok {
		t.Fatalf("ValidateFinalOutput() error = %v, want an *InvalidActionError", err)
	}
}

func TestParseModelDecision_ChecksOnlyTheBasicShape(t *testing.T) {
	// The model result transaction confirms the envelope shape only. A Tool
	// outside the allowlist and arguments the Tool InputSchema rejects must still commit
	// as a Decision, so that ValidateToolCall fails them at execution time as
	// INVALID_ACTION instead of the Decision never existing.
	decision, err := ParseModelDecision(DecisionEnvelope{
		Kind:      domain.DecisionToolCall,
		ToolName:  toolName("not_registered"),
		Arguments: json.RawMessage(`{"unknown":1}`),
	})
	if err != nil {
		t.Fatalf("ParseModelDecision() error = %v, want nil: shape is the only commit-time check", err)
	}
	if err := ValidateToolCall(decision, []string{"lookup"}, lookupMetadata()); err == nil {
		t.Fatal("ValidateToolCall() error = nil, want the deferred allowlist failure")
	}
}

func TestParseModelDecision_MalformedEnvelope_Fails(t *testing.T) {
	cases := map[string]DecisionEnvelope{
		"unknown kind": {Kind: domain.DecisionKind("RETRY"), Output: json.RawMessage(`"done"`)},
		"empty kind":   {Output: json.RawMessage(`"done"`)},
		"arguments are not JSON": {
			Kind:      domain.DecisionToolCall,
			ToolName:  toolName("lookup"),
			Arguments: json.RawMessage(`{"key":`),
		},
		"output is not JSON": {Kind: domain.DecisionFinal, Output: json.RawMessage(`{`)},
		"output carries trailing content": {
			Kind:   domain.DecisionFinal,
			Output: json.RawMessage(`"done" "again"`),
		},
		"FINAL without output":     {Kind: domain.DecisionFinal},
		"TOOL_CALL without a tool": {Kind: domain.DecisionToolCall, Arguments: json.RawMessage(`{}`)},
		"state patch is not JSON": {
			Kind:       domain.DecisionFinal,
			Output:     json.RawMessage(`"done"`),
			StatePatch: json.RawMessage(`{`),
		},
	}
	for name, envelope := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseModelDecision(envelope)
			if err == nil {
				t.Fatal("ParseModelDecision() error = nil, want error for a malformed envelope")
			}
			if _, ok := AsInvalidActionError(err); !ok {
				t.Fatalf("ParseModelDecision() error = %v, want an *InvalidActionError", err)
			}
		})
	}
}

func TestValidateToolCall_ResolvedToolDoesNotMatch_Fails(t *testing.T) {
	decision := domain.AgentDecision{
		Kind:      domain.DecisionToolCall,
		ToolName:  toolName("lookup"),
		Arguments: json.RawMessage(`{"key":"ember"}`),
	}
	other := lookupMetadata()
	other.Name = "other_tool"
	err := ValidateToolCall(decision, []string{"lookup"}, other)
	if err == nil {
		t.Fatal("ValidateToolCall() error = nil, want error when the resolved Tool is not the one the decision named")
	}
	if _, ok := AsInvalidActionError(err); !ok {
		t.Fatalf("ValidateToolCall() error = %v, want an *InvalidActionError", err)
	}
}

// TestValidateToolResult covers the execution-time check of a real Tool call's result
// against the Tool's own registered OutputSchema: a result the contract rejects is a Tool
// failure, and a Tool that registers no OutputSchema still owes valid JSON.
func TestValidateToolResult(t *testing.T) {
	schema := lookupMetadata().OutputSchema

	cases := map[string]struct {
		result  json.RawMessage
		schema  json.RawMessage
		wantErr bool
	}{
		"satisfies the schema":  {result: json.RawMessage(`{"key":"ember"}`), schema: schema},
		"violates the schema":   {result: json.RawMessage(`["ember"]`), schema: schema, wantErr: true},
		"no schema, valid JSON": {result: json.RawMessage(`"ember"`)},
		"no result":             {schema: schema, wantErr: true},
		"json null result":      {result: json.RawMessage(`null`), schema: schema, wantErr: true},
		"invalid JSON":          {result: json.RawMessage(`{`), wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateToolResult(tc.result, tc.schema)
			if tc.wantErr != (err != nil) {
				t.Fatalf("ValidateToolResult() error = %v, wantErr = %v", err, tc.wantErr)
			}
			if err == nil {
				return
			}
			// A rejected Tool result is not an invalid Agent Action: the model's Decision
			// was valid and the Tool was allowed to run.
			if _, ok := AsInvalidActionError(err); ok {
				t.Fatalf("ValidateToolResult() error = %v, want a plain Tool failure, not an *InvalidActionError", err)
			}
		})
	}
}
