package runtime

import (
	"encoding/json"
	"strings"
	"testing"
)

// wantPathPrefix asserts every error's Path starts with "input", matching the
// ConfigSchema stage's "nodes[<id>].config" + instance-location convention (Path reads
// "input/document" style, not a bare instance pointer).
func wantPathPrefix(t *testing.T, errs []ValidationError, prefix string) {
	t.Helper()
	for _, e := range errs {
		if !strings.HasPrefix(e.Path, prefix) {
			t.Errorf("error Path = %q, want prefix %q: %+v", e.Path, prefix, e)
		}
	}
}

const testFrozenSchema = `{"type":"object","additionalProperties":false,"properties":{"document":{"type":"string","minLength":1,"maxLength":20000}},"required":["document"]}`

// TestValidateRunInput_AdditionalProperty_Rejected asserts the frozen schema's
// additionalProperties: false is enforced: a Run.input carrying an extra top-level key
// beyond what the Definition's Input Nodes declared must be rejected, because the
// generated top-level Schema is fixed to additionalProperties: false.
func TestValidateRunInput_AdditionalProperty_Rejected(t *testing.T) {
	input := json.RawMessage(`{"document":"hello","extra":"nope"}`)

	errs := ValidateRunInput(json.RawMessage(testFrozenSchema), input)
	if len(errs) == 0 {
		t.Fatal("ValidateRunInput() expected errors, got none")
	}
	wantPathPrefix(t, errs, "input")
}

// TestValidateRunInput_MissingRequired_Rejected asserts a required Input Node's key must
// be present in Run.input.
func TestValidateRunInput_MissingRequired_Rejected(t *testing.T) {
	input := json.RawMessage(`{}`)

	errs := ValidateRunInput(json.RawMessage(testFrozenSchema), input)
	if len(errs) == 0 {
		t.Fatal("ValidateRunInput() expected errors, got none")
	}
	wantPathPrefix(t, errs, "input")
}

// TestValidateRunInput_OptionalAbsent_Accepted asserts an optional Input Node's key may
// be omitted from Run.input without error, using aigc_media.json's shape (one required
// "brief", one optional "reference").
func TestValidateRunInput_OptionalAbsent_Accepted(t *testing.T) {
	frozenSchema := json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"brief":{"type":"string"},"reference":{"type":"object"}},"required":["brief"]}`)
	input := json.RawMessage(`{"brief":"a creative brief"}`)

	errs := ValidateRunInput(frozenSchema, input)
	if len(errs) != 0 {
		t.Fatalf("ValidateRunInput() unexpected errors: %+v", errs)
	}
}

// TestValidateRunInput_ValidInput_Accepted is the positive control for the
// document_processing.json schema: a well-formed input satisfying type, minLength and
// maxLength must be accepted.
func TestValidateRunInput_ValidInput_Accepted(t *testing.T) {
	input := json.RawMessage(`{"document":"hello world"}`)

	errs := ValidateRunInput(json.RawMessage(testFrozenSchema), input)
	if len(errs) != 0 {
		t.Fatalf("ValidateRunInput() unexpected errors: %+v", errs)
	}
}

// TestValidateRunInput_BelowMinLength_Rejected asserts the frozen minLength constraint
// (copied verbatim from the Input Node's config) is enforced against the instance.
func TestValidateRunInput_BelowMinLength_Rejected(t *testing.T) {
	input := json.RawMessage(`{"document":""}`)

	errs := ValidateRunInput(json.RawMessage(testFrozenSchema), input)
	if len(errs) == 0 {
		t.Fatal("ValidateRunInput() expected errors, got none")
	}
	wantPathPrefix(t, errs, "input")
	if errs[0].Path != "input/document" {
		t.Errorf("Path = %q, want %q (input-prefixed, consistent with the ConfigSchema stage's nodes[<id>].config convention)", errs[0].Path, "input/document")
	}
}
