package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/fixture/mockprovider"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
)

// countingTransport counts every request sent through the client it belongs to.
type countingTransport struct {
	requests atomic.Int32
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.requests.Add(1)
	return http.DefaultTransport.RoundTrip(r)
}

// TestRegisterTools_MockImageUsesDispatchClientOnly covers the image Tool wiring: the
// mock image model is the only image model with a generator, and every request it makes
// - the generation and the fetch of the generated image - goes through the bounded
// dispatch client handed in, not a client of its own. Another image Model ID has no
// generator and fails without a request.
func TestRegisterTools_MockImageUsesDispatchClientOnly(t *testing.T) {
	provider := httptest.NewServer(mockprovider.NewServer(mockprovider.NewDispatcher(nil)))
	t.Cleanup(provider.Close)
	transport := &countingTransport{}
	dispatchClient := &http.Client{Transport: transport, Timeout: defaultTaskDispatchTimeout}

	tools := registry.NewToolRegistry()
	if err := registerImageTool(tools, provider.URL, dispatchClient, nil, asset.NewArtifactStore(t.TempDir(), 1<<20)); err != nil {
		t.Fatalf("registerImageTool() error = %v", err)
	}
	reg, ok := tools.Get(generateimage.ToolName)
	if !ok {
		t.Fatalf("tool %q not registered", generateimage.ToolName)
	}
	if !reg.Metadata.UsesImageModel {
		t.Errorf("tool %q does not declare UsesImageModel", generateimage.ToolName)
	}

	call := registry.ToolAction{
		RunID: "run_1", AgentRunID: "ar_1", TurnID: "turn_1", ActionID: "act_1", ToolName: generateimage.ToolName, AttemptNo: 1,
		Arguments:    json.RawMessage(`{"photoAssetId":"asset_photo_1","settings":{"prompt":"Soft film look"}}`),
		ImageModelID: mockmodel.ImageModelID,
	}
	result, err := reg.Executor.Execute(context.Background(), call)
	if err != nil || result.Kind != registry.ToolResultCompleted {
		t.Fatalf("mock image generation: result %+v err %v, want COMPLETED", result, err)
	}
	if got := transport.requests.Load(); got != 2 {
		t.Errorf("dispatch client requests = %d, want 2 (generation and image fetch)", got)
	}

	call.ImageModelID = "image-real-1"
	if _, err := reg.Executor.Execute(context.Background(), call); err == nil {
		t.Fatal("unwired image model: error = nil, want a failure")
	}
	if got := transport.requests.Load(); got != 2 {
		t.Errorf("dispatch client requests after the unwired call = %d, want still 2", got)
	}
}

type fakeAssetOpener struct {
	asset   domain.Asset
	content string
	err     error
}

func (f fakeAssetOpener) OpenContent(_ context.Context, assetID string) (domain.Asset, io.ReadCloser, error) {
	if f.err != nil {
		return domain.Asset{}, nil, f.err
	}
	if assetID != f.asset.AssetID {
		return domain.Asset{}, nil, domain.ErrNotFound
	}
	return f.asset, io.NopCloser(strings.NewReader(f.content)), nil
}

func TestAssetPhotoReader_OpensAssetContentWithMediaType(t *testing.T) {
	reader := assetPhotoReader{assets: fakeAssetOpener{asset: domain.Asset{AssetID: "asset_1", MediaType: "image/jpeg"}, content: "jpeg-bytes"}}
	photo, err := reader.OpenPhoto(context.Background(), "asset_1")
	if err != nil {
		t.Fatalf("OpenPhoto() error = %v", err)
	}
	defer func() { _ = photo.Content.Close() }()
	body, _ := io.ReadAll(photo.Content)
	if photo.MediaType != "image/jpeg" || string(body) != "jpeg-bytes" {
		t.Errorf("photo = %s %q, want image/jpeg \"jpeg-bytes\"", photo.MediaType, body)
	}
	if _, err := reader.OpenPhoto(context.Background(), "asset_missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing asset error = %v, want ErrNotFound", err)
	}
}
