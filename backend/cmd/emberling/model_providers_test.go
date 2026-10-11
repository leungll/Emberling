package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// mockModelIDs lists the Model IDs the always-present mock Provider registers.
func mockModelIDs(t *testing.T) []string {
	t.Helper()
	registrations, err := mockmodel.NewProvider().Models(context.Background())
	if err != nil {
		t.Fatalf("mock Models() error = %v", err)
	}
	var ids []string
	for _, registration := range registrations {
		ids = append(ids, registration.ID)
	}
	return ids
}

func registeredModelIDs(models *registry.ModelRegistry) []string {
	var ids []string
	for _, metadata := range models.ListMetadata() {
		ids = append(ids, metadata.ID)
	}
	return ids
}

// upstreamModels is a Chat Completions stand-in that records the model name of every
// request it receives and answers each with a FINAL Decision.
type upstreamModels struct {
	mu   sync.Mutex
	seen []string
}

func (u *upstreamModels) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(raw, &body)
		u.mu.Lock()
		u.seen = append(u.seen, body.Model)
		u.mu.Unlock()
		content, _ := json.Marshal(`{"kind":"FINAL","tool":null,"arguments":null,"output":"\"ok\"","statePatch":null}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","choices":[{"message":{"content":`+string(content)+`},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func (u *upstreamModels) last() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.seen) == 0 {
		return ""
	}
	return u.seen[len(u.seen)-1]
}

func TestRegisterModelProviders_ThreeModels_EachResolvesToOwnUpstreamName(t *testing.T) {
	const key = "sk-registration-secret"
	upstream := &upstreamModels{}
	server := upstream.serve(t)
	entries := []config.OpenAIModelEntry{
		{ID: "opus-5.5", Model: "vendor/opus-upstream", DisplayName: "Opus 5.5"},
		{ID: "gemini-3.1", Model: "vendor/gemini-upstream", DisplayName: "Gemini 3.1"},
		{ID: "gpt-5.2", Model: "vendor/gpt-upstream", DisplayName: "GPT 5.2"},
	}
	models := registry.NewModelRegistry()
	err := registerModelProviders(context.Background(), models, config.OpenAIModel{
		BaseURL: server.URL, APIKey: config.NewSecret(key), Models: entries,
	})
	if err != nil {
		t.Fatalf("registerModelProviders() error = %v", err)
	}

	wantIDs := append(mockModelIDs(t), "opus-5.5", "gemini-3.1", "gpt-5.2")
	if diff := cmp.Diff(wantIDs, registeredModelIDs(models), cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
		t.Errorf("registered model IDs mismatch (-want +got):\n%s", diff)
	}
	for _, entry := range entries {
		registration, provider, ok := models.Get(entry.ID)
		if !ok {
			t.Fatalf("model %q not registered", entry.ID)
		}
		if registration.DisplayName != entry.DisplayName {
			t.Errorf("model %q DisplayName = %q, want %q", entry.ID, registration.DisplayName, entry.DisplayName)
		}
		_, err := provider.Generate(context.Background(), registry.ModelRequest{
			ModelID:           entry.ID,
			Messages:          []registry.ModelMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
			FinalOutputSchema: json.RawMessage(`{"type":"string"}`),
			DecisionSchema:    registry.DecisionSchema(),
		})
		if err != nil {
			t.Fatalf("Generate(%s) error = %v", entry.ID, err)
		}
		if got := upstream.last(); got != entry.Model {
			t.Errorf("model %q sent upstream model %q, want %q", entry.ID, got, entry.Model)
		}
	}
	for _, metadata := range models.ListMetadata() {
		if strings.Contains(metadata.DisplayName, key) {
			t.Errorf("model metadata leaks the API key: %+v", metadata)
		}
	}
}

func TestRegisterModelProviders_DuplicateID_Fails(t *testing.T) {
	models := registry.NewModelRegistry()
	err := registerModelProviders(context.Background(), models, config.OpenAIModel{
		BaseURL: "https://llm.example/v1", APIKey: config.NewSecret("sk-dup"),
		Models: []config.OpenAIModelEntry{
			{ID: "opus-5.5", Model: "a", DisplayName: "A"},
			{ID: "opus-5.5", Model: "b", DisplayName: "B"},
		},
	})
	if err == nil {
		t.Fatalf("registerModelProviders() error = nil, want a duplicate Model ID refusal; registered %v", registeredModelIDs(models))
	}
	if strings.Contains(err.Error(), "sk-dup") {
		t.Errorf("error leaks the API key: %q", err.Error())
	}
}

func TestRegisterModelProviders_NotConfigured_RegistersMockOnly(t *testing.T) {
	models := registry.NewModelRegistry()
	if err := registerModelProviders(context.Background(), models, config.OpenAIModel{}); err != nil {
		t.Fatalf("registerModelProviders() error = %v", err)
	}
	if diff := cmp.Diff(mockModelIDs(t), registeredModelIDs(models), cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
		t.Errorf("registered model IDs mismatch (-want +got):\n%s", diff)
	}
}
