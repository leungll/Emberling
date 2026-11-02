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
	// RunID is the Run the Agent Run belongs to. A Tool uses it only to name the
	// Run-scoped address of what it produced, never to read or change Run state.
	RunID      string
	AgentRunID string
	TurnID     string
	ActionID   string
	ToolName   string
	AttemptNo  int
	// Arguments already passed the Tool InputSchema in the Runtime.
	Arguments json.RawMessage
	// ImageModelID is the Agent Run's frozen image Model ID, resolved in the claim
	// transaction, for a Tool whose Metadata declares UsesImageModel; empty otherwise. The
	// Tool uses it verbatim and never selects a model of its own.
	ImageModelID string
	// ReviewModelID is the Agent Run's frozen review Model ID, resolved in the claim
	// transaction, for a Tool whose Metadata declares UsesReviewModel; empty when the
	// Tool does not declare it or the Agent Run froze no review model. The Tool uses it
	// verbatim: empty selects its deterministic mock policy, never another model.
	ReviewModelID string
	// Workspace is the Agent Run's frozen workspace view, resolved in the claim
	// transaction, for a Tool whose Metadata declares UsesWorkspace; nil otherwise. The
	// Tool reads it verbatim and never resolves a repository or commit of its own.
	Workspace *WorkspaceView
	// Callback is set only for an ASYNC Tool dispatch.
	Callback *CallbackContext
}

// WorkspaceView is what a workspace Tool sees of the Agent Run's frozen workspace: the
// registered repository, the base commit the Run is bound to, the cumulative patch so far
// with its digest, and the relative path prefixes the Agent may change. It carries no
// filesystem location: the Tool resolves the repository through its own registered
// configuration.
type WorkspaceView struct {
	RepositoryID string
	BaseCommit   string
	// Patch is the current cumulative unified diff on top of BaseCommit, empty when the
	// Agent has changed nothing yet. PatchDigest identifies that patch; a Tool that
	// requires the latest patch compares digests, never patch text.
	Patch       string
	PatchDigest string
	// AllowedPaths are normalised relative path prefixes inside the repository.
	AllowedPaths []string
}

// ToolResult is the complete logical result of one Tool call. The Runtime validates it
// against the registered OutputSchema.
type ToolResult struct {
	Output json.RawMessage
	// Artifacts declares the Execution Artifacts whose content the Tool already wrote to
	// content storage. The Runtime commits their metadata in the same transaction as the
	// result, so a result that never commits leaves only unreferenced content behind. A
	// Tool that produces no binary leaves it nil.
	Artifacts []domain.ArtifactRef
	// Cost is the charge the Provider reported for this call, or nil when it reported
	// none. It is recorded on the Tool Attempt in the transaction that commits the result
	// and never read back into an execution decision.
	Cost *domain.Cost
	// WorkspacePatch is the new cumulative workspace patch a result carries, for a Tool
	// whose Metadata declares the WORKSPACE_PATCH record; nil otherwise. The Runtime
	// persists it in the result transaction. A result carrying one the registration does
	// not declare is invalid.
	WorkspacePatch *WorkspacePatch
	// ChangeRequest is the change request a result carries, for a Tool whose Metadata
	// declares the CHANGE_REQUEST record; nil otherwise, under the same rule.
	ChangeRequest *ChangeRequest
}

// WorkspacePatch is one new cumulative patch of the Agent Run's workspace: the whole
// unified diff on top of the frozen base commit, its digest and the repository paths it
// touches. The Tool computes it; the Runtime decides whether it may be recorded.
type WorkspacePatch struct {
	Patch        string
	PatchDigest  string
	TouchedPaths []string
}

// ChangeRequest is the record a change-request Tool assembles from committed facts: the
// patch it describes by digest, the verification fact it rests on, the Artifact holding its
// content with that content's digest, and the decision basis it cites. Every reference is
// to something already committed; the Runtime checks them against the Run's facts before
// recording the request.
type ChangeRequest struct {
	PatchDigest        string
	VerificationFactID string
	ContentArtifact    domain.ArtifactRef
	ContentDigest      string
	DecisionBasis      json.RawMessage
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
// status query. Registering one requires a Poll policy in the Metadata, and the Runtime
// polls only Tools that declare one. Poll is called outside every transaction and lock
// with state restored from committed facts; it queries once and never re-dispatches. A
// SUCCEEDED result carries ToolResult, a FAILED one carries Error, RUNNING and UNKNOWN
// change nothing, and UNKNOWN_TASK says the Provider does not know the task.
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
