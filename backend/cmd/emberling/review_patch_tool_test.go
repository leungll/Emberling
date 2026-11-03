package main

import (
	"net/http"
	"testing"

	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/changerequest"
	"github.com/leungll/Emberling/backend/internal/tools/reviewpatch"
)

func TestRegisterReviewPatchTool_Configured_RegistersWithTheRepairTools(t *testing.T) {
	tools := registry.NewToolRegistry()
	store := asset.NewArtifactStore(t.TempDir(), 1<<20)
	cfg := config.VerifyRunner{BaseURL: "http://sandboxrunner:9103"}
	if err := registerVerifyTool(tools, cfg, &http.Client{}, store); err != nil {
		t.Fatalf("registerVerifyTool() error = %v", err)
	}
	if err := registerChangeRequestTool(tools, cfg, store); err != nil {
		t.Fatalf("registerChangeRequestTool() error = %v", err)
	}
	if err := registerReviewPatchTool(tools, cfg, registry.NewModelRegistry(), store, map[string]string{"vuln-demo": t.TempDir()}); err != nil {
		t.Fatalf("registerReviewPatchTool() error = %v", err)
	}
	reg, ok := tools.Get(reviewpatch.ToolName)
	if !ok {
		t.Fatalf("tool %q not registered", reviewpatch.ToolName)
	}
	m := reg.Metadata
	if m.ExecutionKind != domain.ToolExecutionSync || !m.UsesWorkspace || !m.UsesReviewModel || !m.RequiresReviewModel ||
		m.ReviewModelCapability != domain.ModelCapabilityStructuredDecision || m.Produces == nil || m.Produces.FactType != changerequest.ReviewFactType {
		t.Errorf("metadata = %+v, want a synchronous workspace review producing %s with a required independent review model", m, changerequest.ReviewFactType)
	}
	if err := registry.ValidateFactDeclarations(tools.ListMetadata(), nil); err != nil {
		t.Errorf("ValidateFactDeclarations() error = %v", err)
	}
}

func TestRegisterReviewPatchTool_NotConfigured_RegistersNothing(t *testing.T) {
	tools := registry.NewToolRegistry()
	if err := registerReviewPatchTool(tools, config.VerifyRunner{}, registry.NewModelRegistry(), asset.NewArtifactStore(t.TempDir(), 1<<20), nil); err != nil {
		t.Fatalf("registerReviewPatchTool() error = %v", err)
	}
	if _, ok := tools.Get(reviewpatch.ToolName); ok {
		t.Fatalf("tool %q registered without a configured runner", reviewpatch.ToolName)
	}
}
