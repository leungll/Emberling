package mocktask

import (
	"context"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

func TestTaskModels_Register_ResolvesEachModelToTheProvider(t *testing.T) {
	models := registry.NewModelRegistry()
	declared := NewTaskModels(gatewayProviderID, []TaskModel{
		{ID: "gateway-a", DisplayName: "A"},
		{ID: "gateway-b", DisplayName: "B", Description: "Second gateway model."},
	})
	if err := models.Register(context.Background(), declared); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	for _, id := range []string{"gateway-a", "gateway-b"} {
		registration, _, ok := models.Get(id)
		if !ok || !registration.HasCapability(domain.ModelCapabilityImageGeneration) {
			t.Fatalf("Get(%q) = %+v, %v; want an image_generation model", id, registration, ok)
		}
		if got, err := models.ProviderID(id); err != nil || got != gatewayProviderID {
			t.Fatalf("ProviderID(%q) = %q, %v; want %q", id, got, err, gatewayProviderID)
		}
	}
	if _, err := declared.Generate(context.Background(), registry.ModelRequest{}); err == nil {
		t.Fatal("Generate() error = nil, want a refusal")
	}
}
