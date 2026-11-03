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
	// RequiredFacts are the committed facts that satisfied the Tool's declared fact
	// requirements, one per requirement in declaration order, resolved in the claim
	// transaction, followed by any fact the call cited by identifier that the claim
	// verified, such as the review fact a change request attaches. A Tool that declares no
	// requirement and cites nothing receives none. The Tool reads them verbatim, to cite
	// the fact its result rests on; it never looks a fact up itself.
	RequiredFacts []domain.ExecutionFact
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
	// PatchSeq is the sequence number of that patch, 0 while the workspace is unchanged. A
	// Tool reports it with the file content it read, so the material a model saw can be
	// named by path and workspace version.
	PatchSeq int
	// AllowedPaths are normalised relative path prefixes inside the repository.
	AllowedPaths []string
	// DeployTarget is the service and environment the Agent node's workspace releases
	// to, nil when the node configured none. A Tool that describes a change names it;
	// no Tool decides where a release goes from it.
	DeployTarget *domain.WorkspaceDeployTarget
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
// content with that content's digest, the decision basis it cites and, when the call cited
// one, the review fact of the same patch. Every reference is to something already
// committed; the Runtime checks them against the Run's facts before recording the request.
type ChangeRequest struct {
	PatchDigest        string
	VerificationFactID string
	ContentArtifact    domain.ArtifactRef
	ContentDigest      string
	DecisionBasis      json.RawMessage
	// ReviewFactID is the review fact the request attaches, "" when the call cited none. It
	// is checked, never stored as a column: the request's content carries the reference,
	// and whether a patch was reviewed is read from the facts themselves.
	ReviewFactID string
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

// ReconcilableToolExecutor is for the release Tool, whose external operation the Runtime
// reconciles after the fact instead of resending it. Void asks production to void the
// operation named by state and reports the operation's status afterwards in the same
// vocabulary as Poll: FAILED with the VOIDED code when production confirmed the void,
// SUCCEEDED or RUNNING when the operation had already started or finished, UNKNOWN when
// production gave no interpretable answer. CheckTarget reads what the deploy target runs
// now. Each method sends exactly one request and never retries; the Runtime decides what a
// lost answer means.
type ReconcilableToolExecutor interface {
	PollableAsyncToolExecutor
	Void(ctx context.Context, state ToolAsyncState) (PollResult, error)
	CheckTarget(ctx context.Context, state ToolAsyncState) (TargetState, error)
}

// TargetState is what a deploy target runs, as production reports it. Known is false when
// production has no deployment for the target at all.
type TargetState struct {
	Known           bool
	Version         string
	PatchDigest     string
	Healthy         bool
	LastOperationID string
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
	// ReleaseTarget is the frozen deploy target of a release Attempt, restored from the
	// Agent Run's workspace for CheckTarget; nil for every other Tool.
	ReleaseTarget *domain.WorkspaceDeployTarget
}
