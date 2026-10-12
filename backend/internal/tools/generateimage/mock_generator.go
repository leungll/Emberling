package generateimage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ProviderID is the stable identifier of the Provider the mock image model calls; it is
// not a credential.
const ProviderID = "mock-asset-provider-v1"

const assetsPath = "/v1/assets"

// defaultTimeout bounds one request when no HTTP client is injected. The Agent deadline
// still arrives through ctx and wins whenever it is shorter.
const defaultTimeout = 30 * time.Second

// maxResponseBytes bounds the generation response the mock generator reads.
const maxResponseBytes = 64 << 10

// mockFailDirective at the start of the prompt makes the Mock Provider refuse the
// generation, which is a definite failure: no asset exists afterwards. It follows the
// mock decision model's `mock:` directive convention; a real image model treats the same
// text as an ordinary prompt.
const mockFailDirective = "mock:fail"

const outcomeFailed = "failed"

// MockGenerator is the ImageGenerator of the deterministic mock image model: one
// synchronous POST /v1/assets to the Mock Provider, addressed by the photo's Asset id and
// the settings digest, followed by fetching the generated image. It never reads the
// photo's bytes.
type MockGenerator struct {
	baseURL    string
	httpClient *http.Client
}

// NewMockGenerator returns the mock image model's generator calling the Mock Provider at
// baseURL. A nil client means a default client bounded by defaultTimeout.
func NewMockGenerator(baseURL string, client *http.Client) *MockGenerator {
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	return &MockGenerator{baseURL: strings.TrimRight(baseURL, "/"), httpClient: client}
}

type assetRequest struct {
	PhotoAssetID   string `json:"photoAssetId"`
	SettingsDigest string `json:"settingsDigest"`
	Outcome        string `json:"outcome,omitempty"`
}

type assetResponse struct {
	AssetID  string `json:"assetId"`
	ImageURL string `json:"imageUrl"`
}

// Generate sends one generation request and opens the generated image.
//
// The image is fetched from baseURL, not from the returned imageUrl's own origin: only the
// scheme and host of imageUrl are replaced by those of baseURL, and the path is kept. The
// imageUrl may name a public origin the Backend cannot reach (a browser-facing address in
// a container deployment), and fetching only from the configured Provider means a
// Provider response can never point the Backend at an arbitrary host. Neither the
// Provider's asset id nor its address leaves the generator: only the image bytes do.
//
// It runs once: a failed fetch is reported as an error, never retried, because the
// generation itself already happened and its side effect is not idempotent.
func (g *MockGenerator) Generate(ctx context.Context, request ImageRequest) (GeneratedImage, error) {
	body := assetRequest{PhotoAssetID: request.PhotoAssetID, SettingsDigest: request.SettingsDigest}
	if strings.HasPrefix(request.Settings.Prompt, mockFailDirective) {
		body.Outcome = outcomeFailed
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return GeneratedImage{}, generationError("build request", 0, "request body could not be encoded")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+assetsPath, bytes.NewReader(encoded))
	if err != nil {
		return GeneratedImage{}, generationError("build request", 0, "request could not be created")
	}
	httpRequest.Header.Set("Content-Type", "application/json")

	response, err := g.httpClient.Do(httpRequest)
	if err != nil {
		return GeneratedImage{}, generationError("send request", 0, "transport failure")
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		// The body is not read into the error: a Provider may echo request content there.
		return GeneratedImage{}, generationError("provider rejected the generation", response.StatusCode, "non-200 status")
	}
	var generated assetResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&generated); err != nil {
		return GeneratedImage{}, generationError("decode response", response.StatusCode, "response body is not an asset response")
	}
	if generated.AssetID == "" || generated.ImageURL == "" {
		return GeneratedImage{}, generationError("decode response", response.StatusCode, "response carries no assetId or imageUrl")
	}

	content, err := g.openImage(ctx, generated.ImageURL)
	if err != nil {
		return GeneratedImage{}, err
	}
	return GeneratedImage{Content: content}, nil
}

// openImage fetches the generated image from the configured Provider. Errors carry fixed
// labels only - never the image URL or a response body.
func (g *MockGenerator) openImage(ctx context.Context, imageURL string) (io.ReadCloser, error) {
	fetchURL, err := g.fetchURL(imageURL)
	if err != nil {
		return nil, generationError("fetch image", 0, "imageUrl is not a usable image address")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, generationError("fetch image", 0, "request could not be created")
	}
	response, err := g.httpClient.Do(request)
	if err != nil {
		return nil, generationError("fetch image", 0, "transport failure")
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, generationError("fetch image", response.StatusCode, "non-200 status")
	}
	return response.Body, nil
}

// fetchURL replaces the scheme and host of imageURL with those of the configured Provider
// base URL, keeping the path and query.
func (g *MockGenerator) fetchURL(imageURL string) (string, error) {
	base, err := url.Parse(g.baseURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return "", errors.New("provider base URL is not absolute")
	}
	image, err := url.Parse(imageURL)
	if err != nil || image.Host == "" || (image.Scheme != "http" && image.Scheme != "https") || image.Path == "" {
		return "", errors.New("imageUrl is not an absolute http(s) URL")
	}
	fetch := url.URL{Scheme: base.Scheme, Host: base.Host, Path: image.Path, RawPath: image.RawPath, RawQuery: image.RawQuery}
	return fetch.String(), nil
}
