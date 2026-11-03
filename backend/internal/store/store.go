// Package store defines the persistence ports the service layer depends on. It holds
// interfaces and the Run aggregate lock value only; the SQL lives in store/postgres.
package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// UnitOfWork owns transaction boundaries. Repositories never open their own transaction:
// they receive the active one through Tx, so a state change and the Event that proves it
// commit together.
type UnitOfWork interface {
	// WithinTx runs fn inside one database transaction. Returning a non-nil error rolls
	// the whole transaction back, including every Event appended inside it.
	WithinTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error

	// WithinReadTx runs fn inside one read-only transaction that observes a single
	// consistent snapshot for every statement it issues (PostgreSQL REPEATABLE READ),
	// not a fresh snapshot per statement. Use it wherever two or more reads must agree
	// with each other even if a concurrent transaction commits between them — the
	// Snapshot-to-SSE handoff contract (lastSeq and the Snapshot are taken in the same
	// consistent read) needs exactly this, because Run.LastSeq and
	// NodeRuns are read as two separate statements. A caller must not write inside fn:
	// PostgreSQL rejects it.
	WithinReadTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

// Tx exposes the repositories bound to one active transaction.
type Tx interface {
	Definitions() DefinitionRepository
	Assets() AssetRepository
	Artifacts() ArtifactRepository
	Runs() RunRepository
	RunList() RunListRepository
	NodeRuns() NodeRunRepository
	NodeAttempts() NodeAttemptRepository
	CallbackBindings() CallbackBindingRepository
	PendingCallbacks() PendingCallbackRepository
	Events() EventRepository
	AgentRuns() AgentRunRepository
	AgentTurns() AgentTurnRepository
	AgentTurnManifests() AgentTurnManifestRepository
	AgentDecisions() AgentDecisionRepository
	AgentActions() AgentActionRepository
	ToolAttempts() ToolAttemptRepository
	AgentContextVersions() AgentContextVersionRepository
	AgentStateVersions() AgentStateVersionRepository
	ExecutionFacts() ExecutionFactRepository
	ProviderSlots() ProviderSlotRepository
	AgentWorkspaces() AgentWorkspaceRepository
	WorkspacePatches() WorkspacePatchRepository
	Approvals() ApprovalRepository
	ChangeRequests() ChangeRequestRepository
	ReleaseOccupations() ReleaseOccupationRepository
	HumanWaits() HumanWaitRepository
}

// DefinitionRepository persists immutable Workflow versions. It has no update method:
// a stored version can never be rewritten.
type DefinitionRepository interface {
	// Save creates the Workflow if absent and inserts the given version. A brand new
	// Workflow accepts only version 1. An existing Workflow accepts only
	// latest_version+1; any other value, including a re-save of an already stored
	// version, yields domain.ErrVersionConflict rather than reordering or rewriting it.
	Save(ctx context.Context, def domain.Definition) error
	GetVersion(ctx context.Context, workflowID string, version int) (domain.Definition, error)
	ListVersions(ctx context.Context, workflowID string) ([]domain.Definition, error)
	GetWorkflow(ctx context.Context, workflowID string) (domain.Workflow, error)

	// ListWorkflows returns every Workflow with its latest version's Description, most
	// recently updated first. It backs the Definitions-list endpoint
	// (name, description, latestVersion, updatedAt, lastRun).
	ListWorkflows(ctx context.Context) ([]WorkflowSummary, error)
}

// WorkflowSummary is one Definitions-list row's store-level content: a Workflow plus the
// Description of its latest version. Description lives on workflow_definitions (it is
// per-version, not per-workflow), so it is not a domain.Workflow field.
type WorkflowSummary struct {
	Workflow    domain.Workflow
	Description string
}

// AssetRecord is one assets row: the Asset Metadata plus the internal storage key that
// locates its binary. The key is a system Secret, which is why it is a Store
// field rather than a domain.Asset field: it travels between this repository and the
// Asset storage layer only, and never reaches an Event, Trace or API response.
type AssetRecord struct {
	Asset      domain.Asset
	StorageKey string
}

// AssetRepository persists Asset Metadata. It has no update or delete method: an
// asset_id points at immutable content, so replacing content means creating a new Asset.
type AssetRepository interface {
	// Create inserts one Asset. The table's PRIMARY KEY decides duplicates, so a second
	// insert of the same asset_id yields domain.ErrConflict and leaves the committed row
	// untouched.
	Create(ctx context.Context, record AssetRecord) error

	// Get returns the Metadata and storage key of one Asset, or domain.ErrNotFound.
	Get(ctx context.Context, assetID string) (AssetRecord, error)
}

// ArtifactRecord is one execution_artifacts row: the Execution Artifact Metadata plus the
// internal storage key of its content-addressed object. As with AssetRecord, the key is a
// system Secret that travels only between this repository, the service layer and the
// content store.
type ArtifactRecord struct {
	Artifact   domain.Artifact
	StorageKey string
}

// ArtifactRepository persists Execution Artifact Metadata. It has no update or delete
// method: an artifact_id is derived from the content digest and points at immutable
// content.
type ArtifactRepository interface {
	// CreateIfAbsent inserts one Execution Artifact unless a row with the same
	// artifact_id already exists. Identical content produced twice — by a duplicate
	// completion or by two Runs — therefore leaves exactly one row. An existing row whose
	// sha256, size_bytes or media_type differ yields domain.ErrConflict and is left
	// untouched.
	CreateIfAbsent(ctx context.Context, record ArtifactRecord) error

	// Get returns the Metadata and storage key of one Execution Artifact, or
	// domain.ErrNotFound.
	Get(ctx context.Context, artifactID string) (ArtifactRecord, error)

	// LinkToToolAttempt records that a Tool Attempt of a Run declared an Execution
	// Artifact with its result. It belongs to that result transaction: the artifact's row
	// must already exist in it, and a second link of the same Attempt and artifact yields
	// domain.ErrConflict rather than being ignored.
	LinkToToolAttempt(ctx context.Context, link ToolAttemptArtifact) error

	// GetForRun returns the Metadata and storage key of an Execution Artifact that a
	// SUCCEEDED Tool Attempt of runID declared, or domain.ErrNotFound when the Run does
	// not exist, no such link exists, or the artifact has no row. The three cases are
	// deliberately indistinguishable, so a Run reveals nothing about another Run's
	// artifacts.
	GetForRun(ctx context.Context, runID, artifactID string) (ArtifactRecord, error)
}

// ToolAttemptArtifact is one tool_attempt_artifacts row: which Tool Attempt of which Run
// declared which Execution Artifact.
type ToolAttemptArtifact struct {
	ToolAttemptID string
	ArtifactID    string
	RunID         string
	CreatedAt     time.Time
}

// RunRepository persists Runs and serialises concurrent advancement of one Run.
type RunRepository interface {
	Create(ctx context.Context, run domain.Run) error
	Get(ctx context.Context, runID string) (domain.Run, error)

	// GetByIdempotencyKey returns the Run of workflowID created with the given submission
	// key, or domain.ErrNotFound when no Run of that Workflow holds it. Create reports a
	// key already held by another Run of the same Workflow as
	// domain.ErrRunIdempotencyKeyInUse.
	GetByIdempotencyKey(ctx context.Context, workflowID, key string) (domain.Run, error)

	// LockForUpdate takes the Run aggregate lock with SELECT ... FOR UPDATE. Every
	// transaction that touches execution facts of this Run must hold it before updating
	// a NodeRun, an Attempt or Agent facts, and before allocating Event seq.
	//
	// The caller must never invoke external code while holding this lock.
	LockForUpdate(ctx context.Context, runID string) (*RunLock, error)

	// UpdateAggregate writes the recomputed Run status and the seq watermark reached in
	// this transaction back to the Run row. It must be called before the transaction
	// commits whenever events were appended. completed_at is set the first time status
	// becomes terminal (COMPLETED or FAILED) and is never moved by a later call.
	UpdateAggregate(ctx context.Context, lock *RunLock, status domain.RunStatus, now time.Time) error

	// SetOutput writes the Run's output exactly once, sourced from the Output Node's
	// NodeRun output. A second call finds output already set and returns
	// domain.ErrConflict without overwriting it.
	SetOutput(ctx context.Context, lock *RunLock, output json.RawMessage) error

	// SetError records the failure summary for a Run entering FAILED.
	SetError(ctx context.Context, lock *RunLock, execErr domain.ExecutionError) error

	// LatestByWorkflow returns the most recently started Run of a Workflow, or nil if
	// the Workflow has never been run. It backs the Definitions-list endpoint.
	LatestByWorkflow(ctx context.Context, workflowID string) (*domain.Run, error)
}

// NodeRunRepository persists NodeRuns and implements the conditional claims that decide
// the single winner when immediate advancement, callback, timeout and reconciliation race.
type NodeRunRepository interface {
	Create(ctx context.Context, nr domain.NodeRun) error
	Get(ctx context.Context, nodeRunID string) (domain.NodeRun, error)
	ListByRun(ctx context.Context, runID string) ([]domain.NodeRun, error)

	// ClaimReady conditionally moves a NodeRun from READY to RUNNING. It reports true
	// only for the caller whose UPDATE affected one row; everybody else gets false and
	// must stop. A false result is not an error.
	ClaimReady(ctx context.Context, nodeRunID string, now time.Time) (bool, error)

	// Transition conditionally moves a NodeRun between two states. It returns
	// domain.ErrStaleClaim when the row no longer matches `from`, and an
	// *domain.InvalidStateTransitionError when the state machine forbids the edge.
	Transition(ctx context.Context, nodeRunID string, from, to domain.NodeRunStatus, now time.Time) error

	// ListReadyOrRetryable returns persisted work the Reconciler can rediscover after a
	// crash: READY NodeRuns, and RUNNING NodeRuns whose retry backoff has elapsed.
	// The limit bounds the scan batch.
	ListReadyOrRetryable(ctx context.Context, before time.Time, limit int) ([]domain.NodeRun, error)

	// ClaimRetry conditionally clears next_attempt_at on a RUNNING NodeRun whose backoff
	// has elapsed, giving the caller exclusive ownership of dispatching the next Attempt.
	// Same affected-rows-decide-the-winner semantics as ClaimReady: true for the single
	// winner, false (not an error) for a NodeRun that is not yet due or was already
	// claimed by another advancement path.
	ClaimRetry(ctx context.Context, nodeRunID string, now time.Time) (bool, error)

	// ScheduleRetry records the next backoff deadline for a NodeRun that stays RUNNING
	// after a retryable Attempt failure (retry allowed: the Attempt is FAILED, the NodeRun
	// stays RUNNING with next_attempt_at set, and NODE_RETRYING is written). It requires
	// the NodeRun to still be RUNNING and
	// returns domain.ErrStaleClaim otherwise.
	ScheduleRetry(ctx context.Context, nodeRunID string, nextAttemptAt, now time.Time) error

	// IncrementAttemptCount bumps attempt_count and returns the new value, so the caller
	// can stamp the Attempt it is about to create with the right attempt_no.
	IncrementAttemptCount(ctx context.Context, nodeRunID string) (int, error)

	// SetInput overwrites the NodeRun's recorded input, used when scheduling resolves the
	// node's actual input ahead of dispatch.
	SetInput(ctx context.Context, nodeRunID string, input json.RawMessage) error

	// MarkSucceeded conditionally moves a NodeRun to SUCCEEDED and writes its outcome
	// (output, token usage, latency) and completed_at in the same statement. It returns
	// domain.ErrStaleClaim when the row no longer matches `from` and
	// *domain.InvalidStateTransitionError when the state machine forbids the edge; a
	// stale claim leaves the row untouched.
	MarkSucceeded(ctx context.Context, nodeRunID string, from domain.NodeRunStatus, now time.Time, outcome NodeRunOutcome) error

	// MarkFailed conditionally moves a NodeRun to FAILED and writes its error, token
	// usage and completed_at, with the same domain.ErrStaleClaim /
	// *domain.InvalidStateTransitionError semantics as MarkSucceeded. usage is nil for
	// every failure except an Agent NodeRun's, which carries the sum of its Turns' usage.
	MarkFailed(ctx context.Context, nodeRunID string, from domain.NodeRunStatus, now time.Time, execErr domain.ExecutionError, usage *domain.TokenUsage) error

	// MarkWaiting conditionally moves a RUNNING NodeRun to WAITING_CALLBACK and stamps
	// waiting_at. It returns domain.ErrStaleClaim when the row is not RUNNING.
	MarkWaiting(ctx context.Context, nodeRunID string, now time.Time) error
	// MarkCapacityDeferred records that a READY NodeRun was refused a slot of providerID.
	// It writes only while capacity_deferred_at is still NULL and the NodeRun is READY,
	// and reports whether this call was the first refusal: only the first one writes the
	// NODE_WAITING_CAPACITY Event in the caller's transaction.
	MarkCapacityDeferred(ctx context.Context, nodeRunID, providerID string, now time.Time) (bool, error)
}

// NodeRunOutcome bundles the fields MarkSucceeded writes together with the terminal
// status. LatencyMs is optional: when nil, the Store derives it from started_at and the
// completion time passed to MarkSucceeded.
type NodeRunOutcome struct {
	Output     json.RawMessage
	TokenUsage *domain.TokenUsage
	LatencyMs  *int64
}

// NodeAttemptRepository preserves every real execution. A retry creates a new Attempt;
// old Attempts are never overwritten.
type NodeAttemptRepository interface {
	Create(ctx context.Context, attempt domain.NodeAttempt) error
	Get(ctx context.Context, attemptID string) (domain.NodeAttempt, error)
	ListByNodeRun(ctx context.Context, nodeRunID string) ([]domain.NodeAttempt, error)

	// Transition conditionally moves an Attempt between two states, with the same
	// zero-rows semantics as NodeRunRepository.Transition.
	Transition(ctx context.Context, attemptID string, from, to domain.NodeAttemptStatus, now time.Time) error

	// MarkDispatched conditionally moves an Attempt from STARTED to DISPATCHED and
	// stamps dispatched_at. In the same UPDATE it writes firstPollAt as the first
	// next_poll_at; firstPollAt is nil when the registration declares no poll policy.
	// It returns domain.ErrStaleClaim when the row is not STARTED.
	MarkDispatched(ctx context.Context, attemptID string, now time.Time, firstPollAt *time.Time) error

	// ClaimPoll conditionally claims one Provider poll of a DISPATCHED Attempt whose
	// next_poll_at is due and whose poll_count is below maxPolls. It increments
	// poll_count and schedules next_poll_at at now+interval, or clears it when this
	// claim reaches maxPolls. The bounds come from the caller, which reads them from
	// the current registration. A false result is not an error: the poll is not due,
	// the bound was reached, the Attempt left DISPATCHED, or another claimer won.
	// Claiming a poll is a scheduling fact; the caller writes no Event for it.
	ClaimPoll(ctx context.Context, attemptID string, now time.Time, interval time.Duration, maxPolls int) (bool, error)

	// ClearPoll unschedules polling of a DISPATCHED Attempt by setting next_poll_at to
	// NULL, for a registration that no longer declares a usable poll policy or a Callback
	// Binding that does not route a poll to the Attempt. A false
	// result means the Attempt is no longer DISPATCHED and nothing changed.
	ClearPoll(ctx context.Context, attemptID string) (bool, error)

	// ListDuePolls returns DISPATCHED Attempts whose next_poll_at is at or before now,
	// oldest first, bounded by limit. It only discovers persisted work; claiming each
	// poll is the caller's conditional update.
	ListDuePolls(ctx context.Context, now time.Time, limit int) ([]DuePoll, error)

	// MarkSucceeded conditionally moves an Attempt to SUCCEEDED and writes its result,
	// completed_at and the Cost the Provider reported for the call (nil when it reported
	// none). Result and Cost are written once: a stale or illegal caller never overwrites
	// a committed result. Same domain.ErrStaleClaim /
	// *domain.InvalidStateTransitionError semantics as Transition.
	MarkSucceeded(ctx context.Context, attemptID string, from domain.NodeAttemptStatus, now time.Time, result json.RawMessage, cost *domain.Cost) error

	// MarkFailed conditionally moves an Attempt to FAILED and writes its error,
	// completed_at and whether the failed call's external outcome was uncertain, with the
	// same semantics as MarkSucceeded. A losing call writes none of them.
	MarkFailed(ctx context.Context, attemptID string, from domain.NodeAttemptStatus, now time.Time, execErr domain.ExecutionError, uncertain bool) error

	// ListExpired returns non-terminal Attempts (STARTED or DISPATCHED) whose deadline_at
	// has passed. It backs Reconciler timeout rediscovery (the Reconciler must be able to
	// scan expired STARTED or DISPATCHED Attempts). The limit bounds the scan batch.
	ListExpired(ctx context.Context, before time.Time, limit int) ([]domain.NodeAttempt, error)

	// Latest returns the highest attempt_no Attempt of a NodeRun, or nil if none exists.
	Latest(ctx context.Context, nodeRunID string) (*domain.NodeAttempt, error)
}

// DuePoll identifies one persisted Node Attempt whose Provider poll is due.
type DuePoll struct {
	AttemptID string
	NodeRunID string
	RunID     string
	PollCount int
}

// DueToolPoll identifies one persisted Tool Attempt whose Provider poll is due. ActionID is
// what the discovering scan found and is used for logging only; the use case re-reads
// every routing fact itself.
type DueToolPoll struct {
	AttemptID string
	ActionID  string
}

// CallbackBindingRepository persists the only authoritative route from an external task
// identity back to the Attempt that dispatched it. A binding is immutable:
// there is no update method, because re-pointing an external identity at another Attempt
// would make callback routing ambiguous.
type CallbackBindingRepository interface {
	// Create inserts a binding. It commits in the same transaction as the Attempt's
	// DISPATCHED status and the NodeRun's WAITING_CALLBACK status.
	// A second binding for an already bound external_task_id yields domain.ErrConflict.
	Create(ctx context.Context, binding domain.CallbackBinding) error

	// GetByExternalTaskID resolves the callback target. It returns domain.ErrNotFound
	// when no binding exists yet, which is the early-callback case the caller answers by
	// recording a Pending Callback rather than by guessing a target.
	GetByExternalTaskID(ctx context.Context, externalTaskID string) (domain.CallbackBinding, error)

	// ListByTargets returns the bindings of the given Attempts, for the Node Detail
	// projection. targetIDs is a bounded set supplied by the caller (the Attempts of one
	// NodeRun or Run); an empty set returns no rows.
	ListByTargets(ctx context.Context, targetType domain.CallbackTargetType, targetIDs []string) ([]domain.CallbackBinding, error)
}

// PendingCallbackRepository holds authenticated callbacks that arrived before their
// binding committed. It stores facts only: matching a Pending Callback to an
// Attempt, including comparing its CallbackTokenHash with the target Attempt's, is the
// service layer's decision.
type PendingCallbackRepository interface {
	// Record persists a first arrival, or registers a duplicate delivery of one already
	// recorded. A duplicate only bumps duplicate_count and received_at: the first valid
	// payload, its hash and its token hash are never overwritten. It reports true when
	// the record already existed.
	Record(ctx context.Context, pending domain.PendingCallback) (duplicate bool, err error)

	// GetByExternalTaskID returns the record, or domain.ErrNotFound when the Provider
	// has not (yet) delivered an early callback for this external task.
	GetByExternalTaskID(ctx context.Context, externalTaskID string) (domain.PendingCallback, error)

	// ConsumeOnce conditionally claims an unconsumed, unexpired record by stamping
	// consumed_at. It reports true only for the caller whose UPDATE affected one row, so
	// the callback handler and the post-commit check after WAITING_CALLBACK cannot both
	// resume the same Attempt. A false result is not an error: another path won, the
	// record expired, or it does not exist.
	ConsumeOnce(ctx context.Context, externalTaskID string, now time.Time) (domain.PendingCallback, bool, error)

	// ListConsumableForWaiting returns unconsumed, unexpired records whose binding now
	// resolves to a DISPATCHED Node Attempt of a WAITING_CALLBACK NodeRun, or to a
	// DISPATCHED Tool Attempt of a WAITING_CALLBACK Agent Action whose Agent NodeRun is
	// WAITING_CALLBACK. It is how the
	// Reconciler rediscovers an early callback whose consumption never ran, because the
	// in-process post-commit check is a latency optimisation and not the recovery source.
	// The limit bounds the scan batch.
	ListConsumableForWaiting(ctx context.Context, now time.Time, limit int) ([]PendingForWaiting, error)

	// DeleteExpired removes records past their expiry so an unmatched early callback
	// cannot occupy the database indefinitely. The limit bounds the batch; it
	// returns how many rows were removed.
	DeleteExpired(ctx context.Context, now time.Time, limit int) (int64, error)
}

// PendingForWaiting is one rediscovered early callback together with the route it
// resolves to. The identifiers are carried alongside the binding because the caller needs
// the Run to lock and the NodeRun to advance, and reading them here avoids a second
// round-trip per row. For a TOOL_ATTEMPT Binding, NodeRunID is the Agent NodeRun that owns
// the waiting Action and AttemptID is the Tool Attempt.
type PendingForWaiting struct {
	Pending   domain.PendingCallback
	Binding   domain.CallbackBinding
	RunID     string
	NodeRunID string
	AttemptID string
}

// AgentRunRepository persists the frozen configuration and the recovery pointers of one
// Agent NodeRun's Agent Run. The configuration columns have no update method:
// recovery must not re-read current Model parameters, Tool allowlist or Schemas.
type AgentRunRepository interface {
	// Create inserts the Agent Run. UNIQUE (node_run_id) is what makes a duplicated
	// initialisation transaction lose: an Agent NodeRun has at most one Agent Run.
	Create(ctx context.Context, run domain.AgentRun) error
	Get(ctx context.Context, agentRunID string) (domain.AgentRun, error)

	// GetByNodeRunID resolves the Agent Run of an Agent NodeRun, which is how every
	// advancement path enters the Agent Loop from the scheduled NodeRun.
	GetByNodeRunID(ctx context.Context, nodeRunID string) (domain.AgentRun, error)

	// AdvancePointers conditionally moves the recovery position (current Turn number and
	// current Context/State Version). The update matches `from`, so a caller working
	// from pointers another transaction already superseded affects zero rows and reports
	// false instead of overwriting the committed position. A false result is not an error.
	AdvancePointers(ctx context.Context, agentRunID string, from, to AgentRunPointers) (bool, error)

	// Terminate conditionally writes the termination reason, terminated_at and the
	// optional error on an Agent Run that has not terminated yet. Tool failure, timeout
	// and Final completion can all reach the same Agent Run; only the caller whose
	// UPDATE affected one row recorded why it stopped.
	Terminate(ctx context.Context, agentRunID string, termination domain.AgentTermination, now time.Time, execErr *domain.ExecutionError) (bool, error)

	// ListExpired returns Agent Runs that have not terminated, are not paused and whose
	// deadline is at or before `before`, so the Reconciler can rediscover the Agent
	// timeout work an in-process timer never performed (an Agent deadline that has passed
	// enters the timeout use case). Terminated Agent Runs are excluded: their outcome is
	// already committed. Paused ones are excluded because their deadline is not running.
	// The limit bounds the scan batch.
	ListExpired(ctx context.Context, before time.Time, limit int) ([]domain.AgentRun, error)

	// Pause conditionally stops the Agent Run's clock: it writes paused_at and the reason
	// on a row that is not paused. A row already paused affects nothing and returns
	// domain.ErrStaleClaim, because two overlapping pauses are a programming error of the
	// caller, never a race the database should settle.
	Pause(ctx context.Context, agentRunID string, reason domain.AgentPauseReason, now time.Time) error

	// Resume conditionally restarts the clock of a paused Agent Run: the deadline moves
	// later by the time spent paused (now - paused_at), both pause columns are cleared,
	// and the moved deadline is returned. A paused duration that is not positive leaves
	// the deadline unchanged but still clears the pause. A row that is not paused returns
	// domain.ErrStaleClaim.
	Resume(ctx context.Context, agentRunID string, now time.Time) (time.Time, error)

	// SetPauseReason conditionally changes why a paused Agent Run is paused without
	// touching paused_at, so the paused duration keeps accumulating from the original
	// pause. A row that is not paused returns domain.ErrStaleClaim.
	SetPauseReason(ctx context.Context, agentRunID string, reason domain.AgentPauseReason) error
}

// AgentRunPointers is one Agent Run's recovery position: the Turn number created so far
// and the Context/State Versions a restarted process resumes from.
type AgentRunPointers struct {
	CurrentTurnNo         int
	CurrentContextVersion int
	CurrentStateVersion   int
}

// AgentTurnRepository persists model interactions and implements the conditional claim
// that decides which advancement path may call the model.
type AgentTurnRepository interface {
	// Create inserts a Turn. UNIQUE (agent_run_id, turn_no) is what stops immediate
	// advancement, a callback and the Reconciler from producing a duplicate round.
	Create(ctx context.Context, turn domain.AgentTurn) error
	Get(ctx context.Context, turnID string) (domain.AgentTurn, error)

	// ListByAgentRunID returns every Turn of one Agent Run in persisted order (turn_no
	// ascending). It backs the read-only Agent Trace projection,
	// which expands the rounds an Agent Run actually
	// committed rather than inferring them from Events.
	ListByAgentRunID(ctx context.Context, agentRunID string) ([]domain.AgentTurn, error)

	// GetByRunAndTurnNo resolves one Agent Run's Turn by its number, which is how the
	// Agent timeout transaction finds the current Turn from the Agent Run's
	// CurrentTurnNo recovery pointer. It returns domain.ErrNotFound when no such Turn
	// exists.
	GetByRunAndTurnNo(ctx context.Context, agentRunID string, turnNo int) (domain.AgentTurn, error)

	// ClaimReady conditionally moves a Turn from READY to RUNNING and stamps started_at.
	// It reports true only for the caller whose UPDATE affected one row; that
	// caller alone may call the model, and only after the transaction commits. A false
	// result is not an error.
	ClaimReady(ctx context.Context, turnID string, now time.Time) (bool, error)

	// ListReady returns persisted READY Turns the Reconciler can rediscover after a
	// crash. A RUNNING Turn is deliberately excluded: the MVP never re-issues
	// a model request that may already be in flight. The limit bounds the scan batch.
	ListReady(ctx context.Context, limit int) ([]domain.AgentTurn, error)

	// MarkCompleted conditionally moves a RUNNING Turn to COMPLETED and writes the model
	// response, the reported token usage, the reported Cost (nil when none) and
	// completed_at, in the transaction that also commits the Decision and its READY
	// Action. It returns domain.ErrStaleClaim when the Turn is not RUNNING.
	MarkCompleted(ctx context.Context, turnID string, now time.Time, response json.RawMessage, usage *domain.TokenUsage, cost *domain.Cost) error

	// MarkFailed conditionally moves a RUNNING Turn to FAILED and writes its error and
	// completed_at. A failed Turn may have no Decision and no Action.
	MarkFailed(ctx context.Context, turnID string, now time.Time, execErr domain.ExecutionError) error

	// MarkTimedOut conditionally fails a Turn that is still READY or RUNNING. It exists
	// for the Agent timeout transaction alone: the deadline ends the current
	// Turn whether or not anyone ever claimed it, which is the one case in which a Turn
	// that never became RUNNING must still be closed. It reports false when the row was
	// no longer READY or RUNNING, which is not an error.
	MarkTimedOut(ctx context.Context, turnID string, now time.Time, execErr domain.ExecutionError) (bool, error)
}

// AgentDecisionRepository persists committed model decisions. A Decision is immutable:
// there is no update method, because recovery advances the existing Action instead of
// regenerating or rewriting what the model already decided.
type AgentDecisionRepository interface {
	// Create inserts the Decision in the same transaction as the Turn's COMPLETED status
	// and the single READY Action. UNIQUE (turn_id) allows at most one Decision per Turn.
	Create(ctx context.Context, decision domain.AgentDecision) error
	Get(ctx context.Context, decisionID string) (domain.AgentDecision, error)
}

// AgentActionRepository persists the durable work item created with a Decision and
// implements the conditional claim that decides who executes it.
type AgentActionRepository interface {
	// Create inserts the READY Action. UNIQUE (turn_id) and UNIQUE (decision_id) allow at
	// most one Action per Turn and per Decision.
	Create(ctx context.Context, action domain.AgentAction) error
	Get(ctx context.Context, actionID string) (domain.AgentAction, error)
	GetByTurnID(ctx context.Context, turnID string) (domain.AgentAction, error)

	// ClaimReady conditionally moves an Action from READY to RUNNING and stamps
	// started_at. The winner owns the execution right: for a TOOL_CALL it
	// creates the STARTED Tool Attempt and calls the Tool after COMMIT; for a FINAL it
	// closes the Action out inside the same transaction. A false result is not an error.
	ClaimReady(ctx context.Context, actionID string, now time.Time) (bool, error)

	// ListReady returns persisted READY Actions the Reconciler can rediscover after a
	// crash. It advances the original Action; it never asks the model again.
	// The limit bounds the scan batch.
	ListReady(ctx context.Context, limit int) ([]domain.AgentAction, error)

	// MarkSucceeded conditionally moves an Action from `from` (RUNNING for a synchronous
	// call, WAITING_CALLBACK for an asynchronous Tool resumed by its callback) to
	// SUCCEEDED and stamps completed_at. It returns domain.ErrStaleClaim when the row is
	// not in `from`, and domain.InvalidStateTransitionError when `from` cannot reach
	// SUCCEEDED.
	MarkSucceeded(ctx context.Context, actionID string, from domain.AgentActionStatus, now time.Time) error

	// MarkFailed conditionally moves a RUNNING or WAITING_CALLBACK Action to FAILED and
	// writes its error and completed_at, with the same semantics. READY is rejected: only
	// MarkTimedOut may fail an unclaimed Action.
	MarkFailed(ctx context.Context, actionID string, from domain.AgentActionStatus, now time.Time, execErr domain.ExecutionError) error

	// MarkWaiting conditionally moves a RUNNING Action to WAITING_CALLBACK and stamps
	// waiting_at, in the transaction that records an ASYNC Tool's dispatch. It
	// returns domain.ErrStaleClaim when the row is not RUNNING.
	MarkWaiting(ctx context.Context, actionID string, now time.Time) error

	// MarkTimedOut conditionally fails an Action that is still READY, RUNNING or
	// WAITING_CALLBACK. Only the Agent timeout transaction uses it: the
	// deadline ends the current Action in whichever of those states it is, including the
	// READY one no executor ever claimed. It reports false when the row had already
	// reached a terminal status, which is not an error. An Action waiting on a person is
	// not touched: its Agent Run is paused and its clock is not running.
	MarkTimedOut(ctx context.Context, actionID string, now time.Time, execErr domain.ExecutionError) (bool, error)

	// SetAuthorization records the authorization decision the claim made for a RUNNING
	// Action, with the reason of a DENY. It returns domain.ErrStaleClaim when the row is
	// not RUNNING.
	SetAuthorization(ctx context.Context, actionID string, decision domain.AuthorizationDecision, reason *string) error

	// MarkWaitingApproval conditionally moves a RUNNING Action to WAITING_APPROVAL,
	// stamps waiting_at and records the ASK decision, in the claim transaction that
	// requested the approval. It returns domain.ErrStaleClaim when the row is not RUNNING.
	MarkWaitingApproval(ctx context.Context, actionID string, now time.Time) error

	// ResumeApproved conditionally moves a WAITING_APPROVAL Action back to RUNNING, in
	// the transaction that recorded its approval. It returns domain.ErrStaleClaim when
	// the row is not WAITING_APPROVAL.
	ResumeApproved(ctx context.Context, actionID string, now time.Time) error

	// MarkWaitingHuman conditionally moves an Action from `from` (RUNNING or
	// WAITING_CALLBACK) to WAITING_HUMAN when its release passed the confirm deadline
	// without a definite production answer. It returns domain.ErrStaleClaim when the row
	// is no longer in `from`, which is how a poll answer that committed first wins.
	MarkWaitingHuman(ctx context.Context, actionID string, from domain.AgentActionStatus, now time.Time) error

	// ResumeForRelease conditionally moves a WAITING_CALLBACK Action back to RUNNING for
	// the one path that re-releases the same committed Decision after production confirmed
	// the previous operation void. It returns domain.ErrStaleClaim when the row is not
	// WAITING_CALLBACK.
	ResumeForRelease(ctx context.Context, actionID string, now time.Time) error
}

// ApprovalRepository persists human approval requests and implements the single
// conditional decision that settles each one.
type ApprovalRepository interface {
	// Create inserts the PENDING approval in the transaction that suspended its Action.
	// UNIQUE (action_id) makes a second request for the same Action lose in the database.
	Create(ctx context.Context, approval domain.Approval) error
	Get(ctx context.Context, approvalID string) (domain.Approval, error)
	GetByActionID(ctx context.Context, actionID string) (domain.Approval, error)

	// ListByStatus returns approvals in the given status, oldest request first, bounded by
	// limit.
	ListByStatus(ctx context.Context, status domain.ApprovalStatus, limit int) ([]domain.Approval, error)

	// ListExpiredPending returns PENDING approvals whose deadline is at or before now, so
	// the Reconciler can rediscover the expiry work. It claims nothing.
	ListExpiredPending(ctx context.Context, now time.Time, limit int) ([]domain.Approval, error)

	// Decide conditionally settles a PENDING approval with a terminal status, decided_at,
	// the deciding identity (nil for an expiry) and the reason. The `status = 'PENDING'`
	// predicate decides the single winner between approve, reject and expiry; a false
	// result means another decision already committed and nothing was written.
	Decide(ctx context.Context, approvalID string, status domain.ApprovalStatus, now time.Time, decidedBy, reason *string) (bool, error)
}

// ReleaseOccupationRepository persists which approved release Action holds a deploy
// target. The partial unique index on ACTIVE target keys is the only arbiter of occupation
// uniqueness; no process-local check stands in for it.
type ReleaseOccupationRepository interface {
	// Acquire inserts an ACTIVE occupation with INSERT ... ON CONFLICT DO NOTHING and
	// reports whether the row was written. false means the target is held by another
	// ACTIVE occupation; the caller returns the call to the Agent as TARGET_OCCUPIED.
	Acquire(ctx context.Context, occupation domain.ReleaseOccupation) (bool, error)
	Get(ctx context.Context, id string) (domain.ReleaseOccupation, error)
	// GetActiveByTarget returns the ACTIVE occupation of a target, or domain.ErrNotFound
	// when the target is free. It is the read-only check the ask path uses before any
	// approval exists.
	GetActiveByTarget(ctx context.Context, targetKey string) (domain.ReleaseOccupation, error)
	ListByActionID(ctx context.Context, actionID string) ([]domain.ReleaseOccupation, error)
	// SetOperationKey names the operation that now holds the target after a re-release.
	// A row no longer ACTIVE returns domain.ErrStaleClaim.
	SetOperationKey(ctx context.Context, id, operationKey string) error
	// Release conditionally moves ACTIVE to RELEASED with released_at and the reason. A
	// row no longer ACTIVE returns domain.ErrStaleClaim.
	Release(ctx context.Context, id string, reason domain.ReleaseReason, now time.Time) error
}

// HumanWaitRepository persists the human wait a release Action enters when production
// gives no confirmation before the confirm deadline.
type HumanWaitRepository interface {
	// Create inserts the OPEN wait in the transaction that moved its Action to
	// WAITING_HUMAN. UNIQUE (action_id) makes a second wait for the same Action lose.
	Create(ctx context.Context, wait domain.HumanWait) error
	Get(ctx context.Context, id string) (domain.HumanWait, error)
	GetByActionID(ctx context.Context, actionID string) (domain.HumanWait, error)
	// Close conditionally settles an OPEN wait with a terminal status, closed_at, the
	// closing identity (nil unless a person closed it) and the outcome. A false result
	// means another closing already committed and nothing was written.
	Close(ctx context.Context, id string, status domain.HumanWaitStatus, now time.Time, closedBy, outcome *string) (bool, error)
}

// DueConfirmDeadline identifies one release Attempt whose confirm deadline has passed while
// its Action still waits for production. ActionID is for logging only; the use case
// re-reads every fact itself.
type DueConfirmDeadline struct {
	AttemptID string
	ActionID  string
}

// AgentTurnManifestRepository persists one Context Manifest per Turn. It has no update
// method: a Manifest records what one model request carried, and that request is never
// re-issued.
type AgentTurnManifestRepository interface {
	// Create inserts a Turn's Manifest in the transaction that claimed the Turn. The
	// PRIMARY KEY on turn_id yields domain.ErrConflict for a second Manifest of the same
	// Turn, so only the claim winner's transaction can ever hold one.
	Create(ctx context.Context, manifest domain.AgentTurnManifest) error

	// GetByTurnID returns one Turn's Manifest, or domain.ErrNotFound while the Turn has
	// not been claimed.
	GetByTurnID(ctx context.Context, turnID string) (domain.AgentTurnManifest, error)

	// ListByAgentRunID returns every Manifest of one Agent Run, oldest Turn first. It
	// backs the read-only Agent Trace projection.
	ListByAgentRunID(ctx context.Context, agentRunID string) ([]domain.AgentTurnManifest, error)
}

// ToolAttemptRepository preserves every real Tool call of a TOOL_CALL Action.
type ToolAttemptRepository interface {
	// Create inserts the STARTED Attempt in the transaction that claims the Action.
	// UNIQUE (action_id, attempt_no) rejects a duplicated dispatch. A release Attempt's
	// OperationKey and ConfirmDeadline are written by the same INSERT; UNIQUE
	// (operation_key) rejects a second Attempt under the same operation.
	Create(ctx context.Context, attempt domain.ToolAttempt) error
	Get(ctx context.Context, attemptID string) (domain.ToolAttempt, error)
	ListByActionID(ctx context.Context, actionID string) ([]domain.ToolAttempt, error)

	// MarkSucceeded conditionally moves an Attempt from `from` (STARTED for a synchronous
	// call, DISPATCHED for an asynchronous call resumed by its callback) to SUCCEEDED and
	// writes its result, completed_at and the Cost the Provider reported (nil when none).
	// A late duplicate finds the row no longer in `from` and gets domain.ErrStaleClaim
	// instead of overwriting the committed result.
	MarkSucceeded(ctx context.Context, attemptID string, from domain.ToolAttemptStatus, now time.Time, result json.RawMessage, cost *domain.Cost) error

	// MarkFailed conditionally moves an Attempt from `from` to FAILED and writes its error
	// and completed_at, with the same domain.ErrStaleClaim semantics.
	MarkFailed(ctx context.Context, attemptID string, from domain.ToolAttemptStatus, now time.Time, execErr domain.ExecutionError) error

	// MarkDispatched conditionally moves a STARTED Attempt of an ASYNC Tool to DISPATCHED
	// and stamps dispatched_at. In the same UPDATE it writes firstPollAt as the first
	// next_poll_at; firstPollAt is nil when the registration declares no poll policy. It
	// returns domain.ErrStaleClaim when the row is no longer STARTED, e.g. because the
	// Agent timeout closed it while the Tool was dispatching.
	MarkDispatched(ctx context.Context, attemptID string, now time.Time, firstPollAt *time.Time) error

	// ClaimPoll conditionally claims one Provider poll of a DISPATCHED Attempt whose
	// next_poll_at is due and whose poll_count is below maxPolls. It increments
	// poll_count and schedules next_poll_at at now+interval, or clears it when this
	// claim reaches maxPolls. The bounds come from the caller, which reads them from
	// the current registration. A false result is not an error: the poll is not due,
	// the bound was reached, the Attempt left DISPATCHED, or another claimer won.
	// Claiming a poll is a scheduling fact; the caller writes no Event for it.
	ClaimPoll(ctx context.Context, attemptID string, now time.Time, interval time.Duration, maxPolls int) (bool, error)

	// ClearPoll unschedules polling of a DISPATCHED Attempt by setting next_poll_at to
	// NULL, for a registration that no longer declares a usable poll policy or a Callback
	// Binding that does not route a poll to the Attempt. A false result means the Attempt
	// is no longer DISPATCHED and nothing changed.
	ClearPoll(ctx context.Context, attemptID string) (bool, error)

	// ListDuePolls returns DISPATCHED Tool Attempts whose next_poll_at is at or before
	// now, oldest first, bounded by limit. It only discovers persisted work; claiming each
	// poll is the caller's conditional update.
	ListDuePolls(ctx context.Context, now time.Time, limit int) ([]DueToolPoll, error)

	// MarkLost conditionally moves a DISPATCHED Attempt whose Provider no longer knows the
	// dispatched task to FAILED, writing its error, completed_at and the uncertain
	// determination once. A row no longer DISPATCHED returns domain.ErrStaleClaim, so a
	// callback, a timeout or another poll that committed first keeps its outcome.
	MarkLost(ctx context.Context, attemptID string, now time.Time, execErr domain.ExecutionError, uncertain bool) error

	// MarkTimedOut conditionally fails an Attempt that is still STARTED or DISPATCHED.
	// The Agent deadline covers the synchronous call and the wait for a callback alike,
	// so the Agent timeout transaction closes both, and only it may close a
	// DISPATCHED one. It reports false when the Attempt already completed, which is what
	// lets a result that committed first keep its outcome.
	MarkTimedOut(ctx context.Context, attemptID string, now time.Time, execErr domain.ExecutionError) (bool, error)

	// ListOrphanStarted returns STARTED synchronous Tool Attempts written by a runner other
	// than currentRunnerID, of Agent Runs that have not terminated and whose deadline has
	// not passed, oldest first and at most limit of them. An Attempt with no recorded
	// runner is never returned. Read-only; the caller decides nothing from it except which
	// use case to call.
	ListOrphanStarted(ctx context.Context, currentRunnerID string, now time.Time, limit int) ([]domain.ToolAttempt, error)

	// MarkInterrupted conditionally moves a STARTED Attempt to FAILED, writing its error,
	// completed_at and the uncertain determination once. A row no longer STARTED returns
	// domain.ErrStaleClaim.
	MarkInterrupted(ctx context.Context, attemptID string, now time.Time, execErr domain.ExecutionError, uncertain bool) error

	// MarkOutcomeUnknown records that the Runtime stopped knowing whether a release
	// Attempt's operation took effect: a STARTED Attempt becomes DISPATCHED, the first
	// unknown time is written once and never moved, and the next poll is scheduled at
	// nextPollAt so the query path takes over. It reports whether this call set the
	// unknown time, which is when the caller writes the RELEASE_OUTCOME_UNKNOWN Event. A
	// row that is neither STARTED nor DISPATCHED returns domain.ErrStaleClaim.
	MarkOutcomeUnknown(ctx context.Context, attemptID string, now, nextPollAt time.Time) (first bool, err error)

	// MarkVoidRequested writes void_requested_at once on a DISPATCHED Attempt; a later
	// resend of a lost void request leaves the first time in place. A row no longer
	// DISPATCHED returns domain.ErrStaleClaim.
	MarkVoidRequested(ctx context.Context, attemptID string, now time.Time) error

	// MarkFailedDefinite conditionally moves an Attempt from `from` to FAILED with
	// uncertain = false: production gave a definite answer that nothing took effect, so the
	// Attempt is closed as a known non-effect, not as an interrupted call. A row no longer
	// in `from` returns domain.ErrStaleClaim.
	MarkFailedDefinite(ctx context.Context, attemptID string, from domain.ToolAttemptStatus, now time.Time, execErr domain.ExecutionError) error

	// ListDueConfirmDeadlines returns release Attempts that are still STARTED or
	// DISPATCHED, whose confirm deadline is at or before now and whose Action is RUNNING
	// or WAITING_CALLBACK, oldest deadline first, bounded by limit. It discovers persisted
	// work only; opening the human wait is the use case's conditional update.
	ListDueConfirmDeadlines(ctx context.Context, now time.Time, limit int) ([]DueConfirmDeadline, error)

	// CountByAgentRunForTools counts the Tool Attempts of one Agent Run whose Tool is
	// one of toolNames, in every Attempt status. This is the number of generation calls
	// already made: an Attempt exists only once its Action won the claim, and a started,
	// dispatched, succeeded or failed Attempt may each have reached the Provider, so all
	// of them consume the limit. An Action rejected at claim creates no Attempt and does
	// not count. The caller holds the Run lock so concurrent claims cannot both see the
	// last free slot. An empty toolNames counts nothing.
	CountByAgentRunForTools(ctx context.Context, agentRunID string, toolNames []string) (int, error)
}

// ExecutionFactRepository persists the trusted facts Tool results produce. Facts are
// written once, in the Tool result transaction, and never updated.
type ExecutionFactRepository interface {
	// Insert writes one fact and returns the stored row. A second fact of the same type
	// for the same Tool Attempt yields domain.ErrExecutionFactDuplicate; the caller rolls
	// the whole transaction back instead of ignoring it.
	Insert(ctx context.Context, fact domain.ExecutionFact) (domain.ExecutionFact, error)

	// FindLatest returns the newest fact of factType about subjectRef in the Run, ordered
	// by created_at then id, or nil when there is none. Basis resolution and precondition
	// checks use it.
	FindLatest(ctx context.Context, runID, factType, subjectRef string) (*domain.ExecutionFact, error)

	// ListForRequirement returns up to limit facts of factType in the Run, newest first
	// (created_at then id, descending). A non-nil subjectRef restricts the result to that
	// subject. The caller matches bindings against the requested arguments.
	ListForRequirement(ctx context.Context, runID, factType string, subjectRef *string, limit int) ([]domain.ExecutionFact, error)

	// ListByAgentRun returns up to limit facts of one Agent Run, oldest first (created_at
	// then id), for Trace projection. A caller that needs to know whether more facts
	// exist asks for one more than it shows. A non-positive limit is an error rather than
	// an unbounded read.
	ListByAgentRun(ctx context.Context, agentRunID string, limit int) ([]domain.ExecutionFact, error)

	// ListByRunAndType returns up to limit facts of factType in the Run, oldest first
	// (created_at then id, ascending), for the facts a Node Type declares it reads. A
	// non-positive limit is an error rather than an unbounded read.
	ListByRunAndType(ctx context.Context, runID, factType string, limit int) ([]domain.ExecutionFact, error)

	// Get returns one fact by id, or domain.ErrNotFound. A record that names a fact it
	// rests on is checked against the stored row through it.
	Get(ctx context.Context, factID string) (domain.ExecutionFact, error)
}

// AgentContextVersionRepository persists the immutable message chain a restarted process
// replays from. It has no update method: a committed version is never rewritten.
type AgentContextVersionRepository interface {
	// Create inserts one version. UNIQUE (agent_run_id, version) keeps the chain linear.
	Create(ctx context.Context, version domain.AgentContextVersion) error

	// GetByRunAndVersion reads the version the Agent Run's pointer names. Versions are
	// always resolved through that pointer, so there is no lookup by row ID.
	GetByRunAndVersion(ctx context.Context, agentRunID string, version int) (domain.AgentContextVersion, error)
}

// AgentStateVersionRepository persists the immutable structured-state chain, with the
// same identity and immutability rules as the Context chain.
type AgentStateVersionRepository interface {
	Create(ctx context.Context, version domain.AgentStateVersion) error
	GetByRunAndVersion(ctx context.Context, agentRunID string, version int) (domain.AgentStateVersion, error)
}

// EventRepository appends and replays the ordered Event log of one Run.
type EventRepository interface {
	// Append allocates the next seq from the held Run aggregate lock, inserts the Event
	// and advances the lock's watermark. Passing a lock that was not obtained from
	// RunRepository.LockForUpdate is an error: seq must never be allocated without the
	// aggregate lock.
	Append(ctx context.Context, lock *RunLock, ev domain.Event) (domain.Event, error)

	// ListAfter returns committed Events with seq greater than afterSeq, ascending. It
	// backs both Snapshot-to-SSE handoff windows; the limit bounds the read.
	ListAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]domain.Event, error)

	// ListForNodeRun returns the committed Events of one NodeRun of runID whose type is
	// one of types, ascending by seq. The limit bounds the read.
	ListForNodeRun(ctx context.Context, runID, nodeRunID string, types []domain.EventType, limit int) ([]domain.Event, error)
}

// ProviderSlotRepository persists Provider occupancy facts: which NodeRun holds a slot of
// which asynchronous Provider. Every write happens inside the caller's transaction, which
// already holds the Run aggregate lock; the Provider row lock is always taken after it.
type ProviderSlotRepository interface {
	// SeedLocks inserts one provider_slot_locks row per id; existing rows are kept.
	SeedLocks(ctx context.Context, providerIDs []string) error
	// LockProvider takes the Provider row lock (SELECT ... FOR UPDATE) for the rest of the
	// transaction; a missing row is an error, because an unseeded Provider cannot be
	// serialized.
	LockProvider(ctx context.Context, providerID string) error
	// CountHeld counts the provider_slots rows of providerID. Read after LockProvider
	// returned, it sees every admission that committed under that lock.
	CountHeld(ctx context.Context, providerID string) (int, error)
	// Acquire inserts the occupancy row; a primary-key conflict is returned as an error,
	// because a READY NodeRun can never already hold a slot.
	Acquire(ctx context.Context, nodeRunID, providerID string, now time.Time) error
	// Release deletes the occupancy row and reports which Provider it belonged to. A
	// NodeRun that holds no slot releases nothing, which is not an error.
	Release(ctx context.Context, nodeRunID string) (providerID string, released bool, err error)
	// CountEarlierDeferred counts the READY, already-refused NodeRuns of providerID in
	// other Runs that are still RUNNING or PAUSED and whose (ready_at, id) sorts before
	// key. A result above zero means an earlier arrival is still queued; the result plus
	// one is the queue position.
	CountEarlierDeferred(ctx context.Context, providerID, excludeRunID string, key QueueKey) (int, error)
	// NextDeferredRun returns the Run of the earliest READY, already-refused NodeRun of
	// providerID whose Run is still RUNNING or PAUSED.
	NextDeferredRun(ctx context.Context, providerID string) (runID string, found bool, err error)
}

// AgentWorkspaceRepository persists the repository binding frozen onto an Agent Run.
type AgentWorkspaceRepository interface {
	// Create inserts the workspace in the transaction that creates its Agent Run. The
	// primary key on agent_run_id makes a second binding of the same Agent Run lose in
	// the database.
	Create(ctx context.Context, workspace domain.AgentWorkspace) error

	// GetByAgentRunID returns the frozen workspace, or domain.ErrNotFound for an Agent Run
	// started without one.
	GetByAgentRunID(ctx context.Context, agentRunID string) (domain.AgentWorkspace, error)
}

// WorkspacePatchRepository persists the cumulative patches of an Agent Run's workspace.
type WorkspacePatchRepository interface {
	// Append inserts one patch in the Tool result transaction that accepted it. The caller
	// allocates Seq under the Run aggregate lock as the latest Seq plus one; UNIQUE
	// (agent_run_id, seq) and UNIQUE (tool_attempt_id) make a duplicated transaction lose
	// with domain.ErrConflict, as does a patch over the stored length bound.
	Append(ctx context.Context, patch domain.WorkspacePatch) error

	// Latest returns the newest patch of the Agent Run, text included, or nil when the
	// workspace has not been written yet.
	Latest(ctx context.Context, agentRunID string) (*domain.WorkspacePatch, error)

	// GetByDigest returns the newest patch of the Agent Run with the given digest, text
	// included, or nil when the Agent Run recorded no patch with that digest. It backs the
	// approval detail, which shows the full text of the patch a change request is about.
	GetByDigest(ctx context.Context, agentRunID, patchDigest string) (*domain.WorkspacePatch, error)

	// ListSummariesByAgentRun returns up to limit patches of the Agent Run in Seq order
	// without their text, for Trace projection. A caller that needs to know whether more
	// exist asks for one more than it shows. A non-positive limit is an error rather than
	// an unbounded read.
	ListSummariesByAgentRun(ctx context.Context, agentRunID string, limit int) ([]domain.WorkspacePatch, error)
}

// ChangeRequestRepository persists the change requests of an Agent Run.
type ChangeRequestRepository interface {
	// CreateOrGet inserts one change request in the Tool result transaction that accepted
	// it, or, when the Agent Run already holds a request with the same content digest,
	// returns that existing row with created false and writes nothing. UNIQUE
	// (agent_run_id, content_digest) is what decides between the two; every other
	// constraint violation surfaces as domain.ErrConflict.
	CreateOrGet(ctx context.Context, request domain.ChangeRequest) (stored domain.ChangeRequest, created bool, err error)

	// Get returns one change request by id, or domain.ErrNotFound.
	Get(ctx context.Context, id string) (domain.ChangeRequest, error)

	// ListByAgentRun returns up to limit change requests of the Agent Run, oldest first
	// (created_at then id), for Trace projection. A caller that needs to know whether more
	// exist asks for one more than it shows. A non-positive limit is an error rather than
	// an unbounded read.
	ListByAgentRun(ctx context.Context, agentRunID string, limit int) ([]domain.ChangeRequest, error)
}

// QueueKey orders queued NodeRuns of one Provider: ready_at first, NodeRun id as the
// tie-break, so the order is total and the same on every path.
type QueueKey struct {
	ReadyAt   time.Time
	NodeRunID string
}

// RunLock is proof that the caller holds the Run aggregate lock inside the current
// transaction. Its fields are unexported and it is only produced by a RunRepository
// implementation, so an Event cannot be appended without first taking the lock: the
// signature of EventRepository.Append makes that a compile-time requirement, and the
// zero value is rejected at runtime.
type RunLock struct {
	runID   string
	status  domain.RunStatus
	lastSeq int64
	held    bool
}

// NewRunLock is the constructor for RunRepository implementations. Application code
// obtains a lock through RunRepository.LockForUpdate instead.
func NewRunLock(runID string, status domain.RunStatus, lastSeq int64) *RunLock {
	return &RunLock{runID: runID, status: status, lastSeq: lastSeq, held: true}
}

// RunID reports the locked Run.
func (l *RunLock) RunID() string { return l.runID }

// Status reports the Run status read under the lock.
func (l *RunLock) Status() domain.RunStatus { return l.status }

// LastSeq reports the highest Event seq allocated so far, including allocations made
// inside the current transaction.
func (l *RunLock) LastSeq() int64 { return l.lastSeq }

// Held reports whether this value came from an actual lock acquisition.
func (l *RunLock) Held() bool { return l != nil && l.held }

// NextSeq allocates the next Event seq under the lock and advances the watermark. Only
// an EventRepository implementation calls it, once per appended Event.
func (l *RunLock) NextSeq() int64 {
	l.lastSeq++
	return l.lastSeq
}
