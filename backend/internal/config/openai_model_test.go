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
	openAITestKey     = "sk-openai-test-credential"
	openAITestBaseURL = "https://llm-host.example/v1/"
	// openAITestModels holds values that must never appear in an error: each one is
	// distinct from every key name and reason text.
	openAITestModels = `[
		{"id":"opus-5.5","model":"vendor/upstream-opus","displayName":"Opus Display"},
		{"id":"gemini-3.1","model":"vendor/upstream-gemini","displayName":"Gemini Display"}
	]`
)

func withOpenAIModels(env map[string]string) map[string]string {
	env[config.KeyModelBaseURL] = openAITestBaseURL
	env[config.KeyModelAPIKey] = openAITestKey
	env[config.KeyModels] = openAITestModels
	return env
}

func withModelList(list string) map[string]string {
	env := withOpenAIModels(completeEnv())
	env[config.KeyModels] = list
	return env
}

// assertNoConfiguredValue fails when err repeats the API key, the base URL or a value
// from the model list.
func assertNoConfiguredValue(t *testing.T, err error, values ...string) {
	t.Helper()
	for _, value := range append([]string{openAITestKey, "llm-host.example"}, values...) {
		if strings.Contains(err.Error(), value) {
			t.Errorf("error repeats configured value %q: %q", value, err.Error())
		}
	}
}

// modelListError requires err to be an InvalidConfigError for the model list key.
func modelListError(t *testing.T, err error) *config.InvalidConfigError {
	t.Helper()
	var invalid *config.InvalidConfigError
	if !errors.As(err, &invalid) || invalid.Key != config.KeyModels {
		t.Fatalf("Load: want InvalidConfigError for %s, got %v", config.KeyModels, err)
	}
	return invalid
}

func TestConfig_OpenAIModelAbsent_IsDisabled(t *testing.T) {
	cfg, err := config.Load(lookup(completeEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OpenAIModel.Enabled() {
		t.Fatalf("OpenAIModel.Enabled() = true with no model keys set, want false")
	}
}

func TestConfig_OpenAIModelListComplete_LoadsEveryEntry(t *testing.T) {
	env := withOpenAIModels(completeEnv())
	env[config.KeyModelTimeout] = "45s"

	cfg, err := config.Load(lookup(env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := cfg.OpenAIModel
	if !got.Enabled() {
		t.Fatal("OpenAIModel.Enabled() = false, want true")
	}
	if got.BaseURL != "https://llm-host.example/v1" {
		t.Errorf("BaseURL = %q, want the configured URL without its trailing slash", got.BaseURL)
	}
	if got.APIKey.Reveal() != openAITestKey || got.Timeout != 45*time.Second {
		t.Errorf("OpenAIModel timeout %v, want 45s and the configured key", got.Timeout)
	}
	want := []config.OpenAIModelEntry{
		{ID: "opus-5.5", Model: "vendor/upstream-opus", DisplayName: "Opus Display"},
		{ID: "gemini-3.1", Model: "vendor/upstream-gemini", DisplayName: "Gemini Display"},
	}
	if diff := cmp.Diff(want, got.Models); diff != "" {
		t.Errorf("Models mismatch (-want +got):\n%s", diff)
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
		{"key only", []string{config.KeyModelAPIKey}, []string{config.KeyModelBaseURL, config.KeyModels}},
		{"base URL and models", []string{config.KeyModelBaseURL, config.KeyModels}, []string{config.KeyModelAPIKey}},
		{"models only", []string{config.KeyModels}, []string{config.KeyModelBaseURL, config.KeyModelAPIKey}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			full := withOpenAIModels(map[string]string{})
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
			assertNoConfiguredValue(t, err, "upstream-opus", "Opus Display")
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
		{"relative base URL", config.KeyModelBaseURL, "llm.example/v1", true},
		{"base URL with credentials", config.KeyModelBaseURL, "https://user:pw@llm.example/v1", true},
		{"base URL with query", config.KeyModelBaseURL, "https://llm.example/v1?key=x", true},
		{"non-positive timeout", config.KeyModelTimeout, "0s", true},
		{"unparsable timeout", config.KeyModelTimeout, "soon", true},
		{"timeout without the group", config.KeyModelTimeout, "30s", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := completeEnv()
			if tc.group {
				env = withOpenAIModels(env)
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

func TestConfig_ModelListPartialEntry_FailsListingKeysOnly(t *testing.T) {
	for _, tc := range []struct {
		name  string
		list  string
		field string
	}{
		{"no id", `[{"id":"opus-5.5","model":"vendor/upstream-opus","displayName":"Opus Display"},{"model":"vendor/upstream-hidden","displayName":"Hidden Display"}]`, "entry 1: id"},
		{"no model", `[{"id":"opus-5.5","displayName":"Opus Display"}]`, "entry 0: model"},
		{"blank model", `[{"id":"opus-5.5","model":"  ","displayName":"Opus Display"}]`, "entry 0: model"},
		{"no displayName", `[{"id":"opus-5.5","model":"vendor/upstream-opus"}]`, "entry 0: displayName"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(lookup(withModelList(tc.list)))

			invalid := modelListError(t, err)
			if !strings.Contains(invalid.Reason, tc.field) {
				t.Errorf("reason = %q, want it to name %q", invalid.Reason, tc.field)
			}
			assertNoConfiguredValue(t, err, "opus-5.5", "upstream-opus", "Opus Display", "upstream-hidden", "Hidden Display")
		})
	}
}

func TestConfig_ModelListDuplicateID_Fails(t *testing.T) {
	list := `[
		{"id":"opus-5.5","model":"vendor/upstream-opus","displayName":"Opus Display"},
		{"id":"gemini-3.1","model":"vendor/upstream-gemini","displayName":"Gemini Display"},
		{"id":"opus-5.5","model":"vendor/upstream-other","displayName":"Other Display"}
	]`

	_, err := config.Load(lookup(withModelList(list)))

	invalid := modelListError(t, err)
	if !strings.Contains(invalid.Reason, "entry 2: id") || !strings.Contains(invalid.Reason, "entry 0") {
		t.Errorf("reason = %q, want it to name entry 2 as duplicating entry 0", invalid.Reason)
	}
	assertNoConfiguredValue(t, err, "opus-5.5", "upstream-other", "Other Display")
}

func TestConfig_ModelListInvalidID_Fails(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   string
	}{
		{"uppercase letter", "Opus-5.5"},
		{"starts with a digit", "5opus"},
		{"starts with a dot", ".opus"},
		{"contains a space", "opus 5.5"},
		{"contains an underscore", "opus_5"},
		{"contains a slash", "vendor/opus"},
		{"longer than 64 characters", "a" + strings.Repeat("b", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list := fmt.Sprintf(`[{"id":%q,"model":"vendor/upstream-opus","displayName":"Opus Display"}]`, tc.id)

			_, err := config.Load(lookup(withModelList(list)))

			invalid := modelListError(t, err)
			if !strings.Contains(invalid.Reason, "entry 0: id") {
				t.Errorf("reason = %q, want it to name entry 0 field id", invalid.Reason)
			}
			assertNoConfiguredValue(t, err, tc.id, "upstream-opus", "Opus Display")
		})
	}
}

func TestConfig_ModelListValidIDForms_Load(t *testing.T) {
	for _, id := range []string{"a", "opus-5.5", "gemini-3.1-pro", "m" + strings.Repeat("0", 63)} {
		list := fmt.Sprintf(`[{"id":%q,"model":"vendor/m","displayName":"M"}]`, id)
		cfg, err := config.Load(lookup(withModelList(list)))
		if err != nil {
			t.Errorf("Load with id %q: %v", id, err)
			continue
		}
		if len(cfg.OpenAIModel.Models) != 1 || cfg.OpenAIModel.Models[0].ID != id {
			t.Errorf("Models = %+v, want the single entry %q", cfg.OpenAIModel.Models, id)
		}
	}
}

func TestConfig_ModelListMalformed_Fails(t *testing.T) {
	for _, tc := range []struct {
		name string
		list string
	}{
		{"empty array", `[]`},
		{"not JSON", `opus-5.5=vendor/upstream-opus`},
		{"object instead of array", `{"id":"opus-5.5","model":"vendor/upstream-opus","displayName":"Opus Display"}`},
		{"entry is not an object", `["opus-5.5"]`},
		{"field is not a string", `[{"id":"opus-5.5","model":42,"displayName":"Opus Display"}]`},
		{"unknown field", `[{"id":"opus-5.5","model":"vendor/upstream-opus","displayName":"Opus Display","provider":"vendor"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(lookup(withModelList(tc.list)))

			_ = modelListError(t, err)
			assertNoConfiguredValue(t, err, "opus-5.5", "upstream-opus", "Opus Display")
		})
	}
}

// legacyKeys maps every removed OpenAI-named key to the key that replaced it.
var legacyKeys = []config.RemovedKey{
	{Key: config.KeyRemovedOpenAIBaseURL, Replacement: config.KeyModelBaseURL},
	{Key: config.KeyRemovedOpenAIAPIKey, Replacement: config.KeyModelAPIKey},
	{Key: config.KeyRemovedOpenAIModel, Replacement: config.KeyModels},
	{Key: config.KeyRemovedOpenAIModels, Replacement: config.KeyModels},
	{Key: config.KeyRemovedOpenAITimeout, Replacement: config.KeyModelTimeout},
}

// TestConfig_LegacyOpenAIKeys_FailNamingKeysOnly pins that a deployment still using an
// OpenAI-named key fails at startup whatever else is configured, and that the error names
// the old and the new key without repeating the old value.
func TestConfig_LegacyOpenAIKeys_FailNamingKeysOnly(t *testing.T) {
	const legacyValue = "legacy-configured-value"
	for _, legacy := range legacyKeys {
		for _, base := range []struct {
			name string
			env  func() map[string]string
		}{
			{"alone", completeEnv},
			{"beside a complete model list", func() map[string]string { return withOpenAIModels(completeEnv()) }},
		} {
			t.Run(legacy.Key+" "+base.name, func(t *testing.T) {
				env := base.env()
				env[legacy.Key] = legacyValue

				_, err := config.Load(lookup(env))

				var removed *config.RemovedConfigError
				if !errors.As(err, &removed) {
					t.Fatalf("Load: want *config.RemovedConfigError, got %v", err)
				}
				if diff := cmp.Diff([]config.RemovedKey{legacy}, removed.Keys); diff != "" {
					t.Errorf("removed keys mismatch (-want +got):\n%s", diff)
				}
				for _, key := range []string{legacy.Key, legacy.Replacement} {
					if !strings.Contains(err.Error(), key) {
						t.Errorf("error %q does not name %s", err.Error(), key)
					}
				}
				assertNoConfiguredValue(t, err, legacyValue, "upstream-opus")
			})
		}
	}

	t.Run("every legacy key at once is reported together", func(t *testing.T) {
		env := completeEnv()
		for _, legacy := range legacyKeys {
			env[legacy.Key] = legacyValue
		}

		_, err := config.Load(lookup(env))

		var removed *config.RemovedConfigError
		if !errors.As(err, &removed) {
			t.Fatalf("Load: want *config.RemovedConfigError, got %v", err)
		}
		if diff := cmp.Diff(legacyKeys, removed.Keys); diff != "" {
			t.Errorf("removed keys mismatch (-want +got):\n%s", diff)
		}
		assertNoConfiguredValue(t, err, legacyValue)
	})
}

// TestConfig_Validate_RejectsInvalidModelList covers a Config assembled without Load: the
// readiness sequence re-checks the model list, so such a Config cannot become ready.
func TestConfig_Validate_RejectsInvalidModelList(t *testing.T) {
	for _, tc := range []struct {
		name   string
		models []config.OpenAIModelEntry
	}{
		{"empty list", nil},
		{"duplicate id", []config.OpenAIModelEntry{
			{ID: "opus-5.5", Model: "a", DisplayName: "A"},
			{ID: "opus-5.5", Model: "b", DisplayName: "B"},
		}},
		{"invalid id", []config.OpenAIModelEntry{{ID: "Opus", Model: "a", DisplayName: "A"}}},
		{"empty displayName", []config.OpenAIModelEntry{{ID: "opus-5.5", Model: "a"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(lookup(withOpenAIModels(completeEnv())))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			cfg.OpenAIModel.Models = tc.models

			err = cfg.Validate()

			var missing *config.MissingConfigError
			var invalid *config.InvalidConfigError
			switch {
			case errors.As(err, &missing):
				if fmt.Sprint(missing.Keys) != fmt.Sprint([]string{config.KeyModels}) {
					t.Errorf("missing keys = %v, want only %s", missing.Keys, config.KeyModels)
				}
			case errors.As(err, &invalid):
				if invalid.Key != config.KeyModels {
					t.Errorf("invalid key = %s, want %s", invalid.Key, config.KeyModels)
				}
			default:
				t.Fatalf("Validate: want a model list error, got %v", err)
			}
		})
	}
}

func TestConfig_OpenAIModel_SerialisationRedactsAPIKey(t *testing.T) {
	cfg, err := config.Load(lookup(withOpenAIModels(completeEnv())))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	for _, rendered := range []string{string(encoded), fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg)} {
		if strings.Contains(rendered, openAITestKey) {
			t.Errorf("rendered Config leaks the model API key: %s", rendered)
		}
	}
}

func TestConfig_OpenRouterRequireParameters_DefaultsOff(t *testing.T) {
	cfg, err := config.Load(lookup(withOpenAIModels(completeEnv())))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OpenAIModel.OpenRouterRequireParameters {
		t.Errorf("OpenRouterRequireParameters = true with %s unset, want false", config.KeyModelOpenRouterRequireParameters)
	}
}

func TestConfig_OpenRouterRequireParameters_ParsesBoolean(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"true", true}, {"1", true}, {"TRUE", true}, {" true ", true},
		{"false", false}, {"0", false}, {"", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			env := withOpenAIModels(completeEnv())
			env[config.KeyModelOpenRouterRequireParameters] = tc.value

			cfg, err := config.Load(lookup(env))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.OpenAIModel.OpenRouterRequireParameters != tc.want {
				t.Errorf("OpenRouterRequireParameters = %v for %q, want %v", cfg.OpenAIModel.OpenRouterRequireParameters, tc.value, tc.want)
			}
			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate on a loaded Config: %v", err)
			}
		})
	}
}

// TestConfig_OpenRouterRequireParametersInvalid_FailsNamingKeyOnly covers a value that is
// not a boolean: startup fails naming the key, and the value is not repeated.
func TestConfig_OpenRouterRequireParametersInvalid_FailsNamingKeyOnly(t *testing.T) {
	for _, value := range []string{"yes-xyzzy", "on", "2"} {
		t.Run(value, func(t *testing.T) {
			env := withOpenAIModels(completeEnv())
			env[config.KeyModelOpenRouterRequireParameters] = value

			_, err := config.Load(lookup(env))

			var invalid *config.InvalidConfigError
			if !errors.As(err, &invalid) || invalid.Key != config.KeyModelOpenRouterRequireParameters {
				t.Fatalf("Load: want InvalidConfigError for %s, got %v", config.KeyModelOpenRouterRequireParameters, err)
			}
			if strings.Contains(err.Error(), `"`+value+`"`) || strings.Contains(err.Error(), "xyzzy") {
				t.Errorf("error repeats the configured value: %q", err.Error())
			}
			assertNoConfiguredValue(t, err, "upstream-opus", "Opus Display")
		})
	}
}

// TestConfig_OpenRouterRequireParametersWithoutModels_Fails covers the switch turned on
// for a model group that is not configured: a misconfiguration, not a silent no-op.
func TestConfig_OpenRouterRequireParametersWithoutModels_Fails(t *testing.T) {
	env := completeEnv()
	env[config.KeyModelOpenRouterRequireParameters] = "true"

	_, err := config.Load(lookup(env))

	var invalid *config.InvalidConfigError
	if !errors.As(err, &invalid) || invalid.Key != config.KeyModelOpenRouterRequireParameters {
		t.Fatalf("Load: want InvalidConfigError for %s, got %v", config.KeyModelOpenRouterRequireParameters, err)
	}
}

func TestConfig_OpenRouterRequireParametersOffWithoutModels_Loads(t *testing.T) {
	env := completeEnv()
	env[config.KeyModelOpenRouterRequireParameters] = "false"

	cfg, err := config.Load(lookup(env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OpenAIModel.Enabled() || cfg.OpenAIModel.OpenRouterRequireParameters {
		t.Errorf("OpenAIModel enabled %v, switch %v; want both off", cfg.OpenAIModel.Enabled(), cfg.OpenAIModel.OpenRouterRequireParameters)
	}
}

// TestConfig_Validate_RejectsOpenRouterSwitchWithoutModels covers a Config assembled
// without Load: the readiness sequence re-checks that the switch has a model group.
func TestConfig_Validate_RejectsOpenRouterSwitchWithoutModels(t *testing.T) {
	cfg, err := config.Load(lookup(completeEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.OpenAIModel.OpenRouterRequireParameters = true

	err = cfg.Validate()

	var invalid *config.InvalidConfigError
	if !errors.As(err, &invalid) || invalid.Key != config.KeyModelOpenRouterRequireParameters {
		t.Fatalf("Validate: want InvalidConfigError for %s, got %v", config.KeyModelOpenRouterRequireParameters, err)
	}
}
