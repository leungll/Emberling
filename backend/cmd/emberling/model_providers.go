package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/adapters/openaimodel"
	"github.com/leungll/Emberling/backend/internal/adapters/openrouterimage"
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
			Description:    entry.Description,
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

// imageModel is one configured real image model: the Provider registered in the Model
// Registry under id, which generate_image also calls for that id.
type imageModel struct {
	id       string
	provider *openrouterimage.Provider
}

// newImageModels builds one Images API Provider per configured image model, all sharing
// the endpoint, key and client. It returns none when the image group is not configured;
// config.Load has already refused a partial group, an invalid list and an ID that repeats
// a decision model. maxImageBytes caps one generated image.
func newImageModels(cfg config.ImageModel, maxImageBytes int64, client *http.Client) ([]imageModel, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	models := make([]imageModel, 0, len(cfg.Models))
	for i, entry := range cfg.Models {
		provider, err := openrouterimage.New(openrouterimage.Config{
			ModelID:       entry.ID,
			DisplayName:   entry.DisplayName,
			Description:   entry.Description,
			BaseURL:       cfg.BaseURL,
			APIKey:        cfg.APIKey.Reveal(),
			Model:         entry.Model,
			HTTPClient:    client,
			MaxImageBytes: maxImageBytes,
		})
		if err != nil {
			return nil, fmt.Errorf("configure image model entry %d: %w", i, err)
		}
		models = append(models, imageModel{id: entry.ID, provider: provider})
	}
	return models, nil
}

// registerImageModels registers every real image model in the Model Registry, which image
// and decision models share. The Registry refuses an ID already taken, such as one of the
// mock models, so such a deployment never becomes ready.
func registerImageModels(ctx context.Context, models *registry.ModelRegistry, imageModels []imageModel) error {
	for i, model := range imageModels {
		if err := models.Register(ctx, model.provider); err != nil {
			return fmt.Errorf("register image model entry %d: %w", i, err)
		}
	}
	return nil
}
