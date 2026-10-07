package agent

import (
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
