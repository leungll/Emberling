package main

import (
	"fmt"
	"net/http"

	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/sandboxtest"
)

// registerSandboxTool registers the sandbox_test Tool when a sandbox runner is configured.
// Without one the Tool is absent, so a Definition that names it fails explicitly instead
// of dispatching to a runner that does not exist.
func registerSandboxTool(tools *registry.ToolRegistry, cfg config.SandboxRunner, client *http.Client) error {
	if !cfg.Enabled() {
		return nil
	}
	if err := tools.Register(sandboxtest.Registration(cfg.BaseURL, client)); err != nil {
		return fmt.Errorf("register tool %s: %w", sandboxtest.ToolName, err)
	}
	return nil
}
