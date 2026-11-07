//go:build integration

package contract

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/adapters/externaltask"
	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/mediatranscript"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/tools/externaltool"
	"github.com/leungll/Emberling/backend/internal/tools/reviewartifact"
)

// The video-editing Definitions: two live ones, run by hand or by the evaluation batch
// against real models, and two deterministic fixtures bound to the mock models, which
// `make video-demo` runs against the real media service. All four embed one shared
// prompt; the reviewed ones append one paragraph of review rules.
const (
	videoEditDefinitionsDir   = "../../../scripts/demo/definitions/"
	videoEditPromptPath       = videoEditDefinitionsDir + "video_edit_prompt.md"
	videoEditReviewRulesPath  = videoEditDefinitionsDir + "video_edit_review_rules.md"
	videoEditBasicLivePath    = videoEditDefinitionsDir + "video_edit_basic_live.json"
	videoEditReviewedLivePath = videoEditDefinitionsDir + "video_edit_reviewed_live.json"
	videoEditBasicFixture     = "../fixtures/definitions/video_edit_basic.json"
	videoEditReviewedFixture  = "../fixtures/definitions/video_edit_reviewed.json"
	videoEditPromptVersion    = "Prompt version: video-edit/v1."
	videoEditLiveReviewModel  = "text-review"
)

// videoEditDefinitions lists every Definition that embeds the shared prompt, with whether
// it is the reviewed variant.
var videoEditDefinitions = []struct {
	path     string
	reviewed bool
}{
	{videoEditBasicLivePath, false},
	{videoEditReviewedLivePath, true},
	{videoEditBasicFixture, false},
	{videoEditReviewedFixture, true},
}

type videoEditAgentConfig struct {
	Instructions   string          `json:"instructions"`
	ModelID        string          `json:"modelId"`
	ModelConfig    json.RawMessage `json:"modelConfig"`
	ReviewModelID  string          `json:"reviewModelId"`
	AllowedTools   []string        `json:"allowedTools"`
	MaxTurns       int             `json:"maxTurns"`
	TimeoutMs      int             `json:"timeoutMs"`
	MaxFailedFacts map[string]int  `json:"maxFailedFacts"`
	GoalPointer    string          `json:"goalPointer"`
	StateSchema    json.RawMessage `json:"stateSchema"`
	OutputSchema   json.RawMessage `json:"outputSchema"`
}

type videoEditGraph struct {
	Nodes []struct {
		ID              string          `json:"id"`
		Type            string          `json:"type"`
		Config          json.RawMessage `json:"config"`
		ExecutionPolicy *struct {
			TimeoutMs int `json:"timeoutMs"`
		} `json:"executionPolicy"`
	} `json:"nodes"`
	Edges json.RawMessage `json:"edges"`
}

func readVideoEditDefinition(t *testing.T, path string) (json.RawMessage, videoEditGraph, videoEditAgentConfig) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	graph := decodeBody[videoEditGraph](t, raw)
	for _, node := range graph.Nodes {
		if node.ID == "node_agent" {
			return raw, graph, decodeBody[videoEditAgentConfig](t, node.Config)
		}
	}
	t.Fatalf("%s has no node_agent", path)
	return nil, videoEditGraph{}, videoEditAgentConfig{}
}

// unreachableModels and unreachableMaterials stand in for the review Tool's ports:
// validating and saving a Definition never reviews anything, so a call is a test failure.
type unreachableModels struct{ t *testing.T }

func (m unreachableModels) Get(modelID string) (registry.ModelRegistration, registry.ModelProvider, bool) {
	m.t.Errorf("review model %s was resolved; no review is expected", modelID)
	return registry.ModelRegistration{}, nil, false
}

type unreachableMaterials struct{ t *testing.T }

func (m unreachableMaterials) OpenText(context.Context, string, string) (domain.Artifact, io.ReadCloser, error) {
	m.t.Error("a material was opened; no review is expected")
	return domain.Artifact{}, nil, domain.ErrNotFound
}

// newVideoEditEnv wires a Backend with the media transcript node and the four video
// Tools registered, the way a deployment with a media service address has them, over a
// media service that must never be reached, plus the given extra models.
func newVideoEditEnv(t *testing.T, extraModels ...registry.ModelProvider) *testEnv {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("media service was called (%s %s); saving a Definition dispatches nothing", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	artifacts := asset.NewArtifactStore(root, defaultTestArtifactMaxBytes)
	tasks := externaltask.New(server.URL, server.Client())
	node, err := mediatranscript.Registration(mediatranscript.Ports{Tasks: tasks, Artifacts: artifacts})
	if err != nil {
		t.Fatalf("media transcript registration: %v", err)
	}
	var tools []registry.ToolRegistration
	for _, spec := range externaltool.MediaSpecs(artifacts) {
		reg, err := externaltool.Registration(spec, tasks)
		if err != nil {
			t.Fatalf("register %s: %v", spec.Metadata.Name, err)
		}
		tools = append(tools, reg)
	}
	tools = append(tools, reviewartifact.Registration(reviewartifact.Ports{Models: unreachableModels{t}, Materials: unreachableMaterials{t}}))
	return newTestEnvWithOptions(t, testEnvOptions{
		AssetStorageRoot:       root,
		ExtraNodeRegistrations: []registry.NodeRegistration{node},
		ExtraToolRegistrations: tools,
		ExtraModelProviders:    extraModels,
	})
}

// TestVideoEditDefinitions_EmbeddedPromptMatchesTheSharedFiles pins the four Definitions
// to the one prompt file: each embeds it verbatim, the reviewed ones followed by the
// review rules paragraph, and the prompt opens with its version line so the version a
// saved Definition ran with can be read back from the Definition itself.
func TestVideoEditDefinitions_EmbeddedPromptMatchesTheSharedFiles(t *testing.T) {
	promptRaw, err := os.ReadFile(videoEditPromptPath)
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	rulesRaw, err := os.ReadFile(videoEditReviewRulesPath)
	if err != nil {
		t.Fatalf("read review rules: %v", err)
	}
	prompt := strings.TrimRight(string(promptRaw), "\n")
	rules := strings.TrimRight(string(rulesRaw), "\n")
	if !strings.HasPrefix(prompt, videoEditPromptVersion+"\n") {
		t.Fatalf("prompt does not open with %q", videoEditPromptVersion)
	}
	for _, forbidden := range []string{"mock", "docs/"} {
		if strings.Contains(strings.ToLower(prompt+rules), forbidden) {
			t.Errorf("prompt text mentions %q", forbidden)
		}
	}
	for _, def := range videoEditDefinitions {
		_, _, agent := readVideoEditDefinition(t, def.path)
		want := prompt
		if def.reviewed {
			want = prompt + "\n\n" + rules
		}
		if agent.Instructions != want {
			t.Errorf("%s: embedded instructions differ from the shared prompt files; run scripts/demo/definitions/embed-video-edit-prompt.sh", def.path)
		}
		if def.reviewed != strings.Contains(agent.Instructions, "review_artifact") {
			t.Errorf("%s: reviewed=%t but the instructions mention review_artifact=%t", def.path, def.reviewed, !def.reviewed)
		}
	}
}

// TestVideoEditDefinitions_AgentConfigurationIsTheDemoContract pins the Agent fields the
// demo and the evaluation batch rely on, identical between the live Definition and its
// fixture except for the model binding, and the transcript node's explicit Attempt
// deadline, which is what ends a transcript task the media service lost.
func TestVideoEditDefinitions_AgentConfigurationIsTheDemoContract(t *testing.T) {
	for _, def := range videoEditDefinitions {
		_, graph, agent := readVideoEditDefinition(t, def.path)
		wantTools := []string{"read_material", "run_checks", "render_timeline"}
		wantFacts := map[string]int{"media_checked": 3}
		if def.reviewed {
			wantTools = append(wantTools, "review_artifact")
			wantFacts["content_reviewed"] = 1
		}
		if diff := cmp.Diff(wantTools, agent.AllowedTools); diff != "" {
			t.Errorf("%s allowedTools (-want +got):\n%s", def.path, diff)
		}
		if diff := cmp.Diff(wantFacts, agent.MaxFailedFacts); diff != "" {
			t.Errorf("%s maxFailedFacts (-want +got):\n%s", def.path, diff)
		}
		if agent.MaxTurns != 30 || agent.TimeoutMs != 1800000 || agent.GoalPointer != "/goal" {
			t.Errorf("%s maxTurns/timeoutMs/goalPointer = %d/%d/%q, want 30/1800000/\"/goal\"", def.path, agent.MaxTurns, agent.TimeoutMs, agent.GoalPointer)
		}
		if def.reviewed == (agent.ReviewModelID == "") {
			t.Errorf("%s reviewModelId = %q, reviewed=%t", def.path, agent.ReviewModelID, def.reviewed)
		}
		var transcriptNode bool
		for _, node := range graph.Nodes {
			if node.Type != mediatranscript.NodeType {
				continue
			}
			transcriptNode = true
			if node.ExecutionPolicy == nil || node.ExecutionPolicy.TimeoutMs != 600000 {
				t.Errorf("%s transcript node executionPolicy = %+v, want an explicit 600000 ms Attempt deadline", def.path, node.ExecutionPolicy)
			}
		}
		if !transcriptNode {
			t.Errorf("%s has no %s node", def.path, mediatranscript.NodeType)
		}
		assertVideoEditOutputSchema(t, def.path, agent.OutputSchema)
	}

	// The fixtures differ from the live Definitions only in the model binding.
	for _, pair := range []struct{ live, fixture string }{
		{videoEditBasicLivePath, videoEditBasicFixture},
		{videoEditReviewedLivePath, videoEditReviewedFixture},
	} {
		_, liveGraph, live := readVideoEditDefinition(t, pair.live)
		_, fixtureGraph, fixture := readVideoEditDefinition(t, pair.fixture)
		if live.ModelID != "opus-5.5" || len(live.ModelConfig) != 0 {
			t.Errorf("%s modelId/modelConfig = %q/%s, want opus-5.5 with no model config", pair.live, live.ModelID, live.ModelConfig)
		}
		if fixture.ModelID != mockmodel.ModelID || !strings.Contains(string(fixture.ModelConfig), `"script"`) {
			t.Errorf("%s modelId/modelConfig = %q/%s, want %s with a script", pair.fixture, fixture.ModelID, fixture.ModelConfig, mockmodel.ModelID)
		}
		if diff := cmp.Diff(nodeTypes(liveGraphAsLiveDemo(liveGraph)), nodeTypes(liveGraphAsLiveDemo(fixtureGraph))); diff != "" {
			t.Errorf("node types differ between %s and %s (-live +fixture):\n%s", pair.live, pair.fixture, diff)
		}
		var liveEdges, fixtureEdges any
		if err := json.Unmarshal(liveGraph.Edges, &liveEdges); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(fixtureGraph.Edges, &fixtureEdges); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(liveEdges, fixtureEdges); diff != "" {
			t.Errorf("edges differ between %s and %s (-live +fixture):\n%s", pair.live, pair.fixture, diff)
		}
	}
}

// liveGraphAsLiveDemo adapts a video graph to the node-type helper the photo live test
// uses, so both tests compare graphs the same way.
func liveGraphAsLiveDemo(graph videoEditGraph) liveDemoGraph {
	var out liveDemoGraph
	for _, node := range graph.Nodes {
		out.Nodes = append(out.Nodes, struct {
			ID     string          `json:"id"`
			Type   string          `json:"type"`
			Config json.RawMessage `json:"config"`
		}{ID: node.ID, Type: node.Type, Config: node.Config})
	}
	return out
}

// assertVideoEditOutputSchema checks the Agent's FINAL output is the delivery report
// object the clip completion check reads: both outcomes are accepted with the fields
// that do not apply set to null, a report missing a field is an invalid action, and so
// is the report encoded as a JSON string.
func assertVideoEditOutputSchema(t *testing.T, path string, schema json.RawMessage) {
	t.Helper()
	delivered := json.RawMessage(`{"outcome":"DELIVERED","clipSha256":"` + strings.Repeat("a", 64) + `","selectionReason":"S3-S6","cannotSatisfyReason":null}`)
	if err := runtime.ValidateFinalOutput(delivered, schema); err != nil {
		t.Errorf("%s: ValidateFinalOutput(delivered report) = %v, want accepted", path, err)
	}
	cannot := json.RawMessage(`{"outcome":"CANNOT_SATISFY","clipSha256":null,"selectionReason":null,"cannotSatisfyReason":"the talk takes no such view"}`)
	if err := runtime.ValidateFinalOutput(cannot, schema); err != nil {
		t.Errorf("%s: ValidateFinalOutput(cannot-satisfy report) = %v, want accepted", path, err)
	}
	missing := json.RawMessage(`{"outcome":"DELIVERED","clipSha256":"` + strings.Repeat("a", 64) + `"}`)
	if _, ok := runtime.AsInvalidActionError(runtime.ValidateFinalOutput(missing, schema)); !ok {
		t.Errorf("%s: ValidateFinalOutput(report missing fields) accepted, want an invalid action", path)
	}
	other := json.RawMessage(`{"outcome":"DONE","clipSha256":null,"selectionReason":null,"cannotSatisfyReason":null}`)
	if _, ok := runtime.AsInvalidActionError(runtime.ValidateFinalOutput(other, schema)); !ok {
		t.Errorf("%s: ValidateFinalOutput(unknown outcome) accepted, want an invalid action", path)
	}
	encoded, _ := json.Marshal(string(delivered))
	if _, ok := runtime.AsInvalidActionError(runtime.ValidateFinalOutput(encoded, schema)); !ok {
		t.Errorf("%s: ValidateFinalOutput(report as a JSON string) accepted, want an invalid action", path)
	}
}

// TestVideoEditLiveDefinitions_RealModelsRegistered_ValidateAndSave covers the two live
// Definitions against a Backend that registers the decision and review models they
// select beside the media node and Tools: both validate and save with the selected IDs
// verbatim. No model and no media service is called.
func TestVideoEditLiveDefinitions_RealModelsRegistered_ValidateAndSave(t *testing.T) {
	env := newVideoEditEnv(t, configuredModel(t, liveDecisionModelID), configuredModel(t, videoEditLiveReviewModel))
	for _, def := range []struct {
		path        string
		reviewModel string
	}{{videoEditBasicLivePath, ""}, {videoEditReviewedLivePath, videoEditLiveReviewModel}} {
		request, _, _ := readVideoEditDefinition(t, def.path)
		resp, body := env.doJSON(t, http.MethodPost, "/api/definitions/validate", request)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: POST /api/definitions/validate status = %d, body=%s", def.path, resp.StatusCode, body)
		}
		if result := decodeBody[struct {
			Valid bool `json:"valid"`
		}](t, body); !result.Valid {
			t.Fatalf("%s is not valid: %s", def.path, body)
		}
		resp, body = env.doJSON(t, http.MethodPost, "/api/definitions", request)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("%s: POST /api/definitions status = %d, body=%s", def.path, resp.StatusCode, body)
		}
		var saved videoEditGraph
		if err := json.Unmarshal(body, &saved); err != nil {
			t.Fatal(err)
		}
		for _, node := range saved.Nodes {
			if node.ID != "node_agent" {
				continue
			}
			agent := decodeBody[videoEditAgentConfig](t, node.Config)
			if agent.ModelID != liveDecisionModelID || agent.ReviewModelID != def.reviewModel {
				t.Errorf("%s saved modelId/reviewModelId = %q/%q, want %q/%q", def.path, agent.ModelID, agent.ReviewModelID, liveDecisionModelID, def.reviewModel)
			}
			if !strings.HasPrefix(agent.Instructions, videoEditPromptVersion) {
				t.Errorf("%s saved instructions do not open with the prompt version", def.path)
			}
		}
	}
}

// TestVideoEditDefinitions_MockOnlyBackend_FixturesSaveAndLiveOnesAreRefused covers the
// deployment `make video-demo` and CI use, with only the mock models registered: the two
// fixtures save, bound to the mock decision model and, for the reviewed one, to the mock
// review model, while the live Definitions are refused at every unregistered model
// selection and no mock model stands in.
func TestVideoEditDefinitions_MockOnlyBackend_FixturesSaveAndLiveOnesAreRefused(t *testing.T) {
	env := newVideoEditEnv(t)
	for _, path := range []string{videoEditBasicFixture, videoEditReviewedFixture} {
		request, _, _ := readVideoEditDefinition(t, path)
		resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", request)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("%s: POST /api/definitions status = %d, body=%s", path, resp.StatusCode, body)
		}
	}
	for _, def := range []struct {
		path string
		want map[string]string
	}{
		{videoEditBasicLivePath, map[string]string{"nodes[node_agent].config.modelId": "MODEL_NOT_FOUND"}},
		{videoEditReviewedLivePath, map[string]string{
			"nodes[node_agent].config.modelId":       "MODEL_NOT_FOUND",
			"nodes[node_agent].config.reviewModelId": "MODEL_NOT_FOUND",
		}},
	} {
		request, _, _ := readVideoEditDefinition(t, def.path)
		resp, body := env.doJSON(t, http.MethodPost, "/api/definitions/validate", request)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: POST /api/definitions/validate status = %d, body=%s", def.path, resp.StatusCode, body)
		}
		result := decodeBody[struct {
			Valid  bool                    `json:"valid"`
			Errors []definitionErrorDetail `json:"errors"`
		}](t, body)
		if result.Valid {
			t.Fatalf("%s validated on a mock-only Backend: %s", def.path, body)
		}
		got := map[string]string{}
		for _, e := range result.Errors {
			got[e.Path] = e.Code
		}
		if diff := cmp.Diff(def.want, got); diff != "" {
			t.Errorf("%s validation errors (-want +got):\n%s", def.path, diff)
		}
	}
}
