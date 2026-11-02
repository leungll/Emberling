package main

import (
	"context"
	"testing"

	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

func TestRegisterModelProviders_VisionInputEntry_DeclaresVisionCapability(t *testing.T) {
	models := registry.NewModelRegistry()
	err := registerModelProviders(context.Background(), models, config.OpenAIModel{
		BaseURL: "http://127.0.0.1:1/v1",
		APIKey:  config.NewSecret("sk-vision-registration"),
		Models: []config.OpenAIModelEntry{
			{ID: "review-model", Model: "vendor/review", DisplayName: "Review", VisionInput: true},
			{ID: "plain-model", Model: "vendor/plain", DisplayName: "Plain"},
		},
	})
	if err != nil {
		t.Fatalf("registerModelProviders() error = %v", err)
	}
	for id, want := range map[string]bool{"review-model": true, "plain-model": false} {
		reg, _, ok := models.Get(id)
		if !ok {
			t.Fatalf("model %q not registered", id)
		}
		if got := reg.HasCapability(domain.ModelCapabilityVisionInput); got != want {
			t.Errorf("%s declares %s = %v, want %v", id, domain.ModelCapabilityVisionInput, got, want)
		}
	}
}
