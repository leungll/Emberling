package textgeneration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// TestTextGenerationExecutor_Execute_ForwardsModelCost_AndNilWhenUnreported: the charge
// the model Provider reported travels on the output unchanged, and a Provider that
// reported none leaves the output without a Cost rather than inventing a zero.
func TestTextGenerationExecutor_Execute_ForwardsModelCost_AndNilWhenUnreported(t *testing.T) {
	in := registry.NodeInput{Ports: map[string]json.RawMessage{"prompt": json.RawMessage(`"hello"`)}}
	config := map[string]any{"modelId": "text-model-v1"}

	reported := finalResponse("echo: hello")
	reported.Cost = &domain.Cost{Amount: "0.000032", Currency: domain.CurrencyUSD, Source: "text-model-v1"}
	e := Executor{resolver: stubResolver{model: validModel(), provider: &stubModelProvider{response: reported}, ok: true}}
	result, err := e.Execute(context.Background(), in, config)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if diff := cmp.Diff(reported.Cost, result.Output.Cost); diff != "" {
		t.Fatalf("output cost (-want +got):\n%s", diff)
	}

	e = Executor{resolver: stubResolver{model: validModel(), provider: &stubModelProvider{response: finalResponse("echo: hello")}, ok: true}}
	result, err = e.Execute(context.Background(), in, config)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if result.Output.Cost != nil {
		t.Fatalf("output cost = %+v, want nil when the Provider reported none", *result.Output.Cost)
	}
}
