package openaimodel_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// TestProvider_Generate_ToolsJSONCarriesNoDisplayTitle proves that a Tool's display title
// is Studio-only: a registered Tool may carry one, but the Tools section the model reads
// is built from the model-facing members alone, so no "title" key appears in it at any
// depth. The spec is built from ToolMetadata the way the Runtime builds it, member by
// member, which is what keeps the title out.
func TestProvider_Generate_ToolsJSONCarriesNoDisplayTitle(t *testing.T) {
	registered := domain.ToolMetadata{
		Name:         "lookup",
		Title:        "Look up",
		Description:  "Read a deterministic record",
		InputSchema:  raw(`{"type":"object","properties":{"key":{"type":"string"}}}`),
		OutputSchema: raw(`{"type":"object","properties":{"value":{"type":"string"}}}`),
	}
	request := agentRequest()
	request.Tools = []registry.ModelToolSpec{{
		Name:         registered.Name,
		Description:  registered.Description,
		InputSchema:  registered.InputSchema,
		OutputSchema: registered.OutputSchema,
	}}

	content := `{"kind":"FINAL","tool":null,"arguments":null,"output":"Paris","statePatch":null}`
	server, seen := fakeProvider(t, http.StatusOK, completion(t, content, `{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}`))
	provider := newProvider(t, server.URL, server.Client())
	if _, err := provider.Generate(context.Background(), request); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	tools := requestToolsJSON(t, seen.body)
	if len(tools) != 1 {
		t.Fatalf("tools section lists %d tools, want 1: %v", len(tools), tools)
	}
	if got := tools[0]["name"]; got != "lookup" {
		t.Fatalf("tools[0].name = %v, want lookup", got)
	}
	if got := tools[0]["description"]; got != registered.Description {
		t.Fatalf("tools[0].description = %v, want the model-facing description", got)
	}
	if path, found := findKey(tools, "title", "tools"); found {
		t.Fatalf("tools section carries a title key at %s; the display title must stay out of the model request", path)
	}
	if strings.Contains(string(seen.body), registered.Title) {
		t.Fatalf("request body carries the display title %q", registered.Title)
	}
}

// requestToolsJSON returns the decoded Tools section of the request's system message.
func requestToolsJSON(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var request struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if len(request.Messages) == 0 || request.Messages[0].Role != "system" {
		t.Fatalf("request has no leading system message: %s", body)
	}
	var system string
	if err := json.Unmarshal(request.Messages[0].Content, &system); err != nil {
		t.Fatalf("system message content is not a string: %v", err)
	}
	const marker = "## Tools\n"
	start := strings.Index(system, marker)
	if start < 0 {
		t.Fatalf("system message has no Tools section: %q", system)
	}
	section := system[start+len(marker):]
	if end := strings.Index(section, "\n\n## "); end >= 0 {
		section = section[:end]
	}
	var tools []map[string]any
	if err := json.Unmarshal([]byte(section), &tools); err != nil {
		t.Fatalf("Tools section is not a JSON array: %v\n%s", err, section)
	}
	return tools
}

// findKey walks a decoded JSON value and reports the path of the first object member
// named key, at any depth.
func findKey(value any, key string, path string) (string, bool) {
	switch v := value.(type) {
	case map[string]any:
		for name, member := range v {
			memberPath := path + "." + name
			if name == key {
				return memberPath, true
			}
			if found, ok := findKey(member, key, memberPath); ok {
				return found, true
			}
		}
	case []map[string]any:
		for i, item := range v {
			if found, ok := findKey(item, key, path+"["+strconv.Itoa(i)+"]"); ok {
				return found, true
			}
		}
	case []any:
		for i, item := range v {
			if found, ok := findKey(item, key, path+"["+strconv.Itoa(i)+"]"); ok {
				return found, true
			}
		}
	}
	return "", false
}
