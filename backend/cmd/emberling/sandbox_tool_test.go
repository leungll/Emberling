package main

import (
	"net/http"
	"testing"

	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/sandboxtest"
)

func TestRegisterSandboxTool_Configured_RegistersAsyncTool(t *testing.T) {
	tools := registry.NewToolRegistry()
	err := registerSandboxTool(tools, config.SandboxRunner{BaseURL: "http://sandboxrunner:9103"}, &http.Client{})
	if err != nil {
		t.Fatalf("registerSandboxTool() error = %v", err)
	}
	reg, ok := tools.Get(sandboxtest.ToolName)
	if !ok {
		t.Fatalf("tool %q not registered", sandboxtest.ToolName)
	}
	if reg.Metadata.ExecutionKind != domain.ToolExecutionAsync {
		t.Errorf("execution kind = %v, want async", reg.Metadata.ExecutionKind)
	}
	if err := registry.ValidateFactDeclarations(tools.ListMetadata(), nil); err != nil {
		t.Errorf("ValidateFactDeclarations() error = %v", err)
	}
}

func TestRegisterSandboxTool_NotConfigured_RegistersNothing(t *testing.T) {
	tools := registry.NewToolRegistry()
	if err := registerSandboxTool(tools, config.SandboxRunner{}, &http.Client{}); err != nil {
		t.Fatalf("registerSandboxTool() error = %v", err)
	}
	if _, ok := tools.Get(sandboxtest.ToolName); ok {
		t.Fatalf("tool %q registered without a configured runner", sandboxtest.ToolName)
	}
}

func TestRegisterSandboxTool_AlreadyRegistered_FailsExplicitly(t *testing.T) {
	tools := registry.NewToolRegistry()
	cfg := config.SandboxRunner{BaseURL: "http://sandboxrunner:9103"}
	if err := registerSandboxTool(tools, cfg, &http.Client{}); err != nil {
		t.Fatalf("first registerSandboxTool() error = %v", err)
	}
	if err := registerSandboxTool(tools, cfg, &http.Client{}); err == nil {
		t.Fatal("second registerSandboxTool() error = nil, want a duplicate registration error")
	}
}
