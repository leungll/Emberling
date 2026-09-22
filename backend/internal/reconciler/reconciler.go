// Package reconciler rediscovers persisted execution work that the in-process work
// queue never saw or lost track of: a process crash after COMMIT but before Execute, a
// full queue that refused an enqueue, or a retry backoff that elapsed while nothing was
// watching it. It calls the same service use cases internal/work calls and maintains no
// execution path or business state of its own (CLAUDE.md package boundaries).
package reconciler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
)

// Advancer is the subset of *service.ExecutionService the Reconciler drives directly.
// Design decision: RunOnce does not merely call a WorkEnqueuer for the ready/retryable
// NodeRuns it finds; it drives Advance and, on a successful claim, Execute itself,
// exactly like internal/work.Pool's own drain loop. service.NoopEnqueuer's own doc
// comment establishes why: "[NoopEnqueuer] models a Backend with no worker pool, where
// the Reconciler is the only driver" -- a design only an enqueue-only Reconciler could
// not satisfy, since nothing would ever drain that queue. Advance's own conditional
// claims (ClaimReady/ClaimRetry) make it safe for the Reconciler and a work.Pool to
// drive the same Run concurrently: at most one of them ever wins a given claim.
//
// ResumeNode is the same idempotent resume use case HandleCallback and Provider Poll
// enter (invariant #5): the Reconciler calls it to replay an early callback that was
// stored as a Pending Callback (docs/06-execution-model.md §2.1/§3, design decision C3),
// never a second, Reconciler-owned resume path.
//
// The Agent use cases are the same ones immediate advancement calls, entered with
// domain.ClaimReconciler instead of domain.ClaimImmediate (06 §2.1). Each one owns its own
// conditional claim and chains the rest of the Agent Loop itself, so the Reconciler hands
// over one rediscovered work item and makes no Agent state change of its own.
type Advancer interface {
	Advance(ctx context.Context, runID string) (service.AdvanceOutcome, error)
	Execute(ctx context.Context, outcome service.AdvanceOutcome) error
	TimeoutAttempt(ctx context.Context, attemptID string) error
	ResumeNode(ctx context.Context, req service.ResumeNode) (service.ResumeOutcome, error)
	AdvanceAgentTurn(ctx context.Context, turnID string, claimSource domain.ClaimSource) error
	ExecuteAgentAction(ctx context.Context, actionID string, claimSource domain.ClaimSource) error
	CompleteAgentFinal(ctx context.Context, actionID string, claimSource domain.ClaimSource) error
	TimeoutAgentRun(ctx context.Context, agentRunID string) error
}

// defaultBatchLimit bounds one RunOnce scan of ListReadyOrRetryable/ListExpired. MVP
// default (same category as service's plan cache bound), not a documented requirement.
const defaultBatchLimit = 200

// defaultInterval is the MVP tick period for Run's ticker loop; not a documented
// requirement.
const defaultInterval = 5 * time.Second

// Config carries the Reconciler's collaborators.
type Config struct {
	UoW        store.UnitOfWork
	Executor   Advancer
	Clock      domain.Clock
	Interval   time.Duration
	BatchLimit int
	Logger     *slog.Logger
}

func (c Config) withDefaults() Config {
	if c.Clock == nil {
		c.Clock = domain.SystemClock{}
	}
	if c.Interval <= 0 {
		c.Interval = defaultInterval
	}
	if c.BatchLimit <= 0 {
		c.BatchLimit = defaultBatchLimit
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// Reconciler owns no goroutine until Run is called; RunOnce alone is safe to call
// directly (e.g. from a test or an HTTP admin trigger) without starting the ticker.
type Reconciler struct {
	cfg Config
}

// New builds a Reconciler from cfg, filling in MVP defaults for anything unset.
func New(cfg Config) *Reconciler {
	return &Reconciler{cfg: cfg.withDefaults()}
}

// Report summarizes one RunOnce pass. Errors accumulates per-Run, per-Attempt and
// per-callback failures without aborting the rest of the pass, so one stuck Run or one
// permanently-rejected callback cannot prevent recovery of everything else in the same
// scan.
type Report struct {
	ReadyOrRetryableFound int
	RunsAdvanced          int
	ExpiredAttemptsFound  int
	AttemptsTimedOut      int
	// ReadyAgentTurnsFound and ReadyAgentActionsFound are the persisted Agent work items
	// of 06 §2.1 this pass rediscovered; Advanced counts the ones whose use case returned
	// without error (a lost conditional claim is a success, not an error: another path
	// owned that item).
	ReadyAgentTurnsFound   int
	AgentTurnsAdvanced     int
	ReadyAgentActionsFound int
	AgentActionsAdvanced   int
	// ExpiredAgentRunsFound and AgentRunsTimedOut cover the Agent deadline row of the
	// same scan table.
	ExpiredAgentRunsFound int
	AgentRunsTimedOut     int
	// ConsumablePendingFound is how many stored early callbacks
	// ListConsumableForWaiting found routed to a still-waiting Attempt.
	ConsumablePendingFound int
	// PendingCallbacksResumed is how many of those actually advanced their Attempt
	// through ResumeNode (a Duplicate outcome still counts: the row was legitimately
	// already handled, so it is not an error).
	PendingCallbacksResumed int
	// ExpiredPendingDeleted is how many Pending Callback rows DeleteExpired removed this
	// pass (05 §1.7 retention).
	ExpiredPendingDeleted int64
	Errors                []error
}

// RunOnce performs one reconciliation pass: it lists persisted READY NodeRuns and
// RUNNING NodeRuns whose retry backoff has elapsed, drives Advance/Execute for each
// distinct Run until no further claim is made; lists expired (deadline passed) STARTED or
// DISPATCHED Attempts and calls TimeoutAttempt on each; lists Pending Callbacks that now
// route to a still-waiting Attempt and replays each through ResumeNode; and finally
// deletes Pending Callbacks past their retention TTL. Every list call is a read-only store
// scan wrapped in its own throwaway transaction; RunOnce itself never mutates business
// state directly -- every mutation happens inside the service use cases it calls.
func (r *Reconciler) RunOnce(ctx context.Context) (Report, error) {
	var report Report
	now := r.cfg.Clock.Now()

	var nodeRuns []domain.NodeRun
	if err := r.cfg.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		nodeRuns, err = tx.NodeRuns().ListReadyOrRetryable(ctx, now, r.cfg.BatchLimit)
		return err
	}); err != nil {
		return report, fmt.Errorf("reconciler: list ready/retryable node runs: %w", err)
	}
	report.ReadyOrRetryableFound = len(nodeRuns)

	seenRuns := make(map[string]struct{}, len(nodeRuns))
	for _, nr := range nodeRuns {
		if _, ok := seenRuns[nr.RunID]; ok {
			continue
		}
		seenRuns[nr.RunID] = struct{}{}

		if err := r.drive(ctx, nr.RunID, &report); err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("reconciler: advance run %s: %w", nr.RunID, err))
		}
	}

	if err := r.scanAgentWork(ctx, now, &report); err != nil {
		return report, err
	}

	var expired []domain.NodeAttempt
	if err := r.cfg.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		expired, err = tx.NodeAttempts().ListExpired(ctx, now, r.cfg.BatchLimit)
		return err
	}); err != nil {
		return report, fmt.Errorf("reconciler: list expired attempts: %w", err)
	}
	report.ExpiredAttemptsFound = len(expired)

	for _, attempt := range expired {
		if err := r.cfg.Executor.TimeoutAttempt(ctx, attempt.ID); err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("reconciler: timeout attempt %s: %w", attempt.ID, err))
			continue
		}
		report.AttemptsTimedOut++
	}

	var consumable []store.PendingForWaiting
	if err := r.cfg.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		consumable, err = tx.PendingCallbacks().ListConsumableForWaiting(ctx, now, r.cfg.BatchLimit)
		return err
	}); err != nil {
		return report, fmt.Errorf("reconciler: list consumable pending callbacks: %w", err)
	}
	report.ConsumablePendingFound = len(consumable)

	for _, row := range consumable {
		if err := r.resumePending(ctx, row, &report); err != nil {
			report.Errors = append(report.Errors, err)
		}
	}

	deleted, err := r.deleteExpiredPending(ctx, now)
	if err != nil {
		return report, fmt.Errorf("reconciler: delete expired pending callbacks: %w", err)
	}
	report.ExpiredPendingDeleted = deleted
	if deleted > 0 {
		r.cfg.Logger.Debug("reconciler: deleted expired pending callbacks", "count", deleted)
	}

	return report, nil
}

// scanAgentWork performs the three Agent rows of the scan table of
// docs/06-execution-model.md §2.1, each bounded by the same BatchLimit as every other
// scan:
//
//   - Agent Runs past their deadline -> the Agent timeout use case.
//   - READY Agent Turns -> the Turn advance use case. RUNNING Turns are deliberately not
//     listed: the model request may already be in flight, so the MVP never re-issues it;
//     such a Turn ends through the Agent deadline above.
//   - READY Agent Actions -> the Tool execution or Final completion use case, chosen by
//     the Action's own persisted type (the committed Decision's kind). The model is never
//     asked again and no second Decision is created.
//
// The deadline scan runs first on purpose. An Agent Run whose deadline has already passed
// may still hold a READY Turn or Action; claiming that work first would write
// AGENT_TURN_STARTED and call the Provider with an already-expired context, buying a
// cost-bearing call for a round that can never finish. Terminating first leaves the Turn
// and Action scans to touch only Agent Runs still inside their deadline.
//
// A RUNNING Agent NodeRun is never scanned: the next Turn is persisted by the transaction
// that completed the previous round, so recovery advances that existing READY work instead
// of deriving a duplicate round from the NodeRun (06 §2.1).
//
// Each use case takes its own conditional claim, so a lost race with immediate advancement
// is a normal outcome, not an error; only a real failure is collected into the report.
func (r *Reconciler) scanAgentWork(ctx context.Context, now time.Time, report *Report) error {
	var expired []domain.AgentRun
	if err := r.cfg.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		expired, err = tx.AgentRuns().ListExpired(ctx, now, r.cfg.BatchLimit)
		return err
	}); err != nil {
		return fmt.Errorf("reconciler: list expired agent runs: %w", err)
	}
	report.ExpiredAgentRunsFound = len(expired)
	for _, agentRun := range expired {
		if err := r.cfg.Executor.TimeoutAgentRun(ctx, agentRun.ID); err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("reconciler: timeout agent run %s: %w", agentRun.ID, err))
			continue
		}
		report.AgentRunsTimedOut++
	}

	var turns []domain.AgentTurn
	if err := r.cfg.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		turns, err = tx.AgentTurns().ListReady(ctx, r.cfg.BatchLimit)
		return err
	}); err != nil {
		return fmt.Errorf("reconciler: list ready agent turns: %w", err)
	}
	report.ReadyAgentTurnsFound = len(turns)
	for _, turn := range turns {
		if err := r.cfg.Executor.AdvanceAgentTurn(ctx, turn.ID, domain.ClaimReconciler); err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("reconciler: advance agent turn %s: %w", turn.ID, err))
			continue
		}
		report.AgentTurnsAdvanced++
	}

	var actions []domain.AgentAction
	if err := r.cfg.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		actions, err = tx.AgentActions().ListReady(ctx, r.cfg.BatchLimit)
		return err
	}); err != nil {
		return fmt.Errorf("reconciler: list ready agent actions: %w", err)
	}
	report.ReadyAgentActionsFound = len(actions)
	for _, action := range actions {
		var err error
		if action.Type == domain.AgentActionFinal {
			err = r.cfg.Executor.CompleteAgentFinal(ctx, action.ID, domain.ClaimReconciler)
		} else {
			err = r.cfg.Executor.ExecuteAgentAction(ctx, action.ID, domain.ClaimReconciler)
		}
		if err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("reconciler: advance agent action %s: %w", action.ID, err))
			continue
		}
		report.AgentActionsAdvanced++
	}
	return nil
}

// resumePending replays one stored early callback through the same idempotent ResumeNode
// use case a live callback or Provider Poll enters (invariant #5). ListConsumableForWaiting
// only returns rows whose Binding already routes to a still-DISPATCHED Attempt of a still
// WAITING_CALLBACK NodeRun -- a Node Attempt, or a Tool Attempt of a WAITING_CALLBACK Agent
// Action, which ResumeNode routes by the Binding's target type (06 §2.1: the Reconciler
// only rediscovers work and enters the existing use case; it has no Tool-specific path) --
// so service.ErrNoCallbackBinding is not an expected outcome here
// -- it is treated as an error rather than silently dropped. A payload the registered
// Executor cannot interpret leaves the NodeRun untouched (06 §1.6): it is logged at warn,
// without the payload, hash or token, and left for the next tick or for retention to
// eventually delete once it expires -- the Reconciler does not invent a rejection limit
// beyond the Pending Callback's own TTL (docs/09-testing-and-acceptance.md gap; see
// package-level report notes). A row whose stored credential does not match the Attempt
// ResumeNode was about to advance (docs/06-execution-model.md §3, service's own
// consumePendingCallback check) is classified the same way: it is not this Reconciler's
// fault or an operational error, the row is left for its real owner or for expiry, and the
// pass continues.
func (r *Reconciler) resumePending(ctx context.Context, row store.PendingForWaiting, report *Report) error {
	_, err := r.cfg.Executor.ResumeNode(ctx, service.ResumeNode{
		ExternalTaskID: row.Pending.ExternalTaskID,
		Payload:        row.Pending.Payload,
		Source:         domain.CompletionCallback,
		ConsumePending: true,
		PayloadHash:    row.Pending.PayloadHash,
	})
	if err != nil {
		var rejected *service.CallbackPayloadRejectedError
		if errors.As(err, &rejected) {
			r.cfg.Logger.Warn("reconciler: pending callback payload rejected by executor",
				"external_task_id", row.Pending.ExternalTaskID,
				"attempt_id", row.AttemptID)
			return nil
		}
		var mismatch *service.PendingCallbackCredentialMismatchError
		if errors.As(err, &mismatch) {
			r.cfg.Logger.Warn("reconciler: pending callback credential does not match the dispatched attempt",
				"external_task_id", row.Pending.ExternalTaskID,
				"attempt_id", row.AttemptID)
			return nil
		}
		if errors.Is(err, service.ErrNoCallbackBinding) {
			return fmt.Errorf("reconciler: pending callback %s: binding vanished after ListConsumableForWaiting: %w", row.Pending.ExternalTaskID, err)
		}
		return fmt.Errorf("reconciler: resume pending callback %s: %w", row.Pending.ExternalTaskID, err)
	}
	report.PendingCallbacksResumed++
	return nil
}

// deleteExpiredPending removes Pending Callback rows past their retention TTL, bounded by
// the same batch limit as every other scan.
func (r *Reconciler) deleteExpiredPending(ctx context.Context, now time.Time) (int64, error) {
	var deleted int64
	err := r.cfg.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		deleted, err = tx.PendingCallbacks().DeleteExpired(ctx, now, r.cfg.BatchLimit)
		return err
	})
	return deleted, err
}

// drive replays internal/work.Pool's own drain loop for one Run: Advance, and on a
// successful claim, Execute, looping back until Advance makes no further claim. A
// RUNNING NodeRun whose retry backoff has not yet elapsed makes no claim, so this never
// busy-waits; the next RunOnce tick (or a work.Pool, if one is running) picks it up once
// due.
func (r *Reconciler) drive(ctx context.Context, runID string, report *Report) error {
	for {
		outcome, err := r.cfg.Executor.Advance(ctx, runID)
		if err != nil {
			return err
		}
		if !outcome.Claimed {
			return nil
		}
		report.RunsAdvanced++
		if err := r.cfg.Executor.Execute(ctx, outcome); err != nil {
			return err
		}
	}
}

// Run owns a ticker loop calling RunOnce at cfg.Interval until ctx is cancelled, at
// which point it returns. It is the Reconciler's one long-running goroutine body; the
// caller starts it with `go reconciler.Run(ctx)` and stops it by cancelling ctx, the
// same explicit-goroutine-ownership discipline internal/work.Pool uses for its own
// workers.
func (r *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := r.RunOnce(ctx); err != nil {
				r.cfg.Logger.Error("reconciler: run once failed", "error", err)
			}
		}
	}
}
