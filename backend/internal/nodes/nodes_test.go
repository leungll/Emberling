// Package nodes has no production code of its own; it only proves that every built-in
// Node registers cleanly together against one NodeRegistry and carries the display text
// Studio shows; nodes/textinput, .../prompttemplate, .../textgeneration, .../textoutput,
// .../mediaoutput, .../imageinput, .../mediabrief, .../mediaresult, .../mediatranscript,
// .../imagegeneration and .../agent each still own their focused, per-node tests.
package nodes

import (
	"context"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/externaltask"
	"github.com/leungll/Emberling/backend/internal/adapters/mocktask"
	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/nodes/agent"
	"github.com/leungll/Emberling/backend/internal/nodes/imagegeneration"
	"github.com/leungll/Emberling/backend/internal/nodes/imageinput"
	"github.com/leungll/Emberling/backend/internal/nodes/mediabrief"
	"github.com/leungll/Emberling/backend/internal/nodes/mediaoutput"
	"github.com/leungll/Emberling/backend/internal/nodes/mediaresult"
	"github.com/leungll/Emberling/backend/internal/nodes/mediatranscript"
	"github.com/leungll/Emberling/backend/internal/nodes/prompttemplate"
	"github.com/leungll/Emberling/backend/internal/nodes/taskinput"
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

func (noopModelResolver) ProviderID(string) (string, error) {
	return "", registry.ErrModelNotRegistered
}

// builtinRegistrations returns every built-in Node registration, wired to inert ports:
// nothing here dispatches, so the list only has to be complete. A new built-in Node is
// added here and in the type list of the startup test.
func builtinRegistrations(t *testing.T) []registry.NodeRegistration {
	t.Helper()
	transcript, err := mediatranscript.Registration(mediatranscript.Ports{
		Tasks:     externaltask.New("http://media-service.test", nil),
		Artifacts: asset.NewArtifactStore(t.TempDir(), 1<<20),
	})
	if err != nil {
		t.Fatalf("mediatranscript.Registration() error = %v", err)
	}
	return []registry.NodeRegistration{
		textinput.Registration(),
		transcript,
		taskinput.Registration(),
		imageinput.Registration(),
		mediabrief.Registration(),
		prompttemplate.Registration(),
		textgeneration.Registration(noopModelResolver{}),
		textoutput.Registration(),
		mediaoutput.Registration(),
		mediaresult.Registration(),
		imagegeneration.Registration(noopModelResolver{}, mocktask.New("http://mock-provider.test", nil)),
		agent.Registration(),
	}
}

// TestNodeRegistry_Registrations_PassStartupValidation covers every built-in Node
// registered so far: the synchronous text Nodes, the asynchronous image_generation and
// the managed agent share one startup check rather than a second, parallel list.
func TestNodeRegistry_Registrations_PassStartupValidation(t *testing.T) {
	r := registry.NewNodeRegistry()
	registrations := builtinRegistrations(t)
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
		"task_input":       false,
		"image_input":      false,
		"media_brief":      false,
		"prompt_template":  false,
		"text_generation":  false,
		"text_output":      false,
		"media_output":     false,
		"media_result":     false,
		"image_generation": false,
		"media_transcript": false,
		"agent":            false,
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

// TestNodeRegistrations_CarryDisplayText proves that Studio can describe every built-in
// step and every one of its handles without a per-type fallback table: each Node has a
// description and each input and output port has one too. A port title stays optional.
func TestNodeRegistrations_CarryDisplayText(t *testing.T) {
	for _, reg := range builtinRegistrations(t) {
		meta := reg.Metadata
		if meta.Description == "" {
			t.Errorf("node %q has no description", meta.Type)
		}
		for _, port := range meta.Inputs {
			if port.Description == "" {
				t.Errorf("node %q input port %q has no description", meta.Type, port.Name)
			}
		}
		for _, port := range meta.Outputs {
			if port.Description == "" {
				t.Errorf("node %q output port %q has no description", meta.Type, port.Name)
			}
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
