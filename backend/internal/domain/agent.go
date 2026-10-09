package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

// AgentRun is the durable expansion of one Agent NodeRun into a recoverable multi-turn
// execution. The Workflow Scheduler still schedules and aggregates the Agent NodeRun; an
// Agent Run adds no second Runtime. One Agent NodeRun has at most one Agent Run.
//
// Every configuration field is frozen at creation: recovery must replay the Agent Loop
// with the Model parameters, Tool allowlist and Schemas the Run started with, never with
// current Definition values. The bound Definition version is reached through the NodeRun's
// Run, so it is not copied here. Only the pointers, the termination and the error are
// updated after creation.
type AgentRun struct {
	ID        string `json:"id"`
	NodeRunID string `json:"nodeRunId"`

	Instructions  string          `json:"instructions"`
	ModelID       string          `json:"modelId"`
	ModelConfig   json.RawMessage `json:"modelConfig"`
	AllowedTools  []string        `json:"allowedTools"`
	ContextSchema json.RawMessage `json:"contextSchema"`
	StateSchema   json.RawMessage `json:"stateSchema"`
	OutputSchema  json.RawMessage `json:"outputSchema"`
	MaxTurns      int             `json:"maxTurns"`
	// MaxGenerationCalls is the optional generation limit; nil means unlimited. The
	// calls already made are derived from persisted Tool Attempts, never counted here.
	MaxGenerationCalls *int `json:"maxGenerationCalls"`

	// CurrentTurnNo is the highest Turn number created so far; the initialisation
	// transaction sets it to 1. CurrentContextVersion and CurrentStateVersion point at
	// the versions a restarted process resumes from; both start at 0.
	CurrentTurnNo         int `json:"currentTurnNo"`
	CurrentContextVersion int `json:"currentContextVersion"`
	CurrentStateVersion   int `json:"currentStateVersion"`

	StartedAt time.Time `json:"startedAt"`
	// Deadline is one hard bound covering every Turn, Tool call and callback wait.
	Deadline time.Time `json:"deadline"`

	TerminatedAt *time.Time        `json:"terminatedAt"`
	Termination  *AgentTermination `json:"termination"`
	Error        *ExecutionError   `json:"error"`
}

// AgentTurn is one model interaction. It describes the model request and response only;
// whether the Tool ran is the Agent Action's fact, not the Turn's.
type AgentTurn struct {
	ID         string          `json:"id"`
	AgentRunID string          `json:"agentRunId"`
	TurnNo     int             `json:"turnNo"`
	Status     AgentTurnStatus `json:"status"`
	// Request is written when the READY Turn is created and is immutable afterwards.
	// Response, TokenUsage, CompletedAt and Error are written once, from empty.
	Request    json.RawMessage `json:"request"`
	Response   json.RawMessage `json:"response"`
	TokenUsage *TokenUsage     `json:"tokenUsage"`
	// StartedAt is empty while READY and stamped by the transaction that wins the model
	// call, so a persisted Turn shows whether anybody already holds that right.
	StartedAt   *time.Time      `json:"startedAt"`
	CompletedAt *time.Time      `json:"completedAt"`
	Error       *ExecutionError `json:"error"`
}

// AgentDecision is the structured decision the model committed to. It is immutable after
// commit: recovery advances the existing Action instead of asking the model again.
type AgentDecision struct {
	ID       string       `json:"id"`
	TurnID   string       `json:"turnId"`
	Kind     DecisionKind `json:"kind"`
	ToolName *string      `json:"toolName"`
	// Arguments belongs to a TOOL_CALL, Output to a FINAL; see Validate.
	Arguments json.RawMessage `json:"arguments"`
	Output    json.RawMessage `json:"output"`
	// StatePatch is optional for both kinds and carries JSON Merge Patch (RFC 7386)
	// semantics. It is applied by the Action, not by the Decision commit.
	StatePatch json.RawMessage `json:"statePatch"`
	// ResponseSummary is a bounded diagnostic summary of the model response. It is not a
	// recovery input and carries no Provider-private response structure.
	ResponseSummary json.RawMessage `json:"responseSummary"`
	CreatedAt       time.Time       `json:"createdAt"`
}

// Validate reports whether the Decision has the shape its kind requires: a TOOL_CALL
// names a Tool and carries its arguments and no Final output, a FINAL carries an output
// and no Tool call. The same rule is enforced by the agent_decisions CHECK constraint;
// validating here lets the Runtime fail a malformed model result as INVALID_ACTION
// instead of discovering it as a constraint violation mid-transaction.
func (d AgentDecision) Validate() error {
	if !d.Kind.IsValid() {
		return fmt.Errorf("emberling: agent decision %s: unknown kind %q", d.ID, d.Kind)
	}

	switch d.Kind {
	case DecisionToolCall:
		if d.ToolName == nil || *d.ToolName == "" {
			return fmt.Errorf("emberling: agent decision %s: TOOL_CALL requires a Tool Name", d.ID)
		}
		if len(d.Arguments) == 0 {
			return fmt.Errorf("emberling: agent decision %s: TOOL_CALL requires arguments", d.ID)
		}
		if len(d.Output) > 0 {
			return fmt.Errorf("emberling: agent decision %s: TOOL_CALL must not carry a Final output", d.ID)
		}
	case DecisionFinal:
		if len(d.Output) == 0 {
			return fmt.Errorf("emberling: agent decision %s: FINAL requires an output", d.ID)
		}
		if d.ToolName != nil {
			return fmt.Errorf("emberling: agent decision %s: FINAL must not name a Tool", d.ID)
		}
		if len(d.Arguments) > 0 {
			return fmt.Errorf("emberling: agent decision %s: FINAL must not carry Tool arguments", d.ID)
		}
	}
	return nil
}

// AgentAction is the durable work item created together with a Decision. Immediate
// advancement and the Reconciler compete for it through a conditional claim; whoever wins
// executes the committed Decision and never produces a second one.
type AgentAction struct {
	ID         string            `json:"id"`
	TurnID     string            `json:"turnId"`
	DecisionID string            `json:"decisionId"`
	Type       AgentActionType   `json:"type"`
	Status     AgentActionStatus `json:"status"`
	CreatedAt  time.Time         `json:"createdAt"`
	// Only the status, the execution timestamps and the error are updated after
	// creation. WaitingAt stays empty for a synchronous Tool call and for a FINAL Action.
	StartedAt   *time.Time      `json:"startedAt"`
	WaitingAt   *time.Time      `json:"waitingAt"`
	CompletedAt *time.Time      `json:"completedAt"`
	Error       *ExecutionError `json:"error"`
}

// ToolAttempt records one real Tool call made by a TOOL_CALL Action. Synchronous and
// asynchronous Tools share the entity; an async call additionally reaches DISPATCHED and
// resumes through its Callback Binding. The MVP does not retry an Agent Tool, so an
// Action creates at most one Attempt; AttemptNo exists so Tool calls carry the same audit
// and callback target model as Node Attempts.
type ToolAttempt struct {
	ID        string            `json:"id"`
	ActionID  string            `json:"actionId"`
	AttemptNo int               `json:"attemptNo"`
	ToolName  string            `json:"toolName"`
	Status    ToolAttemptStatus `json:"status"`
	// Input is immutable after creation; Result is written once, on success.
	Input  json.RawMessage `json:"input"`
	Result json.RawMessage `json:"result"`
	// CallbackTokenHash is the only persisted form of an async dispatch credential, saved
	// before the external call. The plaintext token is never stored.
	CallbackTokenHash *string         `json:"-"`
	StartedAt         time.Time       `json:"startedAt"`
	DispatchedAt      *time.Time      `json:"dispatchedAt"`
	CompletedAt       *time.Time      `json:"completedAt"`
	Error             *ExecutionError `json:"error"`
}

// AgentContextVersion is one immutable entry of the message chain the next model call
// sees. Version 0 holds the Agent Node's validated upstream input; later versions append
// to the previous one without reordering or rewriting history. The Agent Run's pointer
// decides which version a restarted process resumes from.
type AgentContextVersion struct {
	ID         string `json:"id"`
	AgentRunID string `json:"agentRunId"`
	Version    int    `json:"version"`
	// SourceTurnID is empty for Version 0 and names the Turn that produced the version
	// otherwise.
	SourceTurnID *string         `json:"sourceTurnId"`
	Messages     json.RawMessage `json:"messages"`
	CreatedAt    time.Time       `json:"createdAt"`
}

// AgentStateVersion is one immutable entry of the structured state chain. Version 0 is
// fixed to the empty object; a later version exists only when a Decision's state patch
// actually changed the State and the result passed the frozen State Schema.
type AgentStateVersion struct {
	ID           string          `json:"id"`
	AgentRunID   string          `json:"agentRunId"`
	Version      int             `json:"version"`
	SourceTurnID *string         `json:"sourceTurnId"`
	Value        json.RawMessage `json:"value"`
	CreatedAt    time.Time       `json:"createdAt"`
}
