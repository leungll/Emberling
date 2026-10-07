//go:build integration

package contract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/store"
)

// stopAtWaitingActionOfTurn is the boundary an async Tool dispatch leaves behind: the Tool
// Attempt is DISPATCHED, its Binding committed and the Action WAITING_CALLBACK. Stopping the
// workers at this commit's notification is what loses the in-process replay of a stored
// early callback, which runs right after it on the same worker.
func stopAtWaitingActionOfTurn(turnNo int) func(context.Context, store.Tx, string) (bool, error) {
	return func(ctx context.Context, tx store.Tx, runID string) (bool, error) {
		_, turns, ok, err := agentTurnsOfRun(ctx, tx, runID)
		if err != nil || !ok {
			return false, err
		}
		for _, turn := range turns {
			if turn.TurnNo != turnNo {
				continue
			}
			action, err := tx.AgentActions().GetByTurnID(ctx, turn.ID)
			if errors.Is(err, domain.ErrNotFound) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			return action.Status == domain.AgentActionWaitingCallback, nil
		}
		return false, nil
	}
}

// TestAPI_AgentAsyncToolCallback_EarlyThenRestart_Returns202AndReconcilerCompletesLoop
// covers early-callback handling and post-COMMIT resume for a Tool Attempt target through
// the real Mock Provider. The Provider's callback arrives while the dispatch response is
// still held, so no Callback Binding exists yet: it is answered 202 {accepted:true,
// pending:true, duplicate:false} and stored. The process then dies right after the
// dispatch commit, before the in-process replay. A restarted Backend's Reconciler
// rediscovers the stored delivery through the Tool Attempt's Binding, replays it exactly
// once, and the Agent Loop completes. Neither the Agent Trace nor any Event carries the
// callback token or its hash.
func TestAPI_AgentAsyncToolCallback_EarlyThenRestart_Returns202AndReconcilerCompletesLoop(t *testing.T) {
	fixture := newProviderFixture(t)
	stopper := newAgentLoopStopper()
	first := newTestEnvWithOptions(t, testEnvOptions{
		MockTaskBaseURL:     fixture.server.URL,
		TaskClient:          &http.Client{Transport: fixture.tasks, Timeout: e2eHTTPTimeout},
		WrapServiceNotifier: stopper.notifier,
	})
	fixture.callbacks.setTarget(first.server.URL)
	stopper.arm(first, stopAtWaitingActionOfTurn(1))

	// Callbacks flow freely; it is the dispatch response that is held.
	fixture.callbacks.release()
	fixture.tasks.arm()
	runID := createAgentAsyncRun(t, first)
	fixture.tasks.waitHeld(t, e2eWait)

	early := fixture.waitDelivery(t, e2eWait)
	if early.StatusCode != http.StatusAccepted {
		t.Fatalf("early callback status = %d, want %d, body=%s", early.StatusCode, http.StatusAccepted, early.Body)
	}
	outcome := decodeBody[callbackResponseDTO](t, early.Body)
	if !outcome.Accepted || !outcome.Pending || outcome.Duplicate {
		t.Errorf("early callback outcome = %+v, want {Accepted:true Pending:true Duplicate:false}", outcome)
	}
	if strings.Contains(string(early.Body), early.Token) {
		t.Errorf("early callback response echoes the callback token")
	}

	fixture.tasks.release()
	stopper.waitStopped(t, agentE2EWait)
	if pending, found := first.pendingCallbackRow(t, early.ExternalTaskID); !found || pending.ConsumedAt != nil {
		t.Fatalf("pending callback before the restart found=%v consumedAt=%v, want stored and unconsumed", found, pending.ConsumedAt)
	}

	// --- restart: same database, same Provider, new process image.
	pool := first.pool
	first.stop()
	second := fixture.startBackend(t, pool)
	secondCalls := scriptFinalOnly(second, agentAsyncFinalOutput)

	rec := reconciler.New(reconciler.Config{
		UoW: second.uow, Executor: second.execution, Clock: domain.SystemClock{}, BatchLimit: 100,
	})
	report, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("reconciler RunOnce: %v", err)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("reconciler report errors = %v, want none", report.Errors)
	}
	if report.ConsumablePendingFound != 1 || report.PendingCallbacksResumed != 1 {
		t.Fatalf("reconciler report = %+v, want the stored Tool callback found and resumed once", report)
	}

	snapshot := second.waitForTerminal(t, runID, agentE2EWait)
	run, _ := snapshot["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("run terminal status = %q, want COMPLETED; snapshot=%v", status, snapshot)
	}
	if got := secondCalls.Load(); got != 1 {
		t.Errorf("model calls in the second process = %d, want exactly 1 (turn 2 only)", got)
	}
	if got := len(fixture.tasks.dispatches()); got != 1 {
		t.Errorf("provider dispatches = %d, want 1: the restart must not re-execute the Tool Action", got)
	}

	pending, found := second.pendingCallbackRow(t, early.ExternalTaskID)
	if !found || pending.ConsumedAt == nil || pending.DuplicateCount != 0 {
		t.Errorf("pending callback after the restart found=%v consumedAt=%v duplicateCount=%d, want consumed once", found, pending.ConsumedAt, pending.DuplicateCount)
	}

	agentNodeRunID := second.agentNodeRunIDOf(t, runID)
	attempt := agentAsyncOnlyToolAttempt(t, second, runID, agentNodeRunID)
	if attempt.Status != "SUCCEEDED" || attempt.CallbackBinding == nil || attempt.CallbackBinding.ExternalTaskID != early.ExternalTaskID {
		t.Errorf("tool attempt = %+v, want the original Attempt SUCCEEDED with its Binding", attempt)
	}

	events := second.listEvents(t, runID)
	assertContiguousSeq(t, events)
	if n := agentAsyncCallbackCompletions(events); n != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want 1", n)
	}

	_, body := getAgentTrace(t, second, runID, agentNodeRunID)
	sum := sha256.Sum256([]byte(early.Token))
	hash := hex.EncodeToString(sum[:])
	for _, forbidden := range []string{early.Token, hash, "callbackTokenHash", "callbackToken"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("agent trace after the recovery leaks %q", forbidden)
		}
	}
	for _, ev := range events {
		raw, _ := json.Marshal(ev)
		if strings.Contains(string(raw), early.Token) || strings.Contains(string(raw), hash) {
			t.Errorf("event %v leaks the callback token or its hash", ev["type"])
		}
	}
}
