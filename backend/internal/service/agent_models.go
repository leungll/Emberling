package service

import (
	"fmt"
	"slices"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
)

// This file owns the capability rules of an Agent node's model selections. The decision
// model (modelId) must declare structured_decision. The image model (imageModelId) is
// required exactly when the frozen allowlist holds a Tool whose registered Metadata
// declares UsesImageModel, and must declare reference image generation. The review model
// (reviewModelId) is optional unless the allowlist holds a Tool whose Metadata requires an
// independent review model; then it is mandatory and must differ from modelId. When set, it
// must resolve and declare every capability the allowlist's review Tools name in their
// Metadata; a selection that does not is an error, never a silent fall back to the mock
// review policy, which applies only when no review model is selected. The rules come from
// registered Model capabilities and Tool Metadata only, never from a Model ID, a Provider
// type or a Tool Name.
//
// Saving a Definition and creating a Run report violations as validation errors. Agent
// initialisation and the claim of an image-model Tool re-resolve the frozen IDs against the
// current Registry, because configuration can change across a restart; a frozen ID that
// no longer resolves fails explicitly and is never replaced by another model.

const codeModelNotRegistered = "MODEL_NOT_REGISTERED"

// agentModelSelectionErrors reports the capability violations of one Agent node's model
// selections. An unregistered modelId and unregistered Tools are reported by the caller's
// existence checks, so they are skipped here rather than reported twice.
func agentModelSelectionErrors(models *registry.ModelRegistry, tools *registry.ToolRegistry, nodeID string, cfg runtime.AgentNodeConfig) []runtime.ValidationError {
	var errs []runtime.ValidationError
	pathPrefix := "nodes[" + nodeID + "].config."

	if modelReg, _, ok := models.Get(cfg.ModelID); ok && !modelReg.HasCapability(domain.ModelCapabilityStructuredDecision) {
		errs = append(errs, runtime.ValidationError{
			Code:    runtime.CodeSemanticValidationFailed,
			NodeID:  nodeID,
			Path:    pathPrefix + "modelId",
			Message: fmt.Sprintf("model %q does not declare %s and cannot drive an Agent", cfg.ModelID, domain.ModelCapabilityStructuredDecision),
		})
	}

	imageTool := ""
	for _, toolName := range cfg.AllowedTools {
		if reg, ok := tools.Get(toolName); ok && reg.Metadata.UsesImageModel {
			imageTool = toolName
			break
		}
	}

	imagePath := pathPrefix + "imageModelId"
	switch {
	case cfg.ImageModelID == "" && imageTool != "":
		errs = append(errs, runtime.ValidationError{
			Code:    runtime.CodeSemanticValidationFailed,
			NodeID:  nodeID,
			Path:    imagePath,
			Message: fmt.Sprintf("tool %q runs with the node's image model, so imageModelId is required", imageTool),
		})
	case cfg.ImageModelID != "" && imageTool == "":
		errs = append(errs, runtime.ValidationError{
			Code:    runtime.CodeSemanticValidationFailed,
			NodeID:  nodeID,
			Path:    imagePath,
			Message: "imageModelId is set but no allowed tool uses an image model",
		})
	case cfg.ImageModelID != "":
		imageReg, _, ok := models.Get(cfg.ImageModelID)
		if !ok {
			errs = append(errs, runtime.ValidationError{
				Code:    runtime.CodeModelNotFound,
				NodeID:  nodeID,
				Path:    imagePath,
				Message: fmt.Sprintf("model %q is not registered", cfg.ImageModelID),
			})
		} else if !imageReg.HasCapability(domain.ModelCapabilityReferenceImageGeneration) {
			errs = append(errs, runtime.ValidationError{
				Code:    runtime.CodeSemanticValidationFailed,
				NodeID:  nodeID,
				Path:    imagePath,
				Message: fmt.Sprintf("model %q does not declare %s", cfg.ImageModelID, domain.ModelCapabilityReferenceImageGeneration),
			})
		}
	}
	reviewPath := pathPrefix + "reviewModelId"
	independentTool := independentReviewTool(tools, cfg.AllowedTools)
	switch {
	case cfg.ReviewModelID == "" && independentTool != "":
		errs = append(errs, runtime.ValidationError{
			Code:    runtime.CodeSemanticValidationFailed,
			NodeID:  nodeID,
			Path:    reviewPath,
			Message: fmt.Sprintf("tool %q reviews with a model other than the node's decision model, so reviewModelId is required", independentTool),
		})
	case cfg.ReviewModelID != "":
		if independentTool != "" && cfg.ReviewModelID == cfg.ModelID {
			errs = append(errs, runtime.ValidationError{
				Code:    runtime.CodeSemanticValidationFailed,
				NodeID:  nodeID,
				Path:    reviewPath,
				Message: fmt.Sprintf("tool %q needs an independent review, so reviewModelId must differ from modelId %q", independentTool, cfg.ModelID),
			})
		}
		reviewReg, _, ok := models.Get(cfg.ReviewModelID)
		if !ok {
			errs = append(errs, runtime.ValidationError{
				Code:    runtime.CodeModelNotFound,
				NodeID:  nodeID,
				Path:    reviewPath,
				Message: fmt.Sprintf("model %q is not registered", cfg.ReviewModelID),
			})
			break
		}
		for _, capability := range reviewModelCapabilities(tools, cfg.AllowedTools) {
			if !reviewReg.HasCapability(capability) {
				errs = append(errs, runtime.ValidationError{
					Code:    runtime.CodeSemanticValidationFailed,
					NodeID:  nodeID,
					Path:    reviewPath,
					Message: fmt.Sprintf("model %q does not declare %s", cfg.ReviewModelID, capability),
				})
			}
		}
	}
	return errs
}

// reviewModelCapabilities lists, in allowlist order and without repeats, the capabilities
// the allowlist's registered review Tools require of the node's review model. Unregistered
// Tools contribute nothing; the caller's existence checks report them.
func reviewModelCapabilities(tools *registry.ToolRegistry, allowedTools []string) []string {
	var capabilities []string
	for _, toolName := range allowedTools {
		reg, ok := tools.Get(toolName)
		if !ok || !reg.Metadata.UsesReviewModel || reg.Metadata.ReviewModelCapability == "" {
			continue
		}
		if !slices.Contains(capabilities, reg.Metadata.ReviewModelCapability) {
			capabilities = append(capabilities, reg.Metadata.ReviewModelCapability)
		}
	}
	return capabilities
}

// independentReviewTool names the first allowed, registered Tool that requires an
// independent review model, or "" when there is none.
func independentReviewTool(tools *registry.ToolRegistry, allowedTools []string) string {
	for _, toolName := range allowedTools {
		if reg, ok := tools.Get(toolName); ok && reg.Metadata.UsesReviewModel && reg.Metadata.RequiresReviewModel {
			return toolName
		}
	}
	return ""
}

// unusableReviewModel re-resolves a frozen review Model ID against the current Model
// Registry and reports why it can no longer serve the review Tools that need the given
// capabilities, or nil when it can. An empty ID is reported as a missing selection: a
// review that needs a model never falls back to a mock policy.
func unusableReviewModel(models *registry.ModelRegistry, modelID string, capabilities []string) *domain.ExecutionError {
	if len(capabilities) == 0 {
		capabilities = []string{""}
	}
	for _, capability := range capabilities {
		if unusable := unusableModel(models, "review model", modelID, capability); unusable != nil {
			return unusable
		}
	}
	return nil
}

// unusableModel re-resolves one frozen Model ID against the current Model Registry and
// reports why it can no longer serve the named capability, or nil when it can. role names
// the selection ("decision model", "image model" or "review model") in the error message.
// An empty capability checks registration only.
func unusableModel(models *registry.ModelRegistry, role, modelID, capability string) *domain.ExecutionError {
	if modelID == "" {
		return &domain.ExecutionError{
			Code:    codeModelNotRegistered,
			Message: fmt.Sprintf("the Agent Run froze no %s", role),
		}
	}
	reg, _, ok := models.Get(modelID)
	if !ok {
		return &domain.ExecutionError{
			Code:    codeModelNotRegistered,
			Message: fmt.Sprintf("%s %q is not registered", role, modelID),
		}
	}
	if capability != "" && !reg.HasCapability(capability) {
		return &domain.ExecutionError{
			Code:    codeModelNotRegistered,
			Message: fmt.Sprintf("%s %q no longer declares %s", role, modelID, capability),
		}
	}
	return nil
}

// frozenImageModel returns the image Model ID an Agent Run froze, or "" when it froze none.
func frozenImageModel(agentRun domain.AgentRun) string {
	if agentRun.ImageModelID == nil {
		return ""
	}
	return *agentRun.ImageModelID
}

// frozenReviewModel returns the review Model ID an Agent Run froze, or "" when it froze
// none.
func frozenReviewModel(agentRun domain.AgentRun) string {
	if agentRun.ReviewModelID == nil {
		return ""
	}
	return *agentRun.ReviewModelID
}

// agentAuthorizationConfigErrors reports the configuration a production-risk Tool needs
// on its Agent node: a goal pointer, because the authorization decision of such a Tool
// reads the frozen goal and a node without one could only ever be refused, and an approval
// wait bound, because a call a person must approve needs a deadline and the Runtime never
// substitutes one. Nodes whose allowlist holds no such Tool are unaffected. Unregistered
// Tools are reported by the caller's existence checks and skipped here.
func agentAuthorizationConfigErrors(tools *registry.ToolRegistry, nodeID string, cfg runtime.AgentNodeConfig) []runtime.ValidationError {
	productionTool := ""
	for _, toolName := range cfg.AllowedTools {
		if reg, ok := tools.Get(toolName); ok && reg.Metadata.EffectiveRisk() == domain.ToolRiskProduction {
			productionTool = toolName
			break
		}
	}
	if productionTool == "" {
		return nil
	}
	var errs []runtime.ValidationError
	pathPrefix := "nodes[" + nodeID + "].config."
	if cfg.GoalPointer == "" {
		errs = append(errs, runtime.ValidationError{
			Code:    runtime.CodeSemanticValidationFailed,
			NodeID:  nodeID,
			Path:    pathPrefix + "goalPointer",
			Message: fmt.Sprintf("tool %q changes a production system, so goalPointer is required", productionTool),
		})
	}
	if cfg.ApprovalTimeoutMs == nil {
		errs = append(errs, runtime.ValidationError{
			Code:    runtime.CodeSemanticValidationFailed,
			NodeID:  nodeID,
			Path:    pathPrefix + "approvalTimeoutMs",
			Message: fmt.Sprintf("tool %q needs a human approval, so approvalTimeoutMs is required", productionTool),
		})
	}
	return errs
}

// releaseBoundsConfigErrors reports every release bound an Agent node leaves out while it
// allows a Tool that occupies a release target. The Runtime substitutes none of them: the
// confirm deadline bounds the release call and hands an unanswered release to a person, the
// void grace decides when an unknown operation is voided, the retry count how often a voided
// one is released again, and the human wait bound how long a person may take.
func releaseBoundsConfigErrors(tools *registry.ToolRegistry, nodeID string, cfg runtime.AgentNodeConfig) []runtime.ValidationError {
	releaseTool := ""
	for _, toolName := range cfg.AllowedTools {
		if reg, ok := tools.Get(toolName); ok && reg.Metadata.OccupiesReleaseTarget {
			releaseTool = toolName
			break
		}
	}
	if releaseTool == "" {
		return nil
	}
	var errs []runtime.ValidationError
	for _, field := range []struct {
		name  string
		value *int
	}{
		{"releaseConfirmTimeoutMs", cfg.ReleaseConfirmTimeoutMs},
		{"voidGraceMs", cfg.VoidGraceMs},
		{"maxReleaseRetries", cfg.MaxReleaseRetries},
		{"humanWaitTimeoutMs", cfg.HumanWaitTimeoutMs},
	} {
		if field.value != nil {
			continue
		}
		errs = append(errs, runtime.ValidationError{
			Code:    runtime.CodeSemanticValidationFailed,
			NodeID:  nodeID,
			Path:    "nodes[" + nodeID + "].config." + field.name,
			Message: fmt.Sprintf("tool %q occupies a release target, so %s is required", releaseTool, field.name),
		})
	}
	return errs
}
