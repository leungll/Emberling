package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// This file owns the human approval of one Agent Action: entering the wait when the
// authorization decision is ASK, the authenticated decision that approves or rejects it,
// and the expiry that settles an undecided approval at its deadline. An approval covers
// exactly one Action whose arguments are already committed; approving it executes that
// committed Decision once, through the same Attempt creation and the same execution path
// the claim uses, and never asks the model again.
//
// Every write path takes the Run aggregate lock first and then settles the approval by a
// conditional update from PENDING, so approve, reject and expiry racing for the same
// approval have one winner and the losers write nothing.

// maxApprovalReasonBytes bounds the reason a person gives with a decision, in bytes, so
// the tool message that returns a rejection to the Agent carries the whole reason within
// its own byte bound.
const maxApprovalReasonBytes = 400

// codeApprovalTimeoutNotConfigured fails a call of a Tool a person must approve on an
// Agent node that bounds no approval wait: the Runtime never substitutes a wait bound.
const codeApprovalTimeoutNotConfigured = "APPROVAL_TIMEOUT_NOT_CONFIGURED"

// ApprovalHooks lets a test synchronize the decision paths at exact points. The zero
// value installs no hook.
type ApprovalHooks struct {
	// BeforeDecisionLock runs in DecideApproval and ExpireApproval after the approval was
	// read and before the Run lock is taken, so a test can hold two decisions at the point
	// where both have seen the approval PENDING.
	BeforeDecisionLock func(approvalID string)
}

// agentActionWaitingApprovalPayload is AGENT_ACTION_WAITING_APPROVAL: the Action a person
// must decide on, its approval and the deadline of that decision.
type agentActionWaitingApprovalPayload struct {
	AgentRunID string    `json:"agentRunId"`
	TurnID     string    `json:"turnId"`
	ActionID   string    `json:"actionId"`
	ApprovalID string    `json:"approvalId"`
	Deadline   time.Time `json:"deadline"`
}

// approvalDecidedPayload is APPROVAL_DECIDED: how the approval was settled and by whom.
// DecidedBy is the operator's fixed identity name, never a credential; it is absent for an
// expiry. The reason is a bounded human text and part of the approval row, not the Event.
type approvalDecidedPayload struct {
	AgentRunID string                `json:"agentRunId"`
	TurnID     string                `json:"turnId"`
	ActionID   string                `json:"actionId"`
	ApprovalID string                `json:"approvalId"`
	Decision   domain.ApprovalStatus `json:"decision"`
	DecidedBy  *string               `json:"decidedBy,omitempty"`
}

// DecideApproval is the parameter struct of ExecutionService.DecideApproval. Decision is
// APPROVED or REJECTED; Reason is required for a rejection and bounded for both; DecidedBy
// is the identity the transport authenticated.
type DecideApproval struct {
	ApprovalID string
	Decision   domain.ApprovalStatus
	Reason     string
	DecidedBy  string
}

// DecideApprovalOutcome reports the approval as it stands after the call. Repeated is true
// when the same decision had already been recorded and nothing was written.
type DecideApprovalOutcome struct {
	Approval domain.Approval
	Repeated bool
}

// ApprovalConflictError reports a decision that cannot be recorded because the approval
// was already settled differently, expired, or its Run can no longer act on it. Nothing
// was written.
type ApprovalConflictError struct {
	ApprovalID string
	Status     domain.ApprovalStatus
}

func (e *ApprovalConflictError) Error() string {
	return fmt.Sprintf("execution: approval %s is already %s", e.ApprovalID, e.Status)
}

// ApprovalNotActionableError reports a decision on an approval that is still PENDING but
// whose Run or Agent Run has already ended: there is nothing left for the decision to act
// on. The approval is not decided; the expiry settles it as EXPIRED at its deadline.
// Nothing was written.
type ApprovalNotActionableError struct {
	ApprovalID string
}

func (e *ApprovalNotActionableError) Error() string {
	return fmt.Sprintf("execution: approval %s belongs to a run that has ended and can no longer be acted on", e.ApprovalID)
}

// ApprovalRequestError reports a decision request that is malformed before anything is
// read: an unknown decision, a missing or oversized reason, or a missing identity.
type ApprovalRequestError struct {
	Reason string
}

func (e *ApprovalRequestError) Error() string { return "execution: decide approval: " + e.Reason }

// enterApprovalWaitLocked suspends the RUNNING Action c holds until a person decides, in
// the claim transaction: the Action moves to WAITING_APPROVAL with the ASK decision, a
// PENDING approval with its deadline is created, the Agent's clock is paused, the Agent
// NodeRun waits and the Run re-aggregates to PAUSED. No Attempt exists and no Tool runs.
// The approval wait bound comes from the Run's bound Definition version, read through the
// plan the caller resolved before the lock.
func (s *ExecutionService) enterApprovalWaitLocked(ctx context.Context, tx store.Tx, c *agentActionClaim, now time.Time) error {
	if c.plan == nil {
		return fmt.Errorf("execution: suspend agent action %s for approval: the caller must resolve the run and its plan before the lock", c.action.ID)
	}
	// The same Action never asks twice: an approval row already covering it means the
	// claim path was re-entered for an approved Action, which the approval use case owns.
	if _, err := tx.Approvals().GetByActionID(ctx, c.action.ID); err == nil {
		return fmt.Errorf("execution: suspend agent action %s for approval: an approval already covers it", c.action.ID)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	timeout, err := approvalTimeout(c.plan, c.nodeRun.NodeID)
	if err != nil {
		return err
	}
	if timeout == nil {
		return s.settleToolActionLocked(ctx, tx, c, failVerdict(domain.TerminationToolError, domain.ExecutionError{
			Code:    codeApprovalTimeoutNotConfigured,
			Message: fmt.Sprintf("tool %q needs a human approval, but the Agent node sets no approvalTimeoutMs", c.toolName),
		}), now)
	}

	if err := tx.AgentActions().MarkWaitingApproval(ctx, c.action.ID, now); err != nil {
		if errors.Is(err, domain.ErrStaleClaim) {
			return errAgentTurnSuperseded
		}
		return err
	}
	approval := domain.Approval{
		ID:          s.deps.IDs.NewID(domain.IDPrefixApproval),
		ActionID:    c.action.ID,
		RunID:       c.nodeRun.RunID,
		AgentRunID:  c.agentRun.ID,
		Status:      domain.ApprovalPending,
		RequestedAt: now,
		Deadline:    now.Add(*timeout),
	}
	if err := tx.Approvals().Create(ctx, approval); err != nil {
		return err
	}
	if err := s.appendEvent(ctx, tx, c.lock, c.nodeRun.RunID, &c.nodeRun.ID, domain.EventAgentActionWaitingApproval, now, agentActionWaitingApprovalPayload{
		AgentRunID: c.agentRun.ID,
		TurnID:     c.turn.ID,
		ActionID:   c.action.ID,
		ApprovalID: approval.ID,
		Deadline:   approval.Deadline,
	}); err != nil {
		return err
	}
	if _, err := s.pauseAgentLocked(ctx, tx, c.lock, c.nodeRun, c.agentRun, domain.PauseApproval, now); err != nil {
		return err
	}
	// The Agent NodeRun waits exactly as it does on an asynchronous Tool: Run status is
	// derived from NodeRun state, so the Run becomes PAUSED once nothing else is running.
	if err := tx.NodeRuns().MarkWaiting(ctx, c.nodeRun.ID, now); err != nil {
		if errors.Is(err, domain.ErrStaleClaim) {
			return errAgentTurnSuperseded
		}
		return err
	}
	if err := s.aggregateRunLocked(ctx, tx, c.lock, c.run, c.plan, now); err != nil {
		return err
	}
	c.commit = agentCommit{runID: c.nodeRun.RunID, lastSeq: c.lock.LastSeq()}
	return nil
}

// approvalTimeout reads the approval wait bound of the Agent node from the Run's immutable
// Definition version. Nil means the node bounds no approval wait.
func approvalTimeout(plan *runtime.CompiledDefinition, nodeID string) (*time.Duration, error) {
	for _, node := range plan.Definition.Nodes {
		if node.ID != nodeID {
			continue
		}
		cfg, err := runtime.ParseAgentNodeConfig(node.Config)
		if err != nil {
			return nil, fmt.Errorf("execution: read approval timeout of node %q: %w", nodeID, err)
		}
		if cfg.ApprovalTimeoutMs == nil {
			return nil, nil
		}
		timeout := time.Duration(*cfg.ApprovalTimeoutMs) * time.Millisecond
		return &timeout, nil
	}
	return nil, fmt.Errorf("execution: read approval timeout: node %q is not in the bound definition", nodeID)
}

// approvalDecisionTarget is everything a decision reads before and under the Run lock.
type approvalDecisionTarget struct {
	approval domain.Approval
	action   domain.AgentAction
	decision domain.AgentDecision
	toolName string
	turn     domain.AgentTurn
	agentRun domain.AgentRun
	nodeRun  domain.NodeRun
	run      domain.Run
	plan     *runtime.CompiledDefinition
	lock     *store.RunLock
}

// lockApprovalTarget resolves the approval's Run and plan before the lock, takes the Run
// lock and re-reads the approval and the rows the lock protects. It runs the test hook
// between the first read and the lock.
func (s *ExecutionService) lockApprovalTarget(ctx context.Context, tx store.Tx, approvalID string) (approvalDecisionTarget, error) {
	var t approvalDecisionTarget
	approval, err := tx.Approvals().Get(ctx, approvalID)
	if err != nil {
		return t, err
	}
	if t.run, t.plan, err = s.runPlan(ctx, tx, approval.RunID); err != nil {
		return t, err
	}
	if s.deps.ApprovalHooks.BeforeDecisionLock != nil {
		s.deps.ApprovalHooks.BeforeDecisionLock(approvalID)
	}
	if t.lock, err = tx.Runs().LockForUpdate(ctx, approval.RunID); err != nil {
		return t, err
	}
	// Re-read what the lock now protects: another decision or the expiry may have
	// committed between the first read and the lock.
	if t.approval, err = tx.Approvals().Get(ctx, approvalID); err != nil {
		return t, err
	}
	if t.action, err = tx.AgentActions().Get(ctx, t.approval.ActionID); err != nil {
		return t, err
	}
	if t.decision, err = tx.AgentDecisions().Get(ctx, t.action.DecisionID); err != nil {
		return t, err
	}
	if t.decision.ToolName != nil {
		t.toolName = *t.decision.ToolName
	}
	if t.turn, err = tx.AgentTurns().Get(ctx, t.action.TurnID); err != nil {
		return t, err
	}
	if t.agentRun, err = tx.AgentRuns().Get(ctx, t.approval.AgentRunID); err != nil {
		return t, err
	}
	if t.nodeRun, err = tx.NodeRuns().Get(ctx, t.agentRun.NodeRunID); err != nil {
		return t, err
	}
	return t, nil
}

// DecideApproval records one authenticated decision on a PENDING approval, in a single
// transaction under the Run aggregate lock. A decision already recorded is answered without
// a write: the same decision again returns it as Repeated, a different one or an expiry is
// an ApprovalConflictError. A PENDING approval whose Run or Agent Run has ended is an
// ApprovalNotActionableError, and only the expiry settles it.
//
// A rejection resumes the Agent's clock and returns the Action to the Agent with the
// person's reason, so the next Turn can decide what to do. An approval hands the Action
// its execution right back, re-runs every pre-execution check against the facts as they
// are now, creates the STARTED Attempt and resumes the clock; a check that no longer passes
// returns or fails the Action exactly as the claim would, with the approval kept APPROVED.
// After COMMIT the prepared Tool call is offered to the work queue, where a worker makes it
// on the same path a claim continues on; the response reflects only the committed approval.
// A returned refusal's next Turn is offered to the work queue the same way.
func (s *ExecutionService) DecideApproval(ctx context.Context, req DecideApproval) (DecideApprovalOutcome, error) {
	if err := req.validate(); err != nil {
		return DecideApprovalOutcome{}, err
	}

	var (
		outcome    DecideApprovalOutcome
		commit     agentCommit
		nextTurnID string
		call       *agentToolCall
	)
	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		t, err := s.lockApprovalTarget(ctx, tx, req.ApprovalID)
		if err != nil {
			return err
		}
		now := s.deps.Clock.Now()
		if t.approval.Status.IsDecided() {
			if t.approval.Status == req.Decision {
				outcome = DecideApprovalOutcome{Approval: t.approval, Repeated: true}
				return nil
			}
			return &ApprovalConflictError{ApprovalID: t.approval.ID, Status: t.approval.Status}
		}
		if !t.actionable() {
			// The Run or the Agent Run ended while the approval was open; the decision has
			// nothing left to act on and the approval stays PENDING for the expiry.
			return &ApprovalNotActionableError{ApprovalID: t.approval.ID}
		}

		decidedBy := req.DecidedBy
		var reason *string
		if req.Reason != "" {
			text := req.Reason
			reason = &text
		}
		won, err := tx.Approvals().Decide(ctx, t.approval.ID, req.Decision, now, &decidedBy, reason)
		if err != nil {
			return err
		}
		if !won {
			return errAgentTurnSuperseded
		}
		t.approval.Status, t.approval.DecidedAt, t.approval.DecidedBy, t.approval.Reason = req.Decision, &now, &decidedBy, reason
		if err := s.appendEvent(ctx, tx, t.lock, t.nodeRun.RunID, &t.nodeRun.ID, domain.EventApprovalDecided, now, approvalDecidedPayload{
			AgentRunID: t.agentRun.ID, TurnID: t.turn.ID, ActionID: t.action.ID, ApprovalID: t.approval.ID,
			Decision: req.Decision, DecidedBy: &decidedBy,
		}); err != nil {
			return err
		}
		outcome = DecideApprovalOutcome{Approval: t.approval}

		if req.Decision == domain.ApprovalRejected {
			nextTurnID, err = s.returnWaitingApprovalLocked(ctx, tx, t, domain.ReturnApprovalRejected,
				fmt.Sprintf("A person rejected the %s call: %s", boundedToolName(t.toolName), req.Reason), now)
			if err != nil {
				return err
			}
			commit = agentCommit{runID: t.nodeRun.RunID, lastSeq: t.lock.LastSeq(), terminal: nextTurnID == ""}
			return nil
		}

		call, commit, nextTurnID, err = s.resumeApprovedActionLocked(ctx, tx, t, now)
		return err
	})
	switch {
	case errors.Is(err, errAgentTurnSuperseded):
		return DecideApprovalOutcome{}, &ApprovalConflictError{ApprovalID: req.ApprovalID, Status: domain.ApprovalPending}
	case err != nil:
		return DecideApprovalOutcome{}, err
	}
	if outcome.Repeated {
		return outcome, nil
	}
	s.wake(commit)
	if nextTurnID != "" {
		s.enqueueAgentTurn(commit.runID, nextTurnID)
	}
	if call != nil {
		// The approved Tool is called by a worker, holding no lock, on the same path a
		// claim continues on; the request that carried the decision owns none of it.
		s.enqueueApprovedCall(call)
	}
	return outcome, nil
}

// actionable reports whether a decision still has something to act on: the Run is not
// terminal, the Action still waits for the approval and the Agent Run has not ended.
func (t approvalDecisionTarget) actionable() bool {
	return !t.lock.Status().IsTerminal() && t.action.Status == domain.AgentActionWaitingApproval && t.agentRun.Termination == nil
}

func (req DecideApproval) validate() error {
	if req.Decision != domain.ApprovalApproved && req.Decision != domain.ApprovalRejected {
		return &ApprovalRequestError{Reason: fmt.Sprintf("decision must be %s or %s", domain.ApprovalApproved, domain.ApprovalRejected)}
	}
	if req.DecidedBy == "" {
		return &ApprovalRequestError{Reason: "the deciding identity is required"}
	}
	if len(req.Reason) > maxApprovalReasonBytes {
		return &ApprovalRequestError{Reason: fmt.Sprintf("reason must be at most %d bytes", maxApprovalReasonBytes)}
	}
	if req.Decision == domain.ApprovalRejected && req.Reason == "" {
		return &ApprovalRequestError{Reason: "a rejection requires a reason"}
	}
	return nil
}

// returnWaitingApprovalLocked resumes the Agent's clock and hands the WAITING_APPROVAL
// Action back to the Agent as a refusal, which also resumes the Agent NodeRun from its
// wait and re-derives the Run status. The clock resumes first, so the round advancement
// compares the moved deadline, not the one frozen while the person was deciding.
func (s *ExecutionService) returnWaitingApprovalLocked(ctx context.Context, tx store.Tx, t approvalDecisionTarget, code domain.AgentReturnCode, message string, now time.Time) (string, error) {
	agentRun, err := s.resumeAgentLocked(ctx, tx, t.lock, t.nodeRun, t.agentRun, now)
	if err != nil {
		return "", err
	}
	return s.returnActionToAgentLocked(ctx, tx, agentActionReturn{
		lock: t.lock, nodeRun: t.nodeRun, agentRun: agentRun, turn: t.turn, action: t.action, toolName: t.toolName,
		run: t.run, plan: t.plan,
	}, domain.AgentActionWaitingApproval, code, message, now)
}

// resumeApprovedActionLocked continues an approved Action inside the decision transaction:
// the Action takes its execution right back (WAITING_APPROVAL -> RUNNING), the Agent
// NodeRun leaves its wait and the Run re-aggregates; then every pre-execution check runs
// again against the facts as they are now, because the approval only covered the call's
// arguments, not the world around them. A check that fails resumes the clock and settles
// the Action as the claim would, with the approval kept APPROVED. When every check passes
// the STARTED Attempt is created once and the clock resumes, so the call's deadline and any
// callback credential it issues are bound to the moved deadline. A Tool whose registration
// marks it reconcilable then pauses the Agent again at once, for the release the Runtime
// will reconcile: the approval wait is credited back, and the release wait starts its own
// pause, so neither counts against the Agent's time. It returns the call to make after
// COMMIT (nil when the Action was settled instead) and what the transaction owes.
func (s *ExecutionService) resumeApprovedActionLocked(ctx context.Context, tx store.Tx, t approvalDecisionTarget, now time.Time) (*agentToolCall, agentCommit, string, error) {
	if err := tx.AgentActions().ResumeApproved(ctx, t.action.ID, now); err != nil {
		if errors.Is(err, domain.ErrStaleClaim) {
			return nil, agentCommit{}, "", errAgentTurnSuperseded
		}
		return nil, agentCommit{}, "", err
	}
	t.action.Status = domain.AgentActionRunning
	if err := tx.NodeRuns().Transition(ctx, t.nodeRun.ID, domain.NodeRunWaitingCallback, domain.NodeRunRunning, now); err != nil {
		if errors.Is(err, domain.ErrStaleClaim) {
			return nil, agentCommit{}, "", errAgentTurnSuperseded
		}
		return nil, agentCommit{}, "", err
	}
	t.nodeRun.Status = domain.NodeRunRunning
	if err := s.aggregateRunLocked(ctx, tx, t.lock, t.run, t.plan, now); err != nil {
		return nil, agentCommit{}, "", err
	}
	// The Run just left PAUSED, but the lock still reports the status it read when it was
	// taken, and the Attempt creation below writes that status back as the seq watermark.
	// Re-reading the row this transaction already holds refreshes the lock to RUNNING
	// without releasing anything.
	lock, err := tx.Runs().LockForUpdate(ctx, t.run.ID)
	if err != nil {
		return nil, agentCommit{}, "", err
	}
	t.lock = lock

	c := &agentActionClaim{
		lock: t.lock, nodeRun: t.nodeRun, agentRun: t.agentRun, turn: t.turn, action: t.action, decision: t.decision,
		toolName: t.toolName, claimSource: domain.ClaimImmediate, run: t.run, plan: t.plan,
	}
	verdict, err := s.checkToolActionLocked(ctx, tx, c)
	if err != nil {
		return nil, agentCommit{}, "", err
	}
	if !verdict.ok() {
		if c.agentRun, err = s.resumeAgentLocked(ctx, tx, t.lock, t.nodeRun, t.agentRun, now); err != nil {
			return nil, agentCommit{}, "", err
		}
		if err := s.settleToolActionLocked(ctx, tx, c, verdict, now); err != nil {
			return nil, agentCommit{}, "", err
		}
		return nil, c.commit, c.nextTurnID, nil
	}

	if c.agentRun, err = s.resumeAgentLocked(ctx, tx, t.lock, t.nodeRun, t.agentRun, now); err != nil {
		return nil, agentCommit{}, "", err
	}
	if c.reg.Metadata.Reconcilable {
		if c.agentRun, err = s.pauseAgentLocked(ctx, tx, t.lock, t.nodeRun, c.agentRun, domain.PauseRelease, now); err != nil {
			return nil, agentCommit{}, "", err
		}
	}
	call, err := s.startToolAttemptLocked(ctx, tx, c, verdict.inputs, now)
	if err != nil {
		return nil, agentCommit{}, "", err
	}
	return call, c.commit, "", nil
}

// ExpireApproval settles a PENDING approval whose deadline has passed on the injected
// clock: the approval becomes EXPIRED, APPROVAL_DECIDED records it, the Agent's clock
// resumes and the Action is returned to the Agent as APPROVAL_EXPIRED, all in one
// transaction under the Run lock. An approval a person decided first, or whose deadline has
// not passed, is left untouched. When the Run or the Agent Run ended while the approval was
// open there is nothing to return to: the approval still becomes EXPIRED with its Event, so
// it leaves the expiry scan, and the Agent's clock and Action are left as the termination
// left them. After COMMIT the next Turn, if any, is offered to the work queue.
func (s *ExecutionService) ExpireApproval(ctx context.Context, approvalID string) error {
	var commit agentCommit
	var nextTurnID string
	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		t, err := s.lockApprovalTarget(ctx, tx, approvalID)
		if err != nil {
			return err
		}
		now := s.deps.Clock.Now()
		if t.approval.Status.IsDecided() || now.Before(t.approval.Deadline) {
			return nil
		}
		won, err := tx.Approvals().Decide(ctx, t.approval.ID, domain.ApprovalExpired, now, nil, nil)
		if err != nil {
			return err
		}
		if !won {
			return errAgentTurnSuperseded
		}
		if err := s.appendEvent(ctx, tx, t.lock, t.nodeRun.RunID, &t.nodeRun.ID, domain.EventApprovalDecided, now, approvalDecidedPayload{
			AgentRunID: t.agentRun.ID, TurnID: t.turn.ID, ActionID: t.action.ID, ApprovalID: t.approval.ID,
			Decision: domain.ApprovalExpired,
		}); err != nil {
			return err
		}
		if !t.actionable() {
			// Expiring changes no Run-level status; UpdateAggregate persists the seq
			// watermark APPROVAL_DECIDED allocated under the lock.
			if err := tx.Runs().UpdateAggregate(ctx, t.lock, t.lock.Status(), now); err != nil {
				return err
			}
			commit = agentCommit{runID: t.nodeRun.RunID, lastSeq: t.lock.LastSeq()}
			return nil
		}
		nextTurnID, err = s.returnWaitingApprovalLocked(ctx, tx, t, domain.ReturnApprovalExpired,
			fmt.Sprintf("Nobody approved the %s call before its approval deadline.", boundedToolName(t.toolName)), now)
		if err != nil {
			return err
		}
		commit = agentCommit{runID: t.nodeRun.RunID, lastSeq: t.lock.LastSeq(), terminal: nextTurnID == ""}
		return nil
	})
	if errors.Is(err, errAgentTurnSuperseded) {
		return nil
	}
	if err != nil {
		return err
	}
	s.wake(commit)
	if nextTurnID != "" {
		s.enqueueAgentTurn(commit.runID, nextTurnID)
	}
	return nil
}
