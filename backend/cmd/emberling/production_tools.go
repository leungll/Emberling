package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/deploy"
	"github.com/leungll/Emberling/backend/internal/tools/deployrelease"
)

// releaseClientTimeout bounds each single request of the reconcilable release Tool. Its
// requests are individually short (accept, query, void, target check) and every answer that
// does not arrive is treated as "no answer" and re-asked by the poll plan, so a short
// timeout keeps a slow production from holding a worker.
const releaseClientTimeout = 10 * time.Second

// registerProductionTools registers the deploy Tool when a production deployment service
// is configured and the deploy_change Tool when its release surface is configured. Without
// a URL the Tool is absent, so a Definition naming it fails its Registry check instead of
// calling a service that does not exist.
//
// The deploy client sets no timeout of its own: the Agent Run deadline bounds every call
// through its context, so a slow deployment ends as the Agent's timeout rather than as a
// shorter client timeout misreported as a Tool error.
func registerProductionTools(tools *registry.ToolRegistry, cfg config.Production) error {
	if cfg.Enabled() {
		if err := tools.Register(deploy.Registration(cfg.BaseURL, &http.Client{})); err != nil {
			return fmt.Errorf("register tool: %w", err)
		}
	}
	if cfg.ReleaseEnabled() {
		reg := deployrelease.Registration(cfg.ReleaseBaseURL, &http.Client{Timeout: releaseClientTimeout})
		if err := tools.Register(reg); err != nil {
			return fmt.Errorf("register tool: %w", err)
		}
	}
	return nil
}
