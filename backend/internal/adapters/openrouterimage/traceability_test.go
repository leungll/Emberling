package openrouterimage_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"math"
	"net/http"
	"strings"
	"testing"
)

// answerWith is a one-image answer followed by the extra top-level members in extra,
// which is either empty or starts with a comma.
func answerWith(content []byte, extra string) string {
	return `{"created":1,"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(content) + `"}]` + extra + `}`
}

func costOf(v float64) *float64 { return &v }

// TestOpenRouterImage_UsageCost_BoundedAndOptional covers the traceability fields of a
// successful answer: the upstream-reported model name and cost are returned when they are
// present and within bounds, and are dropped - never failing an image that was already
// generated - when they are absent, of the wrong type or out of bounds.
func TestOpenRouterImage_UsageCost_BoundedAndOptional(t *testing.T) {
	longest := strings.Repeat("m", 128)
	for _, tc := range []struct {
		name      string
		extra     string
		wantModel string
		wantCost  *float64
	}{
		{"both reported", `,"model":"bytedance/seedream-5.0-pro","usage":{"prompt_tokens":10,"cost":0.045,"cost_details":{"upstream_inference_cost":0.045}}`, "bytedance/seedream-5.0-pro", costOf(0.045)},
		{"neither reported", ``, "", nil},
		{"usage without cost", `,"usage":{"prompt_tokens":10}`, "", nil},
		{"zero cost", `,"usage":{"cost":0}`, "", costOf(0)},
		{"negative zero cost", `,"usage":{"cost":-0}`, "", costOf(0)},
		{"negative cost", `,"usage":{"cost":-0.01}`, "", nil},
		{"cost overflows a float", `,"usage":{"cost":1e400}`, "", nil},
		{"cost is a string", `,"usage":{"cost":"0.045"}`, "", nil},
		{"cost is null", `,"usage":{"cost":null}`, "", nil},
		{"usage is not an object", `,"usage":5`, "", nil},
		{"model at the bound", `,"model":"` + longest + `"`, longest, nil},
		{"model over the bound", `,"model":"` + longest + `x"`, "", nil},
		{"model counted in characters", `,"model":"` + strings.Repeat("é", 128) + `"`, strings.Repeat("é", 128), nil},
		{"empty model", `,"model":""`, "", nil},
		{"blank model", `,"model":"   "`, "", nil},
		{"model with a control character", `,"model":"vendor/model\nforged"`, "", nil},
		{"model is not a string", `,"model":42`, "", nil},
		{"model is null", `,"model":null`, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := gradientPNG(t, 8, 8)
			server, seen := fakeImages(t, http.StatusOK, answerWith(want, tc.extra))
			provider := newProvider(t, server.URL, server.Client())

			generated, err := provider.GenerateImage(context.Background(), imageRequest())
			if err != nil {
				t.Fatalf("GenerateImage() error = %v, want the image regardless of the traceability fields", err)
			}
			got, err := readAll(t, generated)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("image read = %d bytes, err %v; want the Provider's image", len(got), err)
			}
			if generated.Model != tc.wantModel {
				t.Errorf("Model = %q, want %q", generated.Model, tc.wantModel)
			}
			switch {
			case tc.wantCost == nil && generated.Cost != nil:
				t.Errorf("Cost = %v, want none", *generated.Cost)
			case tc.wantCost != nil && generated.Cost == nil:
				t.Errorf("Cost = none, want %v", *tc.wantCost)
			case tc.wantCost != nil && *generated.Cost != *tc.wantCost:
				t.Errorf("Cost = %v, want %v", *generated.Cost, *tc.wantCost)
			case generated.Cost != nil && math.Signbit(*generated.Cost):
				t.Errorf("Cost = -0, want 0")
			}
			if n := seen.requests.Load(); n != 1 {
				t.Errorf("requests = %d, want 1", n)
			}
		})
	}
}
