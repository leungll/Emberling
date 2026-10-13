//go:build integration

package integration

import (
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
)

// TestAgentTrace_ReconcilerClaimedAction_ProjectsSourcesFromEvents stops the Agent Loop
// after the first Decision commits, restarts over the same database and lets the
// Reconciler carry the loop to completion. The Trace reports the first Turn as claimed
// immediately, the rediscovered Tool Action and the later Turn as claimed by the
// Reconciler, and the Action as completed by synchronous execution, all read from the
// committed Events.
func TestAgentTrace_ReconcilerClaimedAction_ProjectsSourcesFromEvents(t *testing.T) {
	stop := &agentStopNotifier{}
	crashed := newAgentHarness(t, agentHarnessOptions{Notifier: stop})
	agentScriptToolCallThenFinal(crashed, agentToolCallScenario())
	def := crashed.saveDefinition(agentLoopDefinition("wf-agent-trace-sources"))
	run := crashed.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome, action := agentStopAfterDecision(crashed, stop, run.ID)

	restarted := newAgentHarness(t, agentHarnessOptions{
		Pool: crashed.pool, Clock: crashed.clock, LookupExecutor: &agentRecordingTool{delegate: lookup.Executor{}},
	})
	agentScriptFinal(restarted, mockmodel.Scenario{Output: "recovered answer"})
	agentRunOnce(restarted, agentReconciler(restarted))
	agentRunOnceUntil(t, restarted, run.ID, domain.RunCompleted)

	trace, err := service.NewQueryService(service.Deps{UoW: restarted.uow}).AgentTrace(restarted.ctx, run.ID, outcome.NodeRunID)
	if err != nil {
		t.Fatalf("AgentTrace: %v", err)
	}
	if len(trace.Turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(trace.Turns))
	}
	first, second := trace.Turns[0], trace.Turns[1]

	if first.TurnClaimSource == nil || *first.TurnClaimSource != domain.ClaimImmediate {
		t.Errorf("turn 1 claim source = %v, want IMMEDIATE", first.TurnClaimSource)
	}
	if first.Action == nil || first.Action.ID != action.ID {
		t.Fatalf("turn 1 action = %+v, want %s", first.Action, action.ID)
	}
	src := first.ActionSources
	if src.Claim == nil || *src.Claim != domain.ClaimReconciler {
		t.Errorf("tool action claim source = %v, want RECONCILER", src.Claim)
	}
	if src.Completion == nil || *src.Completion != domain.CompletionSyncExecution || src.Failure != nil {
		t.Errorf("tool action outcome sources = completion %v failure %v, want SYNC_EXECUTION and nil", src.Completion, src.Failure)
	}
	if second.TurnClaimSource == nil || *second.TurnClaimSource != domain.ClaimReconciler {
		t.Errorf("turn 2 claim source = %v, want RECONCILER", second.TurnClaimSource)
	}
	if second.ActionSources.Completion == nil {
		t.Errorf("final action completion source = nil, want the recorded completion")
	}
}
