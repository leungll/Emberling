package domain

import (
	"encoding/json"
	"testing"
)

// The Decision envelope rules come from 05 §1.8 "Decision 与 Action：模型决定和待执行工作":
// a TOOL_CALL carries a Tool Name and arguments and no output, a FINAL carries an output
// and no Tool call. The database CHECK constraint on agent_decisions enforces the same
// shape; Validate lets the Runtime reject a malformed Decision as INVALID_ACTION before
// the insert rather than after a constraint violation.

func TestAgentDecision_Validate_ToolCall_Accepted(t *testing.T) {
	name := "lookup"
	decision := AgentDecision{
		Kind:      DecisionToolCall,
		ToolName:  &name,
		Arguments: json.RawMessage(`{"q":"ember"}`),
	}
	if err := decision.Validate(); err != nil {
		t.Fatalf("Validate() on a well-formed TOOL_CALL: want nil, got %v", err)
	}
}

func TestAgentDecision_Validate_Final_Accepted(t *testing.T) {
	decision := AgentDecision{
		Kind:   DecisionFinal,
		Output: json.RawMessage(`"done"`),
	}
	if err := decision.Validate(); err != nil {
		t.Fatalf("Validate() on a well-formed FINAL: want nil, got %v", err)
	}
}

func TestAgentDecision_Validate_UnknownKind_Rejected(t *testing.T) {
	decision := AgentDecision{Kind: DecisionKind("RETRY"), Output: json.RawMessage(`"done"`)}
	if err := decision.Validate(); err == nil {
		t.Fatal("Validate() on an unknown kind: want error, got nil")
	}
}

func TestAgentDecision_Validate_ToolCallWithoutToolName_Rejected(t *testing.T) {
	empty := ""
	for name, decision := range map[string]AgentDecision{
		"nil tool name":   {Kind: DecisionToolCall, Arguments: json.RawMessage(`{}`)},
		"empty tool name": {Kind: DecisionToolCall, ToolName: &empty, Arguments: json.RawMessage(`{}`)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := decision.Validate(); err == nil {
				t.Fatal("Validate() on a TOOL_CALL without a Tool Name: want error, got nil")
			}
		})
	}
}

func TestAgentDecision_Validate_ToolCallWithoutArguments_Rejected(t *testing.T) {
	name := "lookup"
	decision := AgentDecision{Kind: DecisionToolCall, ToolName: &name}
	if err := decision.Validate(); err == nil {
		t.Fatal("Validate() on a TOOL_CALL without arguments: want error, got nil")
	}
}

func TestAgentDecision_Validate_ToolCallWithOutput_Rejected(t *testing.T) {
	name := "lookup"
	decision := AgentDecision{
		Kind:      DecisionToolCall,
		ToolName:  &name,
		Arguments: json.RawMessage(`{}`),
		Output:    json.RawMessage(`"done"`),
	}
	if err := decision.Validate(); err == nil {
		t.Fatal("Validate() on a TOOL_CALL carrying a Final output: want error, got nil")
	}
}

func TestAgentDecision_Validate_FinalWithoutOutput_Rejected(t *testing.T) {
	decision := AgentDecision{Kind: DecisionFinal}
	if err := decision.Validate(); err == nil {
		t.Fatal("Validate() on a FINAL without output: want error, got nil")
	}
}

func TestAgentDecision_Validate_FinalWithToolCall_Rejected(t *testing.T) {
	name := "lookup"
	for label, decision := range map[string]AgentDecision{
		"tool name": {Kind: DecisionFinal, Output: json.RawMessage(`"done"`), ToolName: &name},
		"arguments": {Kind: DecisionFinal, Output: json.RawMessage(`"done"`), Arguments: json.RawMessage(`{}`)},
	} {
		t.Run(label, func(t *testing.T) {
			if err := decision.Validate(); err == nil {
				t.Fatalf("Validate() on a FINAL carrying %s: want error, got nil", label)
			}
		})
	}
}

// Only FINAL_RESPONSE ends an Agent Run successfully (05 §1.8: "只有 `FINAL_RESPONSE`
// 表示成功"); every other termination fails the Agent NodeRun.
func TestAgentTermination_IsSuccess_OnlyFinalResponse(t *testing.T) {
	if !TerminationFinalResponse.IsSuccess() {
		t.Error("FINAL_RESPONSE.IsSuccess() = false, want true")
	}
	for _, termination := range []AgentTermination{
		TerminationMaxTurns, TerminationTimeout, TerminationModelError,
		TerminationToolError, TerminationInvalidAction,
	} {
		if termination.IsSuccess() {
			t.Errorf("%s.IsSuccess() = true, want false", termination)
		}
	}
}
