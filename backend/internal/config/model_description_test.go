package config_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/config"
)

// TestConfig_ModelListDescription_OptionalAndBounded pins the optional description of a
// model list entry: absent or blank means none, a value is trimmed and kept, and a value
// longer than 280 characters or spanning more than one line fails startup with an error
// that names only the entry index and the field.
func TestConfig_ModelListDescription_OptionalAndBounded(t *testing.T) {
	t.Run("absent and blank load without a description", func(t *testing.T) {
		list := `[
			{"id":"opus-5.5","model":"vendor/upstream-opus","displayName":"Opus Display"},
			{"id":"gemini-3.1","model":"vendor/upstream-gemini","displayName":"Gemini Display","description":"   "}
		]`
		cfg, err := config.Load(lookup(withModelList(list)))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		for _, entry := range cfg.OpenAIModel.Models {
			if entry.Description != "" {
				t.Errorf("entry %s Description = %q, want empty", entry.ID, entry.Description)
			}
		}
	})

	t.Run("a value is trimmed and kept", func(t *testing.T) {
		list := `[{"id":"opus-5.5","model":"vendor/upstream-opus","displayName":"Opus Display","description":"  Strong at long reasoning; moderate speed.  "}]`
		cfg, err := config.Load(lookup(withModelList(list)))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		want := []config.OpenAIModelEntry{{
			ID: "opus-5.5", Model: "vendor/upstream-opus", DisplayName: "Opus Display",
			Description: "Strong at long reasoning; moderate speed.",
		}}
		if diff := cmp.Diff(want, cfg.OpenAIModel.Models); diff != "" {
			t.Errorf("Models mismatch (-want +got):\n%s", diff)
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate on a loaded Config: %v", err)
		}
	})

	t.Run("exactly 280 characters loads", func(t *testing.T) {
		// Multi-byte characters prove the bound counts characters, not bytes.
		description := strings.Repeat("é", 280)
		list := fmt.Sprintf(`[{"id":"opus-5.5","model":"vendor/upstream-opus","displayName":"Opus Display","description":%q}]`, description)
		cfg, err := config.Load(lookup(withModelList(list)))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got := cfg.OpenAIModel.Models[0].Description; got != description {
			t.Errorf("Description has %d characters, want the configured 280", len([]rune(got)))
		}
	})

	for _, tc := range []struct {
		name        string
		description string
	}{
		{"longer than 280 characters", "secret-description-" + strings.Repeat("x", 262)},
		{"contains a line break", "secret-description first line\nsecond line"},
		{"contains a carriage return", "secret-description first line\rsecond line"},
		{"contains a tab", "secret-description\twith a tab"},
	} {
		t.Run(tc.name+" fails naming index and field only", func(t *testing.T) {
			list := fmt.Sprintf(`[
				{"id":"gemini-3.1","model":"vendor/upstream-gemini","displayName":"Gemini Display"},
				{"id":"opus-5.5","model":"vendor/upstream-opus","displayName":"Opus Display","description":%q}
			]`, tc.description)

			_, err := config.Load(lookup(withModelList(list)))

			invalid := modelListError(t, err)
			if !strings.Contains(invalid.Reason, "entry 1: description") {
				t.Errorf("reason = %q, want it to name entry 1 field description", invalid.Reason)
			}
			assertNoConfiguredValue(t, err, "secret-description", "opus-5.5", "upstream-opus", "Opus Display")
		})
	}

	t.Run("a non-string value fails", func(t *testing.T) {
		list := `[{"id":"opus-5.5","model":"vendor/upstream-opus","displayName":"Opus Display","description":42}]`

		_, err := config.Load(lookup(withModelList(list)))

		invalid := modelListError(t, err)
		if !strings.Contains(invalid.Reason, "entry 0") {
			t.Errorf("reason = %q, want it to name entry 0", invalid.Reason)
		}
		assertNoConfiguredValue(t, err, "opus-5.5", "upstream-opus", "Opus Display")
	})

	t.Run("Validate re-checks a Config assembled without Load", func(t *testing.T) {
		cfg, err := config.Load(lookup(withOpenAIModels(completeEnv())))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		cfg.OpenAIModel.Models = []config.OpenAIModelEntry{{
			ID: "opus-5.5", Model: "a", DisplayName: "A", Description: strings.Repeat("x", 281),
		}}

		invalid := modelListError(t, cfg.Validate())
		if !strings.Contains(invalid.Reason, "entry 0: description") {
			t.Errorf("reason = %q, want it to name entry 0 field description", invalid.Reason)
		}
	})
}
