package generateimage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/mockprovider"
	"github.com/leungll/Emberling/backend/internal/registry"
)

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

func action(arguments string) registry.ToolAction {
	return registry.ToolAction{
		AgentRunID: "ar_1", TurnID: "turn_1", ActionID: "act_1",
		ToolName: ToolName, AttemptNo: 1, Arguments: json.RawMessage(arguments),
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
	reg := Registration("http://mock-provider.test", nil, nil)
	if reg.Metadata.ExecutionKind != domain.ToolExecutionSync {
		t.Errorf("executionKind = %s, want SYNC", reg.Metadata.ExecutionKind)
	}
	if want := (domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown}); reg.Metadata.SideEffect != want {
		t.Errorf("sideEffect = %+v, want %+v", reg.Metadata.SideEffect, want)
	}
	if !reg.Metadata.CountsTowardGenerationLimit {
		t.Error("countsTowardGenerationLimit = false, want true")
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

func TestExecute_ProviderGenerates_ReturnsReachableAssetAndSettingsDigest(t *testing.T) {
	provider := newProvider(t)

	got, err := New(provider.URL, provider.Client(), newArtifacts(t)).Execute(context.Background(),
		action(`{"photoAssetId":"asset_photo_1","settings":{"style":"film","strength":0.6}}`))
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
	if out.SettingsDigest != "sha256:6b4fe6c42faebe96fef7242ea90658446d38165bb01b65e98a2541594c1b78b1" {
		t.Errorf("settingsDigest = %q, want the canonical digest of the settings", out.SettingsDigest)
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

	again, err := New(provider.URL, provider.Client(), newArtifacts(t)).Execute(context.Background(),
		action(`{"settings":{"strength":0.6, "style":"film"},"photoAssetId":"asset_photo_1"}`))
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

	got, err := New(provider.URL, provider.Client(), newArtifacts(t)).Execute(context.Background(),
		action(`{"photoAssetId":"asset_photo_1","settings":{"style":"film"}}`))
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

func TestExecute_FailControl_FailsWithoutQuotingArguments(t *testing.T) {
	provider := newProvider(t)

	_, err := New(provider.URL, provider.Client(), newArtifacts(t)).Execute(context.Background(),
		action(`{"photoAssetId":"asset_photo_secretish","settings":{"mock":"fail","style":"film"}}`))
	if err == nil {
		t.Fatal("execute error = nil, want a definite generation failure")
	}
	if !strings.Contains(err.Error(), "status 422") {
		t.Errorf("error = %v, want the Provider's rejection status", err)
	}
	if strings.Contains(err.Error(), "asset_photo_secretish") || strings.Contains(err.Error(), provider.URL) {
		t.Errorf("error %q quotes arguments or the Provider URL", err)
	}
}

func TestExecute_InvalidArgumentsOrUnreachableProvider_Fails(t *testing.T) {
	provider := newProvider(t)
	for name, arguments := range map[string]string{
		"missing photo":       `{"settings":{}}`,
		"settings not object": `{"photoAssetId":"p","settings":[1]}`,
		"settings missing":    `{"photoAssetId":"p"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(provider.URL, provider.Client(), newArtifacts(t)).Execute(context.Background(), action(arguments)); err == nil {
				t.Fatal("execute error = nil, want an explicit error")
			}
		})
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	if _, err := New(closed.URL, nil, newArtifacts(t)).Execute(context.Background(), action(`{"photoAssetId":"p","settings":{}}`)); err == nil || strings.Contains(err.Error(), closed.URL) {
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
