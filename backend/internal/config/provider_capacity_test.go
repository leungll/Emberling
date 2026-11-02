package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/config"
)

func providerCapacityError(t *testing.T, err error) *config.InvalidConfigError {
	t.Helper()
	var invalid *config.InvalidConfigError
	if !errors.As(err, &invalid) || invalid.Key != config.KeyProviderCapacity {
		t.Fatalf("want InvalidConfigError for %s, got %v", config.KeyProviderCapacity, err)
	}
	return invalid
}

func TestLoad_ProviderCapacity_ValidList_Parsed(t *testing.T) {
	env := completeEnv()
	env[config.KeyProviderCapacity] = " task-gateway-v1=5, mock-task-provider-v1 = 12 "
	cfg, err := config.Load(lookup(env))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := map[string]int{"task-gateway-v1": 5, "mock-task-provider-v1": 12}
	if len(cfg.ProviderCapacity) != len(want) {
		t.Fatalf("ProviderCapacity = %v, want %v", cfg.ProviderCapacity, want)
	}
	for id, limit := range want {
		if cfg.ProviderCapacity[id] != limit {
			t.Fatalf("ProviderCapacity[%q] = %d, want %d", id, cfg.ProviderCapacity[id], limit)
		}
	}
}

func TestLoad_ProviderCapacity_Empty_NoLimits(t *testing.T) {
	for name, value := range map[string]string{"unset": "", "blank": "   "} {
		t.Run(name, func(t *testing.T) {
			env := completeEnv()
			if value != "" {
				env[config.KeyProviderCapacity] = value
			}
			cfg, err := config.Load(lookup(env))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if len(cfg.ProviderCapacity) != 0 {
				t.Fatalf("ProviderCapacity = %v, want no limits", cfg.ProviderCapacity)
			}
		})
	}
}

func TestLoad_ProviderCapacity_MalformedEntry_Rejected(t *testing.T) {
	for name, value := range map[string]string{
		"no equals":     "secret-provider",
		"empty id":      "=5",
		"empty limit":   "secret-provider=",
		"not a number":  "secret-provider=five",
		"trailing junk": "secret-provider=5,",
		"id with space": "secret provider=5",
	} {
		t.Run(name, func(t *testing.T) {
			env := completeEnv()
			env[config.KeyProviderCapacity] = value
			_, err := config.Load(lookup(env))
			invalid := providerCapacityError(t, err)
			if strings.Contains(invalid.Reason, "secret") {
				t.Fatalf("reason %q quotes the configured value", invalid.Reason)
			}
			if !strings.Contains(invalid.Reason, "entry ") {
				t.Fatalf("reason %q names no entry index", invalid.Reason)
			}
		})
	}
}

func TestLoad_ProviderCapacity_NonPositiveLimit_Rejected(t *testing.T) {
	for name, value := range map[string]string{"zero": "p=0", "negative": "p=-3"} {
		t.Run(name, func(t *testing.T) {
			env := completeEnv()
			env[config.KeyProviderCapacity] = value
			_, err := config.Load(lookup(env))
			invalid := providerCapacityError(t, err)
			if !strings.Contains(invalid.Reason, "positive integer") {
				t.Fatalf("reason = %q, want the positive-integer rule", invalid.Reason)
			}
		})
	}
}

func TestLoad_ProviderCapacity_DuplicateProvider_Rejected(t *testing.T) {
	env := completeEnv()
	env[config.KeyProviderCapacity] = "p=5,q=2,p=7"
	_, err := config.Load(lookup(env))
	invalid := providerCapacityError(t, err)
	if !strings.Contains(invalid.Reason, "entry 2") || !strings.Contains(invalid.Reason, "repeats") {
		t.Fatalf("reason = %q, want entry 2 reported as a repeat", invalid.Reason)
	}
}

func TestConfig_Validate_ProviderCapacityNonPositive_Rejected(t *testing.T) {
	cfg, err := config.Load(lookup(completeEnv()))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	cfg.ProviderCapacity = map[string]int{"p": 0}
	invalid := providerCapacityError(t, cfg.Validate())
	if !strings.Contains(invalid.Reason, "positive integer") {
		t.Fatalf("reason = %q, want the positive-integer rule", invalid.Reason)
	}
}
