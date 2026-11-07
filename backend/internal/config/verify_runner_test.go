package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/config"
)

func TestConfig_VerifyRunnerAbsent_IsDisabled(t *testing.T) {
	cfg, err := config.Load(lookup(completeEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.VerifyRunner.Enabled() || cfg.VerifyRunner.BaseURL != "" {
		t.Fatalf("VerifyRunner = %+v with no key set, want disabled", cfg.VerifyRunner)
	}
}

func TestConfig_VerifyRunnerSet_LoadsBaseURL(t *testing.T) {
	env := completeEnv()
	env[config.KeyVerifyRunnerBaseURL] = " http://sandboxrunner:9103/ "
	cfg, err := config.Load(lookup(env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.VerifyRunner.Enabled() || cfg.VerifyRunner.BaseURL != "http://sandboxrunner:9103" {
		t.Fatalf("VerifyRunner = %+v, want the configured URL without its trailing slash", cfg.VerifyRunner)
	}
}

func TestConfig_VerifyRunnerInvalidURL_IsRejectedWithoutTheValue(t *testing.T) {
	for name, value := range map[string]string{
		"relative": "sandboxrunner:9103",
		"scheme":   "ftp://sandboxrunner:9103",
		"userinfo": "http://user:runner-pass@sandboxrunner:9103",
		"query":    "http://sandboxrunner:9103?token=runner-pass",
	} {
		t.Run(name, func(t *testing.T) {
			env := completeEnv()
			env[config.KeyVerifyRunnerBaseURL] = value
			_, err := config.Load(lookup(env))
			var invalid *config.InvalidConfigError
			if !errors.As(err, &invalid) || invalid.Key != config.KeyVerifyRunnerBaseURL {
				t.Fatalf("Load error = %v, want InvalidConfigError for %s", err, config.KeyVerifyRunnerBaseURL)
			}
			if strings.Contains(err.Error(), "runner-pass") {
				t.Fatalf("error %q leaks the configured value", err)
			}
		})
	}
}
