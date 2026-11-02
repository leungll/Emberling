package reviewasset

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// TestExecute_ReviewModelReportsCharge_ToolResultCarriesItForTheReviewModel: the charge
// the review model's Adapter normalised is carried on the Tool result, attributed to the
// review Model ID the call was made with, so the Tool Attempt records a figure that
// traces back to the frozen review model and not to whatever name the Adapter used.
func TestExecute_ReviewModelReportsCharge_ToolResultCarriesItForTheReviewModel(t *testing.T) {
	model := visionModel(`{"passed":true,"reasons":["Clay look preserved."]}`)
	model.cost = &domain.Cost{Amount: "0.0068", Currency: domain.CurrencyUSD, Source: "adapter-upstream-name"}

	got, err := modelExecutor(model, nil).Execute(context.Background(), modelAction(modelArguments))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Kind != registry.ToolResultCompleted || got.Result == nil {
		t.Fatalf("result = %+v, want a completed result", got)
	}
	want := &domain.Cost{Amount: "0.0068", Currency: domain.CurrencyUSD, Source: testReviewModel}
	if diff := cmp.Diff(want, got.Result.Cost); diff != "" {
		t.Fatalf("result cost (-want +got):\n%s", diff)
	}
}

// TestExecute_ReviewModelReportsNoCharge_ToolResultCostIsNil: a review model whose
// Adapter reported no charge leaves the result without a Cost, never with a zero, and the
// review itself still succeeds.
func TestExecute_ReviewModelReportsNoCharge_ToolResultCostIsNil(t *testing.T) {
	model := visionModel(`{"passed":true,"reasons":[]}`)

	got, err := modelExecutor(model, nil).Execute(context.Background(), modelAction(modelArguments))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Result == nil || got.Result.Cost != nil {
		t.Fatalf("result = %+v, want a completed result without a cost", got)
	}
}

// TestExecute_MockPolicy_ToolResultCostIsNil: the mock policy calls no model, so there is
// nothing to charge and the result carries no Cost.
func TestExecute_MockPolicy_ToolResultCostIsNil(t *testing.T) {
	got, err := Executor{}.Execute(context.Background(), action(modelArguments))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Result == nil || got.Result.Cost != nil {
		t.Fatalf("result = %+v, want a completed result without a cost", got)
	}
}
