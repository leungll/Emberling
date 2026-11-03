package main

import (
	"fmt"
	"net/http"

	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/verifypatch"
)

// registerVerifyTool registers the verify_patch Tool when a verification runner is
// configured. Without one the Tool is absent, so a Definition that names it fails
// explicitly instead of dispatching to a runner that does not exist. The Tool keeps each
// verification log in artifacts.
func registerVerifyTool(tools *registry.ToolRegistry, cfg config.VerifyRunner, client *http.Client, artifacts verifypatch.ArtifactTextStore) error {
	if !cfg.Enabled() {
		return nil
	}
	if err := tools.Register(verifypatch.Registration(cfg.BaseURL, client, artifacts)); err != nil {
		return fmt.Errorf("register tool %s: %w", verifypatch.ToolName, err)
	}
	return nil
}
