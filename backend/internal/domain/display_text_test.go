package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// validToolMetadata returns the smallest Tool registration that passes Validate, so each
// display-text case below changes exactly one fact.
func validToolMetadata() ToolMetadata {
	return ToolMetadata{
		Name:          "lookup",
		Description:   "Read a deterministic record",
		InputSchema:   json.RawMessage(`{"type":"object"}`),
		OutputSchema:  json.RawMessage(`{"type":"object"}`),
		SideEffect:    SideEffectPolicy{Kind: SideEffectNone, Idempotency: IdempotencySafe},
		ExecutionKind: ToolExecutionSync,
	}
}

func TestValidateDisplayLine_AcceptsEmptyAndBoundedSingleLine(t *testing.T) {
	cases := []struct {
		name string
		text string
		max  int
	}{
		{"empty is optional", "", MaxDisplayTitleLength},
		{"plain sentence", "Asks for a piece of text each time a run starts.", MaxDisplayDescriptionLength},
		{"exactly at the bound counts runes not bytes", strings.Repeat("é", MaxDisplayTitleLength), MaxDisplayTitleLength},
		{"curly apostrophe", "Shows text as the run’s result.", MaxDisplayDescriptionLength},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateDisplayLine(tc.text, tc.max); err != nil {
				t.Fatalf("ValidateDisplayLine() error = %v, want nil", err)
			}
		})
	}
}

func TestValidateDisplayLine_RejectsWithoutEchoingText(t *testing.T) {
	cases := []struct {
		name string
		text string
		max  int
	}{
		{"one rune over the bound", strings.Repeat("a", MaxDisplayTitleLength+1), MaxDisplayTitleLength},
		{"newline", "first line\nsecond line", MaxDisplayDescriptionLength},
		{"carriage return", "first line\rsecond line", MaxDisplayDescriptionLength},
		{"tab", "a\tb", MaxDisplayDescriptionLength},
		{"other control character", "a\x07b", MaxDisplayDescriptionLength},
		{"invalid UTF-8", "ok\xffnot", MaxDisplayDescriptionLength},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateDisplayLine(tc.text, tc.max)
			if err == nil {
				t.Fatal("ValidateDisplayLine() error = nil, want a rejection")
			}
			// The text is a registration value that must not reach startup errors or logs,
			// so the error names the rule and never the offending input.
			for _, fragment := range []string{"first", "second", "ok", "not", strings.Repeat("a", 8)} {
				if strings.Contains(err.Error(), fragment) {
					t.Fatalf("ValidateDisplayLine() error %q echoes the input fragment %q", err, fragment)
				}
			}
		})
	}
}

func TestNodeMetadata_Validate_RejectsInvalidDisplayText(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*NodeMetadata)
		want   string
	}{
		{"node description too long", func(m *NodeMetadata) {
			m.Description = strings.Repeat("x", MaxDisplayDescriptionLength+1)
		}, "description"},
		{"node description multi-line", func(m *NodeMetadata) {
			m.Description = "one\ntwo"
		}, "description"},
		{"input port title too long", func(m *NodeMetadata) {
			m.Inputs[0].Title = strings.Repeat("x", MaxDisplayTitleLength+1)
		}, `input port "prompt": title`},
		{"output port description with control character", func(m *NodeMetadata) {
			m.Outputs[0].Description = "a\x00b"
		}, `output port "image": description`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta := validNodeMetadata()
			tc.mutate(&meta)
			err := meta.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil, want a rejection")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %q, want it to mention %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "xxxxxxxx") || strings.Contains(err.Error(), "one\ntwo") {
				t.Fatalf("Validate() error %q echoes the display text", err)
			}
		})
	}
}

func TestNodeMetadata_Validate_AcceptsDisplayText(t *testing.T) {
	meta := validNodeMetadata()
	meta.Description = "Creates an image from a prompt with a model."
	meta.Inputs[0].Title = "Prompt"
	meta.Inputs[0].Description = "The prompt sent to the model."
	meta.Outputs[0].Description = "The generated image."
	if err := meta.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestToolMetadata_Validate_TitleBounded(t *testing.T) {
	meta := validToolMetadata()
	meta.Title = "Look up"
	if err := meta.Validate(); err != nil {
		t.Fatalf("Validate() with a short title error = %v, want nil", err)
	}

	meta.Title = strings.Repeat("y", MaxDisplayTitleLength+1)
	err := meta.Validate()
	if err == nil {
		t.Fatal("Validate() with an over-long title error = nil, want a rejection")
	}
	if !strings.Contains(err.Error(), "title") {
		t.Fatalf("Validate() error = %q, want it to mention the title", err)
	}
	if strings.Contains(err.Error(), "yyyyyyyy") {
		t.Fatalf("Validate() error %q echoes the title", err)
	}

	meta.Title = "two\nlines"
	if err := meta.Validate(); err == nil {
		t.Fatal("Validate() with a multi-line title error = nil, want a rejection")
	}
}

func TestMetadata_DisplayTextJSON_SerialisedOnlyWhenSet(t *testing.T) {
	port := PortMetadata{Name: "prompt", DataType: PortTypeText, Required: true}
	if got := string(encodeWire(t, port)); got != `{"name":"prompt","dataType":"text","required":true}` {
		t.Fatalf("port without display text = %s, want title and description absent", got)
	}
	port.Title = "Prompt"
	port.Description = "The prompt sent to the model."
	if got, want := string(encodeWire(t, port)), `{"name":"prompt","dataType":"text","required":true,"title":"Prompt","description":"The prompt sent to the model."}`; got != want {
		t.Fatalf("port with display text = %s, want %s", got, want)
	}

	node := validNodeMetadata()
	if strings.Contains(string(encodeWire(t, node)), `"description"`) {
		t.Fatal("node without a description serialises a description key")
	}
	node.Description = "Creates an image from a prompt with a model."
	if !strings.Contains(string(encodeWire(t, node)), `"description":"Creates an image from a prompt with a model."`) {
		t.Fatal("node with a description does not serialise it")
	}

	tool := validToolMetadata()
	if strings.Contains(string(encodeWire(t, tool)), `"title"`) {
		t.Fatal("tool without a title serialises a title key")
	}
	tool.Title = "Look up"
	if !strings.Contains(string(encodeWire(t, tool)), `"title":"Look up"`) {
		t.Fatal("tool with a title does not serialise it")
	}
}
