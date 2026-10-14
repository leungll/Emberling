//go:build integration

package contract

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/adapters/openrouterimage"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
)

// The live demo Definition is run by hand against a deployment that registers real
// models; no test or demo script executes it. These IDs are the ones it selects, and a
// deployment must register them under exactly these names.
const (
	liveDemoDefinitionPath = "../../../scripts/demo/definitions/photo_effect_template_live.json"
	liveDecisionModelID    = "opus-5.5"
	liveImageModelID       = "seedream-5.0-pro"
	liveAltImageModelID    = "gpt-image-2.5"
)

// configuredImageModel builds one OpenRouter image Provider registered as id, the way
// the process registers one Provider per configured image model. Its endpoint fails the
// test if it is ever called: validating and saving a Definition never generate an image.
func configuredImageModel(t *testing.T, id string) registry.ModelProvider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("image model %s was called; no image call is expected", id)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	provider, err := openrouterimage.New(openrouterimage.Config{
		ModelID:       id,
		DisplayName:   id,
		BaseURL:       server.URL,
		APIKey:        "sk-contract-image",
		Model:         "vendor/" + id,
		HTTPClient:    server.Client(),
		MaxImageBytes: defaultTestArtifactMaxBytes,
	})
	if err != nil {
		t.Fatalf("openrouterimage.New(%s): %v", id, err)
	}
	return provider
}

// liveDemoDefinition reads the live demo Definition as a create request.
func liveDemoDefinition(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(liveDemoDefinitionPath)
	if err != nil {
		t.Fatalf("read live demo Definition: %v", err)
	}
	if !json.Valid(raw) {
		t.Fatal("live demo Definition is not valid JSON")
	}
	return raw
}

type liveDemoGraph struct {
	Nodes []struct {
		ID     string          `json:"id"`
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	} `json:"nodes"`
	Edges json.RawMessage `json:"edges"`
}

type liveDemoAgentConfig struct {
	Instructions       string          `json:"instructions"`
	ModelID            string          `json:"modelId"`
	ImageModelID       string          `json:"imageModelId"`
	AllowedTools       []string        `json:"allowedTools"`
	MaxTurns           int             `json:"maxTurns"`
	MaxGenerationCalls int             `json:"maxGenerationCalls"`
	TimeoutMs          int             `json:"timeoutMs"`
	OutputSchema       json.RawMessage `json:"outputSchema"`
}

func decodeLiveDemoGraph(t *testing.T, raw []byte) (liveDemoGraph, liveDemoAgentConfig) {
	t.Helper()
	graph := decodeBody[liveDemoGraph](t, raw)
	for _, node := range graph.Nodes {
		if node.ID == "node_agent" {
			return graph, decodeBody[liveDemoAgentConfig](t, node.Config)
		}
	}
	t.Fatalf("graph has no node_agent: %s", raw)
	return liveDemoGraph{}, liveDemoAgentConfig{}
}

// TestLiveDemoDefinition_RealModelsRegistered_ValidatesAndSaves covers the live photo
// demo Definition against a Backend whose Registry holds the decision and image models it
// selects: it validates, saves with the selected IDs verbatim, keeps the deterministic
// photo template's graph, and sets the long deadline and generation budget a real image
// model needs. No model is called.
func TestLiveDemoDefinition_RealModelsRegistered_ValidatesAndSaves(t *testing.T) {
	env := newTestEnvWithOptions(t, testEnvOptions{
		ExtraModelProviders: []registry.ModelProvider{
			configuredModel(t, liveDecisionModelID),
			configuredImageModel(t, liveImageModelID),
			configuredImageModel(t, liveAltImageModelID),
		},
	})
	request := liveDemoDefinition(t)

	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions/validate", request)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/definitions/validate status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	if result := decodeBody[struct {
		Valid bool `json:"valid"`
	}](t, body); !result.Valid {
		t.Fatalf("live demo Definition is not valid: %s", body)
	}

	resp, body = env.doJSON(t, http.MethodPost, "/api/definitions", request)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	saved, agent := decodeLiveDemoGraph(t, body)
	if agent.ModelID != liveDecisionModelID || agent.ImageModelID != liveImageModelID {
		t.Errorf("saved modelId/imageModelId = %q/%q, want %q/%q", agent.ModelID, agent.ImageModelID, liveDecisionModelID, liveImageModelID)
	}
	if agent.TimeoutMs != 1800000 || agent.MaxGenerationCalls != 6 {
		t.Errorf("saved timeoutMs/maxGenerationCalls = %d/%d, want 1800000/6", agent.TimeoutMs, agent.MaxGenerationCalls)
	}
	if agent.MaxTurns < 2*agent.MaxGenerationCalls+1 {
		t.Errorf("maxTurns = %d leaves no room to review every generation and finish (want >= %d)", agent.MaxTurns, 2*agent.MaxGenerationCalls+1)
	}
	if diff := cmp.Diff([]string{"generate_image", "review_asset"}, agent.AllowedTools); diff != "" {
		t.Errorf("allowedTools mismatch (-want +got):\n%s", diff)
	}
	// A real model would follow a mock directive literally, so the live instructions
	// carry none.
	if strings.Contains(strings.ToLower(agent.Instructions), "mock") {
		t.Errorf("live instructions mention mock controls: %q", agent.Instructions)
	}
	assertLiveDemoObjectOutput(t, agent)

	fixture, err := os.ReadFile("../fixtures/definitions/photo_effect_template.json")
	if err != nil {
		t.Fatalf("read photo_effect_template.json: %v", err)
	}
	template, _ := decodeLiveDemoGraph(t, fixture)
	if diff := cmp.Diff(nodeTypes(template), nodeTypes(saved)); diff != "" {
		t.Errorf("live demo nodes differ from the deterministic photo template (-template +live):\n%s", diff)
	}
	var wantEdges, gotEdges any
	if err := json.Unmarshal(template.Edges, &wantEdges); err != nil {
		t.Fatalf("decode template edges: %v", err)
	}
	if err := json.Unmarshal(saved.Edges, &gotEdges); err != nil {
		t.Fatalf("decode saved edges: %v", err)
	}
	if diff := cmp.Diff(wantEdges, gotEdges); diff != "" {
		t.Errorf("live demo edges differ from the deterministic photo template (-template +live):\n%s", diff)
	}
}

// TestLiveDemoDefinition_MockOnlyBackend_RejectedWithoutSubstitution covers the live
// demo Definition on a Backend that registers only the mock models, as `make demo` and
// CI do: validation names both unregistered selections, and no mock model stands in for
// either.
func TestLiveDemoDefinition_MockOnlyBackend_RejectedWithoutSubstitution(t *testing.T) {
	env := newTestEnv(t)

	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions/validate", liveDemoDefinition(t))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/definitions/validate status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	result := decodeBody[struct {
		Valid  bool                    `json:"valid"`
		Errors []definitionErrorDetail `json:"errors"`
	}](t, body)
	if result.Valid {
		t.Fatalf("live demo Definition validated on a mock-only Backend: %s", body)
	}
	got := map[string]string{}
	for _, e := range result.Errors {
		got[e.Path] = e.Code
	}
	want := map[string]string{
		"nodes[node_agent].config.modelId":      "MODEL_NOT_FOUND",
		"nodes[node_agent].config.imageModelId": "MODEL_NOT_FOUND",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("validation errors mismatch (-want +got):\n%s", diff)
	}
}

// assertLiveDemoObjectOutput checks that the saved live Agent declares its FINAL answer as
// an object-form outputSchema, so the decision model is asked for the delivery object
// itself: the Runtime accepts that object and hands media_result its canonical JSON text,
// and rejects the same object encoded as a JSON string. The instructions must not ask for
// that encoding either.
func assertLiveDemoObjectOutput(t *testing.T, agent liveDemoAgentConfig) {
	t.Helper()
	var root struct {
		Type any `json:"type"`
	}
	if err := json.Unmarshal(agent.OutputSchema, &root); err != nil || root.Type != "object" {
		t.Fatalf("saved outputSchema = %s, want an object-form Schema", agent.OutputSchema)
	}
	answer := json.RawMessage(`{"examples":[{"photoAssetId":"ast_1","assetRef":"art_1"}],"settingsDigest":"sha256:1"}`)
	if err := runtime.ValidateFinalOutput(answer, agent.OutputSchema); err != nil {
		t.Errorf("ValidateFinalOutput(delivery object) = %v, want accepted", err)
	}
	encoded, _ := json.Marshal(string(answer))
	if _, ok := runtime.AsInvalidActionError(runtime.ValidateFinalOutput(encoded, agent.OutputSchema)); !ok {
		t.Errorf("ValidateFinalOutput(delivery object as a JSON string) accepted, want an invalid action")
	}
	if strings.Contains(agent.Instructions, "JSON string") {
		t.Errorf("live instructions still ask for the answer as a JSON string: %q", agent.Instructions)
	}
}

func nodeTypes(graph liveDemoGraph) map[string]string {
	types := make(map[string]string, len(graph.Nodes))
	for _, node := range graph.Nodes {
		types[node.ID] = node.Type
	}
	return types
}
