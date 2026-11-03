//go:build integration

package contract

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// An Agent without a frozen goal is not a goal-bound task: its Trace carries
// `completion: null`, and the completion check wrote no verdict for it. The AIGC
// Agents keep behaving exactly as before the check existed.
func TestAgentTrace_AgentWithoutGoal_CompletionIsNullAndNoVerdictStored(t *testing.T) {
	env := newTestEnv(t)
	runID, snapshot := runCompletedAgentLoop(t, env)
	agentNodeRunID := nodeRunIDOf(t, snapshot, "node_agent")

	status, body := getAgentTrace(t, env, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, want %d, body=%s", status, http.StatusOK, body)
	}
	trace := decodeBody[map[string]any](t, body)
	agentRun, _ := trace["agentRun"].(map[string]any)
	if agentRun == nil {
		t.Fatalf("agent trace has no agentRun: %s", body)
	}
	completion, present := agentRun["completion"]
	if !present {
		t.Errorf("agentRun has no completion field; the contract renders it as null: %s", body)
	}
	if completion != nil {
		t.Errorf("agentRun.completion = %v, want null for an Agent without a goal", completion)
	}
	if agentRun["goal"] != nil {
		t.Fatalf("agentRun.goal = %v, this story needs an Agent without a goal", agentRun["goal"])
	}

	agentRunID, _ := agentRun["id"].(string)
	if agentRunID == "" {
		t.Fatalf("agentRun.id missing: %s", body)
	}
	err := env.uow.WithinReadTx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		_, err := tx.AgentCompletions().Get(ctx, agentRunID)
		return err
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("agent_completions row for agent run %s: err = %v, want none", agentRunID, err)
	}
}
