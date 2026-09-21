// These two tests pin this fixture to the Runtime rule it exists to exercise: the basic
// Decision shape check the model result transaction performs (07 §1.4). They live beside
// the Provider rather than in runtime so that changing a Scenario's envelope fails here,
// where the fixture is defined. Importing internal/runtime from a _test.go file does not
// widen this Adapter's production dependencies (test/architecture/dependencies_test.go
// checks non-test imports only).
package mockmodel

import (
	"context"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
)

func envelope(decision registry.ModelDecision) runtime.DecisionEnvelope {
	return runtime.DecisionEnvelope{
		Kind:       domain.DecisionKind(decision.Kind),
		ToolName:   decision.ToolName,
		Arguments:  decision.Arguments,
		Output:     decision.Output,
		StatePatch: decision.StatePatch,
	}
}

func TestProvider_ScenarioToolCall_DecisionPassesBasicShapeCheck(t *testing.T) {
	p := NewProvider()
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, "mock:tool-call:lookup")}}
	response, err := p.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("Generate() error = %v, want nil", err)
	}
	decision, err := runtime.ParseModelDecision(envelope(response.Decision))
	if err != nil {
		t.Fatalf("ParseModelDecision() error = %v, want nil: ScenarioToolCall must commit as a Decision", err)
	}
	if decision.ToolName == nil || *decision.ToolName != "lookup" {
		t.Fatalf("ToolName = %v, want \"lookup\"", decision.ToolName)
	}
}

func TestProvider_ScenarioInvalidDecision_FailsBasicShapeCheck(t *testing.T) {
	p := NewProvider()
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, "mock:invalid-decision")}}
	response, err := p.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("Generate() error = %v, want nil: an invalid envelope is normalised, not a Provider failure", err)
	}
	_, err = runtime.ParseModelDecision(envelope(response.Decision))
	if err == nil {
		t.Fatal("ParseModelDecision() error = nil, want the INVALID_ACTION path for ScenarioInvalidDecision")
	}
	if _, ok := runtime.AsInvalidActionError(err); !ok {
		t.Fatalf("ParseModelDecision() error = %v, want an *runtime.InvalidActionError", err)
	}
}
