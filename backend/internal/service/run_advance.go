package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// AdvanceOutcome is Advance's result. Claimed is false whenever nothing was claimed in
// this call (terminal Run, no candidate, or lost a race) -- not an error.
type AdvanceOutcome struct {
	Claimed   bool
	RunID     string
	NodeRunID string
	AttemptID string
	NodeType  string
	AttemptNo int
	Input     registry.NodeInput
	Config    map[string]any
	Deadline  *time.Time
	// AgentRunID and AgentTurnID are set instead of AttemptID when the claimed NodeRun is
	// a MANAGED_AGENT one: it has no Node Attempt, and the work Execute must drive after
	// COMMIT is the READY Turn this claim created.
	AgentRunID  string
	AgentTurnID string
}

// nodeStartedPayload is NODE_STARTED. It has two variants: an
// ordinary node names the Attempt it started, an Agent node names the Agent Run the same
// transaction created, because an Agent NodeRun has no Attempt at all.
type nodeStartedPayload struct {
	AttemptNo  int         `json:"attemptNo,omitempty"`
	AgentRunID *string     `json:"agentRunId,omitempty"`
	Input      dataSummary `json:"input"`
}

// Advance is the single claim path shared by the worker pool and the Reconciler: it
// picks at most one NodeRun of runID to start a new Attempt for, in one transaction, and
// returns without ever calling an Executor.
//
// RUNNING-with-due-retry vs. the single execution slot (task-mandated design decision):
// runtime.SelectNextToExecute refuses to select anything at all while `existing`
// contains any RUNNING NodeRun, because that RUNNING NodeRun IS the Run's one occupied
// execution slot. A RUNNING NodeRun whose backoff has elapsed is exactly that occupied
// slot becoming free again, not a second slot to fill -- so Advance must claim it
// directly through NodeRunRepository.ClaimRetry *before* ever calling
// SelectNextToExecute, rather than passing it through the READY-selection path (which
// would see it in `existing` and spuriously refuse). If a RUNNING NodeRun exists but its
// backoff has not yet elapsed, or it is genuinely mid-Attempt (no next_attempt_at at
// all), the slot is simply occupied and Advance is a no-op: no other NodeRun may be
// selected, matching SelectNextToExecute's own refusal rule for the normal path.
func (s *ExecutionService) Advance(ctx context.Context, runID string) (AdvanceOutcome, error) {
	var outcome AdvanceOutcome
	var lastSeq int64
	// notifyRunID is set whenever this call committed Events, which is not the same as
	// "claimed something executable": an Agent NodeRun whose frozen Model or Tool
	// allowlist no longer resolves is failed inside the claim transaction itself and
	// returns an unclaimed outcome, yet NODE_FAILED and RUN_FAILED have committed and an
	// SSE cursor must still wake for them.
	var notifyRunID string

	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		run, err := tx.Runs().Get(ctx, runID)
		if err != nil {
			return err
		}
		if run.Status.IsTerminal() {
			return nil
		}

		def, err := tx.Definitions().GetVersion(ctx, run.WorkflowID, run.DefinitionVersion)
		if err != nil {
			return err
		}

		// A previously-valid, frozen Definition version can stop recompiling if the
		// Registry has since dropped a Node Type or Model ID it depends on -- the same
		// registry-drift condition CreateRun reports as RegistryResolutionError. Unlike
		// CreateRun, there is no "refuse to create" escape hatch here: the Run already
		// exists, and when a Node, Model, Tool or Provider implementation is missing while
		// recovering an existing Run, the affected NodeRun must fail explicitly, not
		// the whole Advance call to error out forever. So a CompileError here is not
		// returned immediately: it is only fatal if this call actually needs the compiled
		// plan (the SelectNextToExecute path below) to make progress. The runningDueRetry
		// branch immediately below never dereferences plan, so a drifted Node Type on that
		// NodeRun is already handled the ordinary way (claim it; Execute's existing "Node
		// Type not registered" check then routes to FailNode).
		plan, planErr := s.compile(ctx, def)
		var driftErr *runtime.CompileError
		if planErr != nil {
			ce, ok := runtime.AsCompileError(planErr)
			if !ok {
				return fmt.Errorf("execution: recompile definition %s v%d for advance: %w", run.WorkflowID, run.DefinitionVersion, planErr)
			}
			driftErr = ce
		}

		// Run aggregate serialization: the NodeRun read that decides what to claim must
		// happen *after* taking the Run aggregate lock, not before, so that two
		// concurrent Advance calls for the same Run cannot both compute their claim
		// decision from the same pre-lock snapshot and both believe a slot is free.
		lock, err := tx.Runs().LockForUpdate(ctx, runID)
		if err != nil {
			return err
		}
		if lock.Status().IsTerminal() {
			return nil
		}

		nodeRuns, err := tx.NodeRuns().ListByRun(ctx, runID)
		if err != nil {
			return err
		}

		existing := make(map[string]runtime.NodeRunState, len(nodeRuns))
		byNodeID := make(map[string]domain.NodeRun, len(nodeRuns))
		var readyIDs []string
		var runningDueRetry *domain.NodeRun
		runningBlocking := false

		now := s.deps.Clock.Now()
		for i := range nodeRuns {
			nr := nodeRuns[i]
			existing[nr.NodeID] = runtime.NodeRunState{NodeID: nr.NodeID, Status: nr.Status}
			byNodeID[nr.NodeID] = nr
			switch nr.Status {
			case domain.NodeRunReady:
				readyIDs = append(readyIDs, nr.NodeID)
			case domain.NodeRunRunning:
				if nr.NextAttemptAt != nil && !nr.NextAttemptAt.After(now) {
					runningDueRetry = &nodeRuns[i]
				} else {
					runningBlocking = true
				}
			case domain.NodeRunWaitingCallback, domain.NodeRunSucceeded, domain.NodeRunFailed:
				// These NodeRuns neither offer a claim nor block one in this scan.
			}
		}

		if runningBlocking {
			return nil
		}

		if runningDueRetry != nil {
			won, err := tx.NodeRuns().ClaimRetry(ctx, runningDueRetry.ID, now)
			if err != nil {
				return err
			}
			if !won {
				return nil
			}
			if err := s.startAttempt(ctx, tx, lock, run, def, *runningDueRetry, now, &outcome); err != nil {
				return err
			}
			lastSeq = lock.LastSeq()
			notifyRunID = run.ID
			return nil
		}

		if driftErr != nil {
			// plan is unavailable, so SelectNextToExecute's own topological tie-break
			// cannot run. The accepted scenarios' Definitions are single linear chains
			// (no fan-out), so there is at most one READY candidate in practice; claim it
			// through the ordinary READY->RUNNING path and let Execute's existing
			// machinery discover and report the concrete cause (an unregistered Node
			// Type, an unregistered Model ID inside an otherwise-valid Node Type, or any
			// other registered-catalog failure) through FailNode, instead of guessing
			// which candidate the CompileError is "about" here. A CompileError can only
			// reach this branch for a Run that already exists: CreateRun ran this same
			// deterministic Compiler against this same frozen Definition and it passed,
			// so any failure now is registry drift by construction -- only the
			// Registry/catalog, not the Definition, can have changed since then.
			if len(readyIDs) == 0 {
				return nil
			}
			nr := byNodeID[readyIDs[0]]
			won, err := tx.NodeRuns().ClaimReady(ctx, nr.ID, now)
			if err != nil {
				return err
			}
			if !won {
				return nil
			}
			if err := s.startAttempt(ctx, tx, lock, run, def, nr, now, &outcome); err != nil {
				return err
			}
			lastSeq = lock.LastSeq()
			notifyRunID = run.ID
			return nil
		}

		nodeID, ok := runtime.SelectNextToExecute(plan, existing, readyIDs)
		if !ok {
			return nil
		}
		nr := byNodeID[nodeID]
		won, err := tx.NodeRuns().ClaimReady(ctx, nr.ID, now)
		if err != nil {
			return err
		}
		if !won {
			return nil
		}
		if err := s.startAttempt(ctx, tx, lock, run, def, nr, now, &outcome); err != nil {
			return err
		}
		lastSeq = lock.LastSeq()
		notifyRunID = run.ID
		return nil
	})
	if err != nil {
		return outcome, err
	}
	if notifyRunID != "" {
		// Notify-only, not postCommit's enqueue+notify pair: Advance's own callers
		// (internal/work.Pool's drain loop and reconciler.drive) already loop calling
		// Advance again immediately after every successful claim, so enqueuing here would
		// let the same Run re-enter the Queue while something is already actively driving
		// it -- harmless (claims are conditional) but wasteful, and blurs the Queue's role
		// as a latency optimization into a second execution path. An SSE
		// cursor still needs to wake for NODE_STARTED without waiting for the next Event,
		// which is exactly what Notifier.EventsCommitted alone provides.
		s.notifyCommitted(notifyRunID, lastSeq)
	}
	return outcome, nil
}

// resolveInputPorts resolves nodeID's input ports from its upstream NodeRuns' recorded
// SUCCEEDED output, using the frozen edge list. Design decision: NodeRun.Output is
// stored as json.Marshal(NodeOutput.Ports) (see startAttempt/CompleteNode), so decoding
// it back into a port map and reading edge.SourceHandle is the inverse of that write.
func resolveInputPorts(def domain.Definition, byNodeID map[string]domain.NodeRun, nodeID string) (map[string]json.RawMessage, error) {
	ports := map[string]json.RawMessage{}
	for _, edge := range def.Edges {
		if edge.Target != nodeID {
			continue
		}
		upstream, ok := byNodeID[edge.Source]
		if !ok || upstream.Status != domain.NodeRunSucceeded {
			return nil, fmt.Errorf("execution: upstream node %q for %q is not SUCCEEDED", edge.Source, nodeID)
		}
		var outputPorts map[string]json.RawMessage
		if len(upstream.Output) > 0 {
			if err := json.Unmarshal(upstream.Output, &outputPorts); err != nil {
				return nil, fmt.Errorf("execution: decode output of upstream node %q: %w", edge.Source, err)
			}
		}
		value, ok := outputPorts[edge.SourceHandle]
		if !ok {
			value = json.RawMessage(`null`)
		}
		ports[edge.TargetHandle] = value
	}
	return ports, nil
}

// startAttempt performs the claimed-NodeRun half of Advance: resolve input, increment
// attempt count, create the STARTED Attempt, append NODE_STARTED, and persist the seq
// watermark. The caller has already won the READY->RUNNING or retry claim and holds the
// Run lock.
func (s *ExecutionService) startAttempt(ctx context.Context, tx store.Tx, lock *store.RunLock, run domain.Run, def domain.Definition, nr domain.NodeRun, now time.Time, out *AdvanceOutcome) error {
	node, ok := nodeByID(def, nr.NodeID)
	if !ok {
		return fmt.Errorf("execution: definition %s v%d no longer declares node %q", def.WorkflowID, def.Version, nr.NodeID)
	}

	allNodeRuns, err := tx.NodeRuns().ListByRun(ctx, run.ID)
	if err != nil {
		return err
	}
	byNodeID := make(map[string]domain.NodeRun, len(allNodeRuns))
	for _, other := range allNodeRuns {
		byNodeID[other.NodeID] = other
	}

	ports, err := resolveInputPorts(def, byNodeID, nr.NodeID)
	if err != nil {
		return err
	}
	inputBytes, err := json.Marshal(ports)
	if err != nil {
		return fmt.Errorf("execution: encode resolved input for node %q: %w", nr.NodeID, err)
	}
	if err := tx.NodeRuns().SetInput(ctx, nr.ID, inputBytes); err != nil {
		return err
	}

	config, err := decodeConfig(node.Config)
	if err != nil {
		return err
	}

	// A MANAGED_AGENT NodeRun is expanded into an Agent Run instead of an Attempt, in this
	// same transaction. The Attempt counter is therefore incremented only below, on the
	// EXECUTOR path: an Agent NodeRun must keep attempt_count at 0, because it never has
	// an Attempt for that count to describe.
	reg, registered := s.deps.Nodes.Get(nr.NodeType)
	if registered {
		if _, isAgent := reg.Binding.(registry.ManagedAgentBinding); isAgent {
			return s.startAgentRun(ctx, tx, lock, run, nr, node, ports, inputBytes, config, now, out)
		}
	}

	attemptNo, err := tx.NodeRuns().IncrementAttemptCount(ctx, nr.ID)
	if err != nil {
		return err
	}

	policy := effectivePolicy(node)
	deadline := runtime.AttemptDeadline(now, policy)
	var deadlinePtr *time.Time
	if !deadline.IsZero() {
		d := deadline
		deadlinePtr = &d
	}

	attemptID := s.deps.IDs.NewID(domain.IDPrefixNodeAttempt)

	// An ASYNC Node is dispatched to a Provider that answers on the callback endpoint, so
	// the Attempt needs its credential before the Provider is ever called. Only
	// sha256(token) is persisted; the plaintext leaves this function inside
	// registry.CallbackContext and nowhere else (CLAUDE.md "Persistence and transactions").
	// An EXTERNAL+KEYED Node additionally gets a stable idempotency key so a retry of an
	// uncertain external call is allowed at all; the NodeRun id is stable across Attempts
	// of the same NodeRun, which is exactly the deduplication scope a Provider needs.
	var callbackCtx *registry.CallbackContext
	var tokenHash *string
	sideEffect, execKind := s.nodeRuntimeMetadata(nr.NodeType)
	if execKind == domain.NodeExecutionAsync {
		// The credential outlives the Attempt deadline by the Pending Callback TTL. Without
		// that grace window a callback arriving while the timeout transaction is still in
		// flight would be refused on its credential instead of competing for the completion
		// right through the conditional update; after the timeout commits it
		// is simply a stale delivery that writes nothing. An Attempt with no deadline gets a
		// token with no expiry (zero time): there is nothing to derive one from.
		var expiresAt time.Time
		if !deadline.IsZero() {
			expiresAt = deadline.Add(s.deps.Callback.PendingTTL)
		}
		token, err := issueCallbackToken(s.deps.Callback.SigningSecret, attemptID, expiresAt)
		if err != nil {
			return fmt.Errorf("execution: issue callback credential for node %q: %w", nr.NodeID, err)
		}
		hash := hashCallbackToken(token)
		tokenHash = &hash
		callbackCtx = &registry.CallbackContext{
			URL:   s.deps.Callback.BaseURL + CallbackPath,
			Token: token,
		}
	}
	var idempotencyKey string
	if hasIdempotencyKey(sideEffect) {
		idempotencyKey = nr.ID
	}

	if err := tx.NodeAttempts().Create(ctx, domain.NodeAttempt{
		ID:                attemptID,
		NodeRunID:         nr.ID,
		AttemptNo:         attemptNo,
		Status:            domain.NodeAttemptStarted,
		Input:             inputBytes,
		CallbackTokenHash: tokenHash,
		StartedAt:         now,
		DeadlineAt:        deadlinePtr,
	}); err != nil {
		return err
	}

	if err := s.appendEvent(ctx, tx, lock, run.ID, &nr.ID, domain.EventNodeStarted, now, nodeStartedPayload{
		AttemptNo: attemptNo,
		Input:     summarize(inputBytes),
	}); err != nil {
		return err
	}

	// Starting a claimed NodeRun (READY->RUNNING, or a retry becoming RUNNING again)
	// never changes the Run's own aggregate status while the Run is non-terminal (both
	// READY and RUNNING map to RunRunning); UpdateAggregate still runs to persist the
	// seq watermark NODE_STARTED allocated.
	if err := tx.Runs().UpdateAggregate(ctx, lock, domain.RunRunning, now); err != nil {
		return err
	}

	*out = AdvanceOutcome{
		Claimed:   true,
		RunID:     run.ID,
		NodeRunID: nr.ID,
		AttemptID: attemptID,
		NodeType:  nr.NodeType,
		AttemptNo: attemptNo,
		Input: registry.NodeInput{
			RunID:          run.ID,
			NodeRunID:      nr.ID,
			AttemptNo:      attemptNo,
			Ports:          ports,
			RunInput:       run.Input,
			Callback:       callbackCtx,
			IdempotencyKey: idempotencyKey,
		},
		Config:   config,
		Deadline: deadlinePtr,
	}
	return nil
}

// nodeRuntimeMetadata reports the registered SideEffectPolicy and ExecutionKind of a Node
// Type. Runtime behaviour comes from registered metadata, never from inspecting the
// executor's Go type (CLAUDE.md "Extensions and external calls"). An unregistered type
// yields zero values; the paths that care fail explicitly on their own.
func (s *ExecutionService) nodeRuntimeMetadata(nodeType string) (domain.SideEffectPolicy, domain.NodeExecutionKind) {
	reg, ok := s.deps.Nodes.Get(nodeType)
	if !ok {
		return domain.SideEffectPolicy{}, ""
	}
	return reg.Metadata.SideEffect, reg.Metadata.ExecutionKind
}
