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
// when its configuration group is complete, as one Provider per configured model sharing
// the endpoint, key and HTTP client; config.Load has already refused a partial group or an
// invalid model list, and the Registry refuses a duplicate Model ID.
func registerModelProviders(ctx context.Context, models *registry.ModelRegistry, cfg config.OpenAIModel) error {
	if err := models.Register(ctx, mockmodel.NewProvider()); err != nil {
		return fmt.Errorf("register mock model provider: %w", err)
	}
	if !cfg.Enabled() {
		return nil
	}
	client := &http.Client{Timeout: cfg.Timeout}
	for i, entry := range cfg.Models {
		provider, err := openaimodel.New(openaimodel.Config{
			ModelID:        entry.ID,
			DisplayName:    entry.DisplayName,
			BaseURL:        cfg.BaseURL,
			APIKey:         cfg.APIKey.Reveal(),
			Model:          entry.Model,
			HTTPClient:     client,
			DecisionSchema: registry.DecisionSchema(),

			OpenRouterRequireParameters: cfg.OpenRouterRequireParameters,
		})
		if err != nil {
			return fmt.Errorf("configure OpenAI-compatible model entry %d: %w", i, err)
		}
		if err := models.Register(ctx, provider); err != nil {
			return fmt.Errorf("register OpenAI-compatible model entry %d: %w", i, err)
		}
	}
	return nil
}
