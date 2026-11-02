package mockmodel

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// TestProvider_Generate_DefaultScenario_ReportsKnownZeroCost: the fixture costs nothing
// and says so, as a Cost with amount "0" attributed to the requested Model ID, for every
// Decision-returning Scenario.
func TestProvider_Generate_DefaultScenario_ReportsKnownZeroCost(t *testing.T) {
	cases := []Scenario{
		{Kind: ScenarioFinal, Output: "done"},
		{Kind: ScenarioToolCall, ToolName: "lookup"},
		{Kind: ScenarioInvalidDecision},
	}
	for _, scenario := range cases {
		t.Run(string(scenario.Kind), func(t *testing.T) {
			p := NewProvider()
			p.Script = func(registry.ModelRequest) *Scenario { return &scenario }
			request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, "one two")}}

			response, err := p.Generate(context.Background(), request)
			if err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			want := &domain.Cost{Amount: "0", Currency: domain.CurrencyUSD, Source: ModelID}
			if diff := cmp.Diff(want, response.Cost); diff != "" {
				t.Fatalf("Cost (-want +got):\n%s", diff)
			}
		})
	}
}

// TestProvider_Generate_ScenarioOmitCost_NormalisesToNil: a Provider that reports no
// charge yields nil, never a zeroed Cost, and the rest of the envelope is unaffected.
func TestProvider_Generate_ScenarioOmitCost_NormalisesToNil(t *testing.T) {
	p := NewProvider()
	scenario := Scenario{Kind: ScenarioFinal, Output: "done", OmitCost: true}
	p.Script = func(registry.ModelRequest) *Scenario { return &scenario }
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{userMessage(t, "one two")}}

	response, err := p.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if response.Cost != nil {
		t.Fatalf("Cost = %+v, want nil: the Provider reported no charge", *response.Cost)
	}
	if response.TokenUsage == nil {
		t.Fatalf("TokenUsage = nil, want the usual usage: omitting the cost changes nothing else")
	}
}
