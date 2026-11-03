package domain

import (
	"encoding/json"
	"time"
)

// Event proves how execution facts changed. It is append-only and ordered by Seq within
// one Run. Events are not a snapshot: reconnecting clients read the Run Snapshot first,
// then replay events with Seq greater than the Snapshot's LastSeq.
//
// Payload carries only bounded summaries, hashes, metadata or stable references. System
// secrets (Provider credentials, callback tokens, signing secrets, storage keys) must
// never be written into it.
type Event struct {
	ID        string          `json:"id"`
	RunID     string          `json:"runId"`
	NodeRunID *string         `json:"nodeRunId"`
	Type      EventType       `json:"type"`
	Seq       int64           `json:"seq"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

// EventType is the MVP event vocabulary. RUN_CANCELLED, NODE_SKIPPED, NODE_CANCELLED and
// the MODEL_STREAM_* types are Phase 2 and are absent here and in the database constraint.
type EventType string

const (
	EventRunCreated   EventType = "RUN_CREATED"
	EventRunPaused    EventType = "RUN_PAUSED"
	EventRunResumed   EventType = "RUN_RESUMED"
	EventRunCompleted EventType = "RUN_COMPLETED"
	EventRunFailed    EventType = "RUN_FAILED"

	EventNodeReady            EventType = "NODE_READY"
	EventNodeStarted          EventType = "NODE_STARTED"
	EventNodeRetrying         EventType = "NODE_RETRYING"
	EventNodeDispatched       EventType = "NODE_DISPATCHED"
	EventNodeCallbackReceived EventType = "NODE_CALLBACK_RECEIVED"
	EventNodeCompleted        EventType = "NODE_COMPLETED"
	EventNodeFailed           EventType = "NODE_FAILED"
	// EventNodeWaitingCapacity records the first time a READY NodeRun was refused a
	// Provider slot. It is written at most once per NodeRun and does not change its status.
	EventNodeWaitingCapacity EventType = "NODE_WAITING_CAPACITY"

	EventAgentStarted           EventType = "AGENT_STARTED"
	EventAgentTurnReady         EventType = "AGENT_TURN_READY"
	EventAgentTurnStarted       EventType = "AGENT_TURN_STARTED"
	EventAgentDecisionCommitted EventType = "AGENT_DECISION_COMMITTED"
	EventAgentActionStarted     EventType = "AGENT_ACTION_STARTED"
	EventAgentActionWaiting     EventType = "AGENT_ACTION_WAITING"
	EventAgentActionCompleted   EventType = "AGENT_ACTION_COMPLETED"
	EventAgentActionFailed      EventType = "AGENT_ACTION_FAILED"
	EventAgentStateUpdated      EventType = "AGENT_STATE_UPDATED"
	EventAgentCompleted         EventType = "AGENT_COMPLETED"
	EventAgentFailed            EventType = "AGENT_FAILED"

	// The authorization and human-wait vocabulary. The approval and pause paths write the
	// first four. RELEASE_OUTCOME_UNKNOWN and RELEASE_VOIDED are written by the release
	// reconciliation path, AGENT_ACTION_WAITING_HUMAN by the human-wait path and
	// AGENT_COMPLETION_EVALUATED by the goal-based completion check; those paths do not
	// exist yet, the vocabulary and the database constraint already admit them.
	EventAgentActionWaitingApproval EventType = "AGENT_ACTION_WAITING_APPROVAL"
	EventApprovalDecided            EventType = "APPROVAL_DECIDED"
	EventAgentPaused                EventType = "AGENT_PAUSED"
	EventAgentResumed               EventType = "AGENT_RESUMED"
	EventReleaseOutcomeUnknown      EventType = "RELEASE_OUTCOME_UNKNOWN"
	EventReleaseVoided              EventType = "RELEASE_VOIDED"
	EventAgentActionWaitingHuman    EventType = "AGENT_ACTION_WAITING_HUMAN"
	EventAgentCompletionEvaluated   EventType = "AGENT_COMPLETION_EVALUATED"
)

var eventTypes = map[EventType]struct{}{
	EventRunCreated: {}, EventRunPaused: {}, EventRunResumed: {},
	EventRunCompleted: {}, EventRunFailed: {},

	EventNodeReady: {}, EventNodeStarted: {}, EventNodeRetrying: {},
	EventNodeDispatched: {}, EventNodeCallbackReceived: {},
	EventNodeCompleted: {}, EventNodeFailed: {}, EventNodeWaitingCapacity: {},

	EventAgentStarted: {}, EventAgentTurnReady: {}, EventAgentTurnStarted: {},
	EventAgentDecisionCommitted: {}, EventAgentActionStarted: {},
	EventAgentActionWaiting: {}, EventAgentActionCompleted: {},
	EventAgentActionFailed: {}, EventAgentStateUpdated: {},
	EventAgentCompleted: {}, EventAgentFailed: {},

	EventAgentActionWaitingApproval: {}, EventApprovalDecided: {},
	EventAgentPaused: {}, EventAgentResumed: {},
	EventReleaseOutcomeUnknown: {}, EventReleaseVoided: {},
	EventAgentActionWaitingHuman: {}, EventAgentCompletionEvaluated: {},
}

func (t EventType) IsValid() bool {
	_, ok := eventTypes[t]
	return ok
}

// ClaimSource records who won the conditional claim of a READY Turn or Action.
type ClaimSource string

const (
	ClaimImmediate  ClaimSource = "IMMEDIATE"
	ClaimReconciler ClaimSource = "RECONCILER"
)

func (s ClaimSource) IsValid() bool {
	return s == ClaimImmediate || s == ClaimReconciler
}

// CompletionSource records which path delivered a successful result.
type CompletionSource string

const (
	CompletionSyncExecution CompletionSource = "SYNC_EXECUTION"
	CompletionCallback      CompletionSource = "CALLBACK"
	CompletionProviderPoll  CompletionSource = "PROVIDER_POLL"
)

func (s CompletionSource) IsValid() bool {
	switch s {
	case CompletionSyncExecution, CompletionCallback, CompletionProviderPoll:
		return true
	default:
		return false
	}
}

// FailureSource records which path produced a failure. It adds TIMEOUT to the completion
// sources, because a deadline can fail work that no path ever completed, and RUNNER_LOST,
// because a synchronous call can be left without the process that was executing it.
//
// AUTHORIZATION, HUMAN and PROVIDER_REFUSAL are the sources of a definite refusal that is
// returned to the Agent rather than ending the Agent Run: the Runtime refused the call on
// the Run's facts, a person rejected or let expire what the call asked for, or the
// external system answered with a definite no. Their Events carry returnedToAgent.
type FailureSource string

const (
	FailureSyncExecution   FailureSource = "SYNC_EXECUTION"
	FailureCallback        FailureSource = "CALLBACK"
	FailureProviderPoll    FailureSource = "PROVIDER_POLL"
	FailureTimeout         FailureSource = "TIMEOUT"
	FailureRunnerLost      FailureSource = "RUNNER_LOST"
	FailureAuthorization   FailureSource = "AUTHORIZATION"
	FailureHuman           FailureSource = "HUMAN"
	FailureProviderRefusal FailureSource = "PROVIDER_REFUSAL"
)

func (s FailureSource) IsValid() bool {
	switch s {
	case FailureSyncExecution, FailureCallback, FailureProviderPoll, FailureTimeout, FailureRunnerLost,
		FailureAuthorization, FailureHuman, FailureProviderRefusal:
		return true
	default:
		return false
	}
}
