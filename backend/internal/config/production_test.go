package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/config"
)

func TestConfig_ProductionAbsent_IsDisabled(t *testing.T) {
	cfg, err := config.Load(lookup(completeEnv()))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Production.Enabled() || cfg.Production.BaseURL != "" {
		t.Errorf("Production = %+v, want disabled when %s is unset", cfg.Production, config.KeyProductionBaseURL)
	}
}

func TestConfig_ProductionBaseURL_LoadsTrimmed(t *testing.T) {
	env := completeEnv()
	env[config.KeyProductionBaseURL] = "http://mockproduction:9102/"

	cfg, err := config.Load(lookup(env))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Production.Enabled() || cfg.Production.BaseURL != "http://mockproduction:9102" {
		t.Errorf("Production = %+v, want enabled with the trailing slash trimmed", cfg.Production)
	}
}

func TestConfig_ProductionBaseURLInvalid_ReturnsInvalidConfigErrorWithoutValue(t *testing.T) {
	for name, value := range map[string]string{
		"relative": "mockproduction:9102",
		"scheme":   "ftp://mockproduction:9102",
		"userinfo": "http://deployer:hunter2@mockproduction:9102",
		"query":    "http://mockproduction:9102/?token=hunter2",
		"fragment": "http://mockproduction:9102/#hunter2",
		"no host":  "http://",
	} {
		t.Run(name, func(t *testing.T) {
			env := completeEnv()
			env[config.KeyProductionBaseURL] = value

			_, err := config.Load(lookup(env))

			var invalid *config.InvalidConfigError
			if !errors.As(err, &invalid) || invalid.Key != config.KeyProductionBaseURL {
				t.Fatalf("Load() error = %v, want InvalidConfigError for %s", err, config.KeyProductionBaseURL)
			}
			if strings.Contains(err.Error(), value) || strings.Contains(err.Error(), "hunter2") {
				t.Errorf("error %q quotes the configured value", err)
			}
		})
	}
}
