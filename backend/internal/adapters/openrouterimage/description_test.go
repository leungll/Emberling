package openrouterimage_test

import (
	"context"
	"slices"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/openrouterimage"
	"github.com/leungll/Emberling/backend/internal/domain"
)

func TestOpenRouterImage_Models_ServesConfiguredDescriptionAndDomainCapability(t *testing.T) {
	const description = "Stylises a reference photo while keeping its subject recognisable."
	provider, err := openrouterimage.New(openrouterimage.Config{
		ModelID: testModelID, DisplayName: testDisplayName, Description: description,
		BaseURL: "http://images.invalid", APIKey: testKey, Model: testUpstream, MaxImageBytes: testMaxBytes,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	models, err := provider.Models(context.Background())
	if err != nil || len(models) != 1 {
		t.Fatalf("Models() = %d registrations, error %v; want 1", len(models), err)
	}
	if got := models[0].Description; got != description {
		t.Errorf("Description = %q, want %q", got, description)
	}
	if !slices.Equal(models[0].Capabilities, []string{domain.ModelCapabilityReferenceImageGeneration}) {
		t.Errorf("capabilities = %v, want only %s", models[0].Capabilities, domain.ModelCapabilityReferenceImageGeneration)
	}
}
