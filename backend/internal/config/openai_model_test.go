package config_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/config"
)

const openAITestKey = "sk-openai-test-credential"

func withOpenAIModel(env map[string]string) map[string]string {
	env[config.KeyModelOpenAIBaseURL] = "https://llm.example/v1/"
	env[config.KeyModelOpenAIAPIKey] = openAITestKey
	env[config.KeyModelOpenAIModel] = "gpt-test"
	return env
}

func TestConfig_OpenAIModelAbsent_IsDisabled(t *testing.T) {
	cfg, err := config.Load(lookup(completeEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OpenAIModel.Enabled() {
		t.Fatalf("OpenAIModel.Enabled() = true with no OpenAI keys set, want false")
	}
}

func TestConfig_OpenAIModelComplete_LoadsGroup(t *testing.T) {
	env := withOpenAIModel(completeEnv())
	env[config.KeyModelOpenAITimeout] = "45s"

	cfg, err := config.Load(lookup(env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := cfg.OpenAIModel
	if !got.Enabled() {
		t.Fatal("OpenAIModel.Enabled() = false, want true")
	}
	if got.BaseURL != "https://llm.example/v1" {
		t.Errorf("BaseURL = %q, want the configured URL without its trailing slash", got.BaseURL)
	}
	if got.APIKey.Reveal() != openAITestKey || got.Model != "gpt-test" || got.Timeout != 45*time.Second {
		t.Errorf("OpenAIModel = model %q timeout %v, want gpt-test 45s and the configured key", got.Model, got.Timeout)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate on a loaded Config: %v", err)
	}
}

func TestConfig_OpenAIModelPartial_ReturnsMissingKeys(t *testing.T) {
	for _, tc := range []struct {
		name    string
		set     []string
		missing []string
	}{
		{"key only", []string{config.KeyModelOpenAIAPIKey}, []string{config.KeyModelOpenAIBaseURL, config.KeyModelOpenAIModel}},
		{"base URL and model", []string{config.KeyModelOpenAIBaseURL, config.KeyModelOpenAIModel}, []string{config.KeyModelOpenAIAPIKey}},
		{"model only", []string{config.KeyModelOpenAIModel}, []string{config.KeyModelOpenAIBaseURL, config.KeyModelOpenAIAPIKey}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			full := withOpenAIModel(map[string]string{})
			env := completeEnv()
			for _, key := range tc.set {
				env[key] = full[key]
			}

			_, err := config.Load(lookup(env))

			var missing *config.MissingConfigError
			if !errors.As(err, &missing) {
				t.Fatalf("Load: want *config.MissingConfigError, got %v", err)
			}
			if fmt.Sprint(missing.Keys) != fmt.Sprint(tc.missing) {
				t.Errorf("missing keys = %v, want %v", missing.Keys, tc.missing)
			}
			if strings.Contains(err.Error(), openAITestKey) {
				t.Errorf("error leaks the API key: %q", err.Error())
			}
		})
	}
}

func TestConfig_OpenAIModelInvalidValue_ReturnsInvalidConfigError(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		value string
		group bool
	}{
		{"relative base URL", config.KeyModelOpenAIBaseURL, "llm.example/v1", true},
		{"base URL with credentials", config.KeyModelOpenAIBaseURL, "https://user:pw@llm.example/v1", true},
		{"base URL with query", config.KeyModelOpenAIBaseURL, "https://llm.example/v1?key=x", true},
		{"non-positive timeout", config.KeyModelOpenAITimeout, "0s", true},
		{"unparsable timeout", config.KeyModelOpenAITimeout, "soon", true},
		{"timeout without the group", config.KeyModelOpenAITimeout, "30s", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := completeEnv()
			if tc.group {
				env = withOpenAIModel(env)
			}
			env[tc.key] = tc.value

			_, err := config.Load(lookup(env))

			var invalid *config.InvalidConfigError
			if !errors.As(err, &invalid) || invalid.Key != tc.key {
				t.Fatalf("Load with %s=%s: want InvalidConfigError for that key, got %v", tc.key, tc.value, err)
			}
			if strings.Contains(err.Error(), "pw@") || strings.Contains(err.Error(), openAITestKey) {
				t.Errorf("error repeats a configured value: %q", err.Error())
			}
		})
	}
}

func TestConfig_OpenAIModel_SerialisationRedactsAPIKey(t *testing.T) {
	cfg, err := config.Load(lookup(withOpenAIModel(completeEnv())))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	for _, rendered := range []string{string(encoded), fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg)} {
		if strings.Contains(rendered, openAITestKey) {
			t.Errorf("rendered Config leaks the OpenAI API key: %s", rendered)
		}
	}
}
