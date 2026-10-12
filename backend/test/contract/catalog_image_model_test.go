//go:build integration

package contract

import (
	"net/http"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
)

// TestToolRegistry_UsesImageModel_ExposedInCatalog covers the catalogue side of the image
// model selection: every Tool states usesImageModel explicitly, only generate_image
// declares it, and the mock image model declares reference image generation next to its
// asynchronous image generation capability.
func TestToolRegistry_UsesImageModel_ExposedInCatalog(t *testing.T) {
	env := newTestEnv(t)

	resp, body := env.doJSON(t, http.MethodGet, "/api/tools", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/tools status = %d, body=%s", resp.StatusCode, body)
	}
	tools := catalogItemsByKey(t, body, "name")
	for name, want := range map[string]string{generateimage.ToolName: "true", "lookup": "false", "review_asset": "false"} {
		item, ok := tools[name]
		if !ok {
			t.Fatalf("GET /api/tools does not list %s: %s", name, body)
		}
		if got := string(item["usesImageModel"]); got != want {
			t.Errorf("%s.usesImageModel = %q, want %s", name, got, want)
		}
	}

	resp, body = env.doJSON(t, http.MethodGet, "/api/models", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/models status = %d, body=%s", resp.StatusCode, body)
	}
	models := catalogItemsByKey(t, body, "id")
	for id, want := range map[string]string{
		mockmodel.ImageModelID: `["image_generation","reference_image_generation"]`,
		mockmodel.ModelID:      `["text_generation","structured_decision"]`,
	} {
		if got := compactJSON(t, string(models[id]["capabilities"])); got != want {
			t.Errorf("%s.capabilities = %s, want %s", id, got, want)
		}
	}
}
