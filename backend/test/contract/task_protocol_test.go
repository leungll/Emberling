//go:build integration

// This file is the task protocol contract shared by the deterministic Mock Provider and
// the task gateway. Both serve the same asynchronous task protocol to the same mocktask
// Adapter, so the same cases run against each of them, in process: a new task answers 202
// with an externalTaskId, a replayed idempotency key answers 200 with the original task,
// a status query reports RUNNING until the task ends and then the callback payload byte
// for byte, an unknown task is 404, and the callback carries the {externalTaskId,
// payload} envelope with the callback token in its header.
//
// Each target holds its task open until the test completes it: the Mock Provider through
// its "lost" scenario and its redeliver control, the gateway through a gated upstream.
// The gateway's upstream is the real Images API Adapter talking to the Mock Provider's
// Images API route.
package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/openrouterimage"
	"github.com/leungll/Emberling/backend/internal/fixture/mockprovider"
	"github.com/leungll/Emberling/backend/internal/fixture/taskgateway"
)

// protocolCallback is one callback as the receiver saw it.
type protocolCallback struct {
	token string
	body  []byte
}

// taskProtocolTarget is one Provider under test. request holds the members every
// dispatch to it carries besides the callback; complete ends a held task.
type taskProtocolTarget struct {
	baseURL   string
	request   map[string]any
	complete  func(t *testing.T, externalTaskID string)
	callbacks chan protocolCallback
	callback  string
}

// newProtocolCallbackReceiver records every callback POSTed to it and answers 200.
func newProtocolCallbackReceiver(t *testing.T) (string, chan protocolCallback) {
	t.Helper()
	received := make(chan protocolCallback, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- protocolCallback{token: r.Header.Get(callbackTokenHeaderForTest), body: body}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server.URL + "/api/callbacks", received
}

// newMockProviderProtocolTarget serves the Mock Provider with its test controls: a "lost"
// task stays RUNNING until the redeliver control delivers its callback.
func newMockProviderProtocolTarget(t *testing.T) *taskProtocolTarget {
	t.Helper()
	record, err := mockprovider.OpenRecord(filepath.Join(t.TempDir(), "record.jsonl"))
	if err != nil {
		t.Fatalf("open mock provider record: %v", err)
	}
	dispatcher := mockprovider.NewDispatcher(nil)
	server := httptest.NewServer(mockprovider.NewServer(dispatcher, mockprovider.WithTestControls(record)))
	t.Cleanup(func() {
		server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = dispatcher.Shutdown(ctx)
		_ = record.Close()
	})
	callbackURL, callbacks := newProtocolCallbackReceiver(t)
	return &taskProtocolTarget{
		baseURL: server.URL,
		request: map[string]any{"prompt": "a small ember creature", "delayMs": "lost"},
		complete: func(t *testing.T, externalTaskID string) {
			t.Helper()
			resp, err := http.Post(server.URL+"/control/tasks/"+externalTaskID+"/callback", "application/json", nil)
			if err != nil {
				t.Fatalf("redeliver %s: %v", externalTaskID, err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("redeliver %s status = %d", externalTaskID, resp.StatusCode)
			}
		},
		callbacks: callbacks,
		callback:  callbackURL,
	}
}

// gatewayImageGenerator mirrors cmd/taskgateway's bridge from the Images API Adapter to
// the gateway's upstream port, which a test cannot import from package main.
type gatewayImageGenerator struct {
	provider *openrouterimage.Provider
	gate     chan struct{}
}

func (g gatewayImageGenerator) Generate(ctx context.Context, req taskgateway.GenerateRequest) (taskgateway.GeneratedImage, error) {
	if g.gate != nil {
		select {
		case <-g.gate:
		case <-ctx.Done():
			return taskgateway.GeneratedImage{}, ctx.Err()
		}
	}
	image, err := g.provider.GenerateImage(ctx, openrouterimage.ImageRequest{
		Prompt: req.Prompt, ReferenceImage: req.ReferenceImage, ReferenceMediaType: req.ReferenceMediaType,
	})
	if err != nil {
		var adapterErr *openrouterimage.Error
		if errors.As(err, &adapterErr) {
			return taskgateway.GeneratedImage{}, errors.New(adapterErr.Error())
		}
		return taskgateway.GeneratedImage{}, errors.New("upstream image call failed")
	}
	content, err := io.ReadAll(image.Content)
	if err != nil {
		return taskgateway.GeneratedImage{}, errors.New("upstream image could not be read")
	}
	return taskgateway.GeneratedImage{MediaType: image.MediaType, Content: content, Cost: image.Cost}, nil
}

// newMockImagesAPIProvider is the real Images API Adapter for modelID, talking to an
// in-process Mock Provider's Images API route.
func newMockImagesAPIProvider(t *testing.T, modelID string) *openrouterimage.Provider {
	t.Helper()
	dispatcher := mockprovider.NewDispatcher(nil)
	upstream := httptest.NewServer(mockprovider.NewServer(dispatcher))
	t.Cleanup(func() {
		upstream.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = dispatcher.Shutdown(ctx)
	})
	provider, err := openrouterimage.New(openrouterimage.Config{
		ModelID: modelID, DisplayName: "Gateway Test Model", BaseURL: upstream.URL + "/api/v1",
		APIKey: "contract-test-upstream-key", Model: "vendor/test-image-model", MaxImageBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("openrouterimage.New() error = %v", err)
	}
	return provider
}

// protocolReferencePNG is a small decodable PNG.
func protocolReferencePNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// newTaskGatewayProtocolTarget serves the gateway with one gated model; complete opens
// the gate, so every task completes from then on.
func newTaskGatewayProtocolTarget(t *testing.T) *taskProtocolTarget {
	t.Helper()
	const modelID = "gateway-protocol-test"
	reference := protocolReferencePNG(t)
	references := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(reference)
	}))
	t.Cleanup(references.Close)

	gate := make(chan struct{})
	server, err := taskgateway.NewServer(taskgateway.Config{
		Models:          map[string]taskgateway.ImageGenerator{modelID: gatewayImageGenerator{provider: newMockImagesAPIProvider(t, modelID), gate: gate}},
		AssetBaseURL:    references.URL,
		PublicBaseURL:   "http://gateway.contract-test.invalid",
		Workers:         2,
		QueueSize:       8,
		MaxTaskDuration: time.Minute,
		MaxImageBytes:   1 << 20,
		ImageStoreBytes: 4 << 20,
	})
	if err != nil {
		t.Fatalf("taskgateway.NewServer() error = %v", err)
	}
	httpServer := httptest.NewServer(server)
	var opened bool
	t.Cleanup(func() {
		httpServer.Close()
		if !opened {
			close(gate)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	callbackURL, callbacks := newProtocolCallbackReceiver(t)
	return &taskProtocolTarget{
		baseURL: httpServer.URL,
		request: map[string]any{
			"prompt":    "a small ember creature",
			"reference": map[string]any{"source": "EXTERNAL", "uri": references.URL + "/photo.png", "mediaType": "image/png"},
			"options":   map[string]any{"modelId": modelID, "width": 1024},
		},
		complete: func(*testing.T, string) {
			if !opened {
				opened = true
				close(gate)
			}
		},
		callbacks: callbacks,
		callback:  callbackURL,
	}
}

// runTaskProtocolSuite runs check once against the Mock Provider and once against the
// task gateway.
func runTaskProtocolSuite(t *testing.T, check func(t *testing.T, target *taskProtocolTarget)) {
	t.Helper()
	for name, newTarget := range map[string]func(*testing.T) *taskProtocolTarget{
		"mockprovider": newMockProviderProtocolTarget,
		"taskgateway":  newTaskGatewayProtocolTarget,
	} {
		t.Run(name, func(t *testing.T) { check(t, newTarget(t)) })
	}
}

// dispatch POSTs one task with token and key and returns the status and externalTaskId.
func (p *taskProtocolTarget) dispatch(t *testing.T, token, key string) (int, string) {
	t.Helper()
	body := map[string]any{"callbackUrl": p.callback, "callbackToken": token}
	if key != "" {
		body["idempotencyKey"] = key
	}
	for member, value := range p.request {
		body[member] = value
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(p.baseURL+"/v1/tasks", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST /v1/tasks: %v", err)
	}
	defer resp.Body.Close()
	var answer struct {
		ExternalTaskID string `json:"externalTaskId"`
	}
	answerRaw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(answerRaw, &answer)
	if strings.Contains(string(answerRaw), token) {
		t.Fatalf("dispatch answer %s echoes the callback token", answerRaw)
	}
	return resp.StatusCode, answer.ExternalTaskID
}

// status GETs one task status.
func (p *taskProtocolTarget) status(t *testing.T, externalTaskID string) (int, map[string]json.RawMessage, []byte) {
	t.Helper()
	resp, err := http.Get(p.baseURL + "/v1/tasks/" + externalTaskID)
	if err != nil {
		t.Fatalf("GET /v1/tasks/%s: %v", externalTaskID, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var members map[string]json.RawMessage
	_ = json.Unmarshal(raw, &members)
	return resp.StatusCode, members, raw
}

func (p *taskProtocolTarget) nextCallback(t *testing.T) protocolCallback {
	t.Helper()
	select {
	case got := <-p.callbacks:
		return got
	case <-time.After(e2eWait):
		t.Fatal("no callback arrived")
		return protocolCallback{}
	}
}

func TestTaskProtocol_NewTask_Returns202WithExternalTaskID(t *testing.T) {
	runTaskProtocolSuite(t, func(t *testing.T, target *taskProtocolTarget) {
		status, id := target.dispatch(t, "protocol-token-1", "nr_new")
		if status != http.StatusAccepted || id == "" {
			t.Fatalf("POST /v1/tasks = %d with externalTaskId %q, want 202 with an id", status, id)
		}
	})
}

func TestTaskProtocol_KeyReplay_Returns200WithOriginalTask(t *testing.T) {
	runTaskProtocolSuite(t, func(t *testing.T, target *taskProtocolTarget) {
		firstStatus, first := target.dispatch(t, "protocol-token-1", "nr_replay")
		replayStatus, replay := target.dispatch(t, "protocol-token-2", "nr_replay")
		if firstStatus != http.StatusAccepted || replayStatus != http.StatusOK || replay != first {
			t.Fatalf("dispatch then replay = %d %q, %d %q; want 202 then 200 with the same task", firstStatus, first, replayStatus, replay)
		}
	})
}

func TestTaskProtocol_Status_RunningThenTerminalWithCallbackPayload(t *testing.T) {
	runTaskProtocolSuite(t, func(t *testing.T, target *taskProtocolTarget) {
		_, id := target.dispatch(t, "protocol-token-1", "nr_status")
		code, running, raw := target.status(t, id)
		if code != http.StatusOK || string(running["status"]) != `"RUNNING"` || len(running["payload"]) != 0 {
			t.Fatalf("status while held = %d %s, want RUNNING without a payload", code, raw)
		}
		if strings.Contains(string(raw), "protocol-token-1") || strings.Contains(string(raw), target.callback) {
			t.Fatalf("status %s carries the callback token or URL", raw)
		}

		target.complete(t, id)
		delivered := target.nextCallback(t)
		var envelope struct {
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(delivered.body, &envelope); err != nil {
			t.Fatalf("callback body %s: %v", delivered.body, err)
		}
		code, terminal, raw := target.status(t, id)
		if code != http.StatusOK || string(terminal["status"]) != `"SUCCEEDED"` {
			t.Fatalf("status after completion = %d %s, want SUCCEEDED", code, raw)
		}
		var fromStatus, fromCallback any
		_ = json.Unmarshal(terminal["payload"], &fromStatus)
		_ = json.Unmarshal(envelope.Payload, &fromCallback)
		statusPayload, _ := json.Marshal(fromStatus)
		callbackPayload, _ := json.Marshal(fromCallback)
		if !bytes.Equal(statusPayload, callbackPayload) {
			t.Fatalf("status payload %s differs from the callback payload %s", terminal["payload"], envelope.Payload)
		}
	})
}

func TestTaskProtocol_UnknownTask_Returns404(t *testing.T) {
	runTaskProtocolSuite(t, func(t *testing.T, target *taskProtocolTarget) {
		if code, _, raw := target.status(t, "task_never_accepted"); code != http.StatusNotFound {
			t.Fatalf("GET unknown task = %d %s, want 404", code, raw)
		}
	})
}

func TestTaskProtocol_Callback_CarriesEnvelopeAndTokenHeader(t *testing.T) {
	runTaskProtocolSuite(t, func(t *testing.T, target *taskProtocolTarget) {
		_, id := target.dispatch(t, "protocol-token-1", "nr_callback")
		target.complete(t, id)
		delivered := target.nextCallback(t)
		if delivered.token != "protocol-token-1" {
			t.Fatalf("callback token header = %q, want the dispatched token", delivered.token)
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(delivered.body, &envelope); err != nil {
			t.Fatalf("callback body %s: %v", delivered.body, err)
		}
		if len(envelope) != 2 || string(envelope["externalTaskId"]) != `"`+id+`"` {
			t.Fatalf("callback envelope = %s, want exactly {externalTaskId, payload} for %s", delivered.body, id)
		}
		var payload struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(envelope["payload"], &payload); err != nil || payload.Status != "SUCCEEDED" {
			t.Fatalf("callback payload = %s, want a SUCCEEDED status", envelope["payload"])
		}
		if strings.Contains(string(delivered.body), "protocol-token-1") {
			t.Fatalf("callback body %s carries the token", delivered.body)
		}
	})
}
