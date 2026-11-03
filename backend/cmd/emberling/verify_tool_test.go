package main

import (
	"net/http"
	"testing"

	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/verifypatch"
)

func TestRegisterVerifyTool_Configured_RegistersPollableAsyncTool(t *testing.T) {
	tools := registry.NewToolRegistry()
	store := asset.NewArtifactStore(t.TempDir(), 1<<20)
	err := registerVerifyTool(tools, config.VerifyRunner{BaseURL: "http://sandboxrunner:9103"}, &http.Client{}, store)
	if err != nil {
		t.Fatalf("registerVerifyTool() error = %v", err)
	}
	reg, ok := tools.Get(verifypatch.ToolName)
	if !ok {
		t.Fatalf("tool %q not registered", verifypatch.ToolName)
	}
	if reg.Metadata.ExecutionKind != domain.ToolExecutionAsync || reg.Metadata.Poll == nil || !reg.Metadata.UsesWorkspace {
		t.Errorf("metadata = %+v, want an async, pollable workspace Tool", reg.Metadata)
	}
	if _, ok := reg.Executor.(registry.PollableAsyncToolExecutor); !ok {
		t.Error("executor is not pollable")
	}
	if err := registry.ValidateFactDeclarations(tools.ListMetadata(), nil); err != nil {
		t.Errorf("ValidateFactDeclarations() error = %v", err)
	}
}

func TestRegisterVerifyTool_NotConfigured_RegistersNothing(t *testing.T) {
	tools := registry.NewToolRegistry()
	if err := registerVerifyTool(tools, config.VerifyRunner{}, &http.Client{}, asset.NewArtifactStore(t.TempDir(), 1<<20)); err != nil {
		t.Fatalf("registerVerifyTool() error = %v", err)
	}
	if _, ok := tools.Get(verifypatch.ToolName); ok {
		t.Fatalf("tool %q registered without a configured runner", verifypatch.ToolName)
	}
}

func TestRegisterVerifyTool_AlreadyRegistered_FailsExplicitly(t *testing.T) {
	tools := registry.NewToolRegistry()
	cfg := config.VerifyRunner{BaseURL: "http://sandboxrunner:9103"}
	store := asset.NewArtifactStore(t.TempDir(), 1<<20)
	if err := registerVerifyTool(tools, cfg, &http.Client{}, store); err != nil {
		t.Fatalf("first registerVerifyTool() error = %v", err)
	}
	if err := registerVerifyTool(tools, cfg, &http.Client{}, store); err == nil {
		t.Fatal("second registerVerifyTool() error = nil, want a duplicate registration error")
	}
}
