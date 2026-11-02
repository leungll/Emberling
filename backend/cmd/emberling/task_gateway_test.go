package main

import (
	"context"
	"testing"

	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

func TestRegisterTaskGateway_Unset_RegistersNoGatewayModels(t *testing.T) {
	models := registry.NewModelRegistry()
	gateway, err := registerTaskGateway(context.Background(), models, config.TaskGateway{}, nil)
	if err != nil {
		t.Fatalf("registerTaskGateway() error = %v", err)
	}
	if gateway != nil {
		t.Fatalf("registerTaskGateway() = %v, want no gateway instance", gateway)
	}
	if listed := models.ListMetadata(); len(listed) != 0 {
		t.Fatalf("registered models = %+v, want none", listed)
	}
}

func TestRegisterTaskGateway_Configured_RegistersGatewayModelsUnderItsProvider(t *testing.T) {
	models := registry.NewModelRegistry()
	gateway, err := registerTaskGateway(context.Background(), models, config.TaskGateway{
		BaseURL: "http://taskgateway:9104",
		Models: []config.OpenAIModelEntry{
			{ID: "gateway-seedream-5.0-pro", Model: "bytedance-seed/seedream-5-0-pro", DisplayName: "Seedream 5.0 Pro", Description: "Through the task gateway."},
		},
	}, nil)
	if err != nil {
		t.Fatalf("registerTaskGateway() error = %v", err)
	}
	if gateway == nil || gateway.ProviderID() != taskGatewayProviderID {
		t.Fatalf("gateway instance = %v, want one reporting %q", gateway, taskGatewayProviderID)
	}
	registration, _, ok := models.Get("gateway-seedream-5.0-pro")
	if !ok || !registration.HasCapability(domain.ModelCapabilityImageGeneration) || registration.Description != "Through the task gateway." {
		t.Fatalf("Get() = %+v, %v; want an image_generation model", registration, ok)
	}
	if got, err := models.ProviderID("gateway-seedream-5.0-pro"); err != nil || got != taskGatewayProviderID {
		t.Fatalf("ProviderID() = %q, %v; want %q", got, err, taskGatewayProviderID)
	}
}
