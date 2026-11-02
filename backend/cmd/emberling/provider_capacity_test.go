package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/config/readiness"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// capacityTestModels registers one gateway image model, so task-gateway-v1 is the only
// Provider any registered model dispatches to.
func capacityTestModels(t *testing.T) *registry.ModelRegistry {
	t.Helper()
	models := registry.NewModelRegistry()
	gateway, err := registerTaskGateway(context.Background(), models, config.TaskGateway{
		BaseURL: "http://taskgateway:9104",
		Models: []config.OpenAIModelEntry{
			{ID: "gateway-image-model", Model: "vendor/image-model", DisplayName: "Image model"},
		},
	}, nil)
	if err != nil || gateway == nil {
		t.Fatalf("registerTaskGateway() = %v, %v", gateway, err)
	}
	return models
}

type startupRecorder struct {
	failed readiness.Step
}

func (r *startupRecorder) StepPassed(readiness.Step, bool)         {}
func (r *startupRecorder) StepFailed(step readiness.Step, _ error) { r.failed = step }

func TestStartup_ProviderCapacityUnknownProvider_NotReady(t *testing.T) {
	models := capacityTestModels(t)
	probe := readiness.NewProbe()
	obs := &startupRecorder{}
	err := probe.Run(context.Background(), readiness.Checks{
		Database:      func(context.Context) error { return nil },
		Configuration: func(context.Context) error { return nil },
		Registry: func(context.Context) error {
			return validateProviderCapacity(models, map[string]int{"task-gateway-v1": 5, "never-registered-provider": 2})
		},
	}, obs)
	var stepErr *readiness.StepError
	if !errors.As(err, &stepErr) || stepErr.Step != readiness.StepRegistry {
		t.Fatalf("Run() error = %v, want a failure at %s", err, readiness.StepRegistry)
	}
	if obs.failed != readiness.StepRegistry || probe.Ready() {
		t.Fatalf("failed step = %s, ready = %v; want %s and not ready", obs.failed, probe.Ready(), readiness.StepRegistry)
	}
	if !strings.Contains(err.Error(), "never-registered-provider") || strings.Contains(err.Error(), "task-gateway-v1") {
		t.Fatalf("error %q should name only the unknown provider", err)
	}
}

func TestValidateProviderCapacity_RegisteredProviders_Accepted(t *testing.T) {
	models := capacityTestModels(t)
	if err := validateProviderCapacity(models, map[string]int{taskGatewayProviderID: 5}); err != nil {
		t.Fatalf("validateProviderCapacity() error = %v, want nil", err)
	}
	if err := validateProviderCapacity(models, nil); err != nil {
		t.Fatalf("validateProviderCapacity(nil) error = %v, want nil", err)
	}
}
