package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
)

const imageTestKey = "sk-image-wiring-secret"

// gradientPNG is a small synthetic gradient image, the deterministic stand-in for both a
// source photo and a generated image.
func gradientPNG(t *testing.T, shade uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := 0; x < 8; x++ {
		for y := 0; y < 8; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 32), G: uint8(y * 32), B: shade, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return buf.Bytes()
}

// upstreamImages is an Images API stand-in that records the model name and path of every
// request it receives and answers each with one generated image.
type upstreamImages struct {
	mu     sync.Mutex
	models []string
	paths  []string
	auth   []string
	image  []byte
}

func (u *upstreamImages) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(raw, &body)
		u.mu.Lock()
		u.models = append(u.models, body.Model)
		u.paths = append(u.paths, r.URL.Path)
		u.auth = append(u.auth, r.Header.Get("Authorization"))
		u.mu.Unlock()
		answer, _ := json.Marshal(map[string]any{"data": []map[string]any{{"b64_json": base64.StdEncoding.EncodeToString(u.image)}}})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(answer)
	}))
	t.Cleanup(server.Close)
	return server
}

func (u *upstreamImages) requests() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.models)
}

func (u *upstreamImages) last() (model, path, auth string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.models) == 0 {
		return "", "", ""
	}
	n := len(u.models) - 1
	return u.models[n], u.paths[n], u.auth[n]
}

func imageModelConfig(baseURL string, entries ...config.ImageModelEntry) config.ImageModel {
	return config.ImageModel{BaseURL: baseURL + "/api/v1", APIKey: config.NewSecret(imageTestKey), Models: entries, Timeout: config.DefaultImageTimeout}
}

func photoReaderFor(t *testing.T) generateimage.PhotoReader {
	t.Helper()
	return assetPhotoReader{assets: fakeAssetOpener{
		asset:   domain.Asset{AssetID: "asset_photo_1", MediaType: "image/png"},
		content: string(gradientPNG(t, 200)),
	}}
}

func imageAction(imageModelID string) registry.ToolAction {
	return registry.ToolAction{
		RunID: "run_1", AgentRunID: "ar_1", TurnID: "turn_1", ActionID: "act_1", ToolName: generateimage.ToolName, AttemptNo: 1,
		Arguments:    json.RawMessage(`{"photoAssetId":"asset_photo_1","settings":{"prompt":"Clay stop-motion look","aspectRatio":"1:1"}}`),
		ImageModelID: imageModelID,
	}
}

// TestRegisterImageModels_EachResolvesToOwnUpstreamName pins the image model wiring: every
// configured entry registers under its own Model ID with its own display name, description
// and the reference image generation capability, and a generate_image call for that ID
// sends exactly that entry's upstream model name, once, to the shared Images API.
func TestRegisterImageModels_EachResolvesToOwnUpstreamName(t *testing.T) {
	upstream := &upstreamImages{image: gradientPNG(t, 40)}
	server := upstream.serve(t)
	entries := []config.ImageModelEntry{
		{ID: "seedream-5.0-pro", Model: "vendor/seedream-upstream", DisplayName: "Seedream 5.0 Pro", Description: "Strong stylisation from a reference photo."},
		{ID: "gpt-image-2.5", Model: "vendor/gpt-image-upstream", DisplayName: "GPT Image 2.5"},
	}
	imageModels, err := newImageModels(imageModelConfig(server.URL, entries...), 1<<20, newImageHTTPClient(config.DefaultImageTimeout))
	if err != nil {
		t.Fatalf("newImageModels() error = %v", err)
	}
	models := registry.NewModelRegistry()
	if err := registerModelProviders(context.Background(), models, config.OpenAIModel{}); err != nil {
		t.Fatalf("registerModelProviders() error = %v", err)
	}
	if err := registerImageModels(context.Background(), models, imageModels); err != nil {
		t.Fatalf("registerImageModels() error = %v", err)
	}
	wantIDs := append(mockModelIDs(t), "seedream-5.0-pro", "gpt-image-2.5")
	if diff := cmp.Diff(wantIDs, registeredModelIDs(models), cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
		t.Errorf("registered model IDs mismatch (-want +got):\n%s", diff)
	}

	tools := registry.NewToolRegistry()
	if err := registerImageTool(tools, imageToolWiring{
		MockBaseURL: "http://mock.invalid", DispatchClient: &http.Client{Timeout: defaultTaskDispatchTimeout},
		ImageModels: imageModels, Photos: photoReaderFor(t), Artifacts: asset.NewArtifactStore(t.TempDir(), 1<<20), MaxPhotoBytes: 1 << 20,
	}); err != nil {
		t.Fatalf("registerImageTool() error = %v", err)
	}
	reg, _ := tools.Get(generateimage.ToolName)

	for i, entry := range entries {
		registration, _, ok := models.Get(entry.ID)
		if !ok {
			t.Fatalf("model %q not registered", entry.ID)
		}
		if registration.DisplayName != entry.DisplayName || registration.Description != entry.Description {
			t.Errorf("model %q = %q/%q, want %q/%q", entry.ID, registration.DisplayName, registration.Description, entry.DisplayName, entry.Description)
		}
		if !registration.HasCapability(domain.ModelCapabilityReferenceImageGeneration) || registration.HasCapability(domain.ModelCapabilityStructuredDecision) {
			t.Errorf("model %q capabilities = %v, want reference image generation only", entry.ID, registration.Capabilities)
		}

		result, err := reg.Executor.Execute(context.Background(), imageAction(entry.ID))
		if err != nil || result.Kind != registry.ToolResultCompleted {
			t.Fatalf("generate with %q: result %+v err %v, want COMPLETED", entry.ID, result, err)
		}
		if got := upstream.requests(); got != i+1 {
			t.Errorf("upstream requests after %q = %d, want %d", entry.ID, got, i+1)
		}
		model, path, auth := upstream.last()
		if model != entry.Model || path != "/api/v1/images" || auth != "Bearer "+imageTestKey {
			t.Errorf("model %q sent %q to %q, want %q to /api/v1/images with the shared key", entry.ID, model, path, entry.Model)
		}
		if len(result.Result.Artifacts) != 1 || result.Result.Artifacts[0].MediaType != "image/png" {
			t.Errorf("model %q artifacts = %+v, want one PNG", entry.ID, result.Result.Artifacts)
		}
	}
	for _, metadata := range models.ListMetadata() {
		if strings.Contains(metadata.DisplayName+metadata.Description, imageTestKey) {
			t.Errorf("model metadata leaks the API key: %+v", metadata)
		}
	}
}

func TestRegisterImageModels_NotConfigured_RegistersNothing(t *testing.T) {
	imageModels, err := newImageModels(config.ImageModel{}, 1<<20, newImageHTTPClient(config.DefaultImageTimeout))
	if err != nil || len(imageModels) != 0 {
		t.Fatalf("newImageModels() = %d models, error %v; want none", len(imageModels), err)
	}
}

// TestRegisterImageModels_IDDuplicatesMockModel_Fails covers the shared Model ID
// namespace: an image model may not take an ID the mock Provider already serves.
func TestRegisterImageModels_IDDuplicatesMockModel_Fails(t *testing.T) {
	imageModels, err := newImageModels(imageModelConfig("https://images.example", config.ImageModelEntry{
		ID: mockmodel.ImageModelID, Model: "vendor/other", DisplayName: "Other",
	}), 1<<20, newImageHTTPClient(config.DefaultImageTimeout))
	if err != nil {
		t.Fatalf("newImageModels() error = %v", err)
	}
	models := registry.NewModelRegistry()
	if err := registerModelProviders(context.Background(), models, config.OpenAIModel{}); err != nil {
		t.Fatalf("registerModelProviders() error = %v", err)
	}
	err = registerImageModels(context.Background(), models, imageModels)
	if err == nil {
		t.Fatal("registerImageModels() with a mock Model ID: error = nil, want a refusal")
	}
	if strings.Contains(err.Error(), imageTestKey) || strings.Contains(err.Error(), "images.example") {
		t.Errorf("error repeats a configured value: %q", err.Error())
	}
}

// TestRegisterTools_RealImage_ClientNotBoundByDispatchTimeout pins that a real image model
// never uses the short dispatch client kept for the mock: with a dispatch client that
// would fail any request at once, the real call still completes through the image client,
// whose only per-call bound is the configured image timeout.
func TestRegisterTools_RealImage_ClientNotBoundByDispatchTimeout(t *testing.T) {
	upstream := &upstreamImages{image: gradientPNG(t, 90)}
	server := upstream.serve(t)
	dispatchTransport := &countingTransport{}
	dispatchClient := &http.Client{Transport: dispatchTransport, Timeout: time.Nanosecond}
	imageClient := newImageHTTPClient(config.DefaultImageTimeout)
	imageTransport := &countingTransport{next: imageClient.Transport}
	imageClient.Transport = imageTransport

	imageModels, err := newImageModels(imageModelConfig(server.URL, config.ImageModelEntry{
		ID: "seedream-5.0-pro", Model: "vendor/seedream-upstream", DisplayName: "Seedream 5.0 Pro",
	}), 1<<20, imageClient)
	if err != nil {
		t.Fatalf("newImageModels() error = %v", err)
	}
	tools := registry.NewToolRegistry()
	if err := registerImageTool(tools, imageToolWiring{
		MockBaseURL: "http://mock.invalid", DispatchClient: dispatchClient,
		ImageModels: imageModels, Photos: photoReaderFor(t), Artifacts: asset.NewArtifactStore(t.TempDir(), 1<<20), MaxPhotoBytes: 1 << 20,
	}); err != nil {
		t.Fatalf("registerImageTool() error = %v", err)
	}
	reg, _ := tools.Get(generateimage.ToolName)

	result, err := reg.Executor.Execute(context.Background(), imageAction("seedream-5.0-pro"))
	if err != nil || result.Kind != registry.ToolResultCompleted {
		t.Fatalf("real image generation: result %+v err %v, want COMPLETED", result, err)
	}
	if got := dispatchTransport.requests.Load(); got != 0 {
		t.Errorf("dispatch client requests = %d, want 0", got)
	}
	if got := imageTransport.requests.Load(); got != 1 {
		t.Errorf("image client requests = %d, want exactly 1", got)
	}
	if imageClient.Timeout != config.DefaultImageTimeout || imageClient.Timeout == defaultTaskDispatchTimeout {
		t.Errorf("image client Timeout = %v, want the configured image timeout %v", imageClient.Timeout, config.DefaultImageTimeout)
	}
}

// TestImageHTTPClient_DialTimeoutOnly_NoResponseHeaderTimeout pins the connection limits of
// the real image client: establishing the connection is bounded at about ten seconds,
// while waiting for the answer is not bounded separately - a slow image model is bounded
// only by the per-call timeout and the caller's context.
func TestImageHTTPClient_DialTimeoutOnly_NoResponseHeaderTimeout(t *testing.T) {
	client := newImageHTTPClient(90 * time.Second)

	if client.Timeout != 90*time.Second {
		t.Errorf("client Timeout = %v, want the per-call timeout 90s", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.TLSHandshakeTimeout != imageConnectTimeout {
		t.Errorf("TLSHandshakeTimeout = %v, want %v", transport.TLSHandshakeTimeout, imageConnectTimeout)
	}
	if transport.ResponseHeaderTimeout != 0 {
		t.Errorf("ResponseHeaderTimeout = %v, want none", transport.ResponseHeaderTimeout)
	}
	if transport.DialContext == nil {
		t.Error("DialContext is nil, want the bounded image dialer")
	}
	if got := imageDialer().Timeout; got != imageConnectTimeout {
		t.Errorf("dialer Timeout = %v, want %v", got, imageConnectTimeout)
	}
	if imageConnectTimeout != 10*time.Second {
		t.Errorf("imageConnectTimeout = %v, want 10s", imageConnectTimeout)
	}
	if transport == http.DefaultTransport {
		t.Error("image client shares http.DefaultTransport, want its own transport")
	}
}

// TestImageGenerator_ClientTimeout_IsToolFailureNotDeadline pins the failure class of the
// image client's own timeout: it is a Tool failure, never reported as the caller's
// context deadline, so the Agent ends with TOOL_ERROR rather than TIMEOUT and nothing is
// resent.
func TestImageGenerator_ClientTimeout_IsToolFailureNotDeadline(t *testing.T) {
	release := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		<-release
	}))
	t.Cleanup(func() { close(release); server.Close() })

	imageModels, err := newImageModels(imageModelConfig(server.URL, config.ImageModelEntry{
		ID: "seedream-5.0-pro", Model: "vendor/seedream-upstream", DisplayName: "Seedream 5.0 Pro",
	}), 1<<20, newImageHTTPClient(50*time.Millisecond))
	if err != nil {
		t.Fatalf("newImageModels() error = %v", err)
	}
	tools := registry.NewToolRegistry()
	if err := registerImageTool(tools, imageToolWiring{
		MockBaseURL: "http://mock.invalid", DispatchClient: &http.Client{},
		ImageModels: imageModels, Photos: photoReaderFor(t), Artifacts: asset.NewArtifactStore(t.TempDir(), 1<<20), MaxPhotoBytes: 1 << 20,
	}); err != nil {
		t.Fatalf("registerImageTool() error = %v", err)
	}
	reg, _ := tools.Get(generateimage.ToolName)

	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	_, err = reg.Executor.Execute(ctx, imageAction("seedream-5.0-pro"))
	if err == nil {
		t.Fatal("Execute() error = nil, want the client timeout")
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("upstream requests = %d, want exactly 1", got)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("client timeout reported as a context deadline: %v", err)
	}
	if strings.Contains(err.Error(), imageTestKey) || strings.Contains(err.Error(), "Clay stop-motion") {
		t.Errorf("error repeats the key or the prompt: %q", err.Error())
	}
}
