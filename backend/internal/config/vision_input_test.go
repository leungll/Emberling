package config_test

import (
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/config"
)

func TestConfig_ModelListVisionInput_LoadsDeclaration(t *testing.T) {
	cfg, err := config.Load(lookup(withModelList(`[
		{"id":"opus-5.5","model":"vendor/upstream-opus","displayName":"Opus Display","visionInput":true},
		{"id":"gemini-3.1","model":"vendor/upstream-gemini","displayName":"Gemini Display"}
	]`)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	models := cfg.OpenAIModel.Models
	if len(models) != 2 || !models[0].VisionInput || models[1].VisionInput {
		t.Fatalf("Models = %+v, want visionInput on the first entry only", models)
	}
}

func TestConfig_ModelListVisionInputNotBoolean_Fails(t *testing.T) {
	_, err := config.Load(lookup(withModelList(`[{"id":"opus-5.5","model":"vendor/upstream-opus","displayName":"Opus","visionInput":"yes"}]`)))
	if err == nil {
		t.Fatal("Load: error = nil, want a non-boolean visionInput rejected")
	}
	assertNoConfiguredValue(t, err, "vendor/upstream-opus")
}

func TestConfig_ImageModelListVisionInput_Fails(t *testing.T) {
	_, err := config.Load(lookup(withImageModelList(`[{"id":"seedream-5.0-pro","model":"vendor/upstream-seedream","displayName":"Seedream","visionInput":true}]`)))
	if err == nil {
		t.Fatal("Load: error = nil, want visionInput refused on an image model")
	}
	if invalid := imageListError(t, err); !strings.Contains(invalid.Reason, "visionInput") {
		t.Errorf("reason = %q, want it to name visionInput", invalid.Reason)
	}
	assertNoImageValue(t, err, "vendor/upstream-seedream")
}
