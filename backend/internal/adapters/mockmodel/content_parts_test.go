package mockmodel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/registry"
)

func TestProvider_Generate_ContentParts_ReadsTextPartsAsPrompt(t *testing.T) {
	p := NewProvider()
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{{
		Role: "user",
		Parts: []registry.ContentPart{
			{Type: registry.ContentPartText, Text: "compare"},
			{Type: registry.ContentPartImage, Image: &registry.InlineImage{MediaType: "image/png", Data: []byte("raw-image-bytes")}},
			{Type: registry.ContentPartText, Text: "these"},
		},
	}}}
	response, err := p.Generate(context.Background(), request)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	var output string
	if err := json.Unmarshal(response.Decision.Output, &output); err != nil {
		t.Fatalf("output %s is not a string", response.Decision.Output)
	}
	if output != "echo: compare\nthese" {
		t.Errorf("output = %q, want the text parts echoed", output)
	}
	if strings.Contains(output, "raw-image-bytes") {
		t.Error("output carries image bytes")
	}
}

func TestProvider_Generate_ContentPartsDirective_IsHonoured(t *testing.T) {
	p := NewProvider()
	request := registry.ModelRequest{ModelID: ModelID, Messages: []registry.ModelMessage{{
		Role:  "user",
		Parts: []registry.ContentPart{{Type: registry.ContentPartText, Text: "mock:fail"}},
	}}}
	if _, err := p.Generate(context.Background(), request); err == nil {
		t.Fatal("Generate() error = nil, want the mock:fail directive read from a text part")
	}
}
