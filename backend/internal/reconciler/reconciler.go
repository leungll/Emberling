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
// enter: the Reconciler calls it to replay an early callback that was stored as a Pending
// Callback, never a second, Reconciler-owned resume path.
//
// PollAttempt is the Provider poll use case. The Reconciler hands it each Attempt whose
// persisted poll time is due; the use case owns the conditional poll claim, the single
// Provider query and the entry into ResumeNode, so the Reconciler never claims a poll,
// queries a Provider or reschedules anything itself. PollToolAttempt is the same use case
// for the Attempt of an asynchronous Agent Tool that declares a poll policy.
//
// The Agent use cases are the same ones immediate advancement calls, entered with
// domain.ClaimReconciler instead of domain.ClaimImmediate. Each one owns its own
// conditional claim and chains the rest of the Agent Loop itself, so the Reconciler hands
// over one rediscovered work item and makes no Agent state change of its own.
//
// HandleOrphanToolAttempt is the use case for a STARTED synchronous Tool Attempt another
// process created. The Reconciler only hands it the Attempts it discovers; the use case
// re-checks every condition under the Run lock and owns every write.
//
// HandleOrphanNodeAttempt is the use case for a STARTED Node Attempt with no deadline that
// another process created. It decides the same way, through the ordinary failure use case.
type Advancer interface {
	Advance(ctx context.Context, runID string) (service.AdvanceOutcome, error)
	Execute(ctx context.Context, outcome service.AdvanceOutcome) error
	TimeoutAttempt(ctx context.Context, attemptID string) error
	ResumeNode(ctx context.Context, req service.ResumeNode) (service.ResumeOutcome, error)
	PollAttempt(ctx context.Context, req service.PollAttempt) (service.PollAttemptOutcome, error)
	PollToolAttempt(ctx context.Context, req service.PollToolAttempt) (service.PollToolAttemptOutcome, error)
	AdvanceAgentTurn(ctx context.Context, turnID string, claimSource domain.ClaimSource) error
	ExecuteAgentAction(ctx context.Context, actionID string, claimSource domain.ClaimSource) error
	CompleteAgentFinal(ctx context.Context, actionID string, claimSource domain.ClaimSource) error
	TimeoutAgentRun(ctx context.Context, agentRunID string) error
	HandleOrphanToolAttempt(ctx context.Context, attemptID string) error
	HandleOrphanNodeAttempt(ctx context.Context, attemptID string) error
	ExpireApproval(ctx context.Context, approvalID string) error
	OpenReleaseHumanWait(ctx context.Context, attemptID string) error
	ExpireHumanWait(ctx context.Context, humanWaitID string) error
	ReconcileReleaseOccupation(ctx context.Context, occupationID string) (service.ReleaseReconcileOutcome, error)
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
	// RunnerID is this process's identity, the same value its ExecutionService writes
	// onto the Node and Tool Attempts it creates. Empty skips both orphaned Attempt scans.
	RunnerID string
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
	// this pass rediscovered; Advanced counts the ones whose use case returned
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
	// OrphanToolAttemptsFound is how many STARTED synchronous Tool Attempts of another
	// process the scan found; OrphanToolAttemptsHandled counts those whose use case
	// returned without error (a handling that writes nothing is a success).
	OrphanToolAttemptsFound   int
	OrphanToolAttemptsHandled int
	// OrphanNodeAttemptsFound is how many STARTED Node Attempts with no deadline of another
	// process the scan found; OrphanNodeAttemptsHandled counts those whose use case returned
	// without error.
	OrphanNodeAttemptsFound   int
	OrphanNodeAttemptsHandled int
	// DueConfirmDeadlinesFound is how many release Attempts had passed their confirm
	// deadline without a definite production answer; HumanWaitsOpened counts the
	// OpenReleaseHumanWait calls that returned without error.
	DueConfirmDeadlinesFound int
	HumanWaitsOpened         int
	// ExpiredHumanWaitsFound is how many OPEN human waits had passed their deadline;
	// HumanWaitsExpired counts the ExpireHumanWait calls that returned without error.
	ExpiredHumanWaitsFound int
	HumanWaitsExpired      int
	// DueOccupationQueriesFound is how many ACTIVE occupations of ended Runs had a due
	// post-task query; OccupationQueriesClaimed counts the queries this pass claimed and
	// OccupationsReleased the subset whose definite answer freed the target.
	DueOccupationQueriesFound int
	OccupationQueriesClaimed  int
	OccupationsReleased       int
	// DuePollsFound is how many DISPATCHED Attempts ListDuePolls found with a due poll.
	// PollsClaimed counts the polls this pass claimed whose answer either resolved
	// nothing or won the resume; PollsResumed is the subset that won the resume.
	// PollsSkipped counts polls the use case did not claim, and PollsDuplicate final
	// answers that lost to a callback, timeout or other poll. Neither is an error: another
	// path owned that Attempt. Only an error returned by the use case is collected.
	DuePollsFound  int
	PollsClaimed   int
	PollsResumed   int
	PollsSkipped   int
	PollsDuplicate int
	// DueToolPollsFound is how many DISPATCHED Tool Attempts had a due poll, and the
	// ToolPolls counts classify each PollToolAttempt outcome as the Node ones above do.
	// ToolPollsReturned is the subset of claimed polls whose Provider no longer knew the
	// task and whose call was handed back to the Agent.
	DueToolPollsFound  int
	ToolPollsClaimed   int
	ToolPollsResumed   int
	ToolPollsReturned  int
	ToolPollsSkipped   int
	ToolPollsDuplicate int
	// ExpiredApprovalsFound is how many PENDING approvals had passed their deadline;
	// ApprovalsExpired counts the ExpireApproval calls that returned without error.
	ExpiredApprovalsFound int
	ApprovalsExpired      int
	// ConsumablePendingFound is how many stored early callbacks
	// ListConsumableForWaiting found routed to a still-waiting Attempt.
	ConsumablePendingFound int
	// PendingCallbacksResumed is how many of those actually advanced their Attempt
	// through ResumeNode (a Duplicate outcome still counts: the row was legitimately
	// already handled, so it is not an error).
	PendingCallbacksResumed int
	// ExpiredPendingDeleted is how many Pending Callback rows DeleteExpired removed this
	// pass (Pending Callback TTL retention).
	ExpiredPendingDeleted int64
	Errors                []error
}

// RunOnce performs one reconciliation pass: it hands orphaned Node Attempts to their use
// case first, because an orphan is what keeps a Run's execution slot occupied, so a Run it
// frees is driven in this same pass; it then lists persisted READY NodeRuns and
// RUNNING NodeRuns whose retry backoff has elapsed, drives Advance/Execute for each
// distinct Run until no further claim is made; lists expired (deadline passed) STARTED or
// DISPATCHED Attempts and calls TimeoutAttempt on each; lists DISPATCHED Attempts whose
// Provider poll is due and calls PollAttempt on each; lists Pending Callbacks that now
// route to a still-waiting Attempt and replays each through ResumeNode; and finally
// deletes Pending Callbacks past their retention TTL. Every list call is a read-only store
// scan wrapped in its own throwaway transaction; RunOnce itself never mutates business
// state directly -- every mutation happens inside the service use cases it calls.
//
// Each scan is bounded by BatchLimit, so a row that keeps coming back without changing can
// starve the rows behind it. The READY, retry, orphan and confirm deadline scans therefore
// leave out terminal Runs, whose rows their use cases refuse. The timeout, poll, callback,
// approval, human wait and occupation scans keep listing them: those use cases still end an
// Attempt, free a Provider slot, close a wait or ask production, and the row leaves the
// set by being processed or rescheduled.
func (r *Reconciler) RunOnce(ctx context.Context) (Report, error) {
	var report Report
	now := r.cfg.Clock.Now()

	if err := r.scanOrphanNodeAttempts(ctx, &report); err != nil {
		return report, err
	}

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

	if err := r.scanDuePolls(ctx, now, &report); err != nil {
		return report, err
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

// scanAgentWork performs the Agent rows of the Reconciler scan table, each bounded by the
// same BatchLimit as every other scan:
//
//   - PENDING approvals past their deadline -> the approval expiry use case, which hands
//     the call back to the Agent and resumes its clock.
//   - Agent Runs past their deadline -> the Agent timeout use case.
//   - STARTED synchronous Tool Attempts another process created -> the orphaned Tool
//     Attempt use case, which hands the call back to the Agent without calling it again.
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
// and Action scans to touch only Agent Runs still inside their deadline. For the same
// reason the orphan scan follows it, so an expired Agent Run ends as TIMEOUT; and it
// precedes the READY Turn scan, so a Turn the orphan handling committed but could not
// enqueue is picked up in the same pass.
//
// A RUNNING Agent NodeRun is never scanned: the next Turn is persisted by the transaction
// that completed the previous round, so recovery advances that existing READY work instead
// of deriving a duplicate round from the NodeRun.
//
// Each use case takes its own conditional claim, so a lost race with immediate advancement
// is a normal outcome, not an error; only a real failure is collected into the report.
func (r *Reconciler) scanAgentWork(ctx context.Context, now time.Time, report *Report) error {
	if err := r.scanExpiredApprovals(ctx, now, report); err != nil {
		return err
	}

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

	if err := r.scanOrphanToolAttempts(ctx, now, report); err != nil {
		return err
	}
	if err := r.scanDueConfirmDeadlines(ctx, now, report); err != nil {
		return err
	}
	if err := r.scanExpiredHumanWaits(ctx, now, report); err != nil {
		return err
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

// scanOrphanToolAttempts hands every STARTED synchronous Tool Attempt created by a process
// other than this one to the orphaned Tool Attempt use case. It is skipped when no runner
// identity is configured, because then no Attempt can be told apart from this process's
// own in-flight calls.
func (r *Reconciler) scanOrphanToolAttempts(ctx context.Context, now time.Time, report *Report) error {
	if r.cfg.RunnerID == "" {
		return nil
	}
	var orphans []domain.ToolAttempt
	if err := r.cfg.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		orphans, err = tx.ToolAttempts().ListOrphanStarted(ctx, r.cfg.RunnerID, now, r.cfg.BatchLimit)
		return err
	}); err != nil {
		return fmt.Errorf("reconciler: list orphaned tool attempts: %w", err)
	}
	report.OrphanToolAttemptsFound = len(orphans)
	for _, attempt := range orphans {
		if err := r.cfg.Executor.HandleOrphanToolAttempt(ctx, attempt.ID); err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("reconciler: handle orphaned tool attempt %s: %w", attempt.ID, err))
			continue
		}
		report.OrphanToolAttemptsHandled++
	}
	return nil
}

// scanOrphanNodeAttempts hands every STARTED Node Attempt with no deadline created by a
// process other than this one to the orphaned Node Attempt use case. Like the Tool scan it
// is skipped when no runner identity is configured, because then no Attempt can be told
// apart from this process's own in-flight calls. It does not look at the Run: an Attempt of
// an ended Run still has to end and free its Provider slot.
func (r *Reconciler) scanOrphanNodeAttempts(ctx context.Context, report *Report) error {
	if r.cfg.RunnerID == "" {
		return nil
	}
	var orphans []domain.NodeAttempt
	if err := r.cfg.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		orphans, err = tx.NodeAttempts().ListOrphanStarted(ctx, r.cfg.RunnerID, r.cfg.BatchLimit)
		return err
	}); err != nil {
		return fmt.Errorf("reconciler: list orphaned node attempts: %w", err)
	}
	report.OrphanNodeAttemptsFound = len(orphans)
	for _, attempt := range orphans {
		if err := r.cfg.Executor.HandleOrphanNodeAttempt(ctx, attempt.ID); err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("reconciler: handle orphaned node attempt %s: %w", attempt.ID, err))
			continue
		}
		report.OrphanNodeAttemptsHandled++
	}
	return nil
}

// scanDueConfirmDeadlines hands every release Attempt that passed its confirm deadline
// while its Action still waits on production to the release human wait use case, which
// re-checks everything under the Run lock and owns every write.
func (r *Reconciler) scanDueConfirmDeadlines(ctx context.Context, now time.Time, report *Report) error {
	var due []store.DueConfirmDeadline
	if err := r.cfg.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		due, err = tx.ToolAttempts().ListDueConfirmDeadlines(ctx, now, r.cfg.BatchLimit)
		return err
	}); err != nil {
		return fmt.Errorf("reconciler: list due release confirm deadlines: %w", err)
	}
	report.DueConfirmDeadlinesFound = len(due)
	for _, row := range due {
		if err := r.cfg.Executor.OpenReleaseHumanWait(ctx, row.AttemptID); err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("reconciler: open release human wait for tool attempt %s: %w", row.AttemptID, err))
			continue
		}
		report.HumanWaitsOpened++
	}
	return nil
}

// scanExpiredHumanWaits hands every OPEN human wait whose deadline has passed to the human
// wait expiry use case, which ends the release exactly as a person's stop would. A stop or
// a production answer that commits first wins, and the expiry then writes nothing.
func (r *Reconciler) scanExpiredHumanWaits(ctx context.Context, now time.Time, report *Report) error {
	var expired []domain.HumanWait
	if err := r.cfg.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		expired, err = tx.HumanWaits().ListExpiredOpen(ctx, now, r.cfg.BatchLimit)
		return err
	}); err != nil {
		return fmt.Errorf("reconciler: list expired human waits: %w", err)
	}
	report.ExpiredHumanWaitsFound = len(expired)
	for _, wait := range expired {
		if err := r.cfg.Executor.ExpireHumanWait(ctx, wait.ID); err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("reconciler: expire human wait %s: %w", wait.ID, err))
			continue
		}
		report.HumanWaitsExpired++
	}
	return nil
}

// scanDueOccupationQueries hands every ACTIVE occupation of an ended Run whose post-task
// query is due to the release reconciliation use case, which owns the conditional claim,
// the single production query and the write to the occupation. It only discovers the work.
func (r *Reconciler) scanDueOccupationQueries(ctx context.Context, now time.Time, report *Report) error {
	var due []domain.ReleaseOccupation
	if err := r.cfg.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		due, err = tx.ReleaseOccupations().ListDueQueries(ctx, now, r.cfg.BatchLimit)
		return err
	}); err != nil {
		return fmt.Errorf("reconciler: list due occupation queries: %w", err)
	}
	report.DueOccupationQueriesFound = len(due)
	for _, occupation := range due {
		outcome, err := r.cfg.Executor.ReconcileReleaseOccupation(ctx, occupation.ID)
		if outcome.Claimed {
			report.OccupationQueriesClaimed++
		}
		if outcome.Released {
			report.OccupationsReleased++
		}
		if err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("reconciler: reconcile occupation %s: %w", occupation.ID, err))
		}
	}
	return nil
}

// scanExpiredApprovals hands every PENDING approval whose deadline has passed to the
// approval expiry use case. It runs before the Agent deadline scan: an Agent waiting on an
// approval is paused and therefore never listed as expired, and once the expiry resumes it
// the deadline has moved. The use case owns the conditional decision; a person who decided
// first wins and the expiry writes nothing.
func (r *Reconciler) scanExpiredApprovals(ctx context.Context, now time.Time, report *Report) error {
	var expired []domain.Approval
	if err := r.cfg.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		expired, err = tx.Approvals().ListExpiredPending(ctx, now, r.cfg.BatchLimit)
		return err
	}); err != nil {
		return fmt.Errorf("reconciler: list expired approvals: %w", err)
	}
	report.ExpiredApprovalsFound = len(expired)
	for _, approval := range expired {
		if err := r.cfg.Executor.ExpireApproval(ctx, approval.ID); err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("reconciler: expire approval %s: %w", approval.ID, err))
			continue
		}
		report.ApprovalsExpired++
	}
	return nil
}

// scanDuePolls hands every DISPATCHED Attempt whose persisted poll time is due to the
// Provider poll use case: Node Attempts first, then the Attempts of asynchronous Agent
// Tools. It runs after the timeout scans so an Attempt already past its deadline is failed
// rather than polled, and before the Pending Callback replay, which does not depend on it.
// A poll and a callback or timeout racing for the same Attempt are decided by the use
// case's conditional updates; the Reconciler only discovers the work.
func (r *Reconciler) scanDuePolls(ctx context.Context, now time.Time, report *Report) error {
	if err := r.scanDueNodePolls(ctx, now, report); err != nil {
		return err
	}
	if err := r.scanDueToolPolls(ctx, now, report); err != nil {
		return err
	}
	return r.scanDueOccupationQueries(ctx, now, report)
}

// scanDueNodePolls is the Node Attempt row of the due-poll scan.
func (r *Reconciler) scanDueNodePolls(ctx context.Context, now time.Time, report *Report) error {
	var due []store.DuePoll
	if err := r.cfg.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		due, err = tx.NodeAttempts().ListDuePolls(ctx, now, r.cfg.BatchLimit)
		return err
	}); err != nil {
		return fmt.Errorf("reconciler: list due polls: %w", err)
	}
	report.DuePollsFound = len(due)

	for _, row := range due {
		outcome, err := r.cfg.Executor.PollAttempt(ctx, service.PollAttempt{
			AttemptID: row.AttemptID,
			NodeRunID: row.NodeRunID,
			RunID:     row.RunID,
		})
		if err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("reconciler: poll attempt %s: %w", row.AttemptID, err))
			continue
		}
		switch outcome.Kind {
		case service.PollAttemptClaimedNotFinal:
			report.PollsClaimed++
		case service.PollAttemptResumed:
			report.PollsClaimed++
			report.PollsResumed++
		case service.PollAttemptDuplicate:
			report.PollsDuplicate++
		default:
			report.PollsSkipped++
		}
	}
	return nil
}

// scanDueToolPolls is the Tool Attempt row of the due-poll scan. It only discovers the due
// Attempts through the store and hands each to the Tool poll use case, which owns the
// claim, the Provider query and every state change.
func (r *Reconciler) scanDueToolPolls(ctx context.Context, now time.Time, report *Report) error {
	var due []store.DueToolPoll
	if err := r.cfg.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		due, err = tx.ToolAttempts().ListDuePolls(ctx, now, r.cfg.BatchLimit)
		return err
	}); err != nil {
		return fmt.Errorf("reconciler: list due tool polls: %w", err)
	}
	report.DueToolPollsFound = len(due)

	for _, row := range due {
		outcome, err := r.cfg.Executor.PollToolAttempt(ctx, service.PollToolAttempt{
			AttemptID: row.AttemptID,
			ActionID:  row.ActionID,
		})
		if err != nil {
			report.Errors = append(report.Errors, fmt.Errorf("reconciler: poll tool attempt %s: %w", row.AttemptID, err))
			continue
		}
		switch outcome.Kind {
		case service.PollAttemptClaimedNotFinal:
			report.ToolPollsClaimed++
		case service.PollAttemptResumed:
			report.ToolPollsClaimed++
			report.ToolPollsResumed++
		case service.PollAttemptReturned:
			report.ToolPollsClaimed++
			report.ToolPollsReturned++
		case service.PollAttemptDuplicate:
			report.ToolPollsDuplicate++
		default:
			report.ToolPollsSkipped++
		}
	}
	return nil
}

// resumePending replays one stored early callback through the same idempotent ResumeNode
// use case a live callback or Provider Poll enters. ListConsumableForWaiting
// only returns rows whose Binding already routes to a still-DISPATCHED Attempt of a still
// WAITING_CALLBACK NodeRun -- a Node Attempt, or a Tool Attempt of a WAITING_CALLBACK Agent
// Action, which ResumeNode routes by the Binding's target type (the Reconciler
// only rediscovers work and enters the existing use case; it has no Tool-specific path) --
// so service.ErrNoCallbackBinding is not an expected outcome here
// -- it is treated as an error rather than silently dropped. A payload the registered
// Executor cannot interpret leaves the NodeRun untouched: it is logged at warn,
// without the payload, hash or token, and left for the next tick or for retention to
// eventually delete once it expires -- the Reconciler does not invent a rejection limit
// beyond the Pending Callback's own TTL (an acceptance-criteria gap; see package-level
// report notes). A row whose stored credential does not match the Attempt ResumeNode
// was about to advance (service's own consumePendingCallback check) is classified the
// same way: it is not this Reconciler's
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
