package registry

import (
	"context"
	"encoding/json"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// ToolRegistration is one stable Tool Name. One Registry lookup must return the
// Metadata and the Executor together; Studio and the API read Metadata only.
type ToolRegistration struct {
	Metadata domain.ToolMetadata
	Executor ToolExecutor
}

// ToolAction is one committed Agent Action handed to a Tool. The Executor performs a
// single call: it creates no Decision, advances no Turn, changes no Agent State and never
// completes the Agent NodeRun.
type ToolAction struct {
	AgentRunID string
	TurnID     string
	ActionID   string
	ToolName   string
	AttemptNo  int
	// Arguments already passed the Tool InputSchema in the Runtime.
	Arguments json.RawMessage
	// Callback is set only for an ASYNC Tool dispatch.
	Callback *CallbackContext
}

// ToolResult is the complete logical result of one Tool call. The Runtime validates it
// against the registered OutputSchema.
type ToolResult struct {
	Output json.RawMessage
}

// ToolResultKind distinguishes a finished Tool call from a dispatch.
type ToolResultKind string

const (
	ToolResultCompleted  ToolResultKind = "COMPLETED"
	ToolResultDispatched ToolResultKind = "DISPATCHED"
)

// ToolExecutionResult is the outcome of one Tool Execute call.
type ToolExecutionResult struct {
	Kind         ToolResultKind
	Result       *ToolResult
	ExternalTask *ExternalTask
}

// ToolExecutor performs one registered Tool call.
type ToolExecutor interface {
	Execute(ctx context.Context, action ToolAction) (ToolExecutionResult, error)
}

// AsyncToolExecutor converts a Provider callback payload into a Tool result. It resumes
// the same Tool Attempt and Agent Action through the shared idempotent resume use case.
type AsyncToolExecutor interface {
	ToolExecutor
	OnCallback(ctx context.Context, state ToolAsyncState, payload []byte) (ToolResult, error)
}

// PollableAsyncToolExecutor is optional and only for async Tools whose Provider supports a
// status query.
type PollableAsyncToolExecutor interface {
	AsyncToolExecutor
	Poll(ctx context.Context, state ToolAsyncState) (PollResult, error)
}

// ToolAsyncState is what the Runtime restores from committed facts before asking an async
// Tool to interpret a callback or poll an external task. It carries no plaintext callback
// token.
type ToolAsyncState struct {
	AgentRunID   string
	ActionID     string
	ToolName     string
	AttemptNo    int
	ExternalTask ExternalTask
}
