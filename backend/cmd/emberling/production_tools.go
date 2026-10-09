package main

import (
	"fmt"
	"net/http"

	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/deploy"
)

// registerProductionTools registers the deploy Tool when a production deployment service
// is configured. Without one the Tool is absent, so a Definition naming it fails its
// Registry check instead of calling a service that does not exist.
//
// The deploy client sets no timeout of its own: the Agent Run deadline bounds every call
// through its context, so a slow deployment ends as the Agent's timeout rather than as a
// shorter client timeout misreported as a Tool error.
func registerProductionTools(tools *registry.ToolRegistry, cfg config.Production) error {
	if !cfg.Enabled() {
		return nil
	}
	if err := tools.Register(deploy.Registration(cfg.BaseURL, &http.Client{})); err != nil {
		return fmt.Errorf("register tool: %w", err)
	}
	return nil
}
