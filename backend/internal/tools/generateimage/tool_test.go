package generateimage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/fixture/mockprovider"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// testImageModel is the image Model ID the tests wire the mock generator under; the Tool
// treats it as an opaque key.
const testImageModel = "image-model-v1"

func newProvider(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(mockprovider.NewServer(mockprovider.NewDispatcher(nil)))
	t.Cleanup(server.Close)
	return server
}

func newArtifacts(t *testing.T) *asset.ArtifactStore {
	t.Helper()
	return asset.NewArtifactStore(t.TempDir(), 1<<20)
}

// mockExecutor wires the mock generator for testImageModel only, against baseURL.
func mockExecutor(baseURL string, client *http.Client, artifacts ArtifactContentStore) *Executor {
	return New(map[string]ImageGenerator{testImageModel: NewMockGenerator(baseURL, client)}, nil, artifacts)
}

func action(arguments string) registry.ToolAction {
	return registry.ToolAction{
		AgentRunID: "ar_1", TurnID: "turn_1", ActionID: "act_1",
		ToolName: ToolName, AttemptNo: 1, Arguments: json.RawMessage(arguments), ImageModelID: testImageModel,
	}
}

func validateOutput(t *testing.T, output json.RawMessage) {
	t.Helper()
	schema, err := registry.CompileSchema(json.RawMessage(outputSchema))
	if err != nil {
		t.Fatalf("compile output schema: %v", err)
	}
	var value any
	if err := json.Unmarshal(output, &value); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if err := registry.ValidateValue(schema, value); err != nil {
		t.Fatalf("output %s does not conform to the output schema: %v", output, err)
	}
}

func TestRegistration_SyncExternalProducerCountingTowardLimit_RegistersCleanly(t *testing.T) {
	reg := Registration(nil, nil, nil)
	if reg.Metadata.ExecutionKind != domain.ToolExecutionSync {
		t.Errorf("executionKind = %s, want SYNC", reg.Metadata.ExecutionKind)
	}
	if want := (domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown}); reg.Metadata.SideEffect != want {
		t.Errorf("sideEffect = %+v, want %+v", reg.Metadata.SideEffect, want)
	}
	if !reg.Metadata.CountsTowardGenerationLimit || !reg.Metadata.UsesImageModel {
		t.Error("countsTowardGenerationLimit and usesImageModel must both be true")
	}
	if reg.Metadata.Produces == nil || reg.Metadata.Produces.FactType != FactType {
		t.Fatalf("produces = %+v, want fact type %q", reg.Metadata.Produces, FactType)
	}
	wantBindings := map[string]domain.FactPointer{
		"imageUrl":       {Source: domain.FactPointerResult, Pointer: "/imageUrl"},
		"photoAssetId":   {Source: domain.FactPointerArguments, Pointer: "/photoAssetId"},
		"settingsDigest": {Source: domain.FactPointerResult, Pointer: "/settingsDigest"},
	}
	if got := reg.Metadata.Produces.BindArguments; !reflect.DeepEqual(got, wantBindings) {
		t.Errorf("bindArguments = %+v, want %+v", got, wantBindings)
	}
	if err := registry.NewToolRegistry().Register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
}

// TestInputSchema_SharedStrictSettings_AcceptsOnlyTheContract pins the settings every
// image model shares: a prompt of 1 to 4000 characters, an optional aspect ratio from a
// fixed set, an optional resolution that is only "1K", an optional integer seed from 0
// to 2147483647, and nothing else.
func TestInputSchema_SharedStrictSettings_AcceptsOnlyTheContract(t *testing.T) {
	schema, err := registry.CompileSchema(json.RawMessage(inputSchema))
	if err != nil {
		t.Fatalf("compile input schema: %v", err)
	}
	validate := func(settings string) error {
		var value any
		if err := json.Unmarshal([]byte(`{"photoAssetId":"asset_photo_1","settings":`+settings+`}`), &value); err != nil {
			t.Fatalf("test arguments are not JSON: %v", err)
		}
		return registry.ValidateValue(schema, value)
	}
	longest := strings.Repeat("a", 4000)
	for _, valid := range []string{
		`{"prompt":"Soft film look"}`,
		`{"prompt":"` + longest + `"}`,
		`{"prompt":"Soft film look","aspectRatio":"3:4","resolution":"1K","seed":0}`,
		`{"prompt":"Soft film look","seed":2147483647}`,
	} {
		if err := validate(valid); err != nil {
			t.Errorf("settings %.80s rejected: %v", valid, err)
		}
	}
	for name, invalid := range map[string]string{
		"no prompt":           `{}`,
		"empty prompt":        `{"prompt":""}`,
		"prompt too long":     `{"prompt":"` + longest + `a"}`,
		"prompt not a string": `{"prompt":7}`,
		"extra member":        `{"prompt":"p","style":"film"}`,
		"mock member":         `{"prompt":"p","mock":"fail"}`,
		"resolution 2K":       `{"prompt":"p","resolution":"2K"}`,
		"unknown aspect":      `{"prompt":"p","aspectRatio":"5:4"}`,
		"negative seed":       `{"prompt":"p","seed":-1}`,
		"fractional seed":     `{"prompt":"p","seed":1.5}`,
		"seed too large":      `{"prompt":"p","seed":2147483648}`,
		"settings not object": `["p"]`,
	} {
		if err := validate(invalid); err == nil {
			t.Errorf("%s: settings %s accepted, want a schema violation", name, invalid)
		}
	}
}

func TestExecute_ProviderGenerates_ReturnsReachableAssetAndSettingsDigest(t *testing.T) {
	provider := newProvider(t)

	got, err := mockExecutor(provider.URL, provider.Client(), newArtifacts(t)).Execute(context.Background(),
		action(`{"photoAssetId":"asset_photo_1","settings":{"prompt":"Soft film look","seed":6}}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Kind != registry.ToolResultCompleted || got.Result == nil {
		t.Fatalf("result = %+v, want COMPLETED", got)
	}
	validateOutput(t, got.Result.Output)

	var out result
	if err := json.Unmarshal(got.Result.Output, &out); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	wantDigest, _, err := SettingsDigest(json.RawMessage(`{"prompt":"Soft film look","seed":6}`))
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if out.SettingsDigest != wantDigest {
		t.Errorf("settingsDigest = %q, want the canonical digest %q of the settings", out.SettingsDigest, wantDigest)
	}
	if out.ImageURL != provider.URL+"/v1/assets/"+out.AssetRef+".png" {
		t.Errorf("imageUrl = %q, want the Provider's asset URL for %q", out.ImageURL, out.AssetRef)
	}
	image, err := provider.Client().Get(out.ImageURL)
	if err != nil {
		t.Fatalf("GET imageUrl: %v", err)
	}
	_ = image.Body.Close()
	if image.StatusCode != http.StatusOK {
		t.Errorf("GET imageUrl status = %d, want 200", image.StatusCode)
	}

	again, err := mockExecutor(provider.URL, provider.Client(), newArtifacts(t)).Execute(context.Background(),
		action(`{"settings":{"seed":6, "prompt":"Soft film look"},"photoAssetId":"asset_photo_1"}`))
	if err != nil {
		t.Fatalf("repeat execute: %v", err)
	}
	if string(again.Result.Output) != string(got.Result.Output) {
		t.Errorf("reordered settings output = %s, want %s", again.Result.Output, got.Result.Output)
	}
}

func TestExecute_ProviderWithPublicBaseURL_BindsBrowserReachableImageURL(t *testing.T) {
	const public = "http://localhost:9101"
	provider := httptest.NewServer(mockprovider.NewServer(mockprovider.NewDispatcher(nil), mockprovider.WithPublicBaseURL(public)))
	t.Cleanup(provider.Close)

	got, err := mockExecutor(provider.URL, provider.Client(), newArtifacts(t)).Execute(context.Background(),
		action(`{"photoAssetId":"asset_photo_1","settings":{"prompt":"Soft film look"}}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Kind != registry.ToolResultCompleted || got.Result == nil {
		t.Fatalf("result = %+v, want COMPLETED", got)
	}
	validateOutput(t, got.Result.Output)
	var out result
	if err := json.Unmarshal(got.Result.Output, &out); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	// The Tool reaches the Provider at its internal address, but the imageUrl it returns,
	// and which its generation fact binds, names the public origin a browser can open.
	if want := public + "/v1/assets/" + out.AssetRef + ".png"; out.ImageURL != want {
		t.Errorf("imageUrl = %q, want %q", out.ImageURL, want)
	}
}

// TestGenerateImage_MockPromptDirective_FailsDefinitely covers the mock image model's
// failure control: a prompt that starts with the mock:fail directive makes the Mock
// Provider refuse the generation, so the call fails with the refusal status, saves no
// image and quotes neither the arguments nor the Provider address. The directive anywhere
// but at the start of the prompt is ordinary prompt text.
func TestGenerateImage_MockPromptDirective_FailsDefinitely(t *testing.T) {
	provider := newProvider(t)
	artifacts := newArtifacts(t)

	_, err := mockExecutor(provider.URL, provider.Client(), artifacts).Execute(context.Background(),
		action(`{"photoAssetId":"asset_photo_secretish","settings":{"prompt":"mock:fail settings-canary-4417"}}`))
	if err == nil {
		t.Fatal("execute error = nil, want a definite generation failure")
	}
	if !strings.Contains(err.Error(), "status 422") {
		t.Errorf("error = %v, want the Provider's rejection status", err)
	}
	for _, leak := range []string{"asset_photo_secretish", "settings-canary-4417", provider.URL} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error %q quotes %q", err, leak)
		}
	}
	if objects, _ := artifacts.Objects(context.Background()); len(objects) != 0 {
		t.Errorf("objects after a refused generation: %+v, want none", objects)
	}

	got, err := mockExecutor(provider.URL, provider.Client(), artifacts).Execute(context.Background(),
		action(`{"photoAssetId":"asset_photo_1","settings":{"prompt":"Soft look, not mock:fail"}}`))
	if err != nil || got.Kind != registry.ToolResultCompleted {
		t.Fatalf("directive inside the prompt: result %+v err %v, want an ordinary generation", got, err)
	}
}

// countingGenerator counts Generate calls and photo reads.
type countingGenerator struct {
	calls atomic.Int32
}

func (g *countingGenerator) Generate(context.Context, ImageRequest) (GeneratedImage, error) {
	g.calls.Add(1)
	return GeneratedImage{}, errors.New("countingGenerator: not expected to be called")
}

type countingPhotos struct {
	reads atomic.Int32
}

func (p *countingPhotos) OpenPhoto(context.Context, string) (Photo, error) {
	p.reads.Add(1)
	return Photo{MediaType: "image/png", Content: io.NopCloser(strings.NewReader("png"))}, nil
}

// TestGenerateImage_UnknownImageModelID_FailsBeforeRequest covers a frozen image Model
// ID this process wired no generator for: the call fails naming the ID, before any
// Provider request or photo read, and the generator of another model is not substituted.
func TestGenerateImage_UnknownImageModelID_FailsBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	t.Cleanup(provider.Close)
	other := &countingGenerator{}
	photos := &countingPhotos{}
	executor := New(map[string]ImageGenerator{
		testImageModel:  NewMockGenerator(provider.URL, provider.Client()),
		"image-other-1": other,
	}, photos, newArtifacts(t))

	for _, imageModelID := range []string{"image-removed-1", ""} {
		call := action(`{"photoAssetId":"asset_photo_1","settings":{"prompt":"Soft film look"}}`)
		call.ImageModelID = imageModelID
		_, err := executor.Execute(context.Background(), call)
		if err == nil {
			t.Fatalf("image model %q: execute error = nil, want a wiring failure", imageModelID)
		}
		if !strings.Contains(err.Error(), `"`+imageModelID+`"`) {
			t.Errorf("image model %q: error = %v, want it to name the image model", imageModelID, err)
		}
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("provider requests = %d, want 0", got)
	}
	if got := other.calls.Load(); got != 0 {
		t.Errorf("other generator calls = %d, want 0: no generator is substituted", got)
	}
	if got := photos.reads.Load(); got != 0 {
		t.Errorf("photo reads = %d, want 0", got)
	}
}

// photoGenerator reads the photo through the request and answers with a fixed image.
type photoGenerator struct {
	photo []byte
	media string
	image []byte
}

func (g *photoGenerator) Generate(ctx context.Context, request ImageRequest) (GeneratedImage, error) {
	photo, err := request.OpenPhoto(ctx)
	if err != nil {
		return GeneratedImage{}, err
	}
	defer func() { _ = photo.Content.Close() }()
	if g.photo, err = io.ReadAll(photo.Content); err != nil {
		return GeneratedImage{}, err
	}
	g.media = photo.MediaType
	return GeneratedImage{Content: io.NopCloser(strings.NewReader(string(g.image))), AssetID: "img_0123456789abcdef", ImageURL: "http://images.test/x.png"}, nil
}

type fixedPhotos map[string]Photo

func (p fixedPhotos) OpenPhoto(_ context.Context, id string) (Photo, error) {
	photo, ok := p[id]
	if !ok {
		return Photo{}, errors.New("no such photo " + id)
	}
	return photo, nil
}

// TestGenerateImage_PhotoPort_ReadsTheCallsPhoto covers the photo port: a generator that
// sends the photo's bytes reads exactly the call's photo, and a photo that cannot be read
// fails the call without quoting its id.
func TestGenerateImage_PhotoPort_ReadsTheCallsPhoto(t *testing.T) {
	photos := fixedPhotos{"asset_photo_1": {MediaType: "image/jpeg", Content: io.NopCloser(strings.NewReader("photo-bytes"))}}
	generator := &photoGenerator{image: pngBytes(t)}
	executor := New(map[string]ImageGenerator{testImageModel: generator}, photos, newArtifacts(t))

	if _, err := executor.Execute(context.Background(), action(`{"photoAssetId":"asset_photo_1","settings":{"prompt":"p"}}`)); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if string(generator.photo) != "photo-bytes" || generator.media != "image/jpeg" {
		t.Errorf("generator read photo %q (%s), want the call's photo", generator.photo, generator.media)
	}

	_, err := executor.Execute(context.Background(), action(`{"photoAssetId":"asset_photo_missing","settings":{"prompt":"p"}}`))
	if err == nil || strings.Contains(err.Error(), "asset_photo_missing") {
		t.Fatalf("missing photo: error = %v, want a read failure that does not quote the id", err)
	}
	if _, err := New(map[string]ImageGenerator{testImageModel: generator}, nil, newArtifacts(t)).Execute(context.Background(),
		action(`{"photoAssetId":"asset_photo_1","settings":{"prompt":"p"}}`)); err == nil {
		t.Fatal("no photo port: error = nil, want an explicit failure")
	}
}

func TestExecute_InvalidArgumentsOrUnreachableProvider_Fails(t *testing.T) {
	provider := newProvider(t)
	for name, arguments := range map[string]string{
		"missing photo":       `{"settings":{"prompt":"p"}}`,
		"settings not object": `{"photoAssetId":"p","settings":[1]}`,
		"settings missing":    `{"photoAssetId":"p"}`,
		"unknown member":      `{"photoAssetId":"p","settings":{"prompt":"p","style":"film"}}`,
		"no prompt":           `{"photoAssetId":"p","settings":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := mockExecutor(provider.URL, provider.Client(), newArtifacts(t)).Execute(context.Background(), action(arguments)); err == nil {
				t.Fatal("execute error = nil, want an explicit error")
			}
		})
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	if _, err := mockExecutor(closed.URL, nil, newArtifacts(t)).Execute(context.Background(), action(`{"photoAssetId":"p","settings":{"prompt":"p"}}`)); err == nil || strings.Contains(err.Error(), closed.URL) {
		t.Fatalf("execute error = %v, want a transport failure that does not name the URL", err)
	}
}

func TestSettingsDigest_CanonicalFormMatchesRuntimeDigest(t *testing.T) {
	// The expected value is the Runtime's digest of the same settings; both sides compare
	// it as a fact binding, so they must agree byte for byte.
	digest, _, err := SettingsDigest(json.RawMessage(`{"style":"warm","size":{"w":1024,"h":768},"seed":42,"tags":["a","b"],"note":"<a&b>","n":1.50}`))
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if digest != "sha256:de56d639d8794fe9e31e88a1b69d6310cecc13616834699a3beae98adb17736a" {
		t.Errorf("digest = %q, want the Runtime's digest of the same settings", digest)
	}
	for _, invalid := range []string{`[]`, `"x"`, `{"a":1} {}`, `{`} {
		if _, _, err := SettingsDigest(json.RawMessage(invalid)); err == nil {
			t.Errorf("SettingsDigest(%s) error = nil, want error", invalid)
		}
	}
}
