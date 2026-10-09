package main

import (
	"context"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/adapters/openaimodel"
	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/registry"
)

func registeredModelIDs(models *registry.ModelRegistry) []string {
	var ids []string
	for _, metadata := range models.ListMetadata() {
		ids = append(ids, metadata.ID)
	}
	return ids
}

func TestRegisterModelProviders_OpenAIConfigured_RegistersOpenAICompatible(t *testing.T) {
	const key = "sk-registration-secret"
	models := registry.NewModelRegistry()
	err := registerModelProviders(context.Background(), models, config.OpenAIModel{
		BaseURL: "https://llm.example/v1", APIKey: config.NewSecret(key), Model: "gpt-test",
	})
	if err != nil {
		t.Fatalf("registerModelProviders() error = %v", err)
	}
	if _, _, ok := models.Get(openaimodel.ModelID); !ok {
		t.Errorf("model %q not registered; registered %v", openaimodel.ModelID, registeredModelIDs(models))
	}
	if _, _, ok := models.Get(mockmodel.ModelID); !ok {
		t.Errorf("mock model %q not registered alongside; registered %v", mockmodel.ModelID, registeredModelIDs(models))
	}
	for _, metadata := range models.ListMetadata() {
		if strings.Contains(metadata.DisplayName, key) {
			t.Errorf("model metadata leaks the API key: %+v", metadata)
		}
	}
}

func TestRegisterModelProviders_NotConfigured_RegistersMockOnly(t *testing.T) {
	models := registry.NewModelRegistry()
	if err := registerModelProviders(context.Background(), models, config.OpenAIModel{}); err != nil {
		t.Fatalf("registerModelProviders() error = %v", err)
	}
	if _, _, ok := models.Get(openaimodel.ModelID); ok {
		t.Errorf("model %q registered without configuration", openaimodel.ModelID)
	}
	if _, _, ok := models.Get(mockmodel.ModelID); !ok {
		t.Errorf("mock model %q not registered; registered %v", mockmodel.ModelID, registeredModelIDs(models))
	}
}
