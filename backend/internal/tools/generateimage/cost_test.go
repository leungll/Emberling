package generateimage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/domain"
)

func tracedResult(t *testing.T, cost *float64) *tracedGenerator {
	t.Helper()
	return &tracedGenerator{image: pngBytes(t), model: "upstream/model", cost: cost}
}

// TestGenerateImage_ReportedCost_RecordedOnToolResult: the charge the generator reports
// becomes the result's Cost, in US dollars, attributed to the image Model ID the call
// used, so the Runtime records it beside the result.
func TestGenerateImage_ReportedCost_RecordedOnToolResult(t *testing.T) {
	executor := New(map[string]ImageGenerator{testImageModel: tracedResult(t, costValue(0.045))}, nil, newArtifacts(t))
	got, err := executor.Execute(context.Background(), action(`{"photoAssetId":"asset_photo_1","settings":{"prompt":"p"}}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	want := &domain.Cost{Amount: "0.045", Currency: domain.CurrencyUSD, Source: testImageModel}
	if diff := cmp.Diff(want, got.Result.Cost); diff != "" {
		t.Fatalf("result cost (-want +got):\n%s", diff)
	}
}

// TestMockGenerator_ProviderPricesGeneration_ToolResultCarriesIt: the Mock Provider's
// generation answer prices itself, and the mock generator carries that charge to the Tool
// like any other generator would, so a call through the mock image model is recorded
// with a known cost attributed to the image Model ID instead of counted as unpriced.
func TestMockGenerator_ProviderPricesGeneration_ToolResultCarriesIt(t *testing.T) {
	provider := newProvider(t)
	got, err := mockExecutor(provider.URL, provider.Client(), newArtifacts(t)).Execute(context.Background(),
		action(`{"photoAssetId":"asset_photo_1","settings":{"prompt":"Soft film look"}}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	want := &domain.Cost{Amount: "0.02", Currency: domain.CurrencyUSD, Source: testImageModel}
	if diff := cmp.Diff(want, got.Result.Cost); diff != "" {
		t.Fatalf("result cost (-want +got):\n%s", diff)
	}
	var out result
	if err := json.Unmarshal(got.Result.Output, &out); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if out.Usage == nil || out.Usage.Cost == nil || *out.Usage.Cost != 0.02 || out.Model != "" {
		t.Fatalf("output usage = %+v model = %q, want usage.cost 0.02 and no model name for the mock image model", out.Usage, out.Model)
	}
}

// TestMockGenerator_ProviderOmitsUsage_GeneratedImageCostIsNil: a generation answer
// without usage, or with a usage whose cost is not a number, leaves the generated image
// without a charge and does not fail the generation, so the Tool records none.
func TestMockGenerator_ProviderOmitsUsage_GeneratedImageCostIsNil(t *testing.T) {
	for name, usage := range map[string]string{"absent": "", "not a number": `,"usage":{"cost":"free"}`, "not an object": `,"usage":"0.02"`} {
		t.Run(name, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/assets" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"assetId":"img_0000000000000001","imageUrl":"http://` + r.Host + `/v1/assets/img_0000000000000001.png"` + usage + `}`))
					return
				}
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(pngBytes(t))
			}))
			t.Cleanup(provider.Close)
			image, err := NewMockGenerator(provider.URL, provider.Client()).Generate(context.Background(), ImageRequest{PhotoAssetID: "asset_photo_1", SettingsDigest: "sha256:aa"})
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			_ = image.Content.Close()
			if image.Cost != nil {
				t.Fatalf("generated image cost = %v, want nil when the answer reports no usable charge", *image.Cost)
			}
		})
	}
}

// TestGenerateImage_NoReportedCost_ToolResultCostIsNil: a generator that reports no charge,
// or one outside the amount bounds, leaves the result without a Cost rather than with a
// zero the Provider never reported.
func TestGenerateImage_NoReportedCost_ToolResultCostIsNil(t *testing.T) {
	for name, cost := range map[string]*float64{"none": nil, "negative": costValue(-0.01)} {
		t.Run(name, func(t *testing.T) {
			executor := New(map[string]ImageGenerator{testImageModel: tracedResult(t, cost)}, nil, newArtifacts(t))
			got, err := executor.Execute(context.Background(), action(`{"photoAssetId":"asset_photo_1","settings":{"prompt":"p"}}`))
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if got.Result.Cost != nil {
				t.Fatalf("result cost = %+v, want nil", *got.Result.Cost)
			}
		})
	}
}
