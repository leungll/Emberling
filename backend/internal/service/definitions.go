// Package service owns Emberling's use cases. This file covers the Definition half:
// Validate/Create/Save run the compile chain in runtime.Compiler and persist through the
// store.UnitOfWork; Get/GetVersion/ListVersions/List are read paths for Studio and the
// API. Nothing here executes a Node, calls a Model or touches HTTP.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// DefinitionService owns the Definition Validate/Save use cases (docs/08-interface-spec.md
// §1.2, §3.1). Save and Create never persist a Definition that failed compilation
// (docs/09-testing-and-acceptance.md §3.4: "Save 校验失败 | 不创建 Definition version，不改变最新版本").
type DefinitionService struct {
	deps Deps
}

// NewDefinitionService builds a DefinitionService from the shared Deps skeleton.
func NewDefinitionService(deps Deps) *DefinitionService {
	return &DefinitionService{deps: deps.withDefaults()}
}

// ValidateResult is the outcome of running the Validate/Save compile chain. A compile
// failure is a result (Valid=false plus Errors), never a Go error: only infrastructure
// failures surface as an error from Validate.
type ValidateResult struct {
	Valid          bool
	RunInputSchema json.RawMessage
	Errors         []runtime.ValidationError
}

// CreateDefinition is the input of DefinitionService.Create: everything a client submits
// to mint the first immutable version of a new Workflow.
type CreateDefinition struct {
	Name        string
	Description string
	Nodes       []domain.Node
	Edges       []domain.Edge
}

// SaveDefinition is the input of DefinitionService.Save. BaseVersion names the version
// the client edited from; it must equal the Workflow's current latest version or the save
// is rejected as a conflict (docs/08-interface-spec.md §3.1).
type SaveDefinition struct {
	WorkflowID  string
	BaseVersion int
	Name        string
	Description string
	Nodes       []domain.Node
	Edges       []domain.Edge
}

// DefinitionListItem is one row of the Definitions-list read model: a Workflow summary
// plus its most recent Run, or a nil LastRun when the Workflow has never run
// (docs/08-interface-spec.md §3.1). Description is the latest version's description; it
// is not a domain.Workflow field because it lives per-version, not per-workflow.
type DefinitionListItem struct {
	Workflow    domain.Workflow
	Description string
	LastRun     *domain.Run
}

// CompileFailedError reports that a Definition failed the Validate/Save compile chain.
// Create and Save return it instead of persisting anything; Result carries the same
// structured errors a direct Validate call would have returned, so a caller never needs a
// second round trip to explain why nothing was saved.
type CompileFailedError struct {
	Result ValidateResult
}

func (e *CompileFailedError) Error() string {
	return fmt.Sprintf("emberling: definition compile failed: %d error(s)", len(e.Result.Errors))
}

// Validate runs the same compile chain Save uses, without persisting anything
// (docs/08-interface-spec.md §1.2). Only an infrastructure failure (not a Definition
// authoring mistake) is returned as an error.
func (s *DefinitionService) Validate(ctx context.Context, nodes []domain.Node, edges []domain.Edge) (ValidateResult, error) {
	result, _, err := s.compile(ctx, domain.Definition{Nodes: nodes, Edges: edges})
	if err != nil {
		return ValidateResult{}, fmt.Errorf("service definitions.Validate: %w", err)
	}
	return result, nil
}

// Create compiles nodes/edges and, on success, mints a new Workflow id and persists
// version 1 in one transaction. On compile failure it returns *CompileFailedError and
// persists nothing.
func (s *DefinitionService) Create(ctx context.Context, in CreateDefinition) (domain.Definition, error) {
	def := domain.Definition{
		Name:        in.Name,
		Description: in.Description,
		Nodes:       in.Nodes,
		Edges:       in.Edges,
	}

	result, compiled, err := s.compile(ctx, def)
	if err != nil {
		return domain.Definition{}, fmt.Errorf("service definitions.Create: %w", err)
	}
	if !result.Valid {
		return domain.Definition{}, &CompileFailedError{Result: result}
	}

	def.WorkflowID = s.deps.IDs.NewID(domain.IDPrefixWorkflow)
	def.Version = 1
	def.RunInputSchema = compiled.RunInputSchema
	def.Validation = compiled.Validation
	def.CreatedAt = s.deps.Clock.Now()

	err = s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Definitions().Save(ctx, def)
	})
	if err != nil {
		return domain.Definition{}, fmt.Errorf("service definitions.Create: workflow=%s: %w", def.WorkflowID, err)
	}
	return def, nil
}

// Save reads the Workflow's current latest version inside one transaction; a stale
// BaseVersion is rejected without compiling. A concurrent racer saving the same
// BaseVersion is decided by store.DefinitionRepository.Save's own locking (it takes the
// Workflow row FOR UPDATE), so the loser here also observes domain.ErrVersionConflict —
// this method does not reimplement that arbitration.
func (s *DefinitionService) Save(ctx context.Context, in SaveDefinition) (domain.Definition, error) {
	var result domain.Definition
	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		wf, err := tx.Definitions().GetWorkflow(ctx, in.WorkflowID)
		if err != nil {
			return fmt.Errorf("get workflow: workflow=%s: %w", in.WorkflowID, err)
		}
		if in.BaseVersion != wf.LatestVersion {
			return fmt.Errorf("workflow=%s baseVersion=%d latest=%d: %w",
				in.WorkflowID, in.BaseVersion, wf.LatestVersion, domain.ErrVersionConflict)
		}

		def := domain.Definition{
			WorkflowID:  in.WorkflowID,
			Name:        in.Name,
			Description: in.Description,
			Nodes:       in.Nodes,
			Edges:       in.Edges,
		}
		valResult, compiled, err := s.compile(ctx, def)
		if err != nil {
			return fmt.Errorf("compile: workflow=%s: %w", in.WorkflowID, err)
		}
		if !valResult.Valid {
			return &CompileFailedError{Result: valResult}
		}

		def.Version = in.BaseVersion + 1
		def.RunInputSchema = compiled.RunInputSchema
		def.Validation = compiled.Validation
		def.CreatedAt = s.deps.Clock.Now()

		if err := tx.Definitions().Save(ctx, def); err != nil {
			return err
		}
		result = def
		return nil
	})
	if err != nil {
		return domain.Definition{}, fmt.Errorf("service definitions.Save: %w", err)
	}
	return result, nil
}

// Get returns the latest saved version of a Workflow.
func (s *DefinitionService) Get(ctx context.Context, workflowID string) (domain.Definition, error) {
	var result domain.Definition
	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		wf, err := tx.Definitions().GetWorkflow(ctx, workflowID)
		if err != nil {
			return err
		}
		result, err = tx.Definitions().GetVersion(ctx, workflowID, wf.LatestVersion)
		return err
	})
	if err != nil {
		return domain.Definition{}, fmt.Errorf("service definitions.Get: workflow=%s: %w", workflowID, err)
	}
	return result, nil
}

// GetVersion returns one immutable, previously saved version.
func (s *DefinitionService) GetVersion(ctx context.Context, workflowID string, version int) (domain.Definition, error) {
	var result domain.Definition
	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		result, err = tx.Definitions().GetVersion(ctx, workflowID, version)
		return err
	})
	if err != nil {
		return domain.Definition{}, fmt.Errorf("service definitions.GetVersion: workflow=%s version=%d: %w", workflowID, version, err)
	}
	return result, nil
}

// ListVersions returns every saved version of a Workflow, ascending.
func (s *DefinitionService) ListVersions(ctx context.Context, workflowID string) ([]domain.Definition, error) {
	var result []domain.Definition
	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		result, err = tx.Definitions().ListVersions(ctx, workflowID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("service definitions.ListVersions: workflow=%s: %w", workflowID, err)
	}
	return result, nil
}

// List returns the Definitions-list read model: every Workflow with its most recent Run,
// nil when the Workflow has never run (docs/08-interface-spec.md §3.1).
func (s *DefinitionService) List(ctx context.Context) ([]DefinitionListItem, error) {
	var items []DefinitionListItem
	err := s.deps.UoW.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		workflows, err := tx.Definitions().ListWorkflows(ctx)
		if err != nil {
			return err
		}
		items = make([]DefinitionListItem, 0, len(workflows))
		for _, wf := range workflows {
			lastRun, err := tx.Runs().LatestByWorkflow(ctx, wf.Workflow.WorkflowID)
			if err != nil {
				return fmt.Errorf("latest run: workflow=%s: %w", wf.Workflow.WorkflowID, err)
			}
			items = append(items, DefinitionListItem{Workflow: wf.Workflow, Description: wf.Description, LastRun: lastRun})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("service definitions.List: %w", err)
	}
	return items, nil
}

// compile runs the Compiler outside any transaction and translates a *runtime.CompileError
// into a ValidateResult. A non-compile error (an infrastructure failure inside the
// Compiler) is returned as-is for the caller to wrap.
//
// On a clean Compile, it also resolves every `agent` node's modelId and allowedTools
// against the current Model and Tool Registries (runtime.Compiler cannot: runtime holds
// no Registry access). This mutates def.Nodes in place to freeze each agent node's
// normalised modelConfig -- def.Nodes shares a backing array with the caller's own
// Definition (Validate/Create/Save all build def with Nodes: in.Nodes, never a copy), so
// the caller observes the frozen config after compile returns without def being passed
// back explicitly. Validate discards its def afterward, so the mutation is inert there;
// Create and Save persist def, so the frozen config is what gets saved.
func (s *DefinitionService) compile(ctx context.Context, def domain.Definition) (ValidateResult, *runtime.CompiledDefinition, error) {
	compiled, err := s.deps.Compiler.Compile(ctx, def)
	if err != nil {
		ce, ok := runtime.AsCompileError(err)
		if !ok {
			return ValidateResult{}, nil, err
		}
		return ValidateResult{Valid: false, Errors: ce.Errors}, nil, nil
	}

	if errs := s.resolveAgentRegistrations(def); len(errs) > 0 {
		return ValidateResult{Valid: false, Errors: errs}, nil, nil
	}

	return ValidateResult{Valid: true, RunInputSchema: compiled.RunInputSchema}, compiled, nil
}

// resolveAgentRegistrations re-resolves every `agent` node's modelId and allowedTools
// against the current Model and Tool Registries, and freezes the node's normalised
// modelConfig back into def.Nodes: `{}` when the author configured nothing, or the
// submitted object with every top-level ConfigSchema default filled in
// (registry.ApplyTopLevelDefaults) otherwise. Frozen config is validated against the
// resolved Model's ConfigSchema, so a Definition can never save a modelConfig its own
// Model would reject.
//
// It runs only after runtime.Compiler already validated the fixed Agent config shape
// (maxTurns/timeoutMs bounds, Schema compilability, stateSchema/outputSchema instance
// checks) at the Semantics stage, so a ParseAgentNodeConfig failure here is unreachable in
// practice and is skipped defensively rather than double-reported.
func (s *DefinitionService) resolveAgentRegistrations(def domain.Definition) []runtime.ValidationError {
	var errs []runtime.ValidationError
	for i, node := range def.Nodes {
		if node.Type != runtime.NodeTypeAgent {
			continue
		}
		cfg, err := runtime.ParseAgentNodeConfig(node.Config)
		if err != nil {
			continue
		}

		if modelReg, _, ok := s.deps.Models.Get(cfg.ModelID); !ok {
			errs = append(errs, runtime.ValidationError{
				Code:    runtime.CodeModelNotFound,
				NodeID:  node.ID,
				Path:    "nodes[" + node.ID + "].config.modelId",
				Message: fmt.Sprintf("model %q is not registered", cfg.ModelID),
			})
		} else {
			frozen, err := freezeModelConfig(modelReg.ConfigSchema, cfg.ModelConfig)
			if err != nil {
				errs = append(errs, runtime.ValidationError{
					Code:    runtime.CodeModelConfigInvalid,
					NodeID:  node.ID,
					Path:    "nodes[" + node.ID + "].config.modelConfig",
					Message: err.Error(),
				})
			} else {
				newConfig, err := withFrozenModelConfig(node.Config, frozen)
				if err != nil {
					errs = append(errs, runtime.ValidationError{
						Code:    runtime.CodeModelConfigInvalid,
						NodeID:  node.ID,
						Path:    "nodes[" + node.ID + "].config.modelConfig",
						Message: err.Error(),
					})
				} else {
					def.Nodes[i].Config = newConfig
				}
			}
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

// freezeModelConfig normalises a submitted modelConfig against a resolved Model's
// ConfigSchema: it fills every top-level declared default the author did not already set,
// then validates the result against the same Schema, so an invalid default can never slip
// through unnoticed. An absent or empty submitted value freezes to `{}` plus defaults, not
// to a JSON null.
func freezeModelConfig(schema json.RawMessage, submitted json.RawMessage) (map[string]any, error) {
	var value map[string]any
	if len(bytes.TrimSpace(submitted)) > 0 {
		if err := json.Unmarshal(submitted, &value); err != nil {
			return nil, fmt.Errorf("modelConfig is not a JSON object: %w", err)
		}
	}
	if value == nil {
		value = map[string]any{}
	}

	frozen, err := registry.ApplyTopLevelDefaults(schema, value)
	if err != nil {
		return nil, fmt.Errorf("model configSchema is not usable: %w", err)
	}

	if len(schema) > 0 {
		compiled, err := registry.CompileSchema(schema)
		if err != nil {
			return nil, fmt.Errorf("model configSchema is not usable: %w", err)
		}
		if err := registry.ValidateValue(compiled, frozen); err != nil {
			return nil, err
		}
	}
	return frozen, nil
}

// withFrozenModelConfig returns nodeConfig with its top-level "modelConfig" field replaced
// by frozen, leaving every other field untouched.
func withFrozenModelConfig(nodeConfig json.RawMessage, frozen map[string]any) (json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if len(bytes.TrimSpace(nodeConfig)) > 0 {
		if err := json.Unmarshal(nodeConfig, &raw); err != nil {
			return nil, fmt.Errorf("config is not a JSON object: %w", err)
		}
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	encoded, err := json.Marshal(frozen)
	if err != nil {
		return nil, fmt.Errorf("modelConfig is not encodable as JSON: %w", err)
	}
	raw["modelConfig"] = json.RawMessage(encoded)

	out, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("config is not encodable as JSON: %w", err)
	}
	return json.RawMessage(out), nil
}
