package taskgateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testWait bounds how long a test waits for an expected fact; it never orders events.
const testWait = 10 * time.Second

const (
	testModelID = "gateway-test-model"
	testToken   = "callback-token-must-stay-secret"
	testAPIKey  = "upstream-api-key-must-stay-secret"
	testPrompt  = "prompt-text-must-not-be-logged"
)

var testNow = time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

func testPNG(t *testing.T) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 4, 4))
	canvas.Set(1, 1, color.RGBA{R: 200, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// fakeGenerator counts calls, optionally blocks each call until released, and answers
// with a fixed image or error.
type fakeGenerator struct {
	calls   atomic.Int32
	entered chan struct{}
	gate    chan struct{}
	image   GeneratedImage
	err     error

	mu       sync.Mutex
	requests []GenerateRequest
}

func (g *fakeGenerator) Generate(ctx context.Context, req GenerateRequest) (GeneratedImage, error) {
	g.calls.Add(1)
	g.mu.Lock()
	g.requests = append(g.requests, req)
	g.mu.Unlock()
	if g.entered != nil {
		g.entered <- struct{}{}
	}
	if g.gate != nil {
		select {
		case <-g.gate:
		case <-ctx.Done():
			return GeneratedImage{}, ctx.Err()
		}
	}
	return g.image, g.err
}

// receivedCallback is one callback delivery as the receiver saw it.
type receivedCallback struct {
	token string
	body  []byte
}

// callbackReceiver records every callback and answers with the next status from
// statuses, then 200.
type callbackReceiver struct {
	server    *httptest.Server
	received  chan receivedCallback
	mu        sync.Mutex
	statuses  []int
	callCount int
}

func newCallbackReceiver(t *testing.T, statuses ...int) *callbackReceiver {
	t.Helper()
	c := &callbackReceiver{received: make(chan receivedCallback, 16), statuses: statuses}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		status := http.StatusOK
		if c.callCount < len(c.statuses) {
			status = c.statuses[c.callCount]
		}
		c.callCount++
		c.mu.Unlock()
		c.received <- receivedCallback{token: r.Header.Get(callbackHeader), body: body}
		w.WriteHeader(status)
	}))
	t.Cleanup(c.server.Close)
	return c
}

func (c *callbackReceiver) next(t *testing.T) receivedCallback {
	t.Helper()
	select {
	case got := <-c.received:
		return got
	case <-time.After(testWait):
		t.Fatal("no callback arrived")
		return receivedCallback{}
	}
}

func (c *callbackReceiver) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.callCount
}

// referenceServer serves Asset content under Emberling's content route and plain images
// for EXTERNAL references. Unknown assets answer 404; "/broken" answers 502.
type referenceServer struct {
	server  *httptest.Server
	content []byte
	headers chan http.Header
}

func newReferenceServer(t *testing.T, content []byte) *referenceServer {
	t.Helper()
	r := &referenceServer{content: content, headers: make(chan http.Header, 16)}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.headers <- req.Header.Clone()
		switch req.URL.Path {
		case "/api/assets/ast_1/content", "/external/photo.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(r.content)
		case "/broken":
			w.WriteHeader(http.StatusBadGateway)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *referenceServer) assetRef(content []byte) map[string]any {
	sum := sha256.Sum256(content)
	return map[string]any{"source": "ASSET", "asset": map[string]any{
		"assetId": "ast_1", "mediaType": "image/png", "sizeBytes": len(content), "sha256": hex.EncodeToString(sum[:]),
	}}
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// sequentialIDs returns deterministic ids per prefix.
func sequentialIDs() func(string) (string, error) {
	var mu sync.Mutex
	counters := map[string]int{}
	return func(prefix string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		counters[prefix]++
		return fmt.Sprintf("%s%016d", prefix, counters[prefix]), nil
	}
}

type gatewayFixture struct {
	server     *Server
	http       *httptest.Server
	generator  *fakeGenerator
	callbacks  *callbackReceiver
	references *referenceServer
	logs       *syncBuffer
}

type fixtureOptions struct {
	generator       *fakeGenerator
	callbackStatus  []int
	workers         int
	queueSize       int
	imageStoreBytes int64
	after           func(time.Duration) <-chan time.Time
	// now overrides the gateway clock; nil means the fixed testNow.
	now func() time.Time
}

func newGatewayFixture(t *testing.T, opts fixtureOptions) *gatewayFixture {
	t.Helper()
	content := testPNG(t)
	if opts.generator == nil {
		cost := 0.04
		opts.generator = &fakeGenerator{image: GeneratedImage{MediaType: "image/png", Content: content, Cost: &cost}}
	}
	if opts.workers == 0 {
		opts.workers = 2
	}
	if opts.queueSize == 0 {
		opts.queueSize = 8
	}
	if opts.imageStoreBytes == 0 {
		opts.imageStoreBytes = 1 << 20
	}
	if opts.now == nil {
		opts.now = func() time.Time { return testNow }
	}
	if opts.after == nil {
		opts.after = func(time.Duration) <-chan time.Time {
			ready := make(chan time.Time, 1)
			ready <- testNow
			return ready
		}
	}
	logs := &syncBuffer{}
	references := newReferenceServer(t, content)
	callbacks := newCallbackReceiver(t, opts.callbackStatus...)
	server, err := NewServer(Config{
		Models:          map[string]ImageGenerator{testModelID: opts.generator},
		AssetBaseURL:    references.server.URL,
		PublicBaseURL:   "http://gateway.public.test:9104/",
		Workers:         opts.workers,
		QueueSize:       opts.queueSize,
		MaxTaskDuration: 15 * time.Minute,
		MaxImageBytes:   1 << 20,
		ImageStoreBytes: opts.imageStoreBytes,
		Logger:          slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:             opts.now,
		After:           opts.after,
		NewID:           sequentialIDs(),
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		httpServer.Close()
		ctx, cancel := context.WithTimeout(context.Background(), testWait)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	return &gatewayFixture{server: server, http: httpServer, generator: opts.generator, callbacks: callbacks, references: references, logs: logs}
}

// taskBody is a valid dispatch with the given reference.
func (f *gatewayFixture) taskBody(key string, reference any) map[string]any {
	body := map[string]any{
		"callbackUrl":   f.callbacks.server.URL + "/api/callbacks",
		"callbackToken": testToken,
		"prompt":        testPrompt,
		"options":       map[string]any{"modelId": testModelID, "width": 1024},
	}
	if key != "" {
		body["idempotencyKey"] = key
	}
	if reference != nil {
		body["reference"] = reference
	}
	return body
}

func (f *gatewayFixture) post(t *testing.T, body any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode body: %v", err)
	}
	resp, err := http.Post(f.http.URL+"/v1/tasks", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST /v1/tasks: %v", err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, answer
}

func (f *gatewayFixture) get(t *testing.T, path string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(f.http.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, answer
}

func externalTaskIDOf(t *testing.T, raw []byte) string {
	t.Helper()
	var answer taskResponse
	if err := json.Unmarshal(raw, &answer); err != nil || answer.ExternalTaskID == "" {
		t.Fatalf("task response %s carries no externalTaskId (%v)", raw, err)
	}
	return answer.ExternalTaskID
}

// decodedCallback splits a callback body into its envelope and payload.
func decodedCallback(t *testing.T, body []byte) (string, resultPayload, json.RawMessage) {
	t.Helper()
	var envelope callbackBody
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("callback body %s is not an envelope: %v", body, err)
	}
	var payload resultPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		t.Fatalf("callback payload %s is not a result payload: %v", envelope.Payload, err)
	}
	return envelope.ExternalTaskID, payload, envelope.Payload
}

var errUpstream = errors.New("openrouterimage: images API returned HTTP 503")

func mustNotContain(t *testing.T, where, text string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(text, secret) {
			t.Errorf("%s contains %q: %s", where, secret, text)
		}
	}
}
