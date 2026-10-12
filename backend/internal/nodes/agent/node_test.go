package agent

import (
	"encoding/json"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// TestRegistration_Validate_Passes pins the real `agent` registration's structural
// validity (domain.NodeMetadata.Validate) and its Binding agreement with a ManagedAgent
// ExecutionKind, mirroring the same check internal/config/readiness runs before the
// Backend becomes ready.
func TestRegistration_Validate_Passes(t *testing.T) {
	reg := Registration()
	if err := reg.Metadata.Validate(); err != nil {
		t.Fatalf("Metadata.Validate() unexpected error: %v", err)
	}
	if reg.Binding.Kind() != registry.BindingManagedAgent {
		t.Errorf("Binding.Kind() = %q, want %q", reg.Binding.Kind(), registry.BindingManagedAgent)
	}
}

// TestRegistration_Register_Succeeds exercises the full NodeRegistry.Register path
// (binding agreement, ConfigSchema compilation and UISchema field resolution against the
// compiled schema), not only the metadata shape TestRegistration_Validate_Passes checks.
func TestRegistration_Register_Succeeds(t *testing.T) {
	nodes := registry.NewNodeRegistry()
	if err := nodes.Register(Registration()); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}
}

// TestRegistration_AllowedTools_UsesToolSelector pins the widget Studio needs to offer the
// allowlist as a choice among registered Tools rather than free-text names.
func TestRegistration_AllowedTools_UsesToolSelector(t *testing.T) {
	for _, field := range Registration().Metadata.UISchema.Fields {
		if field.Path == "allowedTools" {
			if field.Widget != domain.UIWidgetToolSelector {
				t.Fatalf("allowedTools widget = %q, want %q", field.Widget, domain.UIWidgetToolSelector)
			}
			return
		}
	}
	t.Fatal("allowedTools has no uiSchema field")
}

// TestRegistration_ModelSelectors_HaveDistinctTitles pins that the decision Model Selector
// and the image Model Selector each carry their own JSON Schema title. Studio labels an
// untitled Model Selector generically, so without titles both dropdowns read the same.
func TestRegistration_ModelSelectors_HaveDistinctTitles(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Title string `json:"title"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(Registration().Metadata.ConfigSchema, &schema); err != nil {
		t.Fatalf("decode ConfigSchema: %v", err)
	}
	want := map[string]string{"modelId": "Decision model", "imageModelId": "Image model"}
	for path, title := range want {
		if got := schema.Properties[path].Title; got != title {
			t.Errorf("%s title = %q, want %q", path, got, title)
		}
	}
}
