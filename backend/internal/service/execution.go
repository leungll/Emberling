package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
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

// resolveAgentIdentifiers re-resolves every `agent` node's modelId and allowedTools
// against the *current* Model and Tool Registries, existence-only. Unlike
// service.DefinitionService.resolveAgentRegistrations (called at Validate/Save time), it
// never applies or re-freezes modelConfig defaults: a Run stays bound to the Definition
// version's already-frozen config (invariant #8), and defaults are resolved exactly once,
// at Save time, never again at Run creation or recovery.
func (s *ExecutionService) resolveAgentIdentifiers(def domain.Definition) []runtime.ValidationError {
	var errs []runtime.ValidationError
	for _, node := range def.Nodes {
		if node.Type != runtime.NodeTypeAgent {
			continue
		}
		cfg, err := runtime.ParseAgentNodeConfig(node.Config)
		if err != nil {
			// The frozen Definition already passed Semantics-stage validation at Save
			// time and cannot change; this is defensive only.
			continue
		}

		if _, _, ok := s.deps.Models.Get(cfg.ModelID); !ok {
			errs = append(errs, runtime.ValidationError{
				Code:    runtime.CodeModelNotFound,
				NodeID:  node.ID,
				Path:    "nodes[" + node.ID + "].config.modelId",
				Message: fmt.Sprintf("model %q is not registered", cfg.ModelID),
			})
		}
		for _, toolName := range cfg.AllowedTools {
			if _, ok := s.deps.Tools.Get(toolName); !ok {
				errs = append(errs, runtime.ValidationError{
					Code:    runtime.CodeToolNotFound,
					NodeID:  node.ID,
					Path:    "nodes[" + node.ID + "].config.allowedTools",
					Message: fmt.Sprintf("tool %q is not registered", toolName),
				})
			}
		}
	}
	return errs
}

// RunInputInvalidError is returned by CreateRun when Input fails validation against the
// version's frozen RunInputSchema. Nothing is persisted when this error is returned. It
// is a plain instance/schema validation failure (runtime.CodeValidationFailed), the same
// family docs/08-interface-spec.md §6 maps to HTTP 400 ("请求内容无效"), not 422.
type RunInputInvalidError struct {
	Errors []runtime.ValidationError
}

func (e *RunInputInvalidError) Error() string {
	return fmt.Sprintf("execution: run input invalid: %d error(s)", len(e.Errors))
}

// RegistryResolutionError is returned by CreateRun when recompiling an already-frozen,
// already-validated Definition version fails against the *current* Registry (a Node Type
// or Model ID that resolved at Save time has since been deregistered). Design decision:
// this is not a Definition-authoring error (docs/08-interface-spec.md §6 "Definition
// 语义无效", HTTP 422) since the stored Definition was valid and immutable; it is a
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
// docs/05-data-model.md §2.3 requires payloads to carry "summary, hash, or stable
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

// -----------------------------------------------------------------------------------
// CreateRun
// -----------------------------------------------------------------------------------

// CreateRun is the parameter struct for ExecutionService.CreateRun.
type CreateRun struct {
	WorkflowID        string
	DefinitionVersion int
	Input             json.RawMessage
}

// runCreatedPayload is the NODE_READY companion for RUN_CREATED
// (docs/05-data-model.md §2.3: RUN_CREATED{workflowId, definitionVersion, input 摘要}).
type runCreatedPayload struct {
	WorkflowID        string      `json:"workflowId"`
	DefinitionVersion int         `json:"definitionVersion"`
	Input             dataSummary `json:"input"`
}

type nodeReadyPayload struct {
	NodeID   string `json:"nodeId"`
	NodeType string `json:"nodeType"`
}

// CreateRun starts one new Run of one immutable Definition version, in one transaction.
// It never resolves "latest": the caller names the exact version (invariant #8, a Run
// stays bound to one immutable Definition version).
func (s *ExecutionService) CreateRun(ctx context.Context, req CreateRun) (domain.Run, error) {
	var created domain.Run

	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		def, err := tx.Definitions().GetVersion(ctx, req.WorkflowID, req.DefinitionVersion)
		if err != nil {
			return err
		}

		// Recompile to prove the current Registry still resolves every Node Type (and,
		// transitively through ValidateSemantics, every Model ID a SYNC/ASYNC Executor
		// itself resolves) the frozen Definition references. This never regenerates the
		// input contract; see the RunInputSchema use below.
		plan, err := s.compile(ctx, def)
		if err != nil {
			if ce, ok := runtime.AsCompileError(err); ok {
				return &RegistryResolutionError{WorkflowID: req.WorkflowID, Version: req.DefinitionVersion, Underlying: ce}
			}
			return fmt.Errorf("execution: recompile definition %s v%d: %w", req.WorkflowID, req.DefinitionVersion, err)
		}

		// A `agent` node's ManagedAgentBinding has no Executor, so the recompile above
		// never resolves its modelId or allowedTools (registry.NodeRegistry.ValidateSemantics
		// is a no-op for MANAGED_AGENT). Re-resolve them here, existence-only: no default is
		// re-injected and no modelConfig is re-validated, since those were already frozen
		// into the Definition at Save time and must not silently change under a running or
		// about-to-run Workflow (domain.ModelMetadata doc comment: "defaults are resolved
		// once before a Definition version is frozen and are never re-injected at Run or
		// recovery time").
		if errs := s.resolveAgentIdentifiers(def); len(errs) > 0 {
			return &RegistryResolutionError{
				WorkflowID: req.WorkflowID,
				Version:    req.DefinitionVersion,
				Underlying: &runtime.CompileError{Stage: runtime.StageSemantics, Errors: errs},
			}
		}

		// Design decision, task-mandated: validate against def.RunInputSchema (the
		// version's frozen, stored field), never plan.RunInputSchema freshly
		// recomputed by this recompilation (docs/06-execution-model.md §1.2: "创建 Run
		// 时...用版本中冻结的 runInputSchema 校验 input；它不能根据当前 Registry 生成另一份
		// 输入合同").
		if verrs := runtime.ValidateRunInput(def.RunInputSchema, req.Input); len(verrs) > 0 {
			return &RunInputInvalidError{Errors: verrs}
		}

		// The frozen runInputSchema fixes an AssetRef's shape but cannot know whether the
		// Asset it names exists or still describes the same content; only the committed
		// Metadata can (docs/08-interface-spec.md §3.3: "无效 `AssetRef`...不能创建 Run").
		// The lookup runs here, inside this transaction and before the first Run, NodeRun
		// or Event row is written, so a rejection leaves nothing behind and the Assets it
		// reads come from the same snapshot the Run would have been created in. It takes
		// no lock and calls nothing external: assets rows are immutable.
		if verrs, err := s.verifyRunInputAssets(ctx, tx, def, req.Input); err != nil {
			return err
		} else if len(verrs) > 0 {
			return &RunInputInvalidError{Errors: verrs}
		}

		now := s.deps.Clock.Now()
		runID := s.deps.IDs.NewID(domain.IDPrefixRun)
		// docs/06-execution-model.md §1.3 orders Run/NodeRun creation before the Run lock
		// is taken. Safe: neither row is visible to any other transaction until this one
		// commits (PostgreSQL MVCC), so nothing can race these inserts before the lock.
		run := domain.Run{
			ID:                runID,
			WorkflowID:        req.WorkflowID,
			DefinitionVersion: req.DefinitionVersion,
			Status:            domain.RunRunning,
			Input:             req.Input,
			StartedAt:         now,
			UpdatedAt:         now,
		}
		if err := tx.Runs().Create(ctx, run); err != nil {
			return err
		}

		readyIDs := runtime.InitialReady(plan)
		nodeRuns := make([]domain.NodeRun, 0, len(readyIDs))
		for _, nodeID := range readyIDs {
			node, ok := nodeByID(def, nodeID)
			if !ok {
				return fmt.Errorf("execution: compiled plan references unknown node %q", nodeID)
			}
			nr := domain.NodeRun{
				ID:        s.deps.IDs.NewID(domain.IDPrefixNodeRun),
				RunID:     runID,
				NodeID:    nodeID,
				NodeType:  node.Type,
				Status:    domain.NodeRunReady,
				Input:     json.RawMessage(`{}`),
				ReadyAt:   now,
				UpdatedAt: now,
			}
			if err := tx.NodeRuns().Create(ctx, nr); err != nil {
				return err
			}
			nodeRuns = append(nodeRuns, nr)
		}

		lock, err := tx.Runs().LockForUpdate(ctx, runID)
		if err != nil {
			return err
		}

		if err := s.appendEvent(ctx, tx, lock, runID, nil, domain.EventRunCreated, now, runCreatedPayload{
			WorkflowID:        req.WorkflowID,
			DefinitionVersion: req.DefinitionVersion,
			Input:             summarize(req.Input),
		}); err != nil {
			return err
		}
		for _, nr := range nodeRuns {
			if err := s.appendEvent(ctx, tx, lock, runID, &nr.ID, domain.EventNodeReady, now, nodeReadyPayload{
				NodeID: nr.NodeID, NodeType: nr.NodeType,
			}); err != nil {
				return err
			}
		}

		// Run creation reaches RUNNING directly (no RUN_STARTED event exists:
		// docs/05-data-model.md §2 "RUN_CREATED 与...对应 NODE_READY 在创建事务中提交...
		// 因此不定义 RUN_STARTED"); UpdateAggregate still runs to persist the seq
		// watermark this transaction allocated.
		if err := tx.Runs().UpdateAggregate(ctx, lock, domain.RunRunning, now); err != nil {
			return err
		}

		run.LastSeq = lock.LastSeq()
		created = run
		return nil
	})
	if err != nil {
		return domain.Run{}, err
	}

	s.postCommit(created.ID, created.LastSeq)
	return created, nil
}

// verifyRunInputAssets checks every AssetRef this Run input supplies to an Image Input
// against the committed Asset Metadata. It returns validation errors for references that
// name no Asset or disagree with one, and a plain error only when the lookup itself
// failed.
//
// Errors and validation messages name the Image Input node and the asset id alone: the
// storage key never leaves the Store boundary (10-ops §4), and it is not needed to
// explain the rejection.
func (s *ExecutionService) verifyRunInputAssets(ctx context.Context, tx store.Tx, def domain.Definition, input json.RawMessage) ([]runtime.ValidationError, error) {
	refs := runtime.RunInputAssetRefs(def, input)
	if len(refs) == 0 {
		return nil, nil
	}

	var verrs []runtime.ValidationError
	for _, ref := range refs {
		record, err := tx.Assets().Get(ctx, ref.Ref.AssetID)
		if errors.Is(err, domain.ErrNotFound) {
			verrs = append(verrs, ref.AssetRejection("does not name an uploaded asset"))
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("execution: verify run input asset: node=%s asset=%s: %w", ref.NodeID, ref.Ref.AssetID, err)
		}
		if !ref.MatchesAsset(record.Asset) {
			verrs = append(verrs, ref.AssetRejection("does not match the uploaded asset's metadata"))
		}
	}
	return verrs, nil
}

// -----------------------------------------------------------------------------------
// Advance
// -----------------------------------------------------------------------------------

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
	// COMMIT is the READY Turn this claim created (docs/06-execution-model.md §1.7).
	AgentRunID  string
	AgentTurnID string
}

// nodeStartedPayload is NODE_STARTED (docs/05-data-model.md §2.3). It has two variants: an
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
		// exists, and docs/09-testing-and-acceptance.md §3.4 "恢复已有 Run 时 Node、Model、
		// Tool 或 Provider 实现缺失" requires the affected NodeRun to fail explicitly, not
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

		// Docs/05 §3.3 "Run 聚合串行化": the NodeRun read that decides what to claim must
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
			// cannot run. M1's only Definitions are single linear chains (no fan-out), so
			// there is at most one READY candidate in practice; claim it through the
			// ordinary READY->RUNNING path and let Execute's existing machinery discover
			// and report the concrete cause (an unregistered Node Type, an unregistered
			// Model ID inside an otherwise-valid Node Type, or any other registered-catalog
			// failure) through FailNode, instead of guessing which candidate the
			// CompileError is "about" here. A CompileError can only reach this branch for a
			// Run that already exists: CreateRun ran this same deterministic Compiler
			// against this same frozen Definition and it passed, so any failure now is
			// registry drift by construction -- only the Registry/catalog, not the
			// Definition, can have changed since then.
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
		// as "a latency optimization" (invariant #6) into a second execution path. An SSE
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
		// right through the conditional update (invariant #7); after the timeout commits it
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

// -----------------------------------------------------------------------------------
// Execute
// -----------------------------------------------------------------------------------

// Execute runs the claimed Node Attempt described by outcome. It must be called only
// after the transaction that produced outcome has committed (invariant #4): it resolves
// the Executor from the Node Registry, applies the deadline (if any) to ctx, calls
// Execute, and reports the result through CompleteNode or FailNode -- each of which is
// its own, separate transaction.
func (s *ExecutionService) Execute(ctx context.Context, outcome AdvanceOutcome) error {
	if !outcome.Claimed {
		return fmt.Errorf("execution: Execute called with an unclaimed AdvanceOutcome")
	}

	// A MANAGED_AGENT claim carries no Attempt and no Executor: the work its transaction
	// authorised is the READY Turn it created. claimSource is IMMEDIATE because this is
	// the path that just made the claim; the Reconciler enters the very same use case
	// with RECONCILER.
	if outcome.AgentTurnID != "" {
		return s.AdvanceAgentTurn(ctx, outcome.AgentTurnID, domain.ClaimImmediate)
	}

	reg, ok := s.deps.Nodes.Get(outcome.NodeType)
	if !ok {
		return s.FailNode(ctx, FailNode{
			AttemptID: outcome.AttemptID,
			Error:     domain.ExecutionError{Code: "NODE_TYPE_NOT_REGISTERED", Message: fmt.Sprintf("node type %q is not registered", outcome.NodeType)},
			Uncertain: false,
			Source:    domain.FailureSyncExecution,
		})
	}
	binding, ok := reg.Binding.(registry.ExecutorBinding)
	if !ok {
		// Every other binding kind is driven by its own use case and was routed above;
		// reaching here means a claimed NodeRun whose binding nothing can execute.
		return s.FailNode(ctx, FailNode{
			AttemptID: outcome.AttemptID,
			Error:     domain.ExecutionError{Code: "NODE_BINDING_NOT_EXECUTABLE", Message: fmt.Sprintf("node type %q is not an EXECUTOR binding", outcome.NodeType)},
			Uncertain: false,
			Source:    domain.FailureSyncExecution,
		})
	}

	execCtx := ctx
	if outcome.Deadline != nil {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithDeadline(ctx, *outcome.Deadline)
		defer cancel()
	}

	result, err := binding.Executor.Execute(execCtx, outcome.Input, outcome.Config)
	if err != nil {
		return s.FailNode(ctx, FailNode{
			AttemptID: outcome.AttemptID,
			Error:     domain.ExecutionError{Code: "EXECUTOR_ERROR", Message: err.Error()},
			Uncertain: isUncertainFailure(execCtx, err),
			Source:    domain.FailureSyncExecution,
		})
	}

	switch result.Kind {
	case registry.NodeResultCompleted:
		if result.Output == nil {
			return s.FailNode(ctx, FailNode{
				AttemptID: outcome.AttemptID,
				Error:     domain.ExecutionError{Code: "EXECUTOR_ERROR", Message: "executor reported COMPLETED with no Output"},
				Uncertain: false,
				Source:    domain.FailureSyncExecution,
			})
		}
		return s.CompleteNode(ctx, CompleteNode{
			AttemptID:  outcome.AttemptID,
			Output:     *result.Output,
			TokenUsage: result.TokenUsage,
		})
	case registry.NodeResultDispatched:
		return s.dispatchNode(ctx, outcome, result.ExternalTask)
	default:
		return s.FailNode(ctx, FailNode{
			AttemptID: outcome.AttemptID,
			Error:     domain.ExecutionError{Code: "UNKNOWN_RESULT_KIND", Message: string(result.Kind)},
			Uncertain: false,
			Source:    domain.FailureSyncExecution,
		})
	}
}

// isUncertainFailure classifies a failed Execute call, which decides whether the
// SideEffectPolicy may allow a retry at all. A context deadline is uncertain (the Executor
// was cut off mid-flight; whether the external side effect landed is unknown). Beyond
// that, an Adapter that knows whether its dispatch reached the Provider says so through an
// error exposing Uncertain() bool -- a connection reset before any response is uncertain, a
// rejected request is definite. Emberling reads that classification instead of guessing
// from the Go error type, and treats every unclassified error as definite.
func isUncertainFailure(ctx context.Context, err error) bool {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return true
	}
	var classified interface{ Uncertain() bool }
	if errors.As(err, &classified) {
		return classified.Uncertain()
	}
	return false
}

// -----------------------------------------------------------------------------------
// Dispatch (async)
// -----------------------------------------------------------------------------------

// nodeDispatchedPayload is the bounded record of a committed dispatch
// (docs/05-data-model.md §2.3). providerId and externalTaskId are deliberately absent:
// they are projected from the Callback Binding and are not copied into Events
// (docs/08-interface-spec.md line 414).
type nodeDispatchedPayload struct {
	AttemptNo         int    `json:"attemptNo"`
	CallbackBindingID string `json:"callbackBindingId"`
}

// errDispatchBindingConflict rolls the dispatch transaction back when the external task id
// the Provider returned is already bound to another Attempt.
var errDispatchBindingConflict = errors.New("execution: callback binding conflict")

// dispatchNode commits the second phase of an async Node's three-phase dispatch (06 §1.6):
// the Attempt becomes DISPATCHED, the NodeRun WAITING_CALLBACK, the Callback Binding that
// routes future callbacks is created and NODE_DISPATCHED is appended -- all in one
// transaction, so a callback can never find a route to a NodeRun that is not waiting yet.
// The Provider call itself already happened, outside any transaction and any Run lock.
func (s *ExecutionService) dispatchNode(ctx context.Context, outcome AdvanceOutcome, task *registry.ExternalTask) error {
	if task == nil || task.ExternalTaskID == "" || task.ProviderID == "" {
		// The Provider may have accepted the task even though nothing identifies it, so
		// the result of the external call is unknown and the registered SideEffectPolicy
		// -- not this code path -- decides whether another Attempt is allowed
		// (06 §3 "Provider 已接受但 external_task_id 未保存").
		return s.FailNode(ctx, FailNode{
			AttemptID: outcome.AttemptID,
			Error: domain.ExecutionError{
				Code:    "DISPATCH_WITHOUT_EXTERNAL_TASK",
				Message: "executor reported DISPATCHED without a provider id and external task id",
			},
			Uncertain: true,
			Source:    domain.FailureSyncExecution,
		})
	}

	var (
		runID       string
		lastSeq     int64
		bindingID   string
		lostRace    bool
		committedAt time.Time
	)

	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		attempt, err := tx.NodeAttempts().Get(ctx, outcome.AttemptID)
		if err != nil {
			return err
		}
		nodeRun, err := tx.NodeRuns().Get(ctx, attempt.NodeRunID)
		if err != nil {
			return err
		}
		run, err := tx.Runs().Get(ctx, nodeRun.RunID)
		if err != nil {
			return err
		}
		def, err := tx.Definitions().GetVersion(ctx, run.WorkflowID, run.DefinitionVersion)
		if err != nil {
			return err
		}
		plan, err := s.compile(ctx, def)
		if err != nil {
			return fmt.Errorf("execution: recompile definition %s v%d for dispatch: %w", run.WorkflowID, run.DefinitionVersion, err)
		}

		lock, err := tx.Runs().LockForUpdate(ctx, run.ID)
		if err != nil {
			return err
		}
		now := s.deps.Clock.Now()
		committedAt = now

		if err := tx.NodeAttempts().MarkDispatched(ctx, attempt.ID, now); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				// The Attempt is no longer STARTED: a timeout already expired it while the
				// Provider call was in flight. Nothing is written; the external task is
				// left to the Provider (Emberling promises no cancellation).
				lostRace = true
				return nil
			}
			return err
		}
		if err := tx.NodeRuns().MarkWaiting(ctx, nodeRun.ID, now); err != nil {
			return err
		}

		bindingID = s.deps.IDs.NewID(domain.IDPrefixCallbackBinding)
		if err := tx.CallbackBindings().Create(ctx, domain.CallbackBinding{
			ID:             bindingID,
			ProviderID:     task.ProviderID,
			ExternalTaskID: task.ExternalTaskID,
			TargetType:     domain.CallbackTargetNodeAttempt,
			TargetID:       attempt.ID,
			CreatedAt:      now,
		}); err != nil {
			if errors.Is(err, domain.ErrConflict) {
				return errDispatchBindingConflict
			}
			return err
		}

		if err := s.appendEvent(ctx, tx, lock, run.ID, &nodeRun.ID, domain.EventNodeDispatched, now, nodeDispatchedPayload{
			AttemptNo:         attempt.AttemptNo,
			CallbackBindingID: bindingID,
		}); err != nil {
			return err
		}

		allNodeRuns, err := tx.NodeRuns().ListByRun(ctx, run.ID)
		if err != nil {
			return err
		}
		nodeStatuses := make(map[string]domain.NodeRunStatus, len(allNodeRuns))
		for _, nr := range allNodeRuns {
			nodeStatuses[nr.NodeID] = nr.Status
		}
		newStatus := runtime.AggregateRunStatus(runtime.RunAggregateInput{
			NodeStatuses:   nodeStatuses,
			AllNodeIDs:     plan.Order,
			OutputNodeID:   plan.OutputNodeID,
			OutputProduced: len(run.Output) > 0,
		})
		if ev, changed := runtime.NextRunTransitionEvent(lock.Status(), newStatus); changed {
			if err := s.appendEvent(ctx, tx, lock, run.ID, nil, ev.Type, now, runTransitionPayload{From: lock.Status(), To: newStatus}); err != nil {
				return err
			}
		}
		if err := tx.Runs().UpdateAggregate(ctx, lock, newStatus, now); err != nil {
			return err
		}
		runID = run.ID
		lastSeq = lock.LastSeq()
		return nil
	})
	if errors.Is(err, errDispatchBindingConflict) {
		// The external task id is already bound to another Attempt, so this dispatch has no
		// route home and the transaction above is rolled back (the Attempt is still
		// STARTED). This is a definite failure of this Attempt: re-dispatching would
		// produce the same conflict. Documented edge; no automatic recovery is invented.
		return s.FailNode(ctx, FailNode{
			AttemptID: outcome.AttemptID,
			Error: domain.ExecutionError{
				Code:    "CALLBACK_BINDING_CONFLICT",
				Message: fmt.Sprintf("external task id %q is already bound to another Attempt", task.ExternalTaskID),
			},
			Uncertain: false,
			Source:    domain.FailureSyncExecution,
		})
	}
	if err != nil {
		return err
	}
	if lostRace || runID == "" {
		return nil
	}

	s.postCommit(runID, lastSeq)

	// Only now that the Binding is committed can a callback that arrived first be routed
	// (06 §1.6 step 3). The stored credential hash decides whether that early delivery
	// really belongs to this Attempt.
	s.consumeEarlyCallback(ctx, task.ExternalTaskID, outcome.AttemptID, committedAt)
	return nil
}

// consumeEarlyCallback resumes this Attempt from a Pending Callback recorded before its
// Binding committed. Failures are logged rather than returned: the dispatch itself is
// committed, and the Reconciler rediscovers the same persisted pending row (invariant #6).
func (s *ExecutionService) consumeEarlyCallback(ctx context.Context, externalTaskID, attemptID string, now time.Time) {
	var pending domain.PendingCallback
	var found bool

	if err := s.deps.UoW.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		row, err := tx.PendingCallbacks().GetByExternalTaskID(ctx, externalTaskID)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return nil
			}
			return err
		}
		if row.ConsumedAt != nil || !now.Before(row.ExpiresAt) {
			return nil
		}
		pending, found = row, true
		return nil
	}); err != nil {
		s.deps.Logger.Warn("read pending callback after dispatch",
			slog.String("external_task_id", externalTaskID),
			slog.String("attempt_id", attemptID),
			slog.String("error", err.Error()))
		return
	}
	if !found {
		return
	}

	// Whether this stored delivery's credential actually belongs to attemptID is decided
	// inside the resume transaction below (consumePendingCallback), which is also where a
	// competing consumer could win the row first. Checking it again here would only be a
	// second, racy opinion.
	if _, err := s.ResumeNode(ctx, ResumeNode{
		ExternalTaskID: externalTaskID,
		Payload:        pending.Payload,
		Source:         domain.CompletionCallback,
		ConsumePending: true,
		PayloadHash:    pending.PayloadHash,
	}); err != nil {
		var rejected *CallbackPayloadRejectedError
		if errors.As(err, &rejected) {
			// The Executor could not interpret the stored body; the NodeRun stays
			// WAITING_CALLBACK, exactly as for a live callback.
			return
		}
		var mismatch *PendingCallbackCredentialMismatchError
		if errors.As(err, &mismatch) {
			// The stored delivery was authenticated with a credential that is not this
			// Attempt's. It is left untouched (never logged with its hash) for its own owner
			// or for expiry to clean up.
			s.deps.Logger.Warn("pending callback credential does not match the dispatched attempt",
				slog.String("external_task_id", externalTaskID),
				slog.String("attempt_id", attemptID))
			return
		}
		s.deps.Logger.Warn("resume from pending callback after dispatch",
			slog.String("external_task_id", externalTaskID),
			slog.String("attempt_id", attemptID),
			slog.String("error", err.Error()))
	}
}

// -----------------------------------------------------------------------------------
// CompleteNode
// -----------------------------------------------------------------------------------

// CompleteNode is the parameter struct for ExecutionService.CompleteNode.
type CompleteNode struct {
	AttemptID  string
	Output     registry.NodeOutput
	TokenUsage *domain.TokenUsage
}

type nodeCompletedPayload struct {
	Output           dataSummary             `json:"output"`
	LatencyMs        int64                   `json:"latencyMs"`
	TokenUsage       *domain.TokenUsage      `json:"tokenUsage,omitempty"`
	CompletionSource domain.CompletionSource `json:"completionSource"`
}

type runTransitionPayload struct {
	From  domain.RunStatus       `json:"from"`
	To    domain.RunStatus       `json:"to"`
	Error *domain.ExecutionError `json:"error,omitempty"`
}

// nodeCallbackReceivedPayload is the bounded record of an accepted callback
// (docs/05-data-model.md §2.3). It never carries the body, the token or the token hash:
// payloadHash identifies the delivery, and providerId/externalTaskId stay projected from
// the Callback Binding (docs/08-interface-spec.md line 414).
type nodeCallbackReceivedPayload struct {
	AttemptNo         int       `json:"attemptNo"`
	CallbackBindingID string    `json:"callbackBindingId"`
	ReceivedAt        time.Time `json:"receivedAt"`
	PayloadHash       string    `json:"payloadHash"`
}

// completeNodeParams is the internal, parameterised form of a successful completion. The
// synchronous path and the async resume path differ only in which state each transition
// starts from, which completion source the Event records and whether a Pending Callback is
// consumed -- everything downstream (READY successors, Run output, aggregation) is shared.
type completeNodeParams struct {
	attemptID        string
	output           registry.NodeOutput
	tokenUsage       *domain.TokenUsage
	fromAttempt      domain.NodeAttemptStatus
	fromNodeRun      domain.NodeRunStatus
	completionSource domain.CompletionSource
	// callbackBindingID is set only for a resume; it is what NODE_CALLBACK_RECEIVED and
	// the callback metadata refer to.
	callbackBindingID string
	// consumePending, when set, is the external task id of the stored early callback this
	// transaction must consume exactly once.
	consumePending string
	payloadHash    string
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

// CompleteNode records a successful synchronous Node execution result in one transaction:
// it moves the Attempt and NodeRun to SUCCEEDED (each conditionally -- a stale completion
// is silently ignored, never an error), writes Run.output if this was the Output Node,
// creates newly-READY NodeRuns, and re-aggregates the Run status.
func (s *ExecutionService) CompleteNode(ctx context.Context, req CompleteNode) error {
	_, err := s.completeNode(ctx, completeNodeParams{
		attemptID:        req.AttemptID,
		output:           req.Output,
		tokenUsage:       req.TokenUsage,
		fromAttempt:      domain.NodeAttemptStarted,
		fromNodeRun:      domain.NodeRunRunning,
		completionSource: domain.CompletionSyncExecution,
	})
	return err
}

func (s *ExecutionService) completeNode(ctx context.Context, p completeNodeParams) (nodeOutcomeResult, error) {
	var result nodeOutcomeResult
	var runID string
	var lastSeq int64

	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		attempt, err := tx.NodeAttempts().Get(ctx, p.attemptID)
		if err != nil {
			return err
		}
		nodeRun, err := tx.NodeRuns().Get(ctx, attempt.NodeRunID)
		if err != nil {
			return err
		}
		run, err := tx.Runs().Get(ctx, nodeRun.RunID)
		if err != nil {
			return err
		}
		def, err := tx.Definitions().GetVersion(ctx, run.WorkflowID, run.DefinitionVersion)
		if err != nil {
			return err
		}
		plan, err := s.compile(ctx, def)
		if err != nil {
			return fmt.Errorf("execution: recompile definition %s v%d for complete: %w", run.WorkflowID, run.DefinitionVersion, err)
		}

		lock, err := tx.Runs().LockForUpdate(ctx, run.ID)
		if err != nil {
			return err
		}

		now := s.deps.Clock.Now()
		outputBytes, err := json.Marshal(p.output.Ports)
		if err != nil {
			return fmt.Errorf("execution: encode node output: %w", err)
		}

		if err := s.consumePendingCallback(ctx, tx, attempt.ID, attempt.CallbackTokenHash, p.consumePending, p.payloadHash, now); err != nil {
			return err
		}

		if err := tx.NodeAttempts().MarkSucceeded(ctx, attempt.ID, p.fromAttempt, now, outputBytes); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				// A duplicate or late completion of an Attempt that has already been
				// resolved by another path (e.g. TimeoutAttempt won the race first).
				// Ignored, not an error: the transaction commits with no further effect.
				result.duplicate = true
				return nil
			}
			return err
		}

		var latencyMs int64
		if nodeRun.StartedAt != nil {
			latencyMs = now.Sub(*nodeRun.StartedAt).Milliseconds()
		}

		// For a synchronous completion, if NodeAttempts().MarkSucceeded above did not
		// report a stale claim, the NodeRun must still be RUNNING too: a NodeRun's single
		// execution slot and this same Run lock serialize every path (Advance,
		// CompleteNode, FailNode, TimeoutAttempt) that could otherwise move it. A stale
		// claim there would mean that invariant broke, so it is a hard error (rolling back
		// the Attempt success too) rather than a second silent no-op.
		//
		// A resume is different: WAITING_CALLBACK->SUCCEEDED is exactly the conditional
		// UPDATE that elects the single winner among callback, Provider Poll and timeout
		// (06 §1.6, invariant #7), so losing it means another path already completed this
		// NodeRun. The whole transaction rolls back: no Event, no seq consumed.
		if err := tx.NodeRuns().MarkSucceeded(ctx, nodeRun.ID, p.fromNodeRun, now, store.NodeRunOutcome{
			Output:     outputBytes,
			TokenUsage: p.tokenUsage,
			LatencyMs:  &latencyMs,
		}); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) && p.fromNodeRun == domain.NodeRunWaitingCallback {
				return errResumeSuperseded
			}
			return err
		}

		// 06 §1.6: only the callback that wins the completion right writes
		// NODE_CALLBACK_RECEIVED. A Provider Poll completion must not write it.
		if p.completionSource == domain.CompletionCallback && p.callbackBindingID != "" {
			if err := s.appendEvent(ctx, tx, lock, run.ID, &nodeRun.ID, domain.EventNodeCallbackReceived, now, nodeCallbackReceivedPayload{
				AttemptNo:         attempt.AttemptNo,
				CallbackBindingID: p.callbackBindingID,
				ReceivedAt:        now,
				PayloadHash:       p.payloadHash,
			}); err != nil {
				return err
			}
		}

		if err := s.appendEvent(ctx, tx, lock, run.ID, &nodeRun.ID, domain.EventNodeCompleted, now, nodeCompletedPayload{
			Output:           summarize(outputBytes),
			LatencyMs:        latencyMs,
			TokenUsage:       p.tokenUsage,
			CompletionSource: p.completionSource,
		}); err != nil {
			return err
		}

		if err := s.advanceAfterNodeSuccess(ctx, tx, lock, run, nodeRun, def, plan, outputBytes, now); err != nil {
			return err
		}
		runID = run.ID
		result.runID = run.ID
		result.nodeRunID = nodeRun.ID
		lastSeq = lock.LastSeq()
		return nil
	})
	if errors.Is(err, errResumeSuperseded) {
		return nodeOutcomeResult{duplicate: true}, nil
	}
	if err != nil {
		return nodeOutcomeResult{}, err
	}
	if runID != "" {
		s.postCommit(runID, lastSeq)
	}
	return result, nil
}

// advanceAfterNodeSuccess is the shared tail of every transaction that succeeds one
// NodeRun: the Run output when this was the output node, the downstream NodeRuns this
// success makes READY together with their NODE_READY Events, and the re-aggregated Run
// status with its transition Event.
//
// It is shared rather than duplicated because Run aggregation and downstream scheduling
// are one decision (runtime.AggregateRunStatus, runtime.NextReady) reached from two
// completion shapes: a normal Node Attempt (completeNode) and a MANAGED_AGENT NodeRun,
// which has no Attempt and closes out inside the Final completion transaction. The caller
// holds the Run aggregate lock and has already written the NodeRun's own SUCCEEDED status
// and its NODE_COMPLETED Event.
func (s *ExecutionService) advanceAfterNodeSuccess(
	ctx context.Context,
	tx store.Tx,
	lock *store.RunLock,
	run domain.Run,
	nodeRun domain.NodeRun,
	def domain.Definition,
	plan *runtime.CompiledDefinition,
	outputBytes json.RawMessage,
	now time.Time,
) error {
	outputProduced := len(run.Output) > 0
	if nodeRun.NodeID == plan.OutputNodeID {
		if err := tx.Runs().SetOutput(ctx, lock, outputBytes); err != nil {
			return err
		}
		outputProduced = true
	}

	allNodeRuns, err := tx.NodeRuns().ListByRun(ctx, run.ID)
	if err != nil {
		return err
	}
	existing := make(map[string]runtime.NodeRunState, len(allNodeRuns))
	nodeStatuses := make(map[string]domain.NodeRunStatus, len(allNodeRuns))
	for _, nr := range allNodeRuns {
		existing[nr.NodeID] = runtime.NodeRunState{NodeID: nr.NodeID, Status: nr.Status}
		nodeStatuses[nr.NodeID] = nr.Status
	}

	newReadyIDs := runtime.NextReady(plan, existing)
	for _, nodeID := range newReadyIDs {
		node, ok := nodeByID(def, nodeID)
		if !ok {
			return fmt.Errorf("execution: compiled plan references unknown node %q", nodeID)
		}
		newNR := domain.NodeRun{
			ID:        s.deps.IDs.NewID(domain.IDPrefixNodeRun),
			RunID:     run.ID,
			NodeID:    nodeID,
			NodeType:  node.Type,
			Status:    domain.NodeRunReady,
			Input:     json.RawMessage(`{}`),
			ReadyAt:   now,
			UpdatedAt: now,
		}
		if err := tx.NodeRuns().Create(ctx, newNR); err != nil {
			return err
		}
		nodeStatuses[nodeID] = domain.NodeRunReady
		if err := s.appendEvent(ctx, tx, lock, run.ID, &newNR.ID, domain.EventNodeReady, now, nodeReadyPayload{
			NodeID: newNR.NodeID, NodeType: newNR.NodeType,
		}); err != nil {
			return err
		}
	}

	newStatus := runtime.AggregateRunStatus(runtime.RunAggregateInput{
		NodeStatuses:   nodeStatuses,
		AllNodeIDs:     plan.Order,
		OutputNodeID:   plan.OutputNodeID,
		OutputProduced: outputProduced,
	})
	if ev, changed := runtime.NextRunTransitionEvent(lock.Status(), newStatus); changed {
		if err := s.appendEvent(ctx, tx, lock, run.ID, nil, ev.Type, now, runTransitionPayload{From: lock.Status(), To: newStatus}); err != nil {
			return err
		}
	}
	return tx.Runs().UpdateAggregate(ctx, lock, newStatus, now)
}

// PendingCallbackCredentialMismatchError reports that a stored early callback's
// authenticated credential belongs to a different Attempt than the one this resume is
// about to advance. docs/06-execution-model.md §3 gives an unmatched Pending Callback no
// right to advance Execution and docs/05-data-model.md §1.7 scopes the token to one
// Attempt, so a row recorded under Attempt X's credential must never complete Attempt Y
// merely because a later dispatch reused the same external task id. The message carries
// neither hash: only the external task id and the Attempt it was refused for.
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
// acts on it, so a Pending Callback can advance a NodeRun at most once (05 §1.7). An
// already-consumed, expired or different-payload row means this delivery is superseded and
// the whole transaction rolls back. A row whose stored credential does not match attempt's
// own callback token hash is also refused -- the consumption performed by ConsumeOnce above
// is rolled back along with everything else, since attempt is not this row's owner (06 §3).
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

// -----------------------------------------------------------------------------------
// FailNode
// -----------------------------------------------------------------------------------

// FailNode is the parameter struct for ExecutionService.FailNode.
type FailNode struct {
	AttemptID string
	Error     domain.ExecutionError
	Uncertain bool
	Source    domain.FailureSource
}

type nodeRetryingPayload struct {
	AttemptNo     int                   `json:"attemptNo"`
	Error         domain.ExecutionError `json:"error"`
	NextAttemptAt time.Time             `json:"nextAttemptAt"`
}

// nodeFailedPayload is NODE_FAILED (docs/05-data-model.md §2.3), with the same two
// variants as nodeStartedPayload: attemptNo for an ordinary node, agentRunId for an Agent
// node (absent as well when the Agent NodeRun failed before its Agent Run was created).
type nodeFailedPayload struct {
	AttemptNo  int                   `json:"attemptNo,omitempty"`
	AgentRunID *string               `json:"agentRunId,omitempty"`
	Error      domain.ExecutionError `json:"error"`
}

// FailNode records a failed Node execution result in one transaction. It applies
// runtime.DecideRetry using the node's ExecutionPolicy and its registered
// SideEffectPolicy: a retryable failure schedules the next Attempt and keeps the NodeRun
// RUNNING (NODE_RETRYING); otherwise the NodeRun fails, the Run records the error, and
// the Run re-aggregates (to RUN_FAILED, since any FAILED NodeRun dominates). It never
// sleeps: NextAttemptAt is a persisted fact the Reconciler and Advance both consult, not
// something this call waits on.
func (s *ExecutionService) FailNode(ctx context.Context, req FailNode) error {
	_, err := s.failNode(ctx, failNodeParams{
		attemptID:   req.AttemptID,
		execError:   req.Error,
		uncertain:   req.Uncertain,
		source:      req.Source,
		fromAttempt: domain.NodeAttemptStarted,
		fromNodeRun: domain.NodeRunRunning,
	})
	return err
}

// failNodeParams is the internal, parameterised form of a failure. A synchronous failure
// resolves a STARTED Attempt of a RUNNING NodeRun; a Provider failure delivered by
// callback/poll and a DISPATCHED-Attempt timeout resolve a DISPATCHED Attempt of a
// WAITING_CALLBACK NodeRun. The retry decision, Event writing and aggregation are shared.
type failNodeParams struct {
	attemptID   string
	execError   domain.ExecutionError
	uncertain   bool
	source      domain.FailureSource
	fromAttempt domain.NodeAttemptStatus
	fromNodeRun domain.NodeRunStatus
	// consumePending mirrors completeNodeParams: a failure delivered through a stored
	// early callback consumes that row in the same transaction.
	consumePending string
	payloadHash    string
	// terminal skips the retry decision entirely. It is set for a Provider-reported
	// failure of a task that is already waiting: 06 §2.2 marks Attempt and NodeRun FAILED
	// and states that MVP never re-dispatches an external task that reached
	// WAITING_CALLBACK, even when the ExecutionPolicy still has attempts left and the
	// SideEffectPolicy would allow a keyed retry.
	terminal bool
}

func (s *ExecutionService) failNode(ctx context.Context, p failNodeParams) (nodeOutcomeResult, error) {
	var result nodeOutcomeResult
	var runID string
	var lastSeq int64

	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		attempt, err := tx.NodeAttempts().Get(ctx, p.attemptID)
		if err != nil {
			return err
		}
		nodeRun, err := tx.NodeRuns().Get(ctx, attempt.NodeRunID)
		if err != nil {
			return err
		}
		run, err := tx.Runs().Get(ctx, nodeRun.RunID)
		if err != nil {
			return err
		}
		def, err := tx.Definitions().GetVersion(ctx, run.WorkflowID, run.DefinitionVersion)
		if err != nil {
			return err
		}

		// Recompiling a previously-valid, frozen Definition can fail here for the same
		// registry-drift reason documented on Advance above: the very Node Type this
		// Attempt is failing for may be the one the Registry just dropped (Execute's "Node
		// Type not registered" -> FailNode call is exactly that case). Aggregation below
		// only needs plan.Order/OutputNodeID on the branch where no NodeRun has failed --
		// impossible here, since this call is about to mark one FAILED -- so a
		// registry-drift CompileError does not abort the failure itself; it only means
		// plan stays nil and the aggregation call further down passes zero values instead.
		plan, planErr := s.compile(ctx, def)
		if planErr != nil {
			if _, ok := runtime.AsCompileError(planErr); !ok {
				return fmt.Errorf("execution: recompile definition %s v%d for fail: %w", run.WorkflowID, run.DefinitionVersion, planErr)
			}
			plan = nil
		}

		lock, err := tx.Runs().LockForUpdate(ctx, run.ID)
		if err != nil {
			return err
		}

		now := s.deps.Clock.Now()

		if err := s.consumePendingCallback(ctx, tx, attempt.ID, attempt.CallbackTokenHash, p.consumePending, p.payloadHash, now); err != nil {
			return err
		}

		if err := tx.NodeAttempts().MarkFailed(ctx, attempt.ID, p.fromAttempt, now, p.execError); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) {
				// Late/duplicate failure of an Attempt already resolved elsewhere
				// (e.g. it already succeeded, or a competing callback won). Ignored, not
				// an error: 09 §3.2 requires the loser to write nothing.
				result.duplicate = true
				return nil
			}
			return err
		}

		node, ok := nodeByID(def, nodeRun.NodeID)
		if !ok {
			return fmt.Errorf("execution: definition %s v%d no longer declares node %q", def.WorkflowID, def.Version, nodeRun.NodeID)
		}
		reg, regOK := s.deps.Nodes.Get(nodeRun.NodeType)
		var sideEffect domain.SideEffectPolicy
		if regOK {
			sideEffect = reg.Metadata.SideEffect
		}

		decision := runtime.DecideRetry(runtime.RetryInput{
			Policy:     effectivePolicy(node),
			SideEffect: sideEffect,
			AttemptNo:  attempt.AttemptNo,
			// Only an EXTERNAL+KEYED node (image_generation in the MVP) carries a Provider
			// idempotency key: the NodeRun id, which startAttempt puts into NodeInput and
			// which is stable across every Attempt of the same NodeRun, so a retried
			// dispatch presents the exact same key to the Provider. Reporting it here is
			// what lets runtime.DecideRetry allow a retry of a keyed external call at all
			// (06 §1.1 / SideEffectPolicy); EXTERNAL+UNKNOWN still refuses.
			HasIdempotencyKey: hasIdempotencyKey(sideEffect),
			ResultUncertain:   p.uncertain,
		})
		if !regOK {
			// The Node Type is no longer registered at all (docs/09-testing-and-acceptance.md
			// §3.4 "Registry 只存在相似但不兼容的实现" / "恢复已有 Run 时...实现缺失"): there is
			// no SideEffectPolicy left to consult, and CLAUDE.md "Extensions and external
			// calls" forbids substituting a heuristic in its place. Fail outright regardless
			// of what DecideRetry computed off the zero-value SideEffectPolicy above.
			decision.Retry = false
		}
		if p.terminal || p.fromNodeRun == domain.NodeRunWaitingCallback {
			// 06 §2.2 "MVP 不自动重新派发已经进入 WAITING_CALLBACK 的外部任务", and the NodeRun
			// machine in 06 §1.1 leaves a waiting NodeRun only for SUCCEEDED or FAILED. The
			// rule is operative rather than conservative: the external task was already
			// accepted under this NodeRun's Provider idempotency key and external task id, so
			// a re-dispatch could not be routed anyway -- an EXTERNAL+KEYED retry would
			// collide on the existing Callback Binding, and an EXTERNAL+UNKNOWN one must not
			// be repeated at all. Whatever the ExecutionPolicy and the SideEffectPolicy allow
			// for a failure before dispatch, a failure after it is terminal.
			decision.Retry = false
		}

		if decision.Retry {
			// A retry always runs as a new Attempt of a NodeRun that is still RUNNING: only
			// a failure before dispatch can retry, and that NodeRun never left RUNNING.
			nextAttemptAt := now.Add(decision.NextDelay)
			if err := tx.NodeRuns().ScheduleRetry(ctx, nodeRun.ID, nextAttemptAt, now); err != nil {
				return err
			}
			if err := s.appendEvent(ctx, tx, lock, run.ID, &nodeRun.ID, domain.EventNodeRetrying, now, nodeRetryingPayload{
				AttemptNo: attempt.AttemptNo, Error: p.execError, NextAttemptAt: nextAttemptAt,
			}); err != nil {
				return err
			}
			// The NodeRun is RUNNING again; a Run that had been PAUSED by this NodeRun
			// resumes, which NextRunTransitionEvent turns into RUN_RESUMED. For a
			// synchronous retry the Run was already RUNNING and nothing is written.
			if ev, changed := runtime.NextRunTransitionEvent(lock.Status(), domain.RunRunning); changed {
				if err := s.appendEvent(ctx, tx, lock, run.ID, nil, ev.Type, now, runTransitionPayload{From: lock.Status(), To: domain.RunRunning}); err != nil {
					return err
				}
			}
			if err := tx.Runs().UpdateAggregate(ctx, lock, domain.RunRunning, now); err != nil {
				return err
			}
			runID = run.ID
			result.runID = run.ID
			result.nodeRunID = nodeRun.ID
			lastSeq = lock.LastSeq()
			return nil
		}

		if err := tx.NodeRuns().MarkFailed(ctx, nodeRun.ID, p.fromNodeRun, now, p.execError); err != nil {
			if errors.Is(err, domain.ErrStaleClaim) && p.fromNodeRun == domain.NodeRunWaitingCallback {
				return errResumeSuperseded
			}
			return err
		}
		if err := s.appendEvent(ctx, tx, lock, run.ID, &nodeRun.ID, domain.EventNodeFailed, now, nodeFailedPayload{
			AttemptNo: attempt.AttemptNo, Error: p.execError,
		}); err != nil {
			return err
		}
		if err := tx.Runs().SetError(ctx, lock, p.execError); err != nil {
			return err
		}

		allNodeRuns, err := tx.NodeRuns().ListByRun(ctx, run.ID)
		if err != nil {
			return err
		}
		nodeStatuses := make(map[string]domain.NodeRunStatus, len(allNodeRuns))
		for _, nr := range allNodeRuns {
			nodeStatuses[nr.NodeID] = nr.Status
		}
		// plan is nil exactly when recompilation hit registry drift (above): nodeStatuses
		// already records the NodeRun this call just marked FAILED, so
		// AggregateRunStatus's hasFailed short-circuit always yields RunFailed here
		// regardless of AllNodeIDs/OutputNodeID -- passing their zero values is safe.
		var allNodeIDs []string
		var outputNodeID string
		if plan != nil {
			allNodeIDs = plan.Order
			outputNodeID = plan.OutputNodeID
		}
		newStatus := runtime.AggregateRunStatus(runtime.RunAggregateInput{
			NodeStatuses:   nodeStatuses,
			AllNodeIDs:     allNodeIDs,
			OutputNodeID:   outputNodeID,
			OutputProduced: len(run.Output) > 0,
		})
		if ev, changed := runtime.NextRunTransitionEvent(lock.Status(), newStatus); changed {
			errCopy := p.execError
			if err := s.appendEvent(ctx, tx, lock, run.ID, nil, ev.Type, now, runTransitionPayload{From: lock.Status(), To: newStatus, Error: &errCopy}); err != nil {
				return err
			}
		}
		if err := tx.Runs().UpdateAggregate(ctx, lock, newStatus, now); err != nil {
			return err
		}
		runID = run.ID
		result.runID = run.ID
		result.nodeRunID = nodeRun.ID
		lastSeq = lock.LastSeq()
		return nil
	})
	if errors.Is(err, errResumeSuperseded) {
		return nodeOutcomeResult{duplicate: true}, nil
	}
	if err != nil {
		return nodeOutcomeResult{}, err
	}
	if runID != "" {
		s.postCommit(runID, lastSeq)
	}
	return result, nil
}

// hasIdempotencyKey reports whether Attempts of a node with this SideEffectPolicy carry a
// Provider idempotency key. Only EXTERNAL+KEYED does: startAttempt fills
// NodeInput.IdempotencyKey for exactly those nodes, with the NodeRun id as the key.
func hasIdempotencyKey(side domain.SideEffectPolicy) bool {
	return side.Kind == domain.SideEffectExternal && side.Idempotency == domain.IdempotencyKeyed
}

// -----------------------------------------------------------------------------------
// TimeoutAttempt
// -----------------------------------------------------------------------------------

// TimeoutAttempt expires one Attempt if it is still STARTED and its deadline has
// passed; otherwise it is a no-op (already resolved, or not yet due -- a late
// completion after a timeout already fired must not change terminal state, which is
// exactly the stale-claim guard FailNode's own Attempt transition enforces). It performs
// a read-only guard transaction to decide whether to act, then -- only if warranted --
// delegates to FailNode's own transaction with code TIMEOUT, Source TIMEOUT, and
// Uncertain=true exactly when the node's registered SideEffectPolicy is EXTERNAL (an
// EXTERNAL call cut off by a deadline has an unproven remote result; a NONE-side-effect
// node has nothing to be uncertain about).
func (s *ExecutionService) TimeoutAttempt(ctx context.Context, attemptID string) error {
	var expired bool
	var uncertain bool
	fromAttempt := domain.NodeAttemptStarted
	fromNodeRun := domain.NodeRunRunning

	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		attempt, err := tx.NodeAttempts().Get(ctx, attemptID)
		if err != nil {
			return err
		}
		switch attempt.Status {
		case domain.NodeAttemptStarted:
			fromAttempt, fromNodeRun = domain.NodeAttemptStarted, domain.NodeRunRunning
		case domain.NodeAttemptDispatched:
			// 06 §1.1 "等待超时": a dispatched Attempt whose deadline passed competes with
			// the callback and with Provider Poll for the same completion right. The
			// conditional transitions in failNode elect the single winner.
			fromAttempt, fromNodeRun = domain.NodeAttemptDispatched, domain.NodeRunWaitingCallback
		default:
			return nil
		}
		if attempt.DeadlineAt == nil {
			return nil
		}
		now := s.deps.Clock.Now()
		if now.Before(*attempt.DeadlineAt) {
			return nil
		}

		nodeRun, err := tx.NodeRuns().Get(ctx, attempt.NodeRunID)
		if err != nil {
			return err
		}
		reg, ok := s.deps.Nodes.Get(nodeRun.NodeType)
		if !ok {
			return fmt.Errorf("execution: node type %q is not registered", nodeRun.NodeType)
		}

		expired = true
		uncertain = reg.Metadata.SideEffect.Kind == domain.SideEffectExternal
		return nil
	})
	if err != nil {
		return err
	}
	if !expired {
		return nil
	}

	_, err = s.failNode(ctx, failNodeParams{
		attemptID:   attemptID,
		execError:   domain.ExecutionError{Code: "TIMEOUT", Message: "attempt deadline exceeded"},
		uncertain:   uncertain,
		source:      domain.FailureTimeout,
		fromAttempt: fromAttempt,
		fromNodeRun: fromNodeRun,
		// A timeout of an Attempt that already reached the Provider resolves the NodeRun:
		// 06 §2.2 forbids re-dispatching an external task that entered WAITING_CALLBACK.
		terminal: fromNodeRun == domain.NodeRunWaitingCallback,
	})
	return err
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
