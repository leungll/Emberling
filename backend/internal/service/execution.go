package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// ExecutionService turns the pure decisions in runtime and the conditional updates in
// store into a durable, synchronous workflow execution engine. Every exported method
// either runs exactly one transaction (CreateRun, Advance, CompleteNode, FailNode) or
// composes those with a single post-COMMIT step (Execute, TimeoutAttempt). Nothing here
// spawns a goroutine: internal/work and internal/reconciler own that.
type ExecutionService struct {
	deps  Deps
	plans *planCache
}

// NewExecutionService builds an ExecutionService over deps. Deps.Queue and Deps.Notifier
// default to no-ops (Deps.withDefaults), so a Reconciler-only deployment or a test with
// no worker pool remains valid.
func NewExecutionService(deps Deps) *ExecutionService {
	return &ExecutionService{deps: deps.withDefaults(), plans: newPlanCache(planCacheCapacity)}
}

// compile resolves the compiled plan for one immutable Definition version, through the
// bounded plan cache. It is the single place CreateRun/Advance/CompleteNode/FailNode
// recompile a Definition, both to obtain Order/Upstream/Downstream/OutputNodeID and, in
// CreateRun specifically, to prove the current Registry can still resolve every Node
// Type and Model ID the frozen Definition references.
func (s *ExecutionService) compile(ctx context.Context, def domain.Definition) (*runtime.CompiledDefinition, error) {
	if plan, ok := s.plans.get(def.WorkflowID, def.Version); ok {
		return plan, nil
	}
	plan, err := s.deps.Compiler.Compile(ctx, def)
	if err != nil {
		return nil, err
	}
	s.plans.put(def.WorkflowID, def.Version, plan)
	return plan, nil
}

// RegistryResolutionError is returned by CreateRun when recompiling an already-frozen,
// already-validated Definition version fails against the *current* Registry (a Node Type
// or Model ID that resolved at Save time has since been deregistered). Design decision:
// this is not a Definition-authoring error (semantically invalid Definition, HTTP 422)
// since the stored Definition was valid and immutable; it is a
// runtime/system-state conflict between an immutable fact and the current Registry, so
// the HTTP layer should map it to 409, not 422.
type RegistryResolutionError struct {
	WorkflowID string
	Version    int
	Underlying *runtime.CompileError
}

func (e *RegistryResolutionError) Error() string {
	return fmt.Sprintf("execution: workflow=%s version=%d: registry can no longer resolve this frozen definition: %s",
		e.WorkflowID, e.Version, e.Underlying.Error())
}

func (e *RegistryResolutionError) Unwrap() error { return e.Underlying }

// dataSummary is the bounded Event payload shape used for every input/output value.
// Event payloads must carry a "summary, hash, or stable
// reference," not exact encoding; design decision: SHA-256 plus byte length is used
// uniformly (never the raw bytes), since Node input/output can carry user document text
// that must stay out of the Event log while remaining a stable, comparable fact.
type dataSummary struct {
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

func summarize(raw json.RawMessage) dataSummary {
	sum := sha256.Sum256(raw)
	return dataSummary{SHA256: hex.EncodeToString(sum[:]), Bytes: len(raw)}
}

func nodeByID(def domain.Definition, id string) (domain.Node, bool) {
	for _, n := range def.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return domain.Node{}, false
}

// effectivePolicy returns the node's ExecutionPolicy, or its zero value when the node
// declares none. domain.Node.ExecutionPolicy is a pointer specifically because a node
// may opt out of configuring one (confirmed by the shared document_processing fixture,
// which sets it on none of its four nodes). Design decision: the zero value is reused
// rather than inventing a default, because it already produces the right MVP behaviour
// through two existing rules: runtime.AttemptDeadline returns the zero time.Time (no
// deadline) when TimeoutMs <= 0, and runtime.DecideRetry refuses to retry a node whose
// MaxAttempts is 0, since AttemptNo (always >= 1) is immediately >= MaxAttempts. A node
// with no configured ExecutionPolicy therefore runs with no deadline and never retries a
// failed Attempt.
func effectivePolicy(node domain.Node) domain.ExecutionPolicy {
	if node.ExecutionPolicy == nil {
		return domain.ExecutionPolicy{}
	}
	return *node.ExecutionPolicy
}

func decodeConfig(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("execution: decode node config: %w", err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func (s *ExecutionService) newEventID() string { return s.deps.IDs.NewID(domain.IDPrefixEvent) }

// appendEvent marshals payload and appends the Event under lock, allocating seq
// (store.EventRepository.Append reads it from the lock, never from caller state).
func (s *ExecutionService) appendEvent(ctx context.Context, tx store.Tx, lock *store.RunLock, runID string, nodeRunID *string, typ domain.EventType, now time.Time, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("execution: encode %s payload: %w", typ, err)
	}
	_, err = tx.Events().Append(ctx, lock, domain.Event{
		ID:        s.newEventID(),
		RunID:     runID,
		NodeRunID: nodeRunID,
		Type:      typ,
		Timestamp: now,
		Payload:   encoded,
	})
	return err
}

type nodeReadyPayload struct {
	NodeID   string `json:"nodeId"`
	NodeType string `json:"nodeType"`
}

type runTransitionPayload struct {
	From  domain.RunStatus       `json:"from"`
	To    domain.RunStatus       `json:"to"`
	Error *domain.ExecutionError `json:"error,omitempty"`
}

// nodeOutcomeResult reports what a completion or failure transaction actually did.
// duplicate means another path already held the completion right: nothing was written and
// no Event seq was consumed.
type nodeOutcomeResult struct {
	runID     string
	nodeRunID string
	duplicate bool
}

// errResumeSuperseded rolls back a resume transaction whose completion right was taken by
// another path (a timeout, a competing callback, or a Pending Callback consumed
// elsewhere). It never escapes completeNode/failNode.
var errResumeSuperseded = errors.New("execution: resume superseded")

// PendingCallbackCredentialMismatchError reports that a stored early callback's
// authenticated credential belongs to a different Attempt than the one this resume is
// about to advance. An unmatched Pending Callback has no right to advance Execution and
// the callback token is scoped to one Attempt, so a row recorded under Attempt X's
// credential must never complete Attempt Y merely because a later dispatch reused the
// same external task id. The message carries neither hash: only the external task id and
// the Attempt it was refused for.
type PendingCallbackCredentialMismatchError struct {
	ExternalTaskID string
	AttemptID      string
}

func (e *PendingCallbackCredentialMismatchError) Error() string {
	return fmt.Sprintf("execution: pending callback for external task %s carries a credential that does not belong to attempt %s", e.ExternalTaskID, e.AttemptID)
}

// matchesAttemptCredential reports, in constant time, whether a Pending Callback's stored
// token hash equals the target Attempt's own callback credential hash. A nil Attempt hash
// (no callback token was ever issued to this Attempt) never matches.
func matchesAttemptCredential(attemptTokenHash *string, pendingTokenHash string) bool {
	if attemptTokenHash == nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(*attemptTokenHash), []byte(pendingTokenHash)) == 1
}

// consumePendingCallback consumes a stored early callback in the same transaction that
// acts on it, so a Pending Callback can advance a NodeRun at most once. An
// already-consumed, expired or different-payload row means this delivery is superseded and
// the whole transaction rolls back. A row whose stored credential does not match attempt's
// own callback token hash is also refused -- the consumption performed by ConsumeOnce above
// is rolled back along with everything else, since attempt is not this row's owner.
func (s *ExecutionService) consumePendingCallback(ctx context.Context, tx store.Tx, attemptID string, attemptTokenHash *string, externalTaskID, expectedPayloadHash string, now time.Time) error {
	if externalTaskID == "" {
		return nil
	}
	pending, consumed, err := tx.PendingCallbacks().ConsumeOnce(ctx, externalTaskID, now)
	if err != nil {
		return err
	}
	if !consumed {
		return errResumeSuperseded
	}
	if expectedPayloadHash != "" && pending.PayloadHash != expectedPayloadHash {
		return errResumeSuperseded
	}
	if !matchesAttemptCredential(attemptTokenHash, pending.CallbackTokenHash) {
		return &PendingCallbackCredentialMismatchError{ExternalTaskID: externalTaskID, AttemptID: attemptID}
	}
	return nil
}

// hasIdempotencyKey reports whether Attempts of a node with this SideEffectPolicy carry a
// Provider idempotency key. Only EXTERNAL+KEYED does: startAttempt fills
// NodeInput.IdempotencyKey for exactly those nodes, with the NodeRun id as the key.
func hasIdempotencyKey(side domain.SideEffectPolicy) bool {
	return side.Kind == domain.SideEffectExternal && side.Idempotency == domain.IdempotencyKeyed
}

// postCommit is a small helper so CreateRun/CompleteNode/FailNode share the same
// post-COMMIT enqueue+notify sequence when they need it outside the transaction
// closure itself (CreateRun uses it directly above; CompleteNode/FailNode call it from
// their own wrappers below).
func (s *ExecutionService) postCommit(runID string, lastSeq int64) {
	s.deps.Queue.EnqueueAdvance(runID)
	s.deps.Notifier.EventsCommitted(runID, lastSeq)
}

// notifyCommitted wakes an SSE cursor for runID without enqueueing a Queue item. Its
// callers are the paths whose own caller is already driving this Run: Advance (see the
// comment at its post-commit call site) and the Agent Turn transactions, which make no
// Node-level work available.
func (s *ExecutionService) notifyCommitted(runID string, lastSeq int64) {
	s.deps.Notifier.EventsCommitted(runID, lastSeq)
}
