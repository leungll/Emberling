package service

import (
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
)

// This file owns the capability rules of an Agent node's two model selections. The
// decision model (modelId) must declare structured_decision. The image model
// (imageModelId) is required exactly when the frozen allowlist holds a Tool whose
// registered Metadata declares UsesImageModel, and must declare reference image
// generation. The rules come from registered Model capabilities and Tool Metadata only,
// never from a Model ID, a Provider type or a Tool Name.
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
	return errs
}

// unusableModel re-resolves one frozen Model ID against the current Model Registry and
// reports why it can no longer serve the named capability, or nil when it can. role names
// the selection ("decision model" or "image model") in the error message.
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
	if !reg.HasCapability(capability) {
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
