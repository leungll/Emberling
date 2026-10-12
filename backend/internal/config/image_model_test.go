package config_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/config"
)

const (
	imageTestKey     = "sk-image-test-credential"
	imageTestBaseURL = "https://image-host.example/api/v1/"
	// imageTestModels holds values that must never appear in an error.
	imageTestModels = `[
		{"id":"seedream-5.0-pro","model":"vendor/upstream-seedream","displayName":"Seedream Display","description":"Strong stylisation from a reference photo."},
		{"id":"gpt-image-2.5","model":"vendor/upstream-gpt-image","displayName":"GPT Image Display"}
	]`
)

func withImageModels(env map[string]string) map[string]string {
	env[config.KeyImageBaseURL] = imageTestBaseURL
	env[config.KeyImageAPIKey] = imageTestKey
	env[config.KeyImageModels] = imageTestModels
	return env
}

func withImageModelList(list string) map[string]string {
	env := withImageModels(completeEnv())
	env[config.KeyImageModels] = list
	return env
}

func assertNoImageValue(t *testing.T, err error, values ...string) {
	t.Helper()
	for _, value := range append([]string{imageTestKey, "image-host.example"}, values...) {
		if strings.Contains(err.Error(), value) {
			t.Errorf("error repeats configured value %q: %q", value, err.Error())
		}
	}
}

func imageListError(t *testing.T, err error) *config.InvalidConfigError {
	t.Helper()
	var invalid *config.InvalidConfigError
	if !errors.As(err, &invalid) || invalid.Key != config.KeyImageModels {
		t.Fatalf("Load: want InvalidConfigError for %s, got %v", config.KeyImageModels, err)
	}
	return invalid
}

func TestConfig_ImageGroupAbsent_IsDisabled(t *testing.T) {
	cfg, err := config.Load(lookup(completeEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ImageModel.Enabled() {
		t.Fatalf("ImageModel.Enabled() = true with no image keys set, want false")
	}
}

func TestConfig_ImageGroupComplete_LoadsEveryEntry(t *testing.T) {
	env := withImageModels(completeEnv())
	env[config.KeyImageTimeout] = "90s"

	cfg, err := config.Load(lookup(env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := cfg.ImageModel
	if !got.Enabled() {
		t.Fatal("ImageModel.Enabled() = false with the group set, want true")
	}
	if got.BaseURL != "https://image-host.example/api/v1" {
		t.Errorf("BaseURL = %q, want the trailing slash removed", got.BaseURL)
	}
	if got.APIKey.Reveal() != imageTestKey {
		t.Error("APIKey does not hold the configured key")
	}
	if got.Timeout != 90*time.Second {
		t.Errorf("Timeout = %v, want 90s", got.Timeout)
	}
	want := []config.ImageModelEntry{
		{ID: "seedream-5.0-pro", Model: "vendor/upstream-seedream", DisplayName: "Seedream Display", Description: "Strong stylisation from a reference photo."},
		{ID: "gpt-image-2.5", Model: "vendor/upstream-gpt-image", DisplayName: "GPT Image Display"},
	}
	if diff := cmp.Diff(want, got.Models); diff != "" {
		t.Errorf("Models mismatch (-want +got):\n%s", diff)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate on a loaded Config: %v", err)
	}
}

func TestConfig_ImageTimeout_DefaultsToFiveMinutes(t *testing.T) {
	cfg, err := config.Load(lookup(withImageModels(completeEnv())))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ImageModel.Timeout != 5*time.Minute {
		t.Errorf("Timeout = %v with %s unset, want 5m", cfg.ImageModel.Timeout, config.KeyImageTimeout)
	}
	if config.DefaultImageTimeout != 5*time.Minute {
		t.Errorf("DefaultImageTimeout = %v, want 5m", config.DefaultImageTimeout)
	}
}

func TestConfig_ImageGroupPartial_FailsListingKeysOnly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		set     []string
		missing []string
	}{
		{"key only", []string{config.KeyImageAPIKey}, []string{config.KeyImageBaseURL, config.KeyImageModels}},
		{"base URL and models", []string{config.KeyImageBaseURL, config.KeyImageModels}, []string{config.KeyImageAPIKey}},
		{"models only", []string{config.KeyImageModels}, []string{config.KeyImageBaseURL, config.KeyImageAPIKey}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			full := withImageModels(map[string]string{})
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
			assertNoImageValue(t, err, "seedream-5.0-pro", "upstream-seedream", "Seedream Display")
		})
	}
}

func TestConfig_ImageGroupInvalidValue_ReturnsInvalidConfigError(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		value string
		group bool
	}{
		{"relative base URL", config.KeyImageBaseURL, "image-host.example/v1", true},
		{"base URL with credentials", config.KeyImageBaseURL, "https://user:pw@image-host.example/v1", true},
		{"base URL with fragment", config.KeyImageBaseURL, "https://image-host.example/v1#x", true},
		{"non-positive timeout", config.KeyImageTimeout, "0s", true},
		{"unparsable timeout", config.KeyImageTimeout, "soon", true},
		{"timeout without the group", config.KeyImageTimeout, "30s", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := completeEnv()
			if tc.group {
				env = withImageModels(env)
			}
			env[tc.key] = tc.value

			_, err := config.Load(lookup(env))

			var invalid *config.InvalidConfigError
			if !errors.As(err, &invalid) || invalid.Key != tc.key {
				t.Fatalf("Load with %s: want InvalidConfigError for that key, got %v", tc.key, err)
			}
			assertNoImageValue(t, err, "pw@")
		})
	}
}

func TestConfig_ImageModelList_SharesModelListRules(t *testing.T) {
	for _, tc := range []struct {
		name   string
		list   string
		reason string
	}{
		{"not an array", `{"id":"seedream-5.0-pro"}`, "JSON array"},
		{"empty list", `[]`, "at least one model"},
		{"unknown field", `[{"id":"seedream-5.0-pro","model":"vendor/upstream-seedream","displayName":"Seedream Display","price":"1"}]`, "entry 0"},
		{"no model", `[{"id":"seedream-5.0-pro","displayName":"Seedream Display"}]`, "entry 0: model"},
		{"invalid id", `[{"id":"Seedream","model":"vendor/upstream-seedream","displayName":"Seedream Display"}]`, "entry 0: id"},
		{"duplicate id", `[
			{"id":"seedream-5.0-pro","model":"vendor/upstream-seedream","displayName":"Seedream Display"},
			{"id":"seedream-5.0-pro","model":"vendor/upstream-other","displayName":"Other Display"}
		]`, "entry 1: id duplicates entry 0"},
		{"multi-line description", `[{"id":"seedream-5.0-pro","model":"vendor/upstream-seedream","displayName":"Seedream Display","description":"one\ntwo"}]`, "entry 0: description"},
		{"overlong description", `[{"id":"seedream-5.0-pro","model":"vendor/upstream-seedream","displayName":"Seedream Display","description":"` + strings.Repeat("x", 281) + `"}]`, "entry 0: description"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(lookup(withImageModelList(tc.list)))

			invalid := imageListError(t, err)
			if !strings.Contains(invalid.Reason, tc.reason) {
				t.Errorf("reason = %q, want it to contain %q", invalid.Reason, tc.reason)
			}
			assertNoImageValue(t, err, "seedream-5.0-pro", "upstream-seedream", "Seedream Display", "upstream-other")
		})
	}
}

func TestConfig_ImageModelIDDuplicatesDecisionModel_Fails(t *testing.T) {
	env := withOpenAIModels(withImageModels(completeEnv()))
	env[config.KeyImageModels] = `[
		{"id":"seedream-5.0-pro","model":"vendor/upstream-seedream","displayName":"Seedream Display"},
		{"id":"gemini-3.1","model":"vendor/upstream-gemini-image","displayName":"Gemini Image Display"}
	]`

	_, err := config.Load(lookup(env))

	invalid := imageListError(t, err)
	if !strings.Contains(invalid.Reason, "entry 1: id") || !strings.Contains(invalid.Reason, config.KeyModels+" entry 1") {
		t.Errorf("reason = %q, want it to name image entry 1 as duplicating %s entry 1", invalid.Reason, config.KeyModels)
	}
	assertNoImageValue(t, err, "gemini-3.1", "upstream-gemini-image", "Gemini Image Display")

	cfg, loadErr := config.Load(lookup(withOpenAIModels(withImageModels(completeEnv()))))
	if loadErr != nil {
		t.Fatalf("Load with distinct IDs: %v", loadErr)
	}
	cfg.ImageModel.Models = append(cfg.ImageModel.Models, config.ImageModelEntry{ID: "opus-5.5", Model: "x", DisplayName: "X"})
	if err := cfg.Validate(); err == nil {
		t.Error("Validate with an image ID that repeats a decision model ID: error = nil, want a failure")
	}
}

func TestConfig_ImageModel_SerialisationRedactsAPIKey(t *testing.T) {
	cfg, err := config.Load(lookup(withImageModels(completeEnv())))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	for _, rendered := range []string{string(encoded), fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg)} {
		if strings.Contains(rendered, imageTestKey) {
			t.Errorf("rendered Config leaks the image API key: %s", rendered)
		}
	}
}

func TestConfig_Validate_RejectsIncompleteImageGroup(t *testing.T) {
	cfg, err := config.Load(lookup(withImageModels(completeEnv())))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.ImageModel.APIKey = config.Secret{}

	err = cfg.Validate()

	var missing *config.MissingConfigError
	if !errors.As(err, &missing) || fmt.Sprint(missing.Keys) != fmt.Sprint([]string{config.KeyImageAPIKey}) {
		t.Errorf("Validate: want missing %s only, got %v", config.KeyImageAPIKey, err)
	}

	cfg, _ = config.Load(lookup(withImageModels(completeEnv())))
	cfg.ImageModel.Timeout = 0
	var invalid *config.InvalidConfigError
	if err := cfg.Validate(); !errors.As(err, &invalid) || invalid.Key != config.KeyImageTimeout {
		t.Errorf("Validate with a zero image timeout: want InvalidConfigError for %s, got %v", config.KeyImageTimeout, err)
	}
}
