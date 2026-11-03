package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// resolveAgentIdentifiers re-resolves every `agent` node's modelId, allowedTools and
// imageModelId against the *current* Model and Tool Registries, together with the
// capabilities both model selections need. Unlike
// service.DefinitionService.resolveAgentRegistrations (called at Validate/Save time), it
// never applies or re-freezes modelConfig defaults: a Run stays bound to the Definition
// version's already-frozen config, and defaults are resolved exactly once,
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
		errs = append(errs, agentModelSelectionErrors(s.deps.Models, s.deps.Tools, node.ID, cfg)...)
		errs = append(errs, agentAuthorizationConfigErrors(s.deps.Tools, node.ID, cfg)...)
	}
	return errs
}

// RunInputInvalidError is returned by CreateRun when Input fails validation against the
// version's frozen RunInputSchema. Nothing is persisted when this error is returned. It
// is a plain instance/schema validation failure (runtime.CodeValidationFailed), the same
// family the interface contract maps to HTTP 400 (invalid request content), not 422.
type RunInputInvalidError struct {
	Errors []runtime.ValidationError
}

func (e *RunInputInvalidError) Error() string {
	return fmt.Sprintf("execution: run input invalid: %d error(s)", len(e.Errors))
}

// CreateRun is the parameter struct for ExecutionService.CreateRun and SubmitRun.
type CreateRun struct {
	WorkflowID        string
	DefinitionVersion int
	Input             json.RawMessage
	// IdempotencyKey is the optional client submission key. Empty means none: every such
	// submission creates a new Run. A non-empty key is scoped to WorkflowID and binds to
	// the digest of DefinitionVersion and Input; its format is checked at the transport
	// boundary.
	IdempotencyKey string
}

// CreateRunResult is the outcome of SubmitRun. Replayed is true when the submission's key
// already named a Run with the same content: Run is then that Run as currently stored, and
// nothing was written, appended or enqueued.
type CreateRunResult struct {
	Run      domain.Run
	Replayed bool
}

// IdempotencyKeyMismatchError is returned by SubmitRun when the submission key already
// names a Run of the same Workflow created from different content (another Definition
// version or another input). Nothing is persisted. The key value is client input and is
// not carried in the message.
type IdempotencyKeyMismatchError struct {
	WorkflowID string
	RunID      string
}

func (e *IdempotencyKeyMismatchError) Error() string {
	return fmt.Sprintf("execution: idempotency key already used for different content: workflow=%s run=%s", e.WorkflowID, e.RunID)
}

// runCreatedPayload is the NODE_READY companion for RUN_CREATED
// (Event fields: RUN_CREATED{workflowId, definitionVersion, input summary}).
type runCreatedPayload struct {
	WorkflowID        string      `json:"workflowId"`
	DefinitionVersion int         `json:"definitionVersion"`
	Input             dataSummary `json:"input"`
}

// CreateRun starts one new Run of one immutable Definition version, in one transaction.
// It never resolves "latest": the caller names the exact version: a Run
// stays bound to one immutable Definition version. It is SubmitRun without the replay
// flag.
func (s *ExecutionService) CreateRun(ctx context.Context, req CreateRun) (domain.Run, error) {
	result, err := s.SubmitRun(ctx, req)
	return result.Run, err
}

// SubmitRun creates a Run as CreateRun describes, honouring an optional submission key.
//
// With a key, the creation transaction first reads the Workflow's Run holding that key.
// When one exists its digest decides the answer: equal content replays it, other content
// fails with *IdempotencyKeyMismatchError. When none exists the Run is inserted with the
// key and digest, together with RUN_CREATED and the first NODE_READY Events. If a
// concurrent submission inserts the same key first, the unique index rejects this insert
// and the whole transaction rolls back, Events included; the winner is then read in a
// fresh transaction and compared the same way. Only a created Run is offered to the work
// queue after COMMIT; a replay writes, appends and enqueues nothing.
func (s *ExecutionService) SubmitRun(ctx context.Context, req CreateRun) (CreateRunResult, error) {
	var digest string
	if req.IdempotencyKey != "" {
		d, err := runtime.RunSubmissionDigest(req.DefinitionVersion, req.Input)
		if err != nil {
			return CreateRunResult{}, &RunInputInvalidError{Errors: []runtime.ValidationError{{
				Code: runtime.CodeValidationFailed, Path: "input", Message: "input is not a single valid JSON document",
			}}}
		}
		digest = d
	}

	result, err := s.createRunTx(ctx, req, digest)
	if errors.Is(err, domain.ErrRunIdempotencyKeyInUse) {
		// Lost the insert race on this key: this transaction already rolled back, so the
		// winner's committed Run decides the answer.
		return s.replayKeyedRun(ctx, req, digest)
	}
	if err != nil {
		return CreateRunResult{}, err
	}
	if !result.Replayed {
		s.postCommit(result.Run.ID, result.Run.LastSeq)
	}
	return result, nil
}

// replayKeyedRun reads the Run holding req's key and answers with it when its digest
// matches. It writes nothing.
func (s *ExecutionService) replayKeyedRun(ctx context.Context, req CreateRun, digest string) (CreateRunResult, error) {
	var result CreateRunResult
	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		existing, err := tx.Runs().GetByIdempotencyKey(ctx, req.WorkflowID, req.IdempotencyKey)
		if err != nil {
			return fmt.Errorf("execution: read run by idempotency key: workflow=%s: %w", req.WorkflowID, err)
		}
		result, err = keyedReplay(existing, digest)
		return err
	})
	if err != nil {
		return CreateRunResult{}, err
	}
	return result, nil
}

// keyedReplay compares the stored Run's digest with this submission's.
func keyedReplay(existing domain.Run, digest string) (CreateRunResult, error) {
	if existing.IdempotencyDigest != digest {
		return CreateRunResult{}, &IdempotencyKeyMismatchError{WorkflowID: existing.WorkflowID, RunID: existing.ID}
	}
	return CreateRunResult{Run: existing, Replayed: true}, nil
}

// createRunTx is the creation transaction. With a key it answers from the Run already
// holding it, if any, before validating or writing anything.
func (s *ExecutionService) createRunTx(ctx context.Context, req CreateRun, digest string) (CreateRunResult, error) {
	var result CreateRunResult

	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if req.IdempotencyKey != "" {
			existing, err := tx.Runs().GetByIdempotencyKey(ctx, req.WorkflowID, req.IdempotencyKey)
			switch {
			case err == nil:
				result, err = keyedReplay(existing, digest)
				return err
			case !errors.Is(err, domain.ErrNotFound):
				return fmt.Errorf("execution: read run by idempotency key: workflow=%s: %w", req.WorkflowID, err)
			}
		}

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
		// never resolves its modelId, allowedTools or imageModelId
		// (registry.NodeRegistry.ValidateSemantics is a no-op for MANAGED_AGENT). Re-resolve
		// them here, with the model capabilities they need: no default is
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
		// recomputed by this recompilation (creating a Run validates input against the
		// version's frozen runInputSchema and must not generate a different input
		// contract from the current Registry).
		if verrs := runtime.ValidateRunInput(def.RunInputSchema, req.Input); len(verrs) > 0 {
			return &RunInputInvalidError{Errors: verrs}
		}

		// The frozen runInputSchema fixes an AssetRef's shape but cannot know whether the
		// Asset it names exists or still describes the same content; only the committed
		// Metadata can (an invalid `AssetRef` must not create a Run).
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
		// The execution model orders Run/NodeRun creation before the Run lock is taken.
		// Safe: neither row is visible to any other transaction until this one
		// commits (PostgreSQL MVCC), so nothing can race these inserts before the lock.
		run := domain.Run{
			ID:                runID,
			WorkflowID:        req.WorkflowID,
			DefinitionVersion: req.DefinitionVersion,
			Status:            domain.RunRunning,
			Input:             req.Input,
			StartedAt:         now,
			UpdatedAt:         now,
			IdempotencyKey:    req.IdempotencyKey,
			IdempotencyDigest: digest,
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

		// Run creation reaches RUNNING directly (no RUN_STARTED event exists: RUN_CREATED
		// and the corresponding NODE_READY commit in the creation transaction, so
		// RUN_STARTED is not defined); UpdateAggregate still runs to persist the seq
		// watermark this transaction allocated.
		if err := tx.Runs().UpdateAggregate(ctx, lock, domain.RunRunning, now); err != nil {
			return err
		}

		run.LastSeq = lock.LastSeq()
		result = CreateRunResult{Run: run}
		return nil
	})
	if err != nil {
		return CreateRunResult{}, err
	}
	return result, nil
}

// verifyRunInputAssets checks every AssetRef this Run input supplies, an Image Input's
// single reference and each photo of a Media Brief's photo set, against the committed
// Asset Metadata. It returns validation errors for references that
// name no Asset or disagree with one, and a plain error only when the lookup itself
// failed.
//
// Errors and validation messages name the consuming node and the asset id alone: the
// storage key never leaves the Store boundary, and it is not needed to
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
