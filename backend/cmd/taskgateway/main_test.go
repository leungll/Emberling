package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/openrouterimage"
	"github.com/leungll/Emberling/backend/internal/config"
	"github.com/leungll/Emberling/backend/internal/fixture/taskgateway"
)

const testAPIKey = "sk-or-test-key-must-stay-secret"

func envOf(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func completeEnv() map[string]string {
	return map[string]string{
		config.KeyImageBaseURL:      "https://images.example.test/api/v1",
		config.KeyImageAPIKey:       testAPIKey,
		config.KeyTaskGatewayModels: `[{"id":"gateway-seedream-5.0-pro","model":"bytedance-seed/seedream-5-0-pro","displayName":"Seedream 5.0 Pro"}]`,
		keyBaseURL:                  "http://backend:8080",
		keyPublicURL:                "http://localhost:9104",
	}
}

func TestLoadConfig_Complete_AppliesDefaults(t *testing.T) {
	cfg, err := loadConfig(envOf(completeEnv()))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.addr != ":9104" || cfg.workers != 4 || cfg.queueSize != 64 || cfg.maxTaskDuration != 15*time.Minute ||
		cfg.maxImageBytes != 16<<20 || cfg.imageStoreBytes != 256<<20 || cfg.imageTimeout != config.DefaultImageTimeout {
		t.Fatalf("cfg defaults = %+v", cfg)
	}
	if len(cfg.models) != 1 || cfg.models[0].Model != "bytedance-seed/seedream-5-0-pro" {
		t.Fatalf("cfg.models = %+v", cfg.models)
	}
}

func TestLoadConfig_MissingRequired_RejectedWithoutValues(t *testing.T) {
	for _, key := range []string{config.KeyImageBaseURL, config.KeyImageAPIKey, config.KeyTaskGatewayModels, keyBaseURL, keyPublicURL} {
		t.Run(key, func(t *testing.T) {
			env := completeEnv()
			delete(env, key)
			_, err := loadConfig(envOf(env))
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("loadConfig() error = %v, want it to name %s", err, key)
			}
			for _, value := range completeEnv() {
				if strings.Contains(err.Error(), value) {
					t.Fatalf("error %q quotes a configured value", err)
				}
			}
		})
	}
}

func TestLoadConfig_InvalidValues_RejectedWithoutValues(t *testing.T) {
	for key, value := range map[string]string{
		config.KeyImageBaseURL:      "ftp://secret-host",
		keyBaseURL:                  "http://user:secret@backend:8080",
		keyPublicURL:                "localhost:9104",
		config.KeyTaskGatewayModels: `[{"id":"seedream-secret","model":"m/x","displayName":"X"}]`,
		keyWorkers:                  "0",
		keyQueueSize:                "secret",
		keyMaxTaskDuration:          "-1m",
		keyMaxImageBytes:            "big",
		keyImageStoreBytes:          "-5",
		config.KeyImageTimeout:      "soon",
	} {
		t.Run(key, func(t *testing.T) {
			env := completeEnv()
			env[key] = value
			_, err := loadConfig(envOf(env))
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("loadConfig() error = %v, want it to name %s", err, key)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), testAPIKey) {
				t.Fatalf("error %q quotes a configured value", err)
			}
		})
	}
}

// fakeImagesAPI answers the Images API with one PNG and a cost, or with failStatus and
// a body that echoes the Authorization header, as a hostile upstream might.
func fakeImagesAPI(t *testing.T, failStatus int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failStatus != 0 {
			w.WriteHeader(failStatus)
			_, _ = w.Write([]byte(`{"error":{"message":"bad key ` + r.Header.Get("Authorization") + `"}}`))
			return
		}
		var buf bytes.Buffer
		_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2)))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  []map[string]any{{"b64_json": base64.StdEncoding.EncodeToString(buf.Bytes())}},
			"usage": map[string]any{"cost": 0.04},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func testGenerator(t *testing.T, baseURL string) imageGenerator {
	t.Helper()
	provider, err := openrouterimage.New(openrouterimage.Config{
		ModelID: "gateway-test", DisplayName: "Test", BaseURL: baseURL, APIKey: testAPIKey,
		Model: "vendor/model", MaxImageBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("openrouterimage.New() error = %v", err)
	}
	return imageGenerator{provider: provider}
}

func referencePNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 3, 3))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestImageGenerator_Success_ReturnsImageAndCost(t *testing.T) {
	g := testGenerator(t, fakeImagesAPI(t, 0).URL)
	got, err := g.Generate(context.Background(), taskgateway.GenerateRequest{Prompt: "p", ReferenceImage: referencePNG(t), ReferenceMediaType: "image/png"})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if got.MediaType != "image/png" || len(got.Content) == 0 || got.Cost == nil || *got.Cost != 0.04 {
		t.Fatalf("Generate() = %s, %d bytes, cost %v", got.MediaType, len(got.Content), got.Cost)
	}
}

func TestImageGenerator_UpstreamFailure_ReportsFixedLabelWithoutCredential(t *testing.T) {
	g := testGenerator(t, fakeImagesAPI(t, http.StatusUnauthorized).URL)
	_, err := g.Generate(context.Background(), taskgateway.GenerateRequest{Prompt: "secret prompt", ReferenceImage: referencePNG(t), ReferenceMediaType: "image/png"})
	if err == nil {
		t.Fatal("Generate() error = nil, want the upstream failure")
	}
	if err.Error() != "openrouterimage: images API returned HTTP 401" {
		t.Fatalf("Generate() error = %q, want the Adapter's fixed label", err)
	}
	if strings.Contains(err.Error(), testAPIKey) || strings.Contains(err.Error(), "secret prompt") {
		t.Fatalf("Generate() error %q leaks the key or the prompt", err)
	}
}
