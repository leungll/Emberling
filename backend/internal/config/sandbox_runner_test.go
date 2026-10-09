package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/config"
)

func TestConfig_SandboxRunnerAbsent_IsDisabled(t *testing.T) {
	cfg, err := config.Load(lookup(completeEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SandboxRunner.Enabled() || cfg.SandboxRunner.BaseURL != "" {
		t.Fatalf("SandboxRunner = %+v with no key set, want disabled", cfg.SandboxRunner)
	}
}

func TestConfig_SandboxRunnerSet_LoadsBaseURL(t *testing.T) {
	env := completeEnv()
	env[config.KeySandboxRunnerBaseURL] = " http://sandboxrunner:9103/ "
	cfg, err := config.Load(lookup(env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.SandboxRunner.Enabled() || cfg.SandboxRunner.BaseURL != "http://sandboxrunner:9103" {
		t.Fatalf("SandboxRunner = %+v, want the configured URL without its trailing slash", cfg.SandboxRunner)
	}
}

func TestConfig_SandboxRunnerInvalidURL_IsRejectedWithoutTheValue(t *testing.T) {
	for name, value := range map[string]string{
		"relative":  "sandboxrunner:9103",
		"scheme":    "ftp://sandboxrunner:9103",
		"userinfo":  "http://user:runner-pass@sandboxrunner:9103",
		"query":     "http://sandboxrunner:9103?token=runner-pass",
		"fragment":  "http://sandboxrunner:9103#runner-pass",
		"host-less": "http://",
	} {
		t.Run(name, func(t *testing.T) {
			env := completeEnv()
			env[config.KeySandboxRunnerBaseURL] = value
			_, err := config.Load(lookup(env))
			var invalid *config.InvalidConfigError
			if !errors.As(err, &invalid) || invalid.Key != config.KeySandboxRunnerBaseURL {
				t.Fatalf("Load error = %v, want InvalidConfigError for %s", err, config.KeySandboxRunnerBaseURL)
			}
			if strings.Contains(err.Error(), "runner-pass") {
				t.Fatalf("error %q leaks the configured value", err)
			}
		})
	}
}
