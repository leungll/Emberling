//go:build integration

// Tool Attempt runner identity and the uncertain flag: the process that claimed an Action
// is recorded on its STARTED Tool Attempt by the same INSERT that creates it, the orphan
// scan returns only STARTED synchronous Attempts another process created for an Agent Run
// still inside its deadline, the interrupted failure is written once, and only a FAILED
// Attempt may carry the uncertain flag.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// orphanScanRow is one Agent Run with one STARTED Tool Attempt seeded for the orphan scan.
type orphanScanRow struct {
	suffix    string
	runnerID  *string
	tokenHash *string
	// terminated ends the Agent Run before the scan; expired puts its deadline before now;
	// paused stops the Agent Run's clock for a human decision before the scan.
	terminated bool
	expired    bool
	paused     bool
}

// seedOrphanScanRow commits an Agent Run on its own NodeRun of the seeded Run, a decided
// Turn with a RUNNING Action and the given STARTED Attempt.
func seedOrphanScanRow(ctx context.Context, t *testing.T, uow store.UnitOfWork, f fixture, row orphanScanRow, now time.Time) string {
	t.Helper()
	nodeRunID := "nr_" + row.suffix
	agentRunID := "ar_" + row.suffix
	turnID := "turn_" + row.suffix
	actionID := "action_" + row.suffix
	attemptID := "tool_attempt_" + row.suffix

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.NodeRuns().Create(ctx, newNodeRun(nodeRunID, f.runID, "node_"+row.suffix)); err != nil {
			return err
		}
		agentRun := newAgentRun(agentRunID, nodeRunID)
		agentRun.Deadline = now.Add(time.Minute)
		if row.expired {
			agentRun.Deadline = now.Add(-time.Second)
		}
		if err := tx.AgentRuns().Create(ctx, agentRun); err != nil {
			return err
		}
		if err := tx.AgentTurns().Create(ctx, newAgentTurn(turnID, agentRunID, 1)); err != nil {
			return err
		}
		if _, err := tx.AgentTurns().ClaimReady(ctx, turnID, fixtureTime); err != nil {
			return err
		}
		if err := tx.AgentTurns().MarkCompleted(ctx, turnID, fixtureTime, json.RawMessage(`{"kind":"TOOL_CALL"}`), nil, nil); err != nil {
			return err
		}
		if err := tx.AgentDecisions().Create(ctx, newAgentDecision("decision_"+row.suffix, turnID)); err != nil {
			return err
		}
		if err := tx.AgentActions().Create(ctx, newAgentAction(actionID, turnID, "decision_"+row.suffix)); err != nil {
			return err
		}
		if _, err := tx.AgentActions().ClaimReady(ctx, actionID, fixtureTime); err != nil {
			return err
		}
		attempt := newToolAttempt(attemptID, actionID, 1)
		attempt.RunnerID = row.runnerID
		attempt.CallbackTokenHash = row.tokenHash
		if err := tx.ToolAttempts().Create(ctx, attempt); err != nil {
			return err
		}
		if row.terminated {
			if _, err := tx.AgentRuns().Terminate(ctx, agentRunID, domain.TerminationToolError, fixtureTime,
				&domain.ExecutionError{Code: "TOOL_ERROR", Message: "seeded"}); err != nil {
				return err
			}
		}
		if row.paused {
			if err := tx.AgentRuns().Pause(ctx, agentRunID, domain.PauseApproval, fixtureTime); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed orphan scan row %s: %v", row.suffix, err)
	}
	return attemptID
}

func TestToolAttemptStore_ListOrphanStarted_ExcludesCurrentRunnerNullRunnerAndAsync(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	now := fixtureTime.Add(time.Hour)

	current, previous, other := "runner-current", "runner-previous", "runner-other"
	hash := "token-hash"
	want := map[string]bool{}
	for _, row := range []struct {
		orphanScanRow
		orphan bool
	}{
		{orphanScanRow{suffix: "previous", runnerID: &previous}, true},
		{orphanScanRow{suffix: "other", runnerID: &other}, true},
		{orphanScanRow{suffix: "current", runnerID: &current}, false},
		{orphanScanRow{suffix: "null_runner"}, false},
		{orphanScanRow{suffix: "async", runnerID: &previous, tokenHash: &hash}, false},
		{orphanScanRow{suffix: "terminated", runnerID: &previous, terminated: true}, false},
		{orphanScanRow{suffix: "expired", runnerID: &previous, expired: true}, false},
	} {
		id := seedOrphanScanRow(ctx, t, uow, f, row.orphanScanRow, now)
		if row.orphan {
			want[id] = true
		}
	}
	// A completed Attempt of another process is not STARTED, so it is no orphan either.
	completed := seedOrphanScanRow(ctx, t, uow, f, orphanScanRow{suffix: "completed", runnerID: &previous}, now)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ToolAttempts().MarkSucceeded(ctx, completed, domain.ToolAttemptStarted, now, json.RawMessage(`{}`), nil)
	}); err != nil {
		t.Fatalf("complete attempt: %v", err)
	}

	var got []domain.ToolAttempt
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		got, err = tx.ToolAttempts().ListOrphanStarted(ctx, current, now, 10)
		return err
	}); err != nil {
		t.Fatalf("ListOrphanStarted: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("orphans = %+v, want exactly %v", got, want)
	}
	for _, attempt := range got {
		if !want[attempt.ID] {
			t.Errorf("orphan %s was returned, want only %v", attempt.ID, want)
		}
		if attempt.RunnerID == nil || *attempt.RunnerID == current {
			t.Errorf("orphan %s runner = %v, want another process", attempt.ID, attempt.RunnerID)
		}
	}

	// The scan is bounded by its limit.
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		got, err = tx.ToolAttempts().ListOrphanStarted(ctx, current, now, 1)
		return err
	}); err != nil {
		t.Fatalf("ListOrphanStarted limit 1: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("orphans with limit 1 = %d, want 1", len(got))
	}
}

// TestToolAttemptStore_ListOrphanStarted_IncludesPausedAgentRunPastDeadline proves the
// orphan scan does not read a paused Agent Run's deadline as expiry: while the clock is
// stopped the deadline is stale, and a STARTED Attempt of a stopped process under such an
// Agent Run is still an orphan, whether or not the frozen deadline has passed.
func TestToolAttemptStore_ListOrphanStarted_IncludesPausedAgentRunPastDeadline(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	now := fixtureTime.Add(time.Hour)
	previous := "runner-previous"

	pausedExpired := seedOrphanScanRow(ctx, t, uow, f, orphanScanRow{suffix: "paused_expired", runnerID: &previous, expired: true, paused: true}, now)
	pausedLive := seedOrphanScanRow(ctx, t, uow, f, orphanScanRow{suffix: "paused_live", runnerID: &previous, paused: true}, now)
	runningExpired := seedOrphanScanRow(ctx, t, uow, f, orphanScanRow{suffix: "running_expired", runnerID: &previous, expired: true}, now)

	var got []domain.ToolAttempt
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		got, err = tx.ToolAttempts().ListOrphanStarted(ctx, "runner-current", now, 10)
		return err
	}); err != nil {
		t.Fatalf("ListOrphanStarted: %v", err)
	}
	ids := map[string]bool{}
	for _, attempt := range got {
		ids[attempt.ID] = true
	}
	if !ids[pausedExpired] || !ids[pausedLive] {
		t.Errorf("orphans = %v, want both Attempts under paused Agent Runs (%s, %s)", ids, pausedExpired, pausedLive)
	}
	if ids[runningExpired] {
		t.Errorf("orphans = %v, want the Attempt under the expired, running Agent Run left to the timeout path", ids)
	}
}

func TestToolAttemptStore_MarkInterrupted_SecondWrite_StaleClaim(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	previous := "runner-previous"
	id := seedOrphanScanRow(ctx, t, uow, f, orphanScanRow{suffix: "interrupted", runnerID: &previous}, fixtureTime)

	mark := func(code string, uncertain bool) error {
		return uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
			return tx.ToolAttempts().MarkInterrupted(ctx, id, fixtureTime, domain.ExecutionError{Code: code, Message: "stopped"}, uncertain)
		})
	}
	if err := mark("RESULT_UNCERTAIN", true); err != nil {
		t.Fatalf("first MarkInterrupted: %v", err)
	}
	if err := mark("EXECUTION_INTERRUPTED", false); !errors.Is(err, domain.ErrStaleClaim) {
		t.Fatalf("second MarkInterrupted: err = %v, want domain.ErrStaleClaim", err)
	}
	var attempt domain.ToolAttempt
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		attempt, err = tx.ToolAttempts().Get(ctx, id)
		return err
	}); err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if attempt.Status != domain.ToolAttemptFailed || attempt.Error == nil || attempt.Error.Code != "RESULT_UNCERTAIN" ||
		attempt.Uncertain == nil || !*attempt.Uncertain || attempt.CompletedAt == nil {
		t.Errorf("attempt = %+v, want FAILED with the first determination kept", attempt)
	}
}

func TestToolAttemptUncertain_NonFailedAttempt_RejectedByConstraint(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_1", "turn_1", "decision_1", "action_1")

	yes := true
	attempt := newToolAttempt("tool_attempt_uncertain_started", "action_1", 1)
	attempt.Uncertain = &yes
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ToolAttempts().Create(ctx, attempt)
	})
	if err == nil {
		t.Fatal("create a STARTED Tool Attempt carrying uncertain: want a constraint error, got nil")
	}

	// The ordinary failure paths leave the flag NULL.
	createToolAttempt(ctx, t, uow, newToolAttempt("tool_attempt_plain", "action_1", 1))
	var failed domain.ToolAttempt
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.ToolAttempts().MarkFailed(ctx, "tool_attempt_plain", domain.ToolAttemptStarted, fixtureTime,
			domain.ExecutionError{Code: "TOOL_ERROR", Message: "failed"}); err != nil {
			return err
		}
		var err error
		failed, err = tx.ToolAttempts().Get(ctx, "tool_attempt_plain")
		return err
	}); err != nil {
		t.Fatalf("fail attempt: %v", err)
	}
	if failed.Uncertain != nil {
		t.Errorf("MarkFailed uncertain = %v, want NULL", *failed.Uncertain)
	}
}

// runnerProbe observes the one lookup call: while the call is in flight the claim has
// committed and the result has not, so what it reads is exactly what the claim wrote.
type runnerProbe struct {
	h       *agentHarness
	t       *testing.T
	runner  *string
	status  domain.ToolAttemptStatus
	started bool
}

func (p *runnerProbe) Execute(ctx context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	attempts := agentToolAttempts(ctx, p.t, p.h.uow, action.ActionID)
	if len(attempts) == 1 {
		p.started = true
		p.status = attempts[0].Status
		p.runner = attempts[0].RunnerID
	}
	return lookup.Executor{}.Execute(ctx, action)
}

func TestAgentAction_Claim_WritesRunnerIDWithStartedAttempt(t *testing.T) {
	probe := &runnerProbe{t: t}
	h := newAgentHarness(t, agentHarnessOptions{LookupExecutor: probe, RunnerID: "runner-claim"})
	probe.h = h
	agentScriptToolCallThenFinal(h, agentToolCallScenario())
	def := h.saveDefinition(agentLoopDefinition("wf-agent-claim-runner"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)
	h.execute(outcome)

	if !probe.started || probe.status != domain.ToolAttemptStarted {
		t.Fatalf("attempt seen by the call = started %v status %s, want the committed STARTED Attempt", probe.started, probe.status)
	}
	if probe.runner == nil || *probe.runner != "runner-claim" {
		t.Fatalf("runner of the STARTED Attempt = %v, want runner-claim", probe.runner)
	}
	action, found := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
	if !found {
		t.Fatal("no action was committed")
	}
	attempts := agentToolAttempts(h.ctx, t, h.uow, action.ID)
	if len(attempts) != 1 || attempts[0].RunnerID == nil || *attempts[0].RunnerID != "runner-claim" || attempts[0].Uncertain != nil {
		t.Errorf("completed attempt = %+v, want runner-claim kept and no uncertain flag", attempts)
	}
}
