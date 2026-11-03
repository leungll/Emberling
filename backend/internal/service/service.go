// Package service owns Emberling's use cases: it opens every database transaction
// through the Unit of Work, decides what to persist using the pure decisions in
// runtime, and schedules the work that may only happen after COMMIT.
//
// Nothing in this package executes a Node, calls a Model or touches HTTP. Post-COMMIT
// work is handed to a WorkEnqueuer, and SSE cursors are woken through an EventNotifier;
// neither is a source of recovery, because persisted READY work is rediscovered by the
// Reconciler.
package service

import (
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// WorkEnqueuer offers committed READY work to the in-process work queue after its
// transaction has committed: a Run that may have a NodeRun to advance, or one persisted
// READY Agent Turn. It reports whether the item was accepted; a refusal is not an error
// and must never roll back committed facts, because the Reconciler rediscovers the same
// persisted READY work (the persisted-work recovery rule: the queue is only a latency
// optimization).
type WorkEnqueuer interface {
	EnqueueAdvance(runID string) bool
	EnqueueAgentTurn(runID, turnID string) bool
}

// EventNotifier wakes in-process SSE cursors after Events have committed. PostgreSQL
// rows remain the authority for replay; this only avoids waiting for the next poll.
type EventNotifier interface {
	EventsCommitted(runID string, lastSeq int64)
}

// NoopEnqueuer accepts nothing. It models a Backend with no worker pool, where the
// Reconciler is the only driver.
type NoopEnqueuer struct{}

func (NoopEnqueuer) EnqueueAdvance(string) bool { return false }

func (NoopEnqueuer) EnqueueAgentTurn(string, string) bool { return false }

// NoopNotifier drops notifications. SSE still works through cursor polling.
type NoopNotifier struct{}

func (NoopNotifier) EventsCommitted(string, int64) {}

// Deps carries the collaborators every service shares. Callers build it once at
// startup; services hold it by value and never mutate it.
type Deps struct {
	UoW      store.UnitOfWork
	Nodes    *registry.NodeRegistry
	Models   *registry.ModelRegistry
	Tools    *registry.ToolRegistry
	Compiler *runtime.Compiler
	Clock    domain.Clock
	IDs      domain.IDGenerator
	Assets   AssetContentStore
	// ArtifactContent reads Execution Artifact content for the Run-scoped content read.
	// Only ArtifactService uses it; it is nil where no content is ever read back.
	ArtifactContent ArtifactContentReader
	Queue           WorkEnqueuer
	Notifier        EventNotifier
	Logger          *slog.Logger
	Callback        CallbackConfig
	// PollHooks lets a test stop or synchronize PollAttempt between its claim commit and
	// the Provider query. The zero value installs no hook.
	PollHooks PollHooks
	// ProviderCapacity is the in-flight task limit per asynchronous Provider id, read once
	// at startup. A Provider absent from it is never refused a slot; the limit is never
	// persisted, so a restart with a smaller value only stops new admissions until the
	// held count drops below it.
	ProviderCapacity map[string]int
	// CapacityHooks lets a test synchronize admission at exact points inside the claim
	// transaction. The zero value installs no hook.
	CapacityHooks CapacityHooks
	// ApprovalHooks lets a test synchronize the approval decision paths. The zero value
	// installs no hook.
	ApprovalHooks ApprovalHooks
	// RunnerID identifies this process instance. It is generated once per process start
	// and written onto every Tool Attempt this process creates, so a later process can
	// tell that nobody is executing a STARTED Attempt any more. The process entry point
	// passes the same value to the Reconciler; an empty value is replaced by a random one.
	RunnerID string
}

// defaultPendingCallbackTTL bounds an early callback that arrived before its Callback
// Binding committed, when no TTL was configured.
const defaultPendingCallbackTTL = 15 * time.Minute

// withDefaults fills in the collaborators that are optional at a call site, so that a
// service is usable in tests without wiring a queue or a notifier.
func (d Deps) withDefaults() Deps {
	if d.Queue == nil {
		d.Queue = NoopEnqueuer{}
	}
	if d.Notifier == nil {
		d.Notifier = NoopNotifier{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Clock == nil {
		d.Clock = domain.SystemClock{}
	}
	if d.Callback.PendingTTL <= 0 {
		d.Callback.PendingTTL = defaultPendingCallbackTTL
	}
	if d.RunnerID == "" {
		// Every new Tool Attempt carries a runner identity; an Attempt without one is
		// never treated as orphaned, so leaving it empty would silently opt out.
		d.RunnerID = uuid.NewString()
	}
	return d
}
