//go:build integration

// Run cost read path against PostgreSQL: the finished calls of a Run are read from the
// three call tables through their NodeRun, and the Run list reduces them to one exact
// per-currency total plus the count of external calls whose charge is not known.
package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// costReviewModelID is the review Model ID the seeded Agent Run froze.
const costReviewModelID = "vision-model-v1"

type costNoopTool struct{}

func (costNoopTool) Execute(context.Context, registry.ToolAction) (registry.ToolExecutionResult, error) {
	return registry.ToolExecutionResult{}, nil
}

// costToolRegistry registers one EXTERNAL Tool, one NONE Tool and one NONE Tool that
// reviews with the Agent Run's review model, so the read can tell a Tool call that reached
// a Provider from one that never left the process.
func costToolRegistry(t *testing.T) *registry.ToolRegistry {
	t.Helper()
	reg := registry.NewToolRegistry()
	policies := map[string]domain.SideEffectPolicy{
		"draw_image":   {Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown},
		"read_fact":    {Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		"review_image": {Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
	}
	for name, policy := range policies {
		err := reg.Register(registry.ToolRegistration{
			Metadata: domain.ToolMetadata{
				Name:            name,
				ExecutionKind:   domain.ToolExecutionSync,
				InputSchema:     json.RawMessage(`{"type":"object"}`),
				OutputSchema:    json.RawMessage(`{"type":"object"}`),
				SideEffect:      policy,
				UsesReviewModel: name == "review_image",
			},
			Executor: costNoopTool{},
		})
		if err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	return reg
}

// seedCostedRun builds one COMPLETED Run whose image NodeRun made three Attempts and whose
// Agent NodeRun, which froze the review model costReviewModelID, made three Turns and four
// Tool calls, each with a different cost fact:
//
//	image Attempt 1  dispatched, FAILED uncertain, no cost  -> unknown
//	image Attempt 2  dispatched, SUCCEEDED, 0.04 USD        -> charged
//	image Attempt 3  never dispatched, SUCCEEDED, no cost   -> not an external call
//	Turn 1           COMPLETED, 0 USD                       -> known zero
//	Turn 2           COMPLETED, 0.000032 USD                -> charged
//	Turn 3           FAILED, no cost                        -> unknown
//	Tool call a      draw_image (EXTERNAL), 0.004 USD       -> charged
//	Tool call b      draw_image (EXTERNAL), no cost         -> unknown
//	Tool call c      read_fact (NONE), no cost              -> not an external call
//	Tool call d      review_image (NONE, review model), no cost -> unknown
func seedCostedRun(ctx context.Context, t *testing.T, uow store.UnitOfWork, runID string) {
	t.Helper()
	seedListRun(ctx, t, uow, listRunSpec{
		id: runID, workflow: "wf_cost", status: domain.RunCompleted, startedAt: fixtureTime,
		nodes: []listNode{
			{nodeRunID: runID + "_nr_image", nodeID: "node_draft", status: domain.NodeRunSucceeded},
			{nodeRunID: runID + "_nr_agent", nodeID: "node_render", status: domain.NodeRunSucceeded},
		},
	})
	usd := func(amount, source string) *domain.Cost {
		return &domain.Cost{Amount: amount, Currency: domain.CurrencyUSD, Source: source}
	}
	imageRun, agentNodeRun := runID+"_nr_image", runID+"_nr_agent"
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		attempts := tx.NodeAttempts()
		for no, id := range []string{runID + "_na1", runID + "_na2", runID + "_na3"} {
			if err := attempts.Create(ctx, newNodeAttempt(id, imageRun, no+1)); err != nil {
				return err
			}
		}
		if err := attempts.MarkDispatched(ctx, runID+"_na1", fixtureTime, nil); err != nil {
			return err
		}
		if err := attempts.MarkFailed(ctx, runID+"_na1", domain.NodeAttemptDispatched, fixtureTime,
			domain.ExecutionError{Code: "PROVIDER_LOST", Message: "no result"}, true); err != nil {
			return err
		}
		if err := attempts.MarkDispatched(ctx, runID+"_na2", fixtureTime, nil); err != nil {
			return err
		}
		if err := attempts.MarkSucceeded(ctx, runID+"_na2", domain.NodeAttemptDispatched, fixtureTime,
			json.RawMessage(`{"image":"ok"}`), usd("0.04", "image-model-v1")); err != nil {
			return err
		}
		if err := attempts.MarkSucceeded(ctx, runID+"_na3", domain.NodeAttemptStarted, fixtureTime,
			json.RawMessage(`{"text":"ok"}`), nil); err != nil {
			return err
		}

		agentRunID := runID + "_ar"
		agentRun := newAgentRun(agentRunID, agentNodeRun)
		reviewModelID := costReviewModelID
		agentRun.ReviewModelID = &reviewModelID
		if err := tx.AgentRuns().Create(ctx, agentRun); err != nil {
			return err
		}
		turns := []struct {
			id   string
			cost *domain.Cost
			fail bool
		}{
			{id: runID + "_t1", cost: usd("0", "text-model-v1")},
			{id: runID + "_t2", cost: usd("0.000032", "text-model-v1")},
			{id: runID + "_t3", fail: true},
		}
		for no, turn := range turns {
			if err := tx.AgentTurns().Create(ctx, newAgentTurn(turn.id, agentRunID, no+1)); err != nil {
				return err
			}
			if _, err := tx.AgentTurns().ClaimReady(ctx, turn.id, fixtureTime); err != nil {
				return err
			}
			if turn.fail {
				if err := tx.AgentTurns().MarkFailed(ctx, turn.id, fixtureTime, domain.ExecutionError{Code: "MODEL_ERROR", Message: "x"}); err != nil {
					return err
				}
				continue
			}
			if err := tx.AgentTurns().MarkCompleted(ctx, turn.id, fixtureTime, json.RawMessage(`{"kind":"TOOL_CALL"}`), nil, turn.cost); err != nil {
				return err
			}
		}
		// One Decision and Action per completed Turn; the three Tool calls hang off them.
		if err := tx.AgentDecisions().Create(ctx, newAgentDecision(runID+"_d1", runID+"_t1")); err != nil {
			return err
		}
		if err := tx.AgentActions().Create(ctx, newAgentAction(runID+"_a1", runID+"_t1", runID+"_d1")); err != nil {
			return err
		}
		if err := tx.AgentDecisions().Create(ctx, newAgentDecision(runID+"_d2", runID+"_t2")); err != nil {
			return err
		}
		if err := tx.AgentActions().Create(ctx, newAgentAction(runID+"_a2", runID+"_t2", runID+"_d2")); err != nil {
			return err
		}
		toolCalls := []struct {
			id, action, tool string
			no               int
			cost             *domain.Cost
		}{
			{id: runID + "_ta", action: runID + "_a1", tool: "draw_image", no: 1, cost: usd("0.004", "image-model-v1")},
			{id: runID + "_tb", action: runID + "_a1", tool: "draw_image", no: 2},
			{id: runID + "_tc", action: runID + "_a2", tool: "read_fact", no: 1},
			{id: runID + "_td", action: runID + "_a2", tool: "review_image", no: 2},
		}
		for _, call := range toolCalls {
			attempt := newToolAttempt(call.id, call.action, call.no)
			attempt.ToolName = call.tool
			if err := tx.ToolAttempts().Create(ctx, attempt); err != nil {
				return err
			}
			if err := tx.ToolAttempts().MarkSucceeded(ctx, call.id, domain.ToolAttemptStarted, fixtureTime,
				json.RawMessage(`{"ok":true}`), call.cost); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed costed run %s: %v", runID, err)
	}
}

// TestRunList_FinishedCalls_ReadsEveryCallTableThroughItsNodeRun pins the store read: one
// row per finished Attempt or Turn of the requested Runs, each with its recorded Cost in
// canonical form, each Tool call with the review Model ID its Agent Run froze, and
// nothing from an unrelated Run.
func TestRunList_FinishedCalls_ReadsEveryCallTableThroughItsNodeRun(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	saveDefinition(ctx, t, uow, listDefinition("wf_cost", 1, "Agent"))
	seedCostedRun(ctx, t, uow, "run_costed")
	seedCostedRun(ctx, t, uow, "run_other")

	var calls map[string][]store.FinishedCall
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		calls, err = tx.RunList().FinishedCalls(ctx, []string{"run_costed", "run_none"})
		return err
	}); err != nil {
		t.Fatalf("FinishedCalls: %v", err)
	}
	if _, present := calls["run_other"]; present {
		t.Errorf("FinishedCalls returned rows of a Run that was not asked for")
	}
	if _, present := calls["run_none"]; present {
		t.Errorf("FinishedCalls returned an entry for a Run with no finished call")
	}
	got := calls["run_costed"]
	kinds := map[store.FinishedCallKind]int{}
	charged := map[string]int{}
	unknown := 0
	for _, call := range got {
		kinds[call.Kind]++
		if call.Cost == nil {
			unknown++
			continue
		}
		charged[call.Cost.Amount]++
	}
	if kinds[store.FinishedNodeAttempt] != 3 || kinds[store.FinishedToolAttempt] != 4 || kinds[store.FinishedAgentTurn] != 3 {
		t.Errorf("finished calls per kind = %v, want 3 Attempts, 4 Tool calls and 3 Turns", kinds)
	}
	wantCharged := map[string]int{"0.04": 1, "0": 1, "0.000032": 1, "0.004": 1}
	if diff := cmp.Diff(wantCharged, charged); diff != "" {
		t.Errorf("recorded amounts (-want +got):\n%s", diff)
	}
	if unknown != 6 {
		t.Errorf("rows without a cost = %d, want 6 (uncertain Attempt, undispatched Attempt, failed Turn, three Tool calls)", unknown)
	}
	for _, call := range got {
		if call.Kind == store.FinishedNodeAttempt && call.Uncertain && !call.Dispatched {
			t.Errorf("the uncertain Attempt lost its dispatched flag: %+v", call)
		}
		if call.Kind == store.FinishedToolAttempt && (call.ToolName == "" || call.ReviewModelID != costReviewModelID) {
			t.Errorf("a Tool call carries no tool name or not its Agent Run's review model: %+v", call)
		}
		if call.Kind != store.FinishedToolAttempt && call.ReviewModelID != "" {
			t.Errorf("a review model is reported on a call that is not a Tool call: %+v", call)
		}
	}
}

// TestRunList_Cost_SumsKnownChargesAndCountsUnknownExternalCalls is the aggregation
// contract end to end on PostgreSQL: the list item's total is the exact decimal sum
// 0.04 + 0 + 0.000032 + 0.004 across the image Attempt, both completed Turns and the
// charged Tool call, and exactly four calls are unknown: the uncertain Attempt, the
// failed Turn, the EXTERNAL Tool call without a charge and the review Tool call that ran
// with the frozen review model without a charge. The never-dispatched Attempt and the
// NONE Tool call count nowhere, and a Run without calls reports nothing.
func TestRunList_Cost_SumsKnownChargesAndCountsUnknownExternalCalls(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	saveDefinition(ctx, t, uow, listDefinition("wf_cost", 1, "Agent"))
	seedCostedRun(ctx, t, uow, "run_costed")
	seedListRun(ctx, t, uow, listRunSpec{id: "run_idle", workflow: "wf_cost", status: domain.RunRunning, startedAt: fixtureTime.Add(-time.Hour)})
	svc := service.NewQueryService(service.Deps{UoW: uow, Tools: costToolRegistry(t)})

	page := listRuns(ctx, t, svc, service.ListRunsRequest{WorkflowID: "wf_cost"})
	if len(page.Items) != 2 || page.Items[0].ID != "run_costed" || page.Items[1].ID != "run_idle" {
		t.Fatalf("items = %v, want run_costed then run_idle", pageIDs(page))
	}
	want := service.RunCost{
		Totals:       []service.CostTotal{{Amount: "0.044032", Currency: domain.CurrencyUSD}},
		UnknownCalls: 4,
	}
	if diff := cmp.Diff(want, page.Items[0].Cost); diff != "" {
		t.Errorf("run_costed cost (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(service.RunCost{}, page.Items[1].Cost); diff != "" {
		t.Errorf("run_idle cost (-want +got):\n%s", diff)
	}
}
