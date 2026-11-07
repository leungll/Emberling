package domain

import "time"

// AgentGoal is the task goal an Agent Run is frozen to: prepare the change only, or fix
// and deploy it. The goal decides whether a production-risk Tool may be called at all.
type AgentGoal string

const (
	AgentGoalPrepareOnly  AgentGoal = "PREPARE_ONLY"
	AgentGoalFixAndDeploy AgentGoal = "FIX_AND_DEPLOY"
)

func (g AgentGoal) IsValid() bool {
	return g == AgentGoalPrepareOnly || g == AgentGoalFixAndDeploy
}

// AuthorizationDecision is what the claim decided for one Action before any Attempt
// existed: run it, ask a person first, or refuse it.
type AuthorizationDecision string

const (
	AuthorizationAllow AuthorizationDecision = "ALLOW"
	AuthorizationAsk   AuthorizationDecision = "ASK"
	AuthorizationDeny  AuthorizationDecision = "DENY"
)

func (d AuthorizationDecision) IsValid() bool {
	return d == AuthorizationAllow || d == AuthorizationAsk || d == AuthorizationDeny
}

// AgentPauseReason says what an Agent Run is waiting on while its clock is stopped.
type AgentPauseReason string

const (
	// PauseApproval is the wait for a human approval decision.
	PauseApproval AgentPauseReason = "APPROVAL"
	// PauseRelease is the wait for the Runtime to reconcile a release's outcome.
	PauseRelease AgentPauseReason = "RELEASE"
	// PauseHuman is the wait for a human fact after a Tool reported.
	PauseHuman AgentPauseReason = "HUMAN"
)

func (r AgentPauseReason) IsValid() bool {
	return r == PauseApproval || r == PauseRelease || r == PauseHuman
}

// ApprovalStatus is the state of one human decision. PENDING is the only non-terminal
// state; the three terminal ones are decided exactly once by a conditional update.
type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "PENDING"
	ApprovalApproved ApprovalStatus = "APPROVED"
	ApprovalRejected ApprovalStatus = "REJECTED"
	ApprovalExpired  ApprovalStatus = "EXPIRED"
)

func (s ApprovalStatus) IsValid() bool {
	switch s {
	case ApprovalPending, ApprovalApproved, ApprovalRejected, ApprovalExpired:
		return true
	default:
		return false
	}
}

// IsDecided reports whether a person or the deadline settled the approval.
func (s ApprovalStatus) IsDecided() bool { return s != ApprovalPending }

// Approval is one human decision requested for one Action whose authorization was ASK.
// It is created PENDING in the transaction that suspended the Action and settled once:
// APPROVED or REJECTED by an authenticated person, EXPIRED by the deadline. The Action it
// covers has its arguments committed already, so the decision never covers another call.
type Approval struct {
	ID         string         `json:"id"`
	ActionID   string         `json:"actionId"`
	RunID      string         `json:"runId"`
	AgentRunID string         `json:"agentRunId"`
	Status     ApprovalStatus `json:"status"`
	// RequestedAt is when the Action was suspended; Deadline is when an undecided
	// approval expires and the call is handed back to the Agent as refused.
	RequestedAt time.Time `json:"requestedAt"`
	Deadline    time.Time `json:"deadline"`
	// DecidedAt, DecidedBy and Reason are written once with a terminal status. DecidedBy
	// is the authenticated operator's fixed identity name, never a credential; it is nil
	// for an expiry. Reason is the person's text, required on a rejection.
	DecidedAt *time.Time `json:"decidedAt"`
	DecidedBy *string    `json:"decidedBy"`
	Reason    *string    `json:"reason"`
}
