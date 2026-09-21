package domain

import (
	"encoding/json"
	"time"
)

// Run is one complete Execution and the concurrency aggregate root. Its status is always
// derived from its NodeRuns; it is never set directly by a single node.
type Run struct {
	ID                string          `json:"id"`
	WorkflowID        string          `json:"workflowId"`
	DefinitionVersion int             `json:"definitionVersion"`
	Status            RunStatus       `json:"status"`
	Input             json.RawMessage `json:"input"`
	Output            json.RawMessage `json:"output"`
	Error             *ExecutionError `json:"error"`
	// LastSeq is the highest Event seq already reflected by this snapshot. It is written
	// in the same transaction as the state change and the Event it accounts for.
	LastSeq     int64      `json:"lastSeq"`
	StartedAt   time.Time  `json:"startedAt"`
	CompletedAt *time.Time `json:"completedAt"`
	UpdatedAt   time.Time  `json:"-"`
}

// NodeRun is one Definition node executing inside one Run. It owns the node's overall
// result; individual calls are recorded by NodeAttempt.
type NodeRun struct {
	ID       string          `json:"id"`
	RunID    string          `json:"runId"`
	NodeID   string          `json:"nodeId"`
	NodeType string          `json:"nodeType"`
	Status   NodeRunStatus   `json:"status"`
	Input    json.RawMessage `json:"input"`
	Output   json.RawMessage `json:"output"`
	Error    *ExecutionError `json:"error"`

	AttemptCount int `json:"attemptCount"`
	// NextAttemptAt is set only once a retry is known to be allowed, in the same
	// transaction as the failed Attempt and NODE_RETRYING. The Reconciler uses it to
	// recover retries whose backoff was interrupted.
	NextAttemptAt *time.Time `json:"nextAttemptAt"`

	ReadyAt     time.Time  `json:"readyAt"`
	StartedAt   *time.Time `json:"startedAt"`
	WaitingAt   *time.Time `json:"waitingAt"`
	CompletedAt *time.Time `json:"completedAt"`
	// LatencyMs is wall-clock CompletedAt minus StartedAt, including async waiting and
	// retry backoff.
	LatencyMs  *int64      `json:"latencyMs"`
	TokenUsage *TokenUsage `json:"tokenUsage"`
	UpdatedAt  time.Time   `json:"-"`
}

// NodeAttempt records one real execution or external dispatch of an EXECUTOR node.
// A MANAGED_AGENT NodeRun creates no NodeAttempt.
type NodeAttempt struct {
	ID        string            `json:"id"`
	NodeRunID string            `json:"nodeRunId"`
	AttemptNo int               `json:"attemptNo"`
	Status    NodeAttemptStatus `json:"status"`
	// Input is immutable after creation; Result is written once, on success.
	Input  json.RawMessage `json:"input"`
	Result json.RawMessage `json:"result"`
	// CallbackTokenHash is the only persisted form of the dispatch credential. The
	// plaintext token is sent to the Provider and never stored.
	CallbackTokenHash *string   `json:"-"`
	StartedAt         time.Time `json:"startedAt"`
	// DeadlineAt is absolute: entering WAITING_CALLBACK does not restart the clock.
	DeadlineAt   *time.Time      `json:"deadlineAt"`
	DispatchedAt *time.Time      `json:"dispatchedAt"`
	CompletedAt  *time.Time      `json:"completedAt"`
	Error        *ExecutionError `json:"error"`
}

// ExecutionError is the persisted failure summary. It carries no stack traces, database
// details or credentials.
type ExecutionError struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details,omitempty"`
}

// TokenUsage is reported by a model Provider. TotalTokens must equal the sum of the
// other two fields.
type TokenUsage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
	TotalTokens  int `json:"totalTokens"`
}

// Workflow is the container that owns Definition versions.
type Workflow struct {
	WorkflowID    string    `json:"workflowId"`
	Name          string    `json:"name"`
	LatestVersion int       `json:"latestVersion"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}
