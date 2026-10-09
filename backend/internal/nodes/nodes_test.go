// Package nodes has no production code of its own; it only proves that every built-in
// Node registers cleanly together against one NodeRegistry; nodes/textinput, .../
// prompttemplate, .../textgeneration, .../textoutput, .../mediaoutput, .../imageinput,
// .../mediabrief and .../imagegeneration each still own their focused, per-node tests.
package nodes

import (
	"context"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mocktask"
	"github.com/leungll/Emberling/backend/internal/nodes/imagegeneration"
	"github.com/leungll/Emberling/backend/internal/nodes/imageinput"
	"github.com/leungll/Emberling/backend/internal/nodes/mediabrief"
	"github.com/leungll/Emberling/backend/internal/nodes/mediaoutput"
	"github.com/leungll/Emberling/backend/internal/nodes/prompttemplate"
	"github.com/leungll/Emberling/backend/internal/nodes/textgeneration"
	"github.com/leungll/Emberling/backend/internal/nodes/textinput"
	"github.com/leungll/Emberling/backend/internal/nodes/textoutput"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// noopModelResolver satisfies textgeneration.ModelResolver without registering any model;
// Text Generation's Registration only needs a resolver value to construct its Binding, and
// startup validation never calls ValidateSemantics.
type noopModelResolver struct{}

func (noopModelResolver) Get(string) (registry.ModelRegistration, registry.ModelProvider, bool) {
	return registry.ModelRegistration{}, nil, false
}

// TestNodeRegistry_Registrations_PassStartupValidation covers every built-in Node
// registered so far: the synchronous text Nodes and the asynchronous image_generation
// share one startup check rather than a second, parallel list.
func TestNodeRegistry_Registrations_PassStartupValidation(t *testing.T) {
	r := registry.NewNodeRegistry()
	registrations := []registry.NodeRegistration{
		textinput.Registration(),
		imageinput.Registration(),
		mediabrief.Registration(),
		prompttemplate.Registration(),
		textgeneration.Registration(noopModelResolver{}),
		textoutput.Registration(),
		mediaoutput.Registration(),
		imagegeneration.Registration(noopModelResolver{}, mocktask.New("http://mock-provider.test", nil)),
	}
	for _, reg := range registrations {
		if err := r.Register(reg); err != nil {
			t.Fatalf("Register(%q) error = %v, want nil", reg.Metadata.Type, err)
		}
	}

	list := r.ListMetadata()
	if len(list) != len(registrations) {
		t.Fatalf("len(ListMetadata()) = %d, want %d", len(list), len(registrations))
	}
	wantTypes := map[string]bool{
		"text_input":       false,
		"image_input":      false,
		"media_brief":      false,
		"prompt_template":  false,
		"text_generation":  false,
		"text_output":      false,
		"media_output":     false,
		"image_generation": false,
	}
	for _, meta := range list {
		if _, known := wantTypes[meta.Type]; !known {
			t.Fatalf("ListMetadata() contains unexpected type %q", meta.Type)
		}
		wantTypes[meta.Type] = true
	}
	for typ, seen := range wantTypes {
		if !seen {
			t.Fatalf("ListMetadata() is missing built-in node type %q", typ)
		}
	}
}

// TestNodeRegistry_M1Registrations_ValidateSemanticsDispatchesPerNode is a smoke test that
// ValidateSemantics reaches each node's own Executor once registered together, not just a
// registration-time check.
func TestNodeRegistry_M1Registrations_ValidateSemanticsDispatchesPerNode(t *testing.T) {
	r := registry.NewNodeRegistry()
	for _, reg := range []registry.NodeRegistration{
		textinput.Registration(),
		prompttemplate.Registration(),
		textoutput.Registration(),
	} {
		if err := r.Register(reg); err != nil {
			t.Fatalf("Register(%q) error = %v, want nil", reg.Metadata.Type, err)
		}
	}

	if err := r.ValidateSemantics(context.Background(), "text_input", map[string]any{
		"inputKey": "brief", "required": true, "minLength": float64(1), "maxLength": float64(2),
	}); err != nil {
		t.Fatalf("ValidateSemantics(text_input) error = %v, want nil", err)
	}
	if err := r.ValidateSemantics(context.Background(), "prompt_template", map[string]any{
		"template": "{{text}}",
	}); err != nil {
		t.Fatalf("ValidateSemantics(prompt_template) error = %v, want nil", err)
	}
	if err := r.ValidateSemantics(context.Background(), "text_output", map[string]any{}); err != nil {
		t.Fatalf("ValidateSemantics(text_output) error = %v, want nil", err)
	}
}
