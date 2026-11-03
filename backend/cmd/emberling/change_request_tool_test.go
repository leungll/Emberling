package main

import (
	"net/http"
	"testing"

	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/changerequest"
)

func TestRegisterChangeRequestTool_Configured_RegistersBesideItsProducer(t *testing.T) {
	tools := registry.NewToolRegistry()
	store := asset.NewArtifactStore(t.TempDir(), 1<<20)
	cfg := config.VerifyRunner{BaseURL: "http://sandboxrunner:9103"}
	if err := registerVerifyTool(tools, cfg, &http.Client{}, store); err != nil {
		t.Fatalf("registerVerifyTool() error = %v", err)
	}
	if err := registerChangeRequestTool(tools, cfg, store); err != nil {
		t.Fatalf("registerChangeRequestTool() error = %v", err)
	}
	reg, ok := tools.Get(changerequest.ToolName)
	if !ok {
		t.Fatalf("tool %q not registered", changerequest.ToolName)
	}
	if reg.Metadata.ExecutionKind != domain.ToolExecutionSync || !reg.Metadata.UsesWorkspace || !reg.Metadata.DeclaresRecord(domain.ToolRecordChangeRequest) {
		t.Errorf("metadata = %+v, want a synchronous workspace Tool recording CHANGE_REQUEST", reg.Metadata)
	}
	// The required fact has a registered producer only because verify_patch is registered
	// under the same condition.
	if err := registry.ValidateFactDeclarations(tools.ListMetadata(), nil); err != nil {
		t.Errorf("ValidateFactDeclarations() error = %v", err)
	}
}

func TestRegisterChangeRequestTool_NotConfigured_RegistersNothing(t *testing.T) {
	tools := registry.NewToolRegistry()
	if err := registerChangeRequestTool(tools, config.VerifyRunner{}, asset.NewArtifactStore(t.TempDir(), 1<<20)); err != nil {
		t.Fatalf("registerChangeRequestTool() error = %v", err)
	}
	if _, ok := tools.Get(changerequest.ToolName); ok {
		t.Fatalf("tool %q registered without a configured runner", changerequest.ToolName)
	}
}

func TestRegisterChangeRequestTool_WithoutProducer_FactDeclarationsFail(t *testing.T) {
	tools := registry.NewToolRegistry()
	if err := registerChangeRequestTool(tools, config.VerifyRunner{BaseURL: "http://sandboxrunner:9103"}, asset.NewArtifactStore(t.TempDir(), 1<<20)); err != nil {
		t.Fatalf("registerChangeRequestTool() error = %v", err)
	}
	if err := registry.ValidateFactDeclarations(tools.ListMetadata(), nil); err == nil {
		t.Fatal("ValidateFactDeclarations() error = nil, want the missing producer of the required fact reported")
	}
}
