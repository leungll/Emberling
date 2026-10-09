package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/adapters/openaimodel"
	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// registerModelProviders registers every Model Provider this deployment serves. The
// deterministic mock model is always present. The OpenAI-compatible Adapter is added only
// when its configuration group is complete; config.Load has already refused a partial
// group, so an incomplete one never reaches here as "enabled".
func registerModelProviders(ctx context.Context, models *registry.ModelRegistry, cfg config.OpenAIModel) error {
	if err := models.Register(ctx, mockmodel.NewProvider()); err != nil {
		return fmt.Errorf("register mock model provider: %w", err)
	}
	if !cfg.Enabled() {
		return nil
	}
	provider, err := openaimodel.New(openaimodel.Config{
		BaseURL:        cfg.BaseURL,
		APIKey:         cfg.APIKey.Reveal(),
		Model:          cfg.Model,
		HTTPClient:     &http.Client{Timeout: cfg.Timeout},
		DecisionSchema: registry.DecisionSchema(),
	})
	if err != nil {
		return fmt.Errorf("configure OpenAI-compatible model provider: %w", err)
	}
	if err := models.Register(ctx, provider); err != nil {
		return fmt.Errorf("register OpenAI-compatible model provider: %w", err)
	}
	return nil
}
