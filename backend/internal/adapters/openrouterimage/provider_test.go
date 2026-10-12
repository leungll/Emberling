package openrouterimage_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/leungll/Emberling/backend/internal/adapters/openrouterimage"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const testKey = "sk-or-test-0123456789abcdef"

const (
	testModelID     = "image-test.1"
	testDisplayName = "Image Test 1"
	testUpstream    = "vendor/image-model-test"
	testPrompt      = "Render this photo as a clay stop-motion figure"
	testMaxBytes    = 64 << 10
)

// gradientPNG is a deterministic synthetic image: a horizontal colour gradient.
func gradientPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width; x++ {
		for y := 0; y < height; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / width), G: uint8(y * 255 / height), B: 160, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode gradient: %v", err)
	}
	return buf.Bytes()
}

// captured is what the fake Images API observed of the requests it received.
type captured struct {
	requests      atomic.Int32
	path          string
	authorization string
	body          []byte
}

// fakeImages answers every request with status and body and records it.
func fakeImages(t *testing.T, status int, body string) (*httptest.Server, *captured) {
	t.Helper()
	seen := &captured{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen.requests.Add(1)
		seen.path = r.URL.Path
		seen.authorization = r.Header.Get("Authorization")
		seen.body = raw
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server, seen
}

// blockingImages holds every request until the test ends; entered is closed once the
// first request has arrived.
func blockingImages(t *testing.T) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	var once atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if once.CompareAndSwap(false, true) {
			close(entered)
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})
	return server, entered
}

func imagesAnswer(images ...[]byte) string {
	items := make([]map[string]any, 0, len(images))
	for _, content := range images {
		items = append(items, map[string]any{"b64_json": base64.StdEncoding.EncodeToString(content), "media_type": "image/png"})
	}
	raw, _ := json.Marshal(map[string]any{"created": 1, "data": items, "usage": map[string]any{"cost": 0.04}})
	return string(raw)
}

func newProvider(t *testing.T, baseURL string, client *http.Client) *openrouterimage.Provider {
	t.Helper()
	if client == nil {
		client = &http.Client{}
	}
	provider, err := openrouterimage.New(openrouterimage.Config{
		ModelID:       testModelID,
		DisplayName:   testDisplayName,
		BaseURL:       baseURL + "/api/v1/",
		APIKey:        testKey,
		Model:         testUpstream,
		HTTPClient:    client,
		MaxImageBytes: testMaxBytes,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return provider
}

func referencePhoto() []byte {
	// A JPEG signature followed by filler is enough: the Adapter forwards the photo as
	// given and never decodes it.
	return append([]byte("\xff\xd8\xff\xe0"), bytes.Repeat([]byte{0x42}, 64)...)
}

func imageRequest() openrouterimage.ImageRequest {
	seed := int64(7)
	return openrouterimage.ImageRequest{
		Prompt:             testPrompt,
		AspectRatio:        "2:3",
		Resolution:         "1K",
		Seed:               &seed,
		ReferenceImage:     referencePhoto(),
		ReferenceMediaType: "image/jpeg",
	}
}

func dataURL() string {
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(referencePhoto())
}

// readAll drains a generated image the way the Artifact store would.
func readAll(t *testing.T, generated openrouterimage.Image) ([]byte, error) {
	t.Helper()
	return io.ReadAll(generated.Content)
}

func TestOpenRouterImage_Request_CarriesModelDataURLAndSettings(t *testing.T) {
	want := gradientPNG(t, 32, 16)
	server, seen := fakeImages(t, http.StatusOK, imagesAnswer(want))
	provider := newProvider(t, server.URL, server.Client())

	generated, err := provider.GenerateImage(context.Background(), imageRequest())
	if err != nil {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	got, err := readAll(t, generated)
	if err != nil {
		t.Fatalf("read image: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("image bytes differ from the Provider's b64_json content")
	}
	if generated.MediaType != "image/png" {
		t.Errorf("MediaType = %q, want image/png", generated.MediaType)
	}

	if seen.path != "/api/v1/images" {
		t.Errorf("path = %q, want /api/v1/images", seen.path)
	}
	if seen.authorization != "Bearer "+testKey {
		t.Errorf("Authorization header does not carry the configured key")
	}
	var body map[string]any
	if err := json.Unmarshal(seen.body, &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	wantBody := map[string]any{
		"model":        testUpstream,
		"prompt":       testPrompt,
		"n":            float64(1),
		"aspect_ratio": "2:3",
		"resolution":   "1K",
		"seed":         float64(7),
		"input_references": []any{map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": dataURL()},
		}},
	}
	if diff := cmp.Diff(wantBody, body); diff != "" {
		t.Errorf("request body mismatch (-want +got):\n%s", diff)
	}
	if bytes.Contains(seen.body, []byte(testKey)) {
		t.Errorf("request body carries the API key")
	}
}

func TestOpenRouterImage_Request_OmitsUnsetOptionalSettings(t *testing.T) {
	server, seen := fakeImages(t, http.StatusOK, imagesAnswer(gradientPNG(t, 8, 8)))
	provider := newProvider(t, server.URL, server.Client())
	request := imageRequest()
	request.AspectRatio, request.Resolution, request.Seed = "", "", nil

	if _, err := provider.GenerateImage(context.Background(), request); err != nil {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(seen.body, &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	for _, member := range []string{"aspect_ratio", "resolution", "seed", "provider"} {
		if _, ok := body[member]; ok {
			t.Errorf("request body carries %q, want it omitted", member)
		}
	}
}

func TestOpenRouterImage_InvalidRequest_FailsBeforeSending(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*openrouterimage.ImageRequest)
	}{
		{"empty prompt", func(r *openrouterimage.ImageRequest) { r.Prompt = "" }},
		{"no reference image", func(r *openrouterimage.ImageRequest) { r.ReferenceImage = nil }},
		{"reference is not an image", func(r *openrouterimage.ImageRequest) { r.ReferenceMediaType = "text/plain" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, seen := fakeImages(t, http.StatusOK, imagesAnswer(gradientPNG(t, 8, 8)))
			provider := newProvider(t, server.URL, server.Client())
			request := imageRequest()
			tc.mutate(&request)

			_, err := provider.GenerateImage(context.Background(), request)
			var imageErr *openrouterimage.Error
			if !errors.As(err, &imageErr) {
				t.Fatalf("GenerateImage() error = %v, want *openrouterimage.Error", err)
			}
			if n := seen.requests.Load(); n != 0 {
				t.Errorf("requests = %d, want 0", n)
			}
		})
	}
}

func TestOpenRouterImage_TooManyRequests_SingleRequestNoRetry(t *testing.T) {
	server, seen := fakeImages(t, http.StatusTooManyRequests, `{"error":{"code":429,"message":"rate limited"}}`)
	provider := newProvider(t, server.URL, server.Client())

	_, err := provider.GenerateImage(context.Background(), imageRequest())

	var imageErr *openrouterimage.Error
	if !errors.As(err, &imageErr) {
		t.Fatalf("GenerateImage() error = %v, want *openrouterimage.Error", err)
	}
	if imageErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want 429", imageErr.StatusCode)
	}
	if n := seen.requests.Load(); n != 1 {
		t.Errorf("requests = %d, want exactly 1: the Adapter never retries", n)
	}
}

func TestOpenRouterImage_ServerError_SingleRequestNoRetry(t *testing.T) {
	server, seen := fakeImages(t, http.StatusBadGateway, `{"error":{"message":"upstream"}}`)
	provider := newProvider(t, server.URL, server.Client())

	if _, err := provider.GenerateImage(context.Background(), imageRequest()); err == nil {
		t.Fatal("GenerateImage() error = nil, want a failure")
	}
	if n := seen.requests.Load(); n != 1 {
		t.Errorf("requests = %d, want exactly 1", n)
	}
}

func TestOpenRouterImage_Non2xx_ErrorOmitsKeyBodyAndDataURL(t *testing.T) {
	// The Provider echoes the key, the prompt and the data URL; none may surface.
	echo := fmt.Sprintf(`{"error":{"code":400,"message":"bad key %s for prompt %s with %s"}}`, testKey, testPrompt, dataURL())
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server, _ := fakeImages(t, status, echo)
			provider := newProvider(t, server.URL, server.Client())

			_, err := provider.GenerateImage(context.Background(), imageRequest())

			var imageErr *openrouterimage.Error
			if !errors.As(err, &imageErr) || imageErr.StatusCode != status {
				t.Fatalf("GenerateImage() error = %v, want *openrouterimage.Error with status %d", err, status)
			}
			want := fmt.Sprintf("openrouterimage: images API returned HTTP %d", status)
			if err.Error() != want {
				t.Errorf("error = %q, want exactly %q", err.Error(), want)
			}
			for _, secret := range []string{testKey, testPrompt, "data:image", "rate", "bad key"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error %q contains %q", err.Error(), secret)
				}
			}
		})
	}
}

func TestOpenRouterImage_OversizedOrNonImagePayload_FailsWithoutTruncation(t *testing.T) {
	oversized := append(gradientPNG(t, 8, 8), bytes.Repeat([]byte{0}, testMaxBytes)...)
	huge := strings.Repeat("A", (testMaxBytes+2)/3*4+(64<<10)+16)
	for _, tc := range []struct {
		name string
		body string
	}{
		{"decoded image over the cap", imagesAnswer(oversized)},
		{"response body over the read cap", `{"data":[{"b64_json":"` + huge + `"}]}`},
		{"not an image", imagesAnswer([]byte("<html>this is not an image at all</html>"))},
		{"GIF is not an accepted type", imagesAnswer([]byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"))},
		{"empty image", imagesAnswer([]byte{})},
		{"invalid base64", `{"data":[{"b64_json":"iVBORw0KGgo!!!!"}]}`},
		{"body is not JSON", `<html>gateway</html>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := fakeImages(t, http.StatusOK, tc.body)
			provider := newProvider(t, server.URL, server.Client())

			generated, err := provider.GenerateImage(context.Background(), imageRequest())
			if err == nil {
				// A failure the Adapter can only see while streaming must surface as a
				// read error, never as a short but successful read.
				_, err = readAll(t, generated)
			}
			var imageErr *openrouterimage.Error
			if !errors.As(err, &imageErr) {
				t.Fatalf("error = %v, want *openrouterimage.Error", err)
			}
		})
	}
}

func TestOpenRouterImage_CorruptBase64AfterHead_FailsOnReadNotShortRead(t *testing.T) {
	content := append(gradientPNG(t, 8, 8), bytes.Repeat([]byte{0x11}, 4096)...)
	encoded := []byte(base64.StdEncoding.EncodeToString(content))
	encoded[len(encoded)-16] = '!'
	body := `{"data":[{"b64_json":"` + string(encoded) + `"}]}`
	server, _ := fakeImages(t, http.StatusOK, body)
	provider := newProvider(t, server.URL, server.Client())

	generated, err := provider.GenerateImage(context.Background(), imageRequest())
	if err != nil {
		t.Fatalf("GenerateImage() error = %v, want the failure to surface while streaming", err)
	}
	_, err = readAll(t, generated)
	var imageErr *openrouterimage.Error
	if !errors.As(err, &imageErr) {
		t.Fatalf("read error = %v, want *openrouterimage.Error", err)
	}
}

func TestOpenRouterImage_ImageExactlyAtCap_Succeeds(t *testing.T) {
	head := gradientPNG(t, 8, 8)
	content := append(head, bytes.Repeat([]byte{0}, testMaxBytes-len(head))...)
	server, _ := fakeImages(t, http.StatusOK, imagesAnswer(content))
	provider := newProvider(t, server.URL, server.Client())

	generated, err := provider.GenerateImage(context.Background(), imageRequest())
	if err != nil {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	got, err := readAll(t, generated)
	if err != nil || len(got) != testMaxBytes {
		t.Fatalf("read = %d bytes, %v; want %d bytes", len(got), err, testMaxBytes)
	}
}

func TestOpenRouterImage_MultipleImages_Fails(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"two images", imagesAnswer(gradientPNG(t, 8, 8), gradientPNG(t, 9, 9))},
		{"zero images", `{"data":[]}`},
		{"no data member", `{"usage":{"cost":0}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := fakeImages(t, http.StatusOK, tc.body)
			provider := newProvider(t, server.URL, server.Client())

			_, err := provider.GenerateImage(context.Background(), imageRequest())
			var imageErr *openrouterimage.Error
			if !errors.As(err, &imageErr) {
				t.Fatalf("GenerateImage() error = %v, want *openrouterimage.Error", err)
			}
		})
	}
}

func TestOpenRouterImage_CallerContextCancelled_WrapsContextError(t *testing.T) {
	server, entered := blockingImages(t)
	provider := newProvider(t, server.URL, server.Client())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-entered
		cancel()
	}()
	_, err := provider.GenerateImage(ctx, imageRequest())

	var imageErr *openrouterimage.Error
	if !errors.As(err, &imageErr) || !errors.Is(err, context.Canceled) {
		t.Fatalf("GenerateImage() error = %v, want *openrouterimage.Error wrapping context.Canceled", err)
	}
}

func TestOpenRouterImage_CallerDeadlinePassed_WrapsDeadlineExceeded(t *testing.T) {
	server, seen := fakeImages(t, http.StatusOK, imagesAnswer(gradientPNG(t, 8, 8)))
	provider := newProvider(t, server.URL, server.Client())

	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	_, err := provider.GenerateImage(ctx, imageRequest())

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("GenerateImage() error = %v, want it to wrap context.DeadlineExceeded so the Runtime sees its own deadline", err)
	}
	if n := seen.requests.Load(); n != 0 {
		t.Errorf("requests = %d, want 0", n)
	}
}

// TestOpenRouterImage_ClientTimeout_NotReportedAsContextDeadline guards the Agent timeout
// decision: the Adapter's own HTTP timeout is a Tool failure, not the Agent Run's
// deadline, so it must not wrap a context error.
func TestOpenRouterImage_ClientTimeout_NotReportedAsContextDeadline(t *testing.T) {
	server, _ := blockingImages(t)
	client := server.Client()
	client.Timeout = time.Millisecond
	provider := newProvider(t, server.URL, client)

	_, err := provider.GenerateImage(context.Background(), imageRequest())

	var imageErr *openrouterimage.Error
	if !errors.As(err, &imageErr) {
		t.Fatalf("GenerateImage() error = %v, want *openrouterimage.Error", err)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		t.Errorf("GenerateImage() error = %v wraps a context error, want a plain Adapter error", err)
	}
	if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "data:image") {
		t.Errorf("error %q leaks the key or the data URL", err.Error())
	}
}

func TestOpenRouterImage_String_RedactsKey(t *testing.T) {
	provider := newProvider(t, "http://images.invalid", nil)

	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("provider", "value", provider)
	for name, rendered := range map[string]string{
		"String":   provider.String(),
		"%v":       fmt.Sprintf("%v", provider),
		"%+v":      fmt.Sprintf("%+v", provider),
		"%#v":      fmt.Sprintf("%#v", provider),
		"slog":     logged.String(),
		"value %v": fmt.Sprintf("%v", *provider),
	} {
		if strings.Contains(rendered, testKey) {
			t.Errorf("%s rendering %q contains the API key", name, rendered)
		}
	}
	if !strings.Contains(provider.String(), testModelID) {
		t.Errorf("String() = %q, want it to name the model ID", provider.String())
	}
}

func TestOpenRouterImage_Generate_RefusesDecisionRequest(t *testing.T) {
	server, seen := fakeImages(t, http.StatusOK, imagesAnswer(gradientPNG(t, 8, 8)))
	provider := newProvider(t, server.URL, server.Client())

	_, err := provider.Generate(context.Background(), registry.ModelRequest{ModelID: testModelID, Instructions: "decide"})

	var imageErr *openrouterimage.Error
	if !errors.As(err, &imageErr) {
		t.Fatalf("Generate() error = %v, want *openrouterimage.Error", err)
	}
	if n := seen.requests.Load(); n != 0 {
		t.Errorf("requests = %d, want 0: an image model never answers a decision request", n)
	}
}

func TestOpenRouterImage_Models_RegistersReferenceImageGenerationOnly(t *testing.T) {
	provider := newProvider(t, "http://images.invalid", nil)

	models, err := provider.Models(context.Background())
	if err != nil {
		t.Fatalf("Models() error = %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("Models() = %d registrations, want 1", len(models))
	}
	got := models[0]
	if got.ID != testModelID || got.DisplayName != testDisplayName {
		t.Errorf("registration = %q/%q, want %q/%q", got.ID, got.DisplayName, testModelID, testDisplayName)
	}
	if diff := cmp.Diff([]string{"reference_image_generation"}, got.Capabilities); diff != "" {
		t.Errorf("capabilities mismatch (-want +got):\n%s", diff)
	}
	var schema map[string]any
	if err := json.Unmarshal(got.ConfigSchema, &schema); err != nil {
		t.Fatalf("ConfigSchema is not JSON: %v", err)
	}
	if diff := cmp.Diff(map[string]any{"type": "object", "additionalProperties": false}, schema); diff != "" {
		t.Errorf("ConfigSchema mismatch (-want +got):\n%s", diff)
	}
}

func TestOpenRouterImage_New_MissingSetting_FailsWithoutRepeatingKey(t *testing.T) {
	valid := openrouterimage.Config{
		ModelID: testModelID, DisplayName: testDisplayName, BaseURL: "http://images.invalid",
		APIKey: testKey, Model: testUpstream, MaxImageBytes: testMaxBytes,
	}
	for _, tc := range []struct {
		name   string
		mutate func(*openrouterimage.Config)
	}{
		{"model ID", func(c *openrouterimage.Config) { c.ModelID = "" }},
		{"display name", func(c *openrouterimage.Config) { c.DisplayName = " " }},
		{"base URL", func(c *openrouterimage.Config) { c.BaseURL = "" }},
		{"API key", func(c *openrouterimage.Config) { c.APIKey = "" }},
		{"model name", func(c *openrouterimage.Config) { c.Model = "" }},
		{"image cap", func(c *openrouterimage.Config) { c.MaxImageBytes = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			tc.mutate(&cfg)
			_, err := openrouterimage.New(cfg)
			if err == nil {
				t.Fatal("New() error = nil, want a missing-setting error")
			}
			if strings.Contains(err.Error(), testKey) {
				t.Errorf("error %q repeats the API key", err.Error())
			}
		})
	}
}
