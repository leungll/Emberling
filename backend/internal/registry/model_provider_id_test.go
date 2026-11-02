package registry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

func asyncImageModel(id, providerID string) ModelRegistration {
	return ModelRegistration{ModelMetadata: domain.ModelMetadata{
		ID:           id,
		DisplayName:  "Image Model",
		Capabilities: []string{domain.ModelCapabilityImageGeneration},
		ConfigSchema: json.RawMessage(`{"type":"object"}`),
		ProviderID:   providerID,
	}}
}

func registeredModels(t *testing.T, models ...ModelRegistration) *ModelRegistry {
	t.Helper()
	r := NewModelRegistry()
	if err := r.Register(context.Background(), &stubModelProvider{models: models}); err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}
	return r
}

func TestModelRegistry_ProviderID_RegisteredModel_ReturnsItsProvider(t *testing.T) {
	r := registeredModels(t,
		asyncImageModel("image-model-a", "provider-a"),
		asyncImageModel("image-model-b", "provider-b"),
	)
	for modelID, want := range map[string]string{"image-model-a": "provider-a", "image-model-b": "provider-b"} {
		got, err := r.ProviderID(modelID)
		if err != nil || got != want {
			t.Fatalf("ProviderID(%q) = %q, %v; want %q, nil", modelID, got, err, want)
		}
	}
}

func TestModelRegistry_ProviderID_UnknownModel_ReturnsNotRegistered(t *testing.T) {
	r := registeredModels(t, asyncImageModel("image-model-v1", "provider-a"))
	// Neither a near miss nor a different spelling resolves: only the exact Model ID does.
	for _, modelID := range []string{"", "image-model-v2", "image-model", "Image-Model-v1", " image-model-v1"} {
		got, err := r.ProviderID(modelID)
		if !errors.Is(err, ErrModelNotRegistered) || got != "" {
			t.Fatalf("ProviderID(%q) = %q, %v; want \"\", ErrModelNotRegistered", modelID, got, err)
		}
	}
}

func TestModelRegistry_ProviderID_ModelWithoutProvider_ReturnsNotDeclared(t *testing.T) {
	r := registeredModels(t, validModelRegistration("text-model-v1"))
	got, err := r.ProviderID("text-model-v1")
	if !errors.Is(err, ErrModelProviderNotDeclared) || got != "" {
		t.Fatalf("ProviderID(text-model-v1) = %q, %v; want \"\", ErrModelProviderNotDeclared", got, err)
	}
}

func TestModelRegistry_ProviderID_DuplicateModelID_KeepsFirstRegistration(t *testing.T) {
	r := registeredModels(t, asyncImageModel("image-model-v1", "provider-a"))
	second := &stubModelProvider{models: []ModelRegistration{asyncImageModel("image-model-v1", "provider-b")}}
	if err := r.Register(context.Background(), second); err == nil {
		t.Fatal("Register() error = nil, want error for an already registered model id")
	}
	got, err := r.ProviderID("image-model-v1")
	if err != nil || got != "provider-a" {
		t.Fatalf("ProviderID(image-model-v1) = %q, %v; want the first registration's provider-a", got, err)
	}
}

func TestModelRegistry_Register_ImageGenerationWithoutProviderID_Rejected(t *testing.T) {
	provider := &stubModelProvider{models: []ModelRegistration{asyncImageModel("image-model-v1", "")}}
	r := NewModelRegistry()
	if err := r.Register(context.Background(), provider); err == nil {
		t.Fatal("Register() error = nil, want error for an image_generation model without a Provider ID")
	}
	if _, _, ok := r.Get("image-model-v1"); ok {
		t.Fatal("Get() ok = true, want a rejected model left unregistered")
	}
}

func TestModelRegistry_Register_SynchronousModelsWithoutProviderID_Accepted(t *testing.T) {
	reference := validModelRegistration("reference-image-model-v1")
	reference.Capabilities = []string{domain.ModelCapabilityReferenceImageGeneration}
	r := registeredModels(t, validModelRegistration("text-model-v1"), reference)
	if _, _, ok := r.Get("reference-image-model-v1"); !ok {
		t.Fatal("Get(reference-image-model-v1) ok = false, want a synchronous image model accepted without a Provider ID")
	}
}
