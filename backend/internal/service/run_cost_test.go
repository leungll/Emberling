package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/store"
)

// costTestTool is an Executor that is never called: the cost read only consults Metadata.
type costTestTool struct{}

func (costTestTool) Execute(context.Context, registry.ToolAction) (registry.ToolExecutionResult, error) {
	return registry.ToolExecutionResult{}, nil
}

func costToolRegistry(t *testing.T) *registry.ToolRegistry {
	t.Helper()
	reg := registry.NewToolRegistry()
	for name, kind := range map[string]domain.SideEffectKind{
		"draw_image":   domain.SideEffectExternal,
		"read_fact":    domain.SideEffectNone,
		"review_image": domain.SideEffectNone,
	} {
		idempotency := domain.IdempotencyUnknown
		if kind == domain.SideEffectNone {
			idempotency = domain.IdempotencySafe
		}
		err := reg.Register(registry.ToolRegistration{
			Metadata: domain.ToolMetadata{
				Name:          name,
				ExecutionKind: domain.ToolExecutionSync,
				InputSchema:   json.RawMessage(`{"type":"object"}`),
				OutputSchema:  json.RawMessage(`{"type":"object"}`),
				SideEffect:    domain.SideEffectPolicy{Kind: kind, Idempotency: idempotency},
				// review_image reads only, but asks the Agent Run's review model when one
				// was frozen, the way a review Tool does.
				UsesReviewModel:       name == "review_image",
				ReviewModelCapability: reviewCapabilityIf(name == "review_image"),
			},
			Executor: costTestTool{},
		})
		if err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	return reg
}

func usd(amount string) *domain.Cost {
	return &domain.Cost{Amount: amount, Currency: domain.CurrencyUSD, Source: "model-v1"}
}

// TestRunCostOf_UncertainAndUnreportedExternalCalls_CountAsUnknownNotZero: a finished call
// that reached, or may have reached, a Provider without a recorded charge is counted as
// unknown; nothing enters the total for it.
func TestRunCostOf_UncertainAndUnreportedExternalCalls_CountAsUnknownNotZero(t *testing.T) {
	tools := costToolRegistry(t)
	cases := map[string]store.FinishedCall{
		"uncertain node attempt":       {Kind: store.FinishedNodeAttempt, Uncertain: true},
		"dispatched node attempt":      {Kind: store.FinishedNodeAttempt, Dispatched: true},
		"turn without a reported cost": {Kind: store.FinishedAgentTurn},
		"external tool without a cost": {Kind: store.FinishedToolAttempt, ToolName: "draw_image"},
		"uncertain external tool":      {Kind: store.FinishedToolAttempt, ToolName: "draw_image", Uncertain: true},
		"tool no longer registered":    {Kind: store.FinishedToolAttempt, ToolName: "retired_tool"},
		"review with a model, no cost": {Kind: store.FinishedToolAttempt, ToolName: "review_image", ReviewModelID: "vision-model-v1"},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			got := runCostOf([]store.FinishedCall{call}, tools)
			want := RunCost{UnknownCalls: 1}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Fatalf("run cost (-want +got):\n%s", diff)
			}
		})
	}
}

// TestRunCostOf_CallsThatNeverLeftTheProcess_AreNeitherChargedNorUnknown: a Node Attempt
// that dispatched nothing and a Tool whose registered side effect is NONE have no Provider
// charge to be unknown about. A review Tool whose Agent Run froze no review model decided
// with its local policy, and a NONE Tool that does not review with a model never asks one,
// whatever the Agent Run froze.
func TestRunCostOf_CallsThatNeverLeftTheProcess_AreNeitherChargedNorUnknown(t *testing.T) {
	tools := costToolRegistry(t)
	calls := []store.FinishedCall{
		{Kind: store.FinishedNodeAttempt},
		{Kind: store.FinishedToolAttempt, ToolName: "read_fact"},
		{Kind: store.FinishedToolAttempt, ToolName: "read_fact", ReviewModelID: "vision-model-v1"},
		{Kind: store.FinishedToolAttempt, ToolName: "review_image"},
	}
	if diff := cmp.Diff(RunCost{}, runCostOf(calls, tools)); diff != "" {
		t.Fatalf("run cost (-want +got):\n%s", diff)
	}
}

// TestRunCostOf_SumsKnownChargesAcrossAttemptsAndTurns_KeepsZeroKnownAndUnknownApart: the
// Run total is the exact decimal sum of every recorded charge, whatever table it came
// from, grouped per currency; a reported zero adds nothing and is not unknown; the
// unknown count sits beside the totals, never inside them. A recorded charge on a Tool
// whose side effect is NONE still counts: the Provider said it charged.
func TestRunCostOf_SumsKnownChargesAcrossAttemptsAndTurns_KeepsZeroKnownAndUnknownApart(t *testing.T) {
	tools := costToolRegistry(t)
	eur := &domain.Cost{Amount: "1.5", Currency: "EUR", Source: "other-model"}
	calls := []store.FinishedCall{
		{Kind: store.FinishedNodeAttempt, Dispatched: true, Cost: usd("0.04")},
		{Kind: store.FinishedNodeAttempt, Dispatched: true, Cost: usd("0.04")},
		{Kind: store.FinishedNodeAttempt, Uncertain: true},
		{Kind: store.FinishedAgentTurn, Cost: usd("0")},
		{Kind: store.FinishedAgentTurn, Cost: usd("0.000032")},
		{Kind: store.FinishedAgentTurn},
		{Kind: store.FinishedToolAttempt, ToolName: "draw_image", Cost: usd("0.004")},
		{Kind: store.FinishedToolAttempt, ToolName: "read_fact", Cost: eur},
	}
	want := RunCost{
		Totals:       []CostTotal{{Amount: "1.5", Currency: "EUR"}, {Amount: "0.084032", Currency: domain.CurrencyUSD}},
		UnknownCalls: 2,
	}
	if diff := cmp.Diff(want, runCostOf(calls, tools)); diff != "" {
		t.Fatalf("run cost (-want +got):\n%s", diff)
	}
}

// TestRunCostOf_NoFinishedCalls_ReportsNothing: a Run with no finished call has no total
// and no unknown call, which the projection renders as an empty list and zero.
func TestRunCostOf_NoFinishedCalls_ReportsNothing(t *testing.T) {
	if diff := cmp.Diff(RunCost{}, runCostOf(nil, costToolRegistry(t))); diff != "" {
		t.Fatalf("run cost (-want +got):\n%s", diff)
	}
}

// reviewCapabilityIf names the vision input capability for a review Tool, and nothing for
// a Tool that reviews with no model.
func reviewCapabilityIf(reviews bool) string {
	if reviews {
		return domain.ModelCapabilityVisionInput
	}
	return ""
}
