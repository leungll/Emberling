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

// resolveAgentIdentifiers re-resolves every `agent` node's modelId and allowedTools
// against the *current* Model and Tool Registries, existence-only. Unlike
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

// CreateRun is the parameter struct for ExecutionService.CreateRun.
type CreateRun struct {
	WorkflowID        string
	DefinitionVersion int
	Input             json.RawMessage
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
// stays bound to one immutable Definition version.
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
