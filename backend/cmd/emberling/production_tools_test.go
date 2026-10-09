package main

import (
	"testing"

	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/deploy"
)

func TestRegisterProductionTools_Configured_RegistersDeploy(t *testing.T) {
	tools := registry.NewToolRegistry()
	if err := registerProductionTools(tools, config.Production{BaseURL: "http://mockproduction:9102"}); err != nil {
		t.Fatalf("registerProductionTools() error = %v", err)
	}
	if _, ok := tools.Get(deploy.ToolName); !ok {
		t.Errorf("tool %q not registered", deploy.ToolName)
	}
}

func TestRegisterProductionTools_NotConfigured_RegistersNothing(t *testing.T) {
	tools := registry.NewToolRegistry()
	if err := registerProductionTools(tools, config.Production{}); err != nil {
		t.Fatalf("registerProductionTools() error = %v", err)
	}
	if _, ok := tools.Get(deploy.ToolName); ok {
		t.Errorf("tool %q registered without configuration", deploy.ToolName)
	}
	if got := len(tools.ListMetadata()); got != 0 {
		t.Errorf("registered %d tools, want 0", got)
	}
}
