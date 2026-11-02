package openaimodel_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/openaimodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// partsRequest is a review-shaped request: one user message carrying text and two inline
// images, no Tools, and an object final output Schema.
func partsRequest(image []byte) registry.ModelRequest {
	return registry.ModelRequest{
		ModelID:      testModelID,
		Instructions: "Compare the images.",
		Messages: []registry.ModelMessage{{
			Role: "user",
			Parts: []registry.ContentPart{
				{Type: registry.ContentPartText, Text: "Original photo:"},
				{Type: registry.ContentPartImage, Image: &registry.InlineImage{MediaType: "image/jpeg", Data: image}},
				{Type: registry.ContentPartText, Text: "Candidate:"},
				{Type: registry.ContentPartImage, Image: &registry.InlineImage{MediaType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}}},
			},
		}},
		FinalOutputSchema: raw(`{"type":"object","properties":{"passed":{"type":"boolean"}},"required":["passed"],"additionalProperties":false}`),
		DecisionSchema:    registry.DecisionSchema(),
	}
}

func TestProvider_Generate_ContentParts_SendsTextAndImageURLBlocks(t *testing.T) {
	answer := completion(t, `{"kind":"FINAL","tool":null,"arguments":null,"output":{"passed":true},"statePatch":null}`, "")
	server, seen := fakeProvider(t, http.StatusOK, answer)
	provider := newProvider(t, server.URL, nil)

	image := []byte("jpeg-bytes")
	if _, err := provider.Generate(context.Background(), partsRequest(image)); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(seen.body, &body); err != nil {
		t.Fatalf("request body: %v", err)
	}
	if len(body.Messages) != 2 {
		t.Fatalf("messages = %d, want the system message and one user message", len(body.Messages))
	}
	var system string
	if err := json.Unmarshal(body.Messages[0].Content, &system); err != nil {
		t.Fatalf("system content is not a string: %s", body.Messages[0].Content)
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(body.Messages[1].Content, &parts); err != nil {
		t.Fatalf("user content is not a part array: %s", body.Messages[1].Content)
	}
	if len(parts) != 4 {
		t.Fatalf("parts = %d, want 4", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "Original photo:" || parts[2].Type != "text" {
		t.Errorf("text parts = %+v, want text blocks in order", parts)
	}
	wantURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(image)
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil || parts[1].ImageURL.URL != wantURL {
		t.Errorf("image part 1 = %+v, want an image_url data URL of the photo", parts[1])
	}
	if parts[3].Type != "image_url" || parts[3].ImageURL == nil || !strings.HasPrefix(parts[3].ImageURL.URL, "data:image/png;base64,") {
		t.Errorf("image part 3 = %+v, want a PNG data URL", parts[3])
	}
}

func TestProvider_Generate_StringContent_StaysAString(t *testing.T) {
	answer := completion(t, `{"kind":"FINAL","tool":null,"arguments":null,"output":"done","statePatch":null}`, "")
	server, seen := fakeProvider(t, http.StatusOK, answer)
	provider := newProvider(t, server.URL, nil)
	if _, err := provider.Generate(context.Background(), agentRequest()); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	var body struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(seen.body, &body); err != nil {
		t.Fatal(err)
	}
	for i, message := range body.Messages {
		var text string
		if err := json.Unmarshal(message.Content, &text); err != nil {
			t.Errorf("message %d content = %s, want a JSON string", i, message.Content)
		}
	}
}

func TestProvider_Generate_InvalidContentParts_FailWithoutSending(t *testing.T) {
	cases := map[string]registry.ModelMessage{
		"parts on an assistant message": {Role: "assistant", Parts: []registry.ContentPart{{Type: registry.ContentPartText, Text: "x"}}},
		"image part without an image":   {Role: "user", Parts: []registry.ContentPart{{Type: registry.ContentPartImage}}},
		"image part without bytes":      {Role: "user", Parts: []registry.ContentPart{{Type: registry.ContentPartImage, Image: &registry.InlineImage{MediaType: "image/png"}}}},
		"unknown part type":             {Role: "user", Parts: []registry.ContentPart{{Type: "audio"}}},
		"parts and content together":    {Role: "user", Content: raw(`"x"`), Parts: []registry.ContentPart{{Type: registry.ContentPartText, Text: "x"}}},
	}
	for name, message := range cases {
		t.Run(name, func(t *testing.T) {
			server, seen := fakeProvider(t, http.StatusOK, "{}")
			provider := newProvider(t, server.URL, nil)
			request := partsRequest([]byte("secret-image-bytes"))
			request.Messages = []registry.ModelMessage{message}
			_, err := provider.Generate(context.Background(), request)
			if err == nil {
				t.Fatal("Generate() error = nil, want a conversion error")
			}
			if seen.body != nil {
				t.Error("request was sent, want none")
			}
			if strings.Contains(err.Error(), "secret-image-bytes") || strings.Contains(err.Error(), "base64") {
				t.Errorf("error %q carries image content", err)
			}
		})
	}
}

func TestProvider_Generate_ProviderRejectsImageRequest_ErrorCarriesNoImage(t *testing.T) {
	server, _ := fakeProvider(t, http.StatusBadRequest, `{"error":{"message":"bad image data:image/jpeg;base64,amVwZw==","type":"invalid_request_error"}}`)
	provider := newProvider(t, server.URL, nil)
	image := []byte("jpeg-bytes")
	_, err := provider.Generate(context.Background(), partsRequest(image))
	if err == nil {
		t.Fatal("Generate() error = nil, want the Provider rejection")
	}
	encoded := base64.StdEncoding.EncodeToString(image)
	for _, leak := range []string{encoded, "data:image", string(image)} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error %q carries %q", err, leak)
		}
	}
}

func TestProvider_Models_VisionInput_DeclaresCapability(t *testing.T) {
	for _, vision := range []bool{false, true} {
		provider, err := openaimodel.New(openaimodel.Config{
			ModelID: testModelID, DisplayName: testDisplayName, BaseURL: "http://localhost/v1", APIKey: testKey,
			Model: "gpt-test", DecisionSchema: registry.DecisionSchema(), VisionInput: vision,
		})
		if err != nil {
			t.Fatal(err)
		}
		models, err := provider.Models(context.Background())
		if err != nil || len(models) != 1 {
			t.Fatalf("Models() = %v, %v", models, err)
		}
		if got := slices.Contains(models[0].Capabilities, domain.ModelCapabilityVisionInput); got != vision {
			t.Errorf("VisionInput=%v: declares %s = %v", vision, domain.ModelCapabilityVisionInput, got)
		}
		if !models[0].HasCapability(domain.ModelCapabilityStructuredDecision) {
			t.Errorf("VisionInput=%v: lost %s", vision, domain.ModelCapabilityStructuredDecision)
		}
	}
}

func TestInlineImage_Rendering_OmitsBytes(t *testing.T) {
	image := registry.InlineImage{MediaType: "image/png", Data: []byte("raw-image-bytes")}
	for _, rendered := range []string{image.String(), image.GoString(), image.LogValue().String()} {
		if strings.Contains(rendered, "raw-image-bytes") {
			t.Errorf("rendering %q carries the image bytes", rendered)
		}
	}
}
