package mediaoutput

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// externalImageRef is the canonical encoding of a valid EXTERNAL ImageRef, the shape
// image_generation publishes on its `image` port.
const externalImageRef = `{"source":"EXTERNAL","uri":"https://example.test/a.png","mediaType":"image/png","width":1024}`

func TestRegistration_Validate_Passes(t *testing.T) {
	if err := registry.NewNodeRegistry().Register(Registration()); err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}
}

func TestMediaOutputExecutor_ValidateSemantics_AlwaysPasses(t *testing.T) {
	if err := (Executor{}).ValidateSemantics(context.Background(), map[string]any{}); err != nil {
		t.Fatalf("ValidateSemantics() error = %v, want nil", err)
	}
}

func TestMediaOutput_Execute_CombinesImageRefAndCaption(t *testing.T) {
	in := registry.NodeInput{Ports: map[string]json.RawMessage{
		"image":   json.RawMessage(externalImageRef),
		"caption": json.RawMessage(`"a small ember creature"`),
	}}
	result, err := (Executor{}).Execute(context.Background(), in, map[string]any{})
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if string(result.Output.Ports["image"]) != externalImageRef {
		t.Fatalf("Execute().Output.Ports[image] = %s, want the normalised reference %s", result.Output.Ports["image"], externalImageRef)
	}
	if _, err := domain.ParseImageRef(result.Output.Ports["image"]); err != nil {
		t.Fatalf("Execute().Output.Ports[image] is not a valid ImageRef: %v", err)
	}
	if string(result.Output.Ports["caption"]) != `"a small ember creature"` {
		t.Fatalf("Execute().Output.Ports[caption] = %s, want the input value unchanged", result.Output.Ports["caption"])
	}
}

// TestMediaOutput_Execute_PreservesAssetBranch: an ASSET reference is preserved as it
// arrived, member for member -- this node stores the normalised reference, it does not
// re-derive or rewrite it (08 §2.2).
func TestMediaOutput_Execute_PreservesAssetBranch(t *testing.T) {
	asset := `{"source":"ASSET","asset":{"assetId":"asset_123","mediaType":"image/png","sizeBytes":102400,"sha256":"abc"}}`
	in := registry.NodeInput{Ports: map[string]json.RawMessage{
		"image":   json.RawMessage(asset),
		"caption": json.RawMessage(`"a small ember creature"`),
	}}
	result, err := (Executor{}).Execute(context.Background(), in, map[string]any{})
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if string(result.Output.Ports["image"]) != asset {
		t.Fatalf("Execute().Output.Ports[image] = %s, want %s", result.Output.Ports["image"], asset)
	}
}

// TestMediaOutput_Execute_ImageIsNotAnImageRef_Fails: 08 §2.2 forbids this node from
// accepting an arbitrary JSON object. A Run.output must not be able to carry a
// Provider-private or legacy reference shape just because something upstream produced one.
func TestMediaOutput_Execute_ImageIsNotAnImageRef_Fails(t *testing.T) {
	cases := map[string]string{
		"legacy url shape":     `{"url":"https://example.test/a.png","mediaType":"image/png"}`,
		"arbitrary object":     `{"anything":"at all"}`,
		"empty object":         `{}`,
		"string":               `"https://example.test/a.png"`,
		"provider private key": `{"source":"EXTERNAL","uri":"https://example.test/a.png","mediaType":"image/png","providerJobId":"job_1"}`,
		"incomplete asset":     `{"source":"ASSET","asset":{"assetId":"asset_123"}}`,
	}
	for name, image := range cases {
		t.Run(name, func(t *testing.T) {
			in := registry.NodeInput{Ports: map[string]json.RawMessage{
				"image":   json.RawMessage(image),
				"caption": json.RawMessage(`"a small ember creature"`),
			}}
			result, err := (Executor{}).Execute(context.Background(), in, map[string]any{})
			if err == nil {
				t.Fatalf("Execute() error = nil, want an error for an image port that is not an ImageRef; result = %+v", result)
			}
		})
	}
}

func TestMediaOutput_Execute_MissingImage_Fails(t *testing.T) {
	in := registry.NodeInput{Ports: map[string]json.RawMessage{
		"caption": json.RawMessage(`"a small ember creature"`),
	}}
	if _, err := (Executor{}).Execute(context.Background(), in, map[string]any{}); err == nil {
		t.Fatal("Execute() error = nil, want error for missing required image port")
	}
}

func TestMediaOutput_Execute_CaptionNotString_Fails(t *testing.T) {
	in := registry.NodeInput{Ports: map[string]json.RawMessage{
		"image":   json.RawMessage(externalImageRef),
		"caption": json.RawMessage(`42`),
	}}
	if _, err := (Executor{}).Execute(context.Background(), in, map[string]any{}); err == nil {
		t.Fatal("Execute() error = nil, want error for non-string caption port")
	}
}
