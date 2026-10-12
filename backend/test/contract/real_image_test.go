//go:build integration

// Real image model scenarios over the public API: generate_image calls a real image model
// through the OpenRouter Images API Adapter, here pointed at the Mock Provider's
// OpenRouter-shaped endpoint, which answers deterministic gradient images. The generated
// bytes become Execution Artifacts that Studio previews through the Run-scoped content
// route, and a failed call leaves no credential, photo data URL, image content or
// upstream response body in Trace, Events, the Snapshot or the logs.
package contract

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/adapters/openrouterimage"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
)

const (
	contractRealImageModelID = "image-real-1"
	contractRealImageKey     = "sk-real-image-contract-secret"
)

// realImageGenerator bridges generate_image's generator port to the Images API Adapter
// the way the Backend process wires it: it reads the source photo through the photo
// port, makes exactly one call and hands back the decoded image with what the Provider
// reported about it.
type realImageGenerator struct {
	provider *openrouterimage.Provider
}

func (g realImageGenerator) Generate(ctx context.Context, request generateimage.ImageRequest) (generateimage.GeneratedImage, error) {
	photo, err := request.OpenPhoto(ctx)
	if err != nil {
		return generateimage.GeneratedImage{}, err
	}
	reference, err := io.ReadAll(io.LimitReader(photo.Content, defaultTestAssetMaxUploadBytes))
	_ = photo.Content.Close()
	if err != nil {
		return generateimage.GeneratedImage{}, fmt.Errorf("read photo: photo could not be read")
	}
	image, err := g.provider.GenerateImage(ctx, openrouterimage.ImageRequest{
		Prompt:             request.Settings.Prompt,
		AspectRatio:        request.Settings.AspectRatio,
		Resolution:         request.Settings.Resolution,
		Seed:               request.Settings.Seed,
		ReferenceImage:     reference,
		ReferenceMediaType: photo.MediaType,
	})
	if err != nil {
		return generateimage.GeneratedImage{}, fmt.Errorf("%s: image model: %w", generateimage.ToolName, err)
	}
	return generateimage.GeneratedImage{Content: io.NopCloser(image.Content), Model: image.Model, Cost: image.Cost}, nil
}

// assetPhotos is generate_image's photo port backed by the Asset service, which reads a
// source photo through the same verified path as an Asset download. The harness sets
// assets once the service exists, before any Run starts.
type assetPhotos struct {
	assets *service.AssetService
}

func (p *assetPhotos) OpenPhoto(ctx context.Context, photoAssetID string) (generateimage.Photo, error) {
	asset, content, err := p.assets.OpenContent(ctx, photoAssetID)
	if err != nil {
		return generateimage.Photo{}, err
	}
	return generateimage.Photo{MediaType: asset.MediaType, Content: content}, nil
}

// imageModelIDOf is the one Model ID an image Adapter registers.
func imageModelIDOf(t *testing.T, model *openrouterimage.Provider) string {
	t.Helper()
	registrations, err := model.Models(context.Background())
	if err != nil || len(registrations) != 1 {
		t.Fatalf("image model registrations = %v, %v; want exactly one", registrations, err)
	}
	return registrations[0].ID
}

// withRealImageModel wires one real image model whose upstream model name is
// upstreamModel against the Mock Provider's Images API.
func withRealImageModel(t *testing.T, upstreamModel string) func(string, *testEnvOptions) {
	return func(providerURL string, opts *testEnvOptions) {
		model, err := openrouterimage.New(openrouterimage.Config{
			ModelID:       contractRealImageModelID,
			DisplayName:   "Real Image",
			BaseURL:       providerURL + "/api/v1",
			APIKey:        contractRealImageKey,
			Model:         upstreamModel,
			HTTPClient:    &http.Client{Timeout: e2eHTTPTimeout},
			MaxImageBytes: defaultTestArtifactMaxBytes,
		})
		if err != nil {
			t.Fatalf("openrouterimage.New: %v", err)
		}
		opts.ImageModels = append(opts.ImageModels, model)
	}
}

// imagesAPIArrivals counts the Images API requests the Mock Provider record shows.
func (e *photoSetEnv) imagesAPIArrivals(t *testing.T) int {
	t.Helper()
	resp, err := (&http.Client{Timeout: e2eHTTPTimeout}).Get(e.providerURL + "/control/record?after=0")
	if err != nil {
		t.Fatalf("GET /control/record: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	arrivals := 0
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		var line struct {
			Event    string `json:"event"`
			Kind     string `json:"kind"`
			Scenario string `json:"scenario"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("decode record line %q: %v", scanner.Text(), err)
		}
		if line.Event == "arrived" && line.Kind == "image" && strings.HasPrefix(line.Scenario, "api=images") {
			arrivals++
		}
	}
	return arrivals
}

// TestE2E_PhotoSet_RealImageModel_PreviewThroughRunScopedRoute runs the photo-set main
// story with the Agent's image model set to a real image model. Every generation is one
// Images API call; each generated image is saved as an Execution Artifact named by its
// bytes, matches what the Provider answered, and is served by the Run-scoped content
// route the generation fact's imageUrl names, as is the delivered cover.
func TestE2E_PhotoSet_RealImageModel_PreviewThroughRunScopedRoute(t *testing.T) {
	env := newPhotoSetEnv(t, withRealImageModel(t, "vendor/image-real"))

	snapshot, trace, _ := runPhotoSet(t, env, "photo-set", 0, map[string]any{"imageModelId": contractRealImageModelID})
	if got := runStatusOf(snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; snapshot=%v", got, snapshot)
	}
	runID, _ := snapshot["run"].(map[string]any)["id"].(string)

	var subjects []string
	for _, fact := range trace.Facts.Items {
		if fact.FactType != generateimage.FactType {
			continue
		}
		subjects = append(subjects, fact.Subject)
		if want := generateimage.ArtifactContentPath(runID, fact.Subject); fact.Bindings["imageUrl"] != want {
			t.Errorf("generation fact %s imageUrl = %q, want the Run-scoped route %q", fact.ID, fact.Bindings["imageUrl"], want)
		}
	}
	generated, _ := env.providerRecord(t)
	slices.Sort(generated)
	slices.Sort(subjects)
	if len(subjects) != 6 || !slices.Equal(generated, subjects) {
		t.Fatalf("provider generated %v, ledger generated %v, want the same six images", generated, subjects)
	}
	if got := env.imagesAPIArrivals(t); got != 6 {
		t.Errorf("Images API requests = %d, want exactly one per generation (6)", got)
	}

	cover := photoSetCoverArtifact(t, snapshot)
	if !slices.Contains(subjects, cover.ArtifactID) {
		t.Errorf("cover artifact %s is not a generation of this Run %v", cover.ArtifactID, subjects)
	}
	client := &http.Client{Timeout: e2eHTTPTimeout}
	for _, artifactID := range slices.Compact(subjects) {
		resp, err := client.Get(env.server.URL + generateimage.ArtifactContentPath(runID, artifactID))
		if err != nil {
			t.Fatalf("GET artifact %s content: %v", artifactID, err)
		}
		content, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" {
			t.Errorf("artifact %s content = %d %q, want 200 image/png", artifactID, resp.StatusCode, resp.Header.Get("Content-Type"))
			continue
		}
		sum := sha256.Sum256(content)
		if id, err := domain.ArtifactIDForSHA256(hex.EncodeToString(sum[:])); err != nil || id != artifactID {
			t.Errorf("artifact %s serves bytes named %s, want the generated image itself", artifactID, id)
		}
	}
}

// TestTrace_RealImageFailure_NoSecretOrBase64InTraceEventsLogs covers a real image call
// the upstream rejects with an error body. The Agent ends TOOL_ERROR after exactly one
// request, and neither the Agent Trace, the Node Detail, the Events, the Snapshot nor the
// Backend logs carry the API key, the photo's data URL or base64 content, any base64
// image member or the upstream response body.
func TestTrace_RealImageFailure_NoSecretOrBase64InTraceEventsLogs(t *testing.T) {
	logs := &syncBuffer{}
	env := newPhotoSetEnv(t, withRealImageModel(t, "vendor/image-real:mock-status-502"), func(_ string, opts *testEnvOptions) {
		opts.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})

	var photoID atomic.Value // string, published before the Run is created
	env.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		id, _ := photoID.Load().(string)
		arguments, _ := json.Marshal(map[string]any{
			"photoAssetId": id,
			"settings":     map[string]any{"prompt": "warm film look"},
		})
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioToolCall, ToolName: generateimage.ToolName, ToolArguments: arguments}
	}
	workflowID, version := savePhotoSetDefinition(t, env.testEnv, "photo-set", 0, map[string]any{"imageModelId": contractRealImageModelID})
	photo := []byte("\x89PNG\r\n\x1a\nreal-image-photo-canary-bytes")
	ref := env.uploadPNG(t, photo)
	id, _ := ref["assetId"].(string)
	photoID.Store(id)
	resp, body := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version,
		"input": map[string]any{"brief": "one warm film look for the whole set", "photos": []any{ref}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)

	snapshot := env.waitForTerminal(t, runID, photoSetWait)
	if got := runStatusOf(snapshot); got != "FAILED" {
		t.Fatalf("Run status = %q, want FAILED; snapshot=%v", got, snapshot)
	}
	agentNodeRunID := nodeRunIDOf(t, snapshot, "node_agent")
	status, traceBody := getAgentTrace(t, env.testEnv, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, traceBody)
	}
	trace := decodePhotoSetTrace(t, traceBody)
	if trace.AgentRun.Termination == nil || *trace.AgentRun.Termination != "TOOL_ERROR" {
		t.Errorf("agentRun.termination = %v, want TOOL_ERROR", trace.AgentRun.Termination)
	}
	if got := env.imagesAPIArrivals(t); got != 1 {
		t.Errorf("Images API requests = %d, want exactly 1", got)
	}
	detailResp, detailBody := env.doJSON(t, http.MethodGet, "/api/runs/"+runID+"/nodes/"+agentNodeRunID, nil)
	if detailResp.StatusCode != http.StatusOK {
		t.Fatalf("GET node detail status = %d, body=%s", detailResp.StatusCode, detailBody)
	}
	events, _ := json.Marshal(env.listEvents(t, runID))

	photoBase64 := base64.StdEncoding.EncodeToString(photo)
	forbidden := []string{
		contractRealImageKey, "Bearer", "data:image", photoBase64, photoBase64[:24],
		"b64_json", "upstream image failure detail",
	}
	surfaces := map[string]string{
		"agent trace": string(traceBody),
		"node detail": string(detailBody),
		"events":      string(events),
		"snapshot":    string(mustMarshal(t, snapshot)),
		"logs":        logs.String(),
	}
	for label, surface := range surfaces {
		for _, value := range forbidden {
			if strings.Contains(surface, value) {
				t.Errorf("%s carries %q", label, value)
			}
		}
	}
	if !strings.Contains(string(traceBody), "TOOL_ERROR") {
		t.Errorf("agent trace does not record the TOOL_ERROR it must show: %s", traceBody)
	}
}
