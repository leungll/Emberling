package service

import (
	"context"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// RunCost is a Run's read-time cost: the charges its external calls reported, summed per
// currency, and the number of finished external calls whose charge is not known. It is
// derived from the Costs recorded beside the call results in the same read as the Run
// rows, is never persisted, and never feeds an execution decision. A total is exact
// decimal text, not a float. An unknown charge is never estimated into a total, and a
// reported zero is a known charge, not an unknown one.
type RunCost struct {
	Totals       []CostTotal
	UnknownCalls int
}

// CostTotal is the sum of the known charges reported in one currency.
type CostTotal struct {
	Amount   string
	Currency string
}

// runCostsOf reads the finished calls of the given Runs and reduces them to one RunCost
// per Run. Every Run gets an entry, so a Run that made no external call reports no totals
// and no unknown calls.
func runCostsOf(ctx context.Context, repo store.RunListRepository, tools *registry.ToolRegistry, runIDs []string) (map[string]RunCost, error) {
	costs := make(map[string]RunCost, len(runIDs))
	if len(runIDs) == 0 {
		return costs, nil
	}
	calls, err := repo.FinishedCalls(ctx, runIDs)
	if err != nil {
		return nil, err
	}
	for _, runID := range runIDs {
		costs[runID] = runCostOf(calls[runID], tools)
	}
	return costs, nil
}

// runCostOf keeps the finished calls that reached an external Provider and sums them.
func runCostOf(calls []store.FinishedCall, tools *registry.ToolRegistry) RunCost {
	var charged []*domain.Cost
	for _, call := range calls {
		if isExternalCall(call, tools) {
			charged = append(charged, call.Cost)
		}
	}
	summary := runtime.SumCosts(charged)
	cost := RunCost{UnknownCalls: summary.UnknownCalls}
	for _, total := range summary.Totals {
		cost.Totals = append(cost.Totals, CostTotal{Amount: total.Amount, Currency: total.Currency})
	}
	return cost
}

// isExternalCall decides whether a finished call is one whose charge the Run cost
// accounts for. A call that recorded a Cost did reach a Provider. Otherwise an Agent Turn
// always counts, since every Turn is one model call; a Node Attempt counts when it
// dispatched a task to a Provider or failed without knowing whether the Provider acted;
// and a Tool Attempt counts when its registered side effect is EXTERNAL, when its Tool
// reviews with the Agent Run's review model and the Agent Run froze one, since that
// review is one call to a billed model however it ended, or when its Tool is no longer
// registered and the question cannot be answered, so the call is reported as unknown
// rather than silently dropped. A Node or Tool call that never left the process, which
// includes a review the local mock policy decided, has no Provider charge and is not an
// unknown one.
func isExternalCall(call store.FinishedCall, tools *registry.ToolRegistry) bool {
	if call.Cost != nil {
		return true
	}
	switch call.Kind {
	case store.FinishedAgentTurn:
		return true
	case store.FinishedNodeAttempt:
		return call.Dispatched || call.Uncertain
	case store.FinishedToolAttempt:
		if tools == nil {
			return true
		}
		reg, ok := tools.Get(call.ToolName)
		if !ok || reg.Metadata.SideEffect.Kind == domain.SideEffectExternal {
			return true
		}
		return reg.Metadata.UsesReviewModel && call.ReviewModelID != ""
	default:
		return false
	}
}
