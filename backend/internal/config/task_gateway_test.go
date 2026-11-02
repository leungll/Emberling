package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/config"
)

const (
	gatewayTestBaseURL = "http://taskgateway:9104/"
	gatewayModelsValue = `[{"id":"gateway-seedream-5.0-pro","model":"bytedance-seed/seedream-5-0-pro","displayName":"Seedream 5.0 Pro (gateway)"}]`
)

func withTaskGateway(env map[string]string) map[string]string {
	env[config.KeyTaskGatewayBaseURL] = gatewayTestBaseURL
	env[config.KeyTaskGatewayModels] = gatewayModelsValue
	return env
}

func gatewayModelsError(t *testing.T, err error) *config.InvalidConfigError {
	t.Helper()
	var invalid *config.InvalidConfigError
	if !errors.As(err, &invalid) || invalid.Key != config.KeyTaskGatewayModels {
		t.Fatalf("Load: want InvalidConfigError for %s, got %v", config.KeyTaskGatewayModels, err)
	}
	return invalid
}

func TestParseTaskGatewayModels_ValidList_ReturnsEntries(t *testing.T) {
	entries, err := config.ParseTaskGatewayModels(gatewayModelsValue)
	if err != nil {
		t.Fatalf("ParseTaskGatewayModels() error = %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "gateway-seedream-5.0-pro" || entries[0].Model != "bytedance-seed/seedream-5-0-pro" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestParseTaskGatewayModels_InvalidList_RejectedWithoutValues(t *testing.T) {
	for name, value := range map[string]string{
		"not json":       `secret-looking-value`,
		"without prefix": `[{"id":"seedream-secret","model":"m/x","displayName":"X"}]`,
		"uppercase id":   `[{"id":"gateway-Secret","model":"m/x","displayName":"X"}]`,
		"unknown member": `[{"id":"gateway-x","model":"m/x","displayName":"X","apiKey":"secret"}]`,
		"empty":          `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.ParseTaskGatewayModels(value)
			var invalid *config.InvalidConfigError
			if !errors.As(err, &invalid) || invalid.Key != config.KeyTaskGatewayModels {
				t.Fatalf("error = %v, want an InvalidConfigError for %s", err, config.KeyTaskGatewayModels)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "Secret") {
				t.Fatalf("error %q quotes the configured value", err)
			}
		})
	}
}

func TestLoad_TaskGatewayUnset_Disabled(t *testing.T) {
	cfg, err := config.Load(lookup(completeEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TaskGateway.Enabled() || cfg.TaskGateway.BaseURL != "" || len(cfg.TaskGateway.Models) != 0 {
		t.Fatalf("TaskGateway = %+v, want disabled", cfg.TaskGateway)
	}
}

func TestLoad_TaskGatewayComplete_Enabled(t *testing.T) {
	cfg, err := config.Load(lookup(withTaskGateway(completeEnv())))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.TaskGateway.Enabled() || cfg.TaskGateway.BaseURL != "http://taskgateway:9104" {
		t.Fatalf("TaskGateway = %+v, want enabled with the trimmed base URL", cfg.TaskGateway)
	}
	if len(cfg.TaskGateway.Models) != 1 || cfg.TaskGateway.Models[0].ID != "gateway-seedream-5.0-pro" {
		t.Fatalf("TaskGateway.Models = %+v", cfg.TaskGateway.Models)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestLoad_TaskGatewayPartial_Rejected(t *testing.T) {
	for missing, set := range map[string]string{
		config.KeyTaskGatewayModels:  config.KeyTaskGatewayBaseURL,
		config.KeyTaskGatewayBaseURL: config.KeyTaskGatewayModels,
	} {
		t.Run("only "+set, func(t *testing.T) {
			env := withTaskGateway(completeEnv())
			delete(env, missing)
			_, err := config.Load(lookup(env))
			var missingErr *config.MissingConfigError
			if !errors.As(err, &missingErr) || len(missingErr.Keys) != 1 || missingErr.Keys[0] != missing {
				t.Fatalf("Load: error = %v, want MissingConfigError naming %s", err, missing)
			}
			if strings.Contains(err.Error(), "taskgateway:9104") || strings.Contains(err.Error(), "seedream") {
				t.Fatalf("error %q quotes a configured value", err)
			}
		})
	}
}

func TestLoad_TaskGatewayModelIDCollision_Rejected(t *testing.T) {
	collidingImage := withImageModels(withTaskGateway(completeEnv()))
	collidingImage[config.KeyImageModels] = `[{"id":"gateway-seedream-5.0-pro","model":"vendor/x","displayName":"X"}]`
	collidingDecision := withTaskGateway(completeEnv())
	collidingDecision[config.KeyModelBaseURL] = "https://decision.example/api/v1"
	collidingDecision[config.KeyModelAPIKey] = "sk-decision"
	collidingDecision[config.KeyModels] = `[{"id":"gateway-seedream-5.0-pro","model":"vendor/y","displayName":"Y"}]`
	for name, env := range map[string]map[string]string{"image model": collidingImage, "decision model": collidingDecision} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(lookup(env))
			invalid := gatewayModelsError(t, err)
			if !strings.Contains(invalid.Reason, "duplicates") {
				t.Fatalf("reason = %q, want a duplicate id", invalid.Reason)
			}
		})
	}
}

func TestLoad_TaskGatewayModelIDWithoutPrefix_Rejected(t *testing.T) {
	env := withTaskGateway(completeEnv())
	env[config.KeyTaskGatewayModels] = `[{"id":"seedream-5.0-pro","model":"bytedance-seed/seedream-5-0-pro","displayName":"Seedream"}]`
	_, err := config.Load(lookup(env))
	invalid := gatewayModelsError(t, err)
	if !strings.Contains(invalid.Reason, config.TaskGatewayModelIDPrefix) || strings.Contains(err.Error(), "bytedance") {
		t.Fatalf("reason = %q, want the prefix rule without the configured value", invalid.Reason)
	}
}

func TestLoad_TaskGatewayBaseURLWithCredential_Rejected(t *testing.T) {
	env := withTaskGateway(completeEnv())
	env[config.KeyTaskGatewayBaseURL] = "http://user:secret@taskgateway:9104"
	_, err := config.Load(lookup(env))
	var invalid *config.InvalidConfigError
	if !errors.As(err, &invalid) || invalid.Key != config.KeyTaskGatewayBaseURL || strings.Contains(err.Error(), "secret") {
		t.Fatalf("Load: error = %v, want InvalidConfigError for %s without the value", err, config.KeyTaskGatewayBaseURL)
	}
}
