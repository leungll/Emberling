package generateimage

import (
	"context"
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
