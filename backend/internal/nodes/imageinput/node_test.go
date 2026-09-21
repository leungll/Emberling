package imageinput

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const validAssetRefJSON = `{"assetId":"asset_123","mediaType":"image/png","sizeBytes":102400,"sha256":"abc"}`

func TestRegistration_Validate_Passes(t *testing.T) {
	if err := registry.NewNodeRegistry().Register(Registration()); err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}
}

func TestImageInputExecutor_Execute_PresentAssetRefReturnsAssetImageRef(t *testing.T) {
	config := map[string]any{"inputKey": "reference", "required": true}
	runInput := json.RawMessage(`{"reference":` + validAssetRefJSON + `}`)

	result, err := Executor{}.Execute(context.Background(), registry.NodeInput{RunInput: runInput}, config)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if result.Kind != registry.NodeResultCompleted {
		t.Fatalf("Execute().Kind = %v, want COMPLETED", result.Kind)
	}
	ref, err := domain.ParseImageRef(result.Output.Ports["image"])
	if err != nil {
		t.Fatalf("the `image` port does not carry an ImageRef: %v (port=%s)", err, result.Output.Ports["image"])
	}
	if ref.Source != domain.ImageSourceAsset {
		t.Errorf("image.source = %q, want %q", ref.Source, domain.ImageSourceAsset)
	}
	if ref.Asset == nil {
		t.Fatalf("image ref carries no asset: %+v", ref)
	}
	want := domain.AssetRef{AssetID: "asset_123", MediaType: "image/png", SizeBytes: 102400, SHA256: "abc"}
	if *ref.Asset != want {
		t.Errorf("image.asset = %+v, want %+v", *ref.Asset, want)
	}
}

func TestImageInputExecutor_Execute_MissingRequiredKeyFails(t *testing.T) {
	config := map[string]any{"inputKey": "reference", "required": true}

	_, err := Executor{}.Execute(context.Background(), registry.NodeInput{RunInput: json.RawMessage(`{}`)}, config)

	if err == nil {
		t.Fatal("Execute() error = nil, want error for missing required run input key")
	}
}

func TestImageInputExecutor_Execute_MissingOptionalKeyReturnsNullPort(t *testing.T) {
	config := map[string]any{"inputKey": "reference", "required": false}

	result, err := Executor{}.Execute(context.Background(), registry.NodeInput{RunInput: json.RawMessage(`{}`)}, config)

	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if string(result.Output.Ports["image"]) != "null" {
		t.Fatalf("Execute().Output.Ports[image] = %s, want null for an absent optional key", result.Output.Ports["image"])
	}
}

func TestImageInputExecutor_Execute_ValueIsNotAnAssetRefFails(t *testing.T) {
	config := map[string]any{"inputKey": "reference", "required": true}
	runInput := json.RawMessage(`{"reference":{"assetId":"asset_123","storageKey":"a/asset_123"}}`)

	_, err := Executor{}.Execute(context.Background(), registry.NodeInput{RunInput: runInput}, config)

	if err == nil {
		t.Fatal("Execute() error = nil, want error for a run input value that is not an AssetRef")
	}
}

func TestImageInputExecutor_Execute_RunInputNotObjectFails(t *testing.T) {
	config := map[string]any{"inputKey": "reference", "required": true}

	_, err := Executor{}.Execute(context.Background(), registry.NodeInput{RunInput: json.RawMessage(`[]`)}, config)

	if err == nil {
		t.Fatal("Execute() error = nil, want error when run input is not a JSON object")
	}
}
