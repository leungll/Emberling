//go:build integration

// The Reconciler's orphaned Tool Attempt row: a pass hands every STARTED synchronous
// Attempt of another process to the orphan use case and nothing else. This process's own
// in-flight call is never an orphan, an expired Agent Run ends as TIMEOUT instead, and a
// handling that crashed before its COMMIT is rediscovered by the next process and
// committed exactly once.
package integration

import (
	"context"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/store"
)

func TestReconciler_OwnStartedToolAttempt_NotTreatedAsOrphan(t *testing.T) {
	paused := newAgentPausedTool()
	h := newAgentHarness(t, agentHarnessOptions{LookupExecutor: paused})
	agentScriptToolCallThenFinal(h, agentToolCallScenario())
	def := h.saveDefinition(agentLoopDefinition("wf-orphan-own-attempt"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)

	executed := make(chan error, 1)
	go func() { executed <- h.svc.Execute(h.ctx, outcome) }()
	// The call is in flight: its Attempt is committed STARTED by this very process.
	<-paused.inFlight

	if report := agentRunOnce(h, agentReconciler(h)); report.OrphanToolAttemptsFound != 0 {
		t.Fatalf("report = %+v, want this process's own call not listed as an orphan", report)
	}
	// Even a Reconciler wired with another identity cannot fail it: the use case
	// re-checks the Attempt's runner under the Run lock against its own.
	misconfigured := reconciler.New(reconciler.Config{
		UoW: h.uow, Executor: h.svc, Clock: h.clock, BatchLimit: 100, RunnerID: "runner-misconfigured",
	})
	if report := agentRunOnce(h, misconfigured); report.OrphanToolAttemptsFound != 1 || report.OrphanToolAttemptsHandled != 1 {
		t.Fatalf("misconfigured report = %+v, want the one Attempt found and handed over", report)
	}
	action, found := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if !found {
		t.Fatal("no action was committed")
	}
	if action.Status != domain.AgentActionRunning {
		t.Fatalf("action while the call is in flight = %s, want RUNNING", action.Status)
	}
	if attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID); len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptStarted {
		t.Fatalf("attempts while the call is in flight = %+v, want one STARTED", attempts)
	}

	close(paused.release)
	if err := <-executed; err != nil {
		t.Fatalf("execute: %v", err)
	}
	if err := h.drainTurns(h.ctx); err != nil {
		t.Fatalf("drain turns: %v", err)
	}
	if got := getAgentAction(h.ctx, t, h.uow, action.ID).Status; got != domain.AgentActionSucceeded {
		t.Errorf("action after the call returned = %s, want SUCCEEDED", got)
	}
	attempt := agentToolAttempts(h.ctx, t, h.uow, action.ID)[0]
	if attempt.Status != domain.ToolAttemptSucceeded || attempt.Uncertain != nil {
		t.Errorf("attempt = %+v, want SUCCEEDED without an uncertain flag", attempt)
	}
}

func TestReconciler_OrphanPastDeadline_TimesOutNotUncertain(t *testing.T) {
	crashed, c := crashLookup(t, "wf-orphan-past-deadline", agentMaxTurns)
	restarted := newAgentHarness(t, agentHarnessOptions{Pool: crashed.pool, Clock: crashed.clock})
	restarted.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)

	report := agentRunOnce(restarted, agentReconciler(restarted))
	if report.ExpiredAgentRunsFound != 1 || report.OrphanToolAttemptsFound != 0 {
		t.Fatalf("report = %+v, want the expired Agent Run timed out and no orphan", report)
	}
	attempt := getToolAttempt(t, restarted, c.attempt.ID)
	if attempt.Status != domain.ToolAttemptFailed || attempt.Error == nil || attempt.Error.Code != "TIMEOUT" || attempt.Uncertain != nil {
		t.Errorf("attempt = %+v, want FAILED by the timeout without an uncertain flag", attempt)
	}
	if agentRun := agentRunByID(t, restarted, c); agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Errorf("agent run termination = %s, want TIMEOUT", agentTermination(agentRun))
	}
	failed := agentOnlyPayloadFor(t, listEvents(restarted.ctx, t, restarted.uow, c.run.ID), domain.EventAgentActionFailed, "actionId", c.action.ID)
	if failed["failureSource"] != string(domain.FailureTimeout) {
		t.Errorf("AGENT_ACTION_FAILED failureSource = %v, want TIMEOUT", failed["failureSource"])
	}
}

// crashBeforeCommitUoW lets a transaction do all its writes and then crashes the calling
// goroutine before COMMIT, once armed; the deferred rollback discards everything.
type crashBeforeCommitUoW struct {
	store.UnitOfWork
	armed chan struct{}
}

func (u crashBeforeCommitUoW) WithinTx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	return u.UnitOfWork.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := fn(ctx, tx); err != nil {
			return err
		}
		select {
		case <-u.armed:
			panic("simulated crash before the orphan handling commits")
		default:
			return nil
		}
	})
}

func TestAgentOrphan_CrashBeforeCommit_RediscoveredAndHandledOnce(t *testing.T) {
	crashed, c := crashLookup(t, "wf-orphan-crash-before-commit", agentMaxTurns)
	eventsBefore := listEvents(crashed.ctx, t, crashed.uow, c.run.ID)

	armed := make(chan struct{})
	close(armed)
	second := newAgentHarness(t, agentHarnessOptions{
		Pool: crashed.pool, Clock: crashed.clock,
		WrapUoW: func(inner store.UnitOfWork) store.UnitOfWork {
			return crashBeforeCommitUoW{UnitOfWork: inner, armed: armed}
		},
	})
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatalf("the simulated crash did not happen")
			}
		}()
		_, _ = agentReconciler(second).RunOnce(second.ctx)
	}()
	if attempt := getToolAttempt(t, second, c.attempt.ID); attempt.Status != domain.ToolAttemptStarted || attempt.Uncertain != nil ||
		attempt.RunnerID == nil || *attempt.RunnerID != crashed.runnerID {
		t.Fatalf("attempt after the crash = %+v, want STARTED with the original runner", attempt)
	}
	if got := listEvents(second.ctx, t, second.uow, c.run.ID); len(got) != len(eventsBefore) {
		t.Fatalf("events after the crash = %d, want still %d", len(got), len(eventsBefore))
	}

	// The next process rediscovers the same orphan and commits its handling once.
	third := newAgentHarness(t, agentHarnessOptions{Pool: crashed.pool, Clock: crashed.clock})
	report := agentRunOnce(third, agentReconciler(third))
	if report.OrphanToolAttemptsFound != 1 || report.OrphanToolAttemptsHandled != 1 {
		t.Fatalf("report = %+v, want the orphan rediscovered and handled", report)
	}
	assertOrphanFailed(t, third, c, "EXECUTION_INTERRUPTED", false)
	if report := agentRunOnce(third, agentReconciler(third)); report.OrphanToolAttemptsFound != 0 {
		t.Errorf("second pass report = %+v, want no orphan left", report)
	}
	events := listEvents(third.ctx, t, third.uow, c.run.ID)
	if got := agentCountEventType(agentEventTypesFor(events, c.outcome.NodeRunID), domain.EventAgentActionFailed); got != 1 {
		t.Errorf("AGENT_ACTION_FAILED events = %d, want 1", got)
	}
	if agentTurnNoExists(third.ctx, t, third.uow, c.agentRunID, 3) {
		t.Error("a Turn 3 exists, want the orphan to produce one next Turn only")
	}
	assertAgentAnswered(t, third, c)
}
