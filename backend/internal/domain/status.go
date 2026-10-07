package domain

// RunStatus is the aggregated status of one Execution. A Run never has a SUCCEEDED
// status: its success terminal is COMPLETED. CANCELLED is Phase 2 and is deliberately
// absent from both this set and the database CHECK constraint.
type RunStatus string

const (
	RunRunning   RunStatus = "RUNNING"
	RunPaused    RunStatus = "PAUSED"
	RunCompleted RunStatus = "COMPLETED"
	RunFailed    RunStatus = "FAILED"
)

func (s RunStatus) IsValid() bool {
	switch s {
	case RunRunning, RunPaused, RunCompleted, RunFailed:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether the Run can no longer change status.
func (s RunStatus) IsTerminal() bool {
	return s == RunCompleted || s == RunFailed
}

// NodeRunStatus decides whether the Workflow may schedule downstream nodes.
// SKIPPED and CANCELLED are Phase 2.
type NodeRunStatus string

const (
	NodeRunReady           NodeRunStatus = "READY"
	NodeRunRunning         NodeRunStatus = "RUNNING"
	NodeRunWaitingCallback NodeRunStatus = "WAITING_CALLBACK"
	NodeRunSucceeded       NodeRunStatus = "SUCCEEDED"
	NodeRunFailed          NodeRunStatus = "FAILED"
)

func (s NodeRunStatus) IsValid() bool {
	switch s {
	case NodeRunReady, NodeRunRunning, NodeRunWaitingCallback, NodeRunSucceeded, NodeRunFailed:
		return true
	default:
		return false
	}
}

func (s NodeRunStatus) IsTerminal() bool {
	return s == NodeRunSucceeded || s == NodeRunFailed
}

// nodeRunTransitions mirrors the NodeRun state machine. WAITING_CALLBACK returns to
// RUNNING only for an Agent NodeRun resuming after an async Tool; a plain async Node
// resumes straight to SUCCEEDED.
var nodeRunTransitions = map[NodeRunStatus][]NodeRunStatus{
	NodeRunReady:           {NodeRunRunning},
	NodeRunRunning:         {NodeRunWaitingCallback, NodeRunSucceeded, NodeRunFailed},
	NodeRunWaitingCallback: {NodeRunRunning, NodeRunSucceeded, NodeRunFailed},
	NodeRunSucceeded:       nil,
	NodeRunFailed:          nil,
}

// CanTransitionTo reports whether the state machine allows this transition. It does not
// decide whether the caller holds the claim; that is settled by a conditional update.
func (s NodeRunStatus) CanTransitionTo(to NodeRunStatus) bool {
	for _, allowed := range nodeRunTransitions[s] {
		if allowed == to {
			return true
		}
	}
	return false
}

// NodeAttemptStatus records one real execution or external dispatch of an EXECUTOR node.
type NodeAttemptStatus string

const (
	NodeAttemptStarted    NodeAttemptStatus = "STARTED"
	NodeAttemptDispatched NodeAttemptStatus = "DISPATCHED"
	NodeAttemptSucceeded  NodeAttemptStatus = "SUCCEEDED"
	NodeAttemptFailed     NodeAttemptStatus = "FAILED"
)

func (s NodeAttemptStatus) IsValid() bool {
	switch s {
	case NodeAttemptStarted, NodeAttemptDispatched, NodeAttemptSucceeded, NodeAttemptFailed:
		return true
	default:
		return false
	}
}

func (s NodeAttemptStatus) IsTerminal() bool {
	return s == NodeAttemptSucceeded || s == NodeAttemptFailed
}

var nodeAttemptTransitions = map[NodeAttemptStatus][]NodeAttemptStatus{
	NodeAttemptStarted:    {NodeAttemptDispatched, NodeAttemptSucceeded, NodeAttemptFailed},
	NodeAttemptDispatched: {NodeAttemptSucceeded, NodeAttemptFailed},
	NodeAttemptSucceeded:  nil,
	NodeAttemptFailed:     nil,
}

func (s NodeAttemptStatus) CanTransitionTo(to NodeAttemptStatus) bool {
	for _, allowed := range nodeAttemptTransitions[s] {
		if allowed == to {
			return true
		}
	}
	return false
}

// ToolAttemptStatus uses the same set as NodeAttemptStatus. Tool Attempts are a separate
// entity, so they carry a separate type rather than aliasing the Node Attempt status.
type ToolAttemptStatus string

const (
	ToolAttemptStarted    ToolAttemptStatus = "STARTED"
	ToolAttemptDispatched ToolAttemptStatus = "DISPATCHED"
	ToolAttemptSucceeded  ToolAttemptStatus = "SUCCEEDED"
	ToolAttemptFailed     ToolAttemptStatus = "FAILED"
)

func (s ToolAttemptStatus) IsValid() bool {
	switch s {
	case ToolAttemptStarted, ToolAttemptDispatched, ToolAttemptSucceeded, ToolAttemptFailed:
		return true
	default:
		return false
	}
}

// A synchronous call resolves from STARTED; an asynchronous one resolves from DISPATCHED
// when its callback arrives. A terminal Attempt never moves again, so a
// duplicated or late result cannot overwrite the committed one.
var toolAttemptTransitions = map[ToolAttemptStatus][]ToolAttemptStatus{
	ToolAttemptStarted:    {ToolAttemptDispatched, ToolAttemptSucceeded, ToolAttemptFailed},
	ToolAttemptDispatched: {ToolAttemptSucceeded, ToolAttemptFailed},
	ToolAttemptSucceeded:  nil,
	ToolAttemptFailed:     nil,
}

func (s ToolAttemptStatus) CanTransitionTo(to ToolAttemptStatus) bool {
	for _, allowed := range toolAttemptTransitions[s] {
		if allowed == to {
			return true
		}
	}
	return false
}

// AgentTurnStatus tracks one model interaction. A Turn has no WAITING_CALLBACK state;
// waiting belongs to the Agent Action that follows the Decision.
type AgentTurnStatus string

const (
	AgentTurnReady     AgentTurnStatus = "READY"
	AgentTurnRunning   AgentTurnStatus = "RUNNING"
	AgentTurnCompleted AgentTurnStatus = "COMPLETED"
	AgentTurnFailed    AgentTurnStatus = "FAILED"
)

func (s AgentTurnStatus) IsValid() bool {
	switch s {
	case AgentTurnReady, AgentTurnRunning, AgentTurnCompleted, AgentTurnFailed:
		return true
	default:
		return false
	}
}

func (s AgentTurnStatus) IsTerminal() bool {
	return s == AgentTurnCompleted || s == AgentTurnFailed
}

var agentTurnTransitions = map[AgentTurnStatus][]AgentTurnStatus{
	AgentTurnReady:     {AgentTurnRunning, AgentTurnFailed},
	AgentTurnRunning:   {AgentTurnCompleted, AgentTurnFailed},
	AgentTurnCompleted: nil,
	AgentTurnFailed:    nil,
}

func (s AgentTurnStatus) CanTransitionTo(to AgentTurnStatus) bool {
	for _, allowed := range agentTurnTransitions[s] {
		if allowed == to {
			return true
		}
	}
	return false
}

// AgentActionStatus tracks the durable work item created together with a Decision.
type AgentActionStatus string

const (
	AgentActionReady           AgentActionStatus = "READY"
	AgentActionRunning         AgentActionStatus = "RUNNING"
	AgentActionWaitingCallback AgentActionStatus = "WAITING_CALLBACK"
	AgentActionSucceeded       AgentActionStatus = "SUCCEEDED"
	AgentActionFailed          AgentActionStatus = "FAILED"
)

func (s AgentActionStatus) IsValid() bool {
	switch s {
	case AgentActionReady, AgentActionRunning, AgentActionWaitingCallback, AgentActionSucceeded, AgentActionFailed:
		return true
	default:
		return false
	}
}

func (s AgentActionStatus) IsTerminal() bool {
	return s == AgentActionSucceeded || s == AgentActionFailed
}

var agentActionTransitions = map[AgentActionStatus][]AgentActionStatus{
	// READY -> FAILED belongs to the Agent timeout transaction alone:
	// an expired Agent deadline ends the current
	// Action even when no executor ever claimed it. Every other failure path reaches an
	// Action it holds the execution right on, so it starts from RUNNING.
	AgentActionReady:           {AgentActionRunning, AgentActionFailed},
	AgentActionRunning:         {AgentActionWaitingCallback, AgentActionSucceeded, AgentActionFailed},
	AgentActionWaitingCallback: {AgentActionSucceeded, AgentActionFailed},
	AgentActionSucceeded:       nil,
	AgentActionFailed:          nil,
}

func (s AgentActionStatus) CanTransitionTo(to AgentActionStatus) bool {
	for _, allowed := range agentActionTransitions[s] {
		if allowed == to {
			return true
		}
	}
	return false
}

// AgentActionType mirrors the kind of the Decision the Action executes.
type AgentActionType string

const (
	AgentActionToolCall AgentActionType = "TOOL_CALL"
	AgentActionFinal    AgentActionType = "FINAL"
)

func (t AgentActionType) IsValid() bool {
	return t == AgentActionToolCall || t == AgentActionFinal
}

// DecisionKind is the structured result the model committed to.
type DecisionKind string

const (
	DecisionToolCall DecisionKind = "TOOL_CALL"
	DecisionFinal    DecisionKind = "FINAL"
)

func (k DecisionKind) IsValid() bool {
	return k == DecisionToolCall || k == DecisionFinal
}

// AgentTermination explains why an Agent Run stopped. Only FINAL_RESPONSE is success.
type AgentTermination string

const (
	TerminationFinalResponse AgentTermination = "FINAL_RESPONSE"
	TerminationMaxTurns      AgentTermination = "MAX_TURNS"
	TerminationTimeout       AgentTermination = "TIMEOUT"
	TerminationModelError    AgentTermination = "MODEL_ERROR"
	TerminationToolError     AgentTermination = "TOOL_ERROR"
	TerminationInvalidAction AgentTermination = "INVALID_ACTION"
)

func (t AgentTermination) IsValid() bool {
	switch t {
	case TerminationFinalResponse, TerminationMaxTurns, TerminationTimeout,
		TerminationModelError, TerminationToolError, TerminationInvalidAction:
		return true
	default:
		return false
	}
}

// IsSuccess reports whether this termination completes the Agent NodeRun successfully.
// Only FINAL_RESPONSE does; every other reason fails it.
func (t AgentTermination) IsSuccess() bool { return t == TerminationFinalResponse }

// CallbackTargetType names which Attempt entity a Callback Binding routes back to.
type CallbackTargetType string

const (
	CallbackTargetNodeAttempt CallbackTargetType = "NODE_ATTEMPT"
	CallbackTargetToolAttempt CallbackTargetType = "TOOL_ATTEMPT"
)

func (t CallbackTargetType) IsValid() bool {
	return t == CallbackTargetNodeAttempt || t == CallbackTargetToolAttempt
}
