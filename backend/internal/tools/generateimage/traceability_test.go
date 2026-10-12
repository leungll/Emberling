package generateimage

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/registry"
)

// tracedGenerator answers with a fixed image and the given traceability fields.
type tracedGenerator struct {
	image []byte
	model string
	cost  *float64
}

func (g *tracedGenerator) Generate(context.Context, ImageRequest) (GeneratedImage, error) {
	return GeneratedImage{Content: io.NopCloser(strings.NewReader(string(g.image))), Model: g.model, Cost: g.cost}, nil
}

func costValue(v float64) *float64 { return &v }

// tracedOutput runs one call against a generator reporting model and cost and returns the
// decoded output members.
func tracedOutput(t *testing.T, model string, cost *float64) (json.RawMessage, map[string]json.RawMessage) {
	t.Helper()
	executor := New(map[string]ImageGenerator{testImageModel: &tracedGenerator{image: pngBytes(t), model: model, cost: cost}}, nil, newArtifacts(t))
	got, err := executor.Execute(context.Background(), action(`{"photoAssetId":"asset_photo_1","settings":{"prompt":"p"}}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	validateOutput(t, got.Result.Output)
	var members map[string]json.RawMessage
	if err := json.Unmarshal(got.Result.Output, &members); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	return got.Result.Output, members
}

// TestGenerateImage_ReportedModelAndCost_CarriedInOutput covers a generator that reports
// the upstream model and the call's cost: the result carries them as model and
// usage.cost, alongside the unchanged artifact members.
func TestGenerateImage_ReportedModelAndCost_CarriedInOutput(t *testing.T) {
	output, members := tracedOutput(t, "bytedance/seedream-5.0-pro", costValue(0.045))
	if got := string(members["model"]); got != `"bytedance/seedream-5.0-pro"` {
		t.Errorf("model = %s, want the reported model", got)
	}
	if got := string(members["usage"]); got != `{"cost":0.045}` {
		t.Errorf("usage = %s, want {\"cost\":0.045}", got)
	}
	for _, member := range []string{"assetRef", "imageUrl", "settingsDigest", "artifact"} {
		if _, ok := members[member]; !ok {
			t.Errorf("output %s lacks %q", output, member)
		}
	}

	_, zero := tracedOutput(t, "", costValue(0))
	if got := string(zero["usage"]); got != `{"cost":0}` {
		t.Errorf("usage = %s, want a reported zero cost kept as {\"cost\":0}", got)
	}
}

// TestGenerateImage_NothingReported_OutputOmitsModelAndUsage covers a generator that
// reports neither field, as the mock image model does: the result has no model and no
// usage member rather than empty placeholders.
func TestGenerateImage_NothingReported_OutputOmitsModelAndUsage(t *testing.T) {
	output, members := tracedOutput(t, "", nil)
	for _, member := range []string{"model", "usage"} {
		if _, ok := members[member]; ok {
			t.Errorf("output %s carries %q, want it omitted", output, member)
		}
	}

	provider := newProvider(t)
	got, err := mockExecutor(provider.URL, provider.Client(), newArtifacts(t)).Execute(context.Background(),
		action(`{"photoAssetId":"asset_photo_1","settings":{"prompt":"Soft film look"}}`))
	if err != nil {
		t.Fatalf("execute mock: %v", err)
	}
	if strings.Contains(string(got.Result.Output), `"model"`) || strings.Contains(string(got.Result.Output), `"usage"`) {
		t.Errorf("mock output %s carries model or usage, want neither", got.Result.Output)
	}
}

// TestGenerateImage_OutOfBoundsModelOrCost_DroppedNotFailed covers traceability values a
// generator reports outside the result's bounds: each is left out of the result, which
// stays valid, and the already generated image is not failed over a tracing field.
func TestGenerateImage_OutOfBoundsModelOrCost_DroppedNotFailed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model string
		cost  *float64
	}{
		{"model over 128 characters", strings.Repeat("m", 129), nil},
		{"blank model", "  ", nil},
		{"model with a control character", "vendor/model\u0000", nil},
		{"negative cost", "", costValue(-0.01)},
		{"infinite cost", "", costValue(math.Inf(1))},
		{"not-a-number cost", "", costValue(math.NaN())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, members := tracedOutput(t, tc.model, tc.cost)
			for _, member := range []string{"model", "usage"} {
				if _, ok := members[member]; ok {
					t.Errorf("output %s carries %q, want it dropped", output, member)
				}
			}
		})
	}
}

// TestOutputSchema_ModelAndUsage_BoundedAndOptional pins the result contract of the
// traceability members: model is an optional string of 1 to 128 characters, usage is an
// optional object whose only member is a non-negative cost, and neither is required.
func TestOutputSchema_ModelAndUsage_BoundedAndOptional(t *testing.T) {
	schema, err := registry.CompileSchema(json.RawMessage(outputSchema))
	if err != nil {
		t.Fatalf("compile output schema: %v", err)
	}
	base := `"assetRef":"artifact_0123456789abcdef0123456789abcdef","imageUrl":"/api/runs/run_1/artifacts/artifact_0123456789abcdef0123456789abcdef/content","settingsDigest":"sha256:abc","artifact":{"artifactId":"artifact_0123456789abcdef0123456789abcdef","mediaType":"image/png","sizeBytes":1,"sha256":"` + strings.Repeat("a", 64) + `"}`
	validate := func(extra string) error {
		var value any
		if err := json.Unmarshal([]byte(`{`+base+extra+`}`), &value); err != nil {
			t.Fatalf("test output is not JSON: %v", err)
		}
		return registry.ValidateValue(schema, value)
	}
	for _, valid := range []string{
		``,
		`,"model":"m"`,
		`,"model":"` + strings.Repeat("m", 128) + `"`,
		`,"usage":{"cost":0}`,
		`,"usage":{"cost":0.045}`,
		`,"usage":{}`,
		`,"model":"bytedance/seedream-5.0-pro","usage":{"cost":0.045}`,
	} {
		if err := validate(valid); err != nil {
			t.Errorf("output with %q rejected: %v", valid, err)
		}
	}
	for _, invalid := range []string{
		`,"model":""`,
		`,"model":"` + strings.Repeat("m", 129) + `"`,
		`,"model":42`,
		`,"usage":{"cost":-0.01}`,
		`,"usage":{"cost":"0.045"}`,
		`,"usage":{"cost":0.045,"prompt_tokens":10}`,
		`,"usage":5`,
		`,"provider":"x"`,
	} {
		if err := validate(invalid); err == nil {
			t.Errorf("output with %.60q accepted, want rejected", invalid)
		}
	}
}
