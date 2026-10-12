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
// Saving a Definition and creating a Run report violations as validation errors.

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
