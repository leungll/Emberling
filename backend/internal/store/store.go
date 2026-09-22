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
// commit together (invariant #4).
type UnitOfWork interface {
	// WithinTx runs fn inside one database transaction. Returning a non-nil error rolls
	// the whole transaction back, including every Event appended inside it.
	WithinTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error

	// WithinReadTx runs fn inside one read-only transaction that observes a single
	// consistent snapshot for every statement it issues (PostgreSQL REPEATABLE READ),
	// not a fresh snapshot per statement. Use it wherever two or more reads must agree
	// with each other even if a concurrent transaction commits between them — the
	// Snapshot-to-SSE handoff contract (docs/08-interface-spec.md §5: "lastSeq 与
	// Snapshot 在同一个一致性读取中取得") needs exactly this, because Run.LastSeq and
	// NodeRuns are read as two separate statements. A caller must not write inside fn:
	// PostgreSQL rejects it.
	WithinReadTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

// Tx exposes the repositories bound to one active transaction.
type Tx interface {
	Definitions() DefinitionRepository
	Assets() AssetRepository
	Runs() RunRepository
	NodeRuns() NodeRunRepository
	NodeAttempts() NodeAttemptRepository
	CallbackBindings() CallbackBindingRepository
	PendingCallbacks() PendingCallbackRepository
	Events() EventRepository
	AgentRuns() AgentRunRepository
	AgentTurns() AgentTurnRepository
	AgentDecisions() AgentDecisionRepository
	AgentActions() AgentActionRepository
	ToolAttempts() ToolAttemptRepository
	AgentContextVersions() AgentContextVersionRepository
	AgentStateVersions() AgentStateVersionRepository
}

// DefinitionRepository persists immutable Workflow versions. It has no update method:
// a stored version can never be rewritten (invariant #8).
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
	// (docs/08-interface-spec.md §3.1: name, description, latestVersion, updatedAt,
	// lastRun).
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
// locates its binary. The key is a system Secret (10-ops §4), which is why it is a Store
// field rather than a domain.Asset field: it travels between this repository and the
// Asset storage layer only, and never reaches an Event, Trace or API response.
type AssetRecord struct {
	Asset      domain.Asset
	StorageKey string
}

// AssetRepository persists Asset Metadata. It has no update or delete method: an
// asset_id points at immutable content, so replacing content means creating a new Asset
// (10-ops §3).
type AssetRepository interface {
	// Create inserts one Asset. The table's PRIMARY KEY decides duplicates, so a second
	// insert of the same asset_id yields domain.ErrConflict and leaves the committed row
	// untouched.
	Create(ctx context.Context, record AssetRecord) error

	// Get returns the Metadata and storage key of one Asset, or domain.ErrNotFound.
	Get(ctx context.Context, assetID string) (AssetRecord, error)
}

// RunRepository persists Runs and serialises concurrent advancement of one Run.
type RunRepository interface {
	Create(ctx context.Context, run domain.Run) error
	Get(ctx context.Context, runID string) (domain.Run, error)

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
	// after a retryable Attempt failure (06 §1.4: "允许重试 | FAILED | 保持RUNNING |
	// next_attempt_at、NODE_RETRYING"). It requires the NodeRun to still be RUNNING and
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

	// MarkFailed conditionally moves a NodeRun to FAILED and writes its error and
	// completed_at, with the same domain.ErrStaleClaim /
	// *domain.InvalidStateTransitionError semantics as MarkSucceeded.
	MarkFailed(ctx context.Context, nodeRunID string, from domain.NodeRunStatus, now time.Time, execErr domain.ExecutionError) error

	// MarkWaiting conditionally moves a RUNNING NodeRun to WAITING_CALLBACK and stamps
	// waiting_at. It returns domain.ErrStaleClaim when the row is not RUNNING.
	MarkWaiting(ctx context.Context, nodeRunID string, now time.Time) error
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
	// stamps dispatched_at. It returns domain.ErrStaleClaim when the row is not STARTED.
	MarkDispatched(ctx context.Context, attemptID string, now time.Time) error

	// MarkSucceeded conditionally moves an Attempt to SUCCEEDED and writes its result
	// and completed_at. Result is written once: a stale or illegal caller never
	// overwrites a committed result. Same domain.ErrStaleClaim /
	// *domain.InvalidStateTransitionError semantics as Transition.
	MarkSucceeded(ctx context.Context, attemptID string, from domain.NodeAttemptStatus, now time.Time, result json.RawMessage) error

	// MarkFailed conditionally moves an Attempt to FAILED and writes its error and
	// completed_at, with the same semantics as MarkSucceeded.
	MarkFailed(ctx context.Context, attemptID string, from domain.NodeAttemptStatus, now time.Time, execErr domain.ExecutionError) error

	// ListExpired returns non-terminal Attempts (STARTED or DISPATCHED) whose deadline_at
	// has passed. It backs Reconciler timeout rediscovery (06 §2: "Reconciler 必须能够
	// 扫描过期的 STARTED 或 DISPATCHED Attempt"). The limit bounds the scan batch.
	ListExpired(ctx context.Context, before time.Time, limit int) ([]domain.NodeAttempt, error)

	// Latest returns the highest attempt_no Attempt of a NodeRun, or nil if none exists.
	Latest(ctx context.Context, nodeRunID string) (*domain.NodeAttempt, error)
}

// CallbackBindingRepository persists the only authoritative route from an external task
// identity back to the Attempt that dispatched it (05 §1.6). A binding is immutable:
// there is no update method, because re-pointing an external identity at another Attempt
// would make callback routing ambiguous.
type CallbackBindingRepository interface {
	// Create inserts a binding. It commits in the same transaction as the Attempt's
	// DISPATCHED status and the NodeRun's WAITING_CALLBACK status (05 §3.1 unit 6).
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
// binding committed (05 §1.7). It stores facts only: matching a Pending Callback to an
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
	// in-process post-commit check is a latency optimisation and not the recovery source
	// (invariant #6). The limit bounds the scan batch.
	ListConsumableForWaiting(ctx context.Context, now time.Time, limit int) ([]PendingForWaiting, error)

	// DeleteExpired removes records past their expiry so an unmatched early callback
	// cannot occupy the database indefinitely (05 §1.7). The limit bounds the batch; it
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
// Agent NodeRun's Agent Run (05 §1.8). The configuration columns have no update method:
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

	// ListExpired returns Agent Runs that have not terminated and whose frozen deadline
	// is at or before `before`, so the Reconciler can rediscover the Agent timeout work
	// an in-process timer never performed (06 §2.1, "Agent deadline 已到 -> timeout 用
	// 例"). Terminated Agent Runs are excluded: their outcome is already committed. The
	// limit bounds the scan batch.
	ListExpired(ctx context.Context, before time.Time, limit int) ([]domain.AgentRun, error)
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
	// ascending). It backs the read-only Agent Trace projection
	// (docs/08-interface-spec.md §3.4), which expands the rounds an Agent Run actually
	// committed rather than inferring them from Events.
	ListByAgentRunID(ctx context.Context, agentRunID string) ([]domain.AgentTurn, error)

	// GetByRunAndTurnNo resolves one Agent Run's Turn by its number, which is how the
	// Agent timeout transaction finds the current Turn from the Agent Run's
	// CurrentTurnNo recovery pointer. It returns domain.ErrNotFound when no such Turn
	// exists.
	GetByRunAndTurnNo(ctx context.Context, agentRunID string, turnNo int) (domain.AgentTurn, error)

	// ClaimReady conditionally moves a Turn from READY to RUNNING and stamps started_at
	// (06 §1.7). It reports true only for the caller whose UPDATE affected one row; that
	// caller alone may call the model, and only after the transaction commits. A false
	// result is not an error.
	ClaimReady(ctx context.Context, turnID string, now time.Time) (bool, error)

	// ListReady returns persisted READY Turns the Reconciler can rediscover after a
	// crash (06 §2.1). A RUNNING Turn is deliberately excluded: the MVP never re-issues
	// a model request that may already be in flight. The limit bounds the scan batch.
	ListReady(ctx context.Context, limit int) ([]domain.AgentTurn, error)

	// MarkCompleted conditionally moves a RUNNING Turn to COMPLETED and writes the model
	// response, the reported token usage and completed_at, in the transaction that also
	// commits the Decision and its READY Action. It returns domain.ErrStaleClaim when
	// the Turn is not RUNNING.
	MarkCompleted(ctx context.Context, turnID string, now time.Time, response json.RawMessage, usage *domain.TokenUsage) error

	// MarkFailed conditionally moves a RUNNING Turn to FAILED and writes its error and
	// completed_at. A failed Turn may have no Decision and no Action.
	MarkFailed(ctx context.Context, turnID string, now time.Time, execErr domain.ExecutionError) error

	// MarkTimedOut conditionally fails a Turn that is still READY or RUNNING. It exists
	// for the Agent timeout transaction alone (06 §1.7): the deadline ends the current
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
	// started_at (06 §1.7). The winner owns the execution right: for a TOOL_CALL it
	// creates the STARTED Tool Attempt and calls the Tool after COMMIT; for a FINAL it
	// closes the Action out inside the same transaction. A false result is not an error.
	ClaimReady(ctx context.Context, actionID string, now time.Time) (bool, error)

	// ListReady returns persisted READY Actions the Reconciler can rediscover after a
	// crash (06 §2.1). It advances the original Action; it never asks the model again.
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
	// waiting_at, in the transaction that records an ASYNC Tool's dispatch (06 §1.7). It
	// returns domain.ErrStaleClaim when the row is not RUNNING.
	MarkWaiting(ctx context.Context, actionID string, now time.Time) error

	// MarkTimedOut conditionally fails an Action that is still READY, RUNNING or
	// WAITING_CALLBACK. Only the Agent timeout transaction uses it (06 §1.7): the
	// deadline ends the current Action in whichever of those states it is, including the
	// READY one no executor ever claimed. It reports false when the row had already
	// reached a terminal status, which is not an error.
	MarkTimedOut(ctx context.Context, actionID string, now time.Time, execErr domain.ExecutionError) (bool, error)
}

// ToolAttemptRepository preserves every real Tool call of a TOOL_CALL Action.
type ToolAttemptRepository interface {
	// Create inserts the STARTED Attempt in the transaction that claims the Action.
	// UNIQUE (action_id, attempt_no) rejects a duplicated dispatch.
	Create(ctx context.Context, attempt domain.ToolAttempt) error
	Get(ctx context.Context, attemptID string) (domain.ToolAttempt, error)
	ListByActionID(ctx context.Context, actionID string) ([]domain.ToolAttempt, error)

	// MarkSucceeded conditionally moves an Attempt from `from` (STARTED for a synchronous
	// call, DISPATCHED for an asynchronous call resumed by its callback) to SUCCEEDED and
	// writes its result and completed_at. A late duplicate finds the row no longer in
	// `from` and gets domain.ErrStaleClaim instead of overwriting the committed result.
	MarkSucceeded(ctx context.Context, attemptID string, from domain.ToolAttemptStatus, now time.Time, result json.RawMessage) error

	// MarkFailed conditionally moves an Attempt from `from` to FAILED and writes its error
	// and completed_at, with the same domain.ErrStaleClaim semantics.
	MarkFailed(ctx context.Context, attemptID string, from domain.ToolAttemptStatus, now time.Time, execErr domain.ExecutionError) error

	// MarkDispatched conditionally moves a STARTED Attempt of an ASYNC Tool to DISPATCHED
	// and stamps dispatched_at. It returns domain.ErrStaleClaim when the row is no longer
	// STARTED, e.g. because the Agent timeout closed it while the Tool was dispatching.
	MarkDispatched(ctx context.Context, attemptID string, now time.Time) error

	// MarkTimedOut conditionally fails an Attempt that is still STARTED or DISPATCHED.
	// The Agent deadline covers the synchronous call and the wait for a callback alike
	// (05 §1.8), so the Agent timeout transaction closes both, and only it may close a
	// DISPATCHED one. It reports false when the Attempt already completed, which is what
	// lets a result that committed first keep its outcome.
	MarkTimedOut(ctx context.Context, attemptID string, now time.Time, execErr domain.ExecutionError) (bool, error)
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
