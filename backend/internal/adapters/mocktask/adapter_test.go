package mocktask

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const testToken = "plaintext-callback-token-9f3c"

func testCallback() registry.CallbackContext {
	return registry.CallbackContext{URL: "http://emberling.test/callbacks/attempt_1", Token: testToken}
}

func TestMockTaskAdapter_Dispatch_ReturnsExternalTask(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode dispatch body: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"externalTaskId":"provider_task_789"}`))
	}))
	t.Cleanup(server.Close)

	task, err := New(server.URL, server.Client()).Dispatch(
		context.Background(), "a small ember creature", nil, map[string]any{"modelId": "image-model-v1", "width": float64(1024)}, testCallback(), "nr_1")
	if err != nil {
		t.Fatalf("Dispatch() error = %v, want nil", err)
	}
	if task.ProviderID != ProviderID {
		t.Fatalf("ProviderID = %q, want %q", task.ProviderID, ProviderID)
	}
	if task.ExternalTaskID != "provider_task_789" {
		t.Fatalf("ExternalTaskID = %q, want %q", task.ExternalTaskID, "provider_task_789")
	}
	if got := body["prompt"]; got != "a small ember creature" {
		t.Fatalf("prompt = %v, want the node's prompt", got)
	}
	options, _ := body["options"].(map[string]any)
	if options["width"] != float64(1024) {
		t.Fatalf("options = %v, want the node config forwarded", body["options"])
	}
}

// TestMockTaskAdapter_Dispatch_SuccessPayloadIsAnImageRef pins what the fabricated success
// callback carries: a valid domain.ImageRef, never a Provider-private object. The
// EXTERNAL branch's uri is the Mock Provider's own image route, so it is stable,
// credential-free and readable for as long as that Provider is serving (08 §2.2, 05 §1.2).
func TestMockTaskAdapter_Dispatch_SuccessPayloadIsAnImageRef(t *testing.T) {
	cases := map[string]struct {
		options   map[string]any
		wantWidth int
	}{
		"width configured": {options: map[string]any{"modelId": "image-model-v1", "width": float64(1024)}, wantWidth: 1024},
		"no width option":  {options: map[string]any{"modelId": "image-model-v1"}, wantWidth: 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var body struct {
				Payload json.RawMessage `json:"payload"`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode dispatch body: %v", err)
				}
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"externalTaskId":"provider_task_789"}`))
			}))
			t.Cleanup(server.Close)

			if _, err := New(server.URL, server.Client()).Dispatch(
				context.Background(), "a small ember creature", nil, tc.options, testCallback(), "nr_1"); err != nil {
				t.Fatalf("Dispatch() error = %v, want nil", err)
			}

			var payload struct {
				Status string          `json:"status"`
				Image  json.RawMessage `json:"image"`
			}
			if err := json.Unmarshal(body.Payload, &payload); err != nil {
				t.Fatalf("dispatch payload is not a callback payload object: %v", err)
			}
			if payload.Status != "SUCCEEDED" {
				t.Fatalf("payload status = %q, want %q", payload.Status, "SUCCEEDED")
			}
			ref, err := domain.ParseImageRef(payload.Image)
			if err != nil {
				t.Fatalf("payload image is not a valid ImageRef: %v (image = %s)", err, payload.Image)
			}
			if ref.Source != domain.ImageSourceExternal {
				t.Errorf("ImageRef source = %q, want %q", ref.Source, domain.ImageSourceExternal)
			}
			if !strings.HasPrefix(ref.URI, server.URL+"/v1/images/") || !strings.HasSuffix(ref.URI, ".png") {
				t.Errorf("ImageRef uri = %q, want the Mock Provider's image route under %q", ref.URI, server.URL)
			}
			if ref.MediaType != "image/png" {
				t.Errorf("ImageRef mediaType = %q, want %q", ref.MediaType, "image/png")
			}
			if ref.Width != tc.wantWidth {
				t.Errorf("ImageRef width = %d, want %d", ref.Width, tc.wantWidth)
			}
			if ref.Height != 0 {
				t.Errorf("ImageRef height = %d, want it absent (the Provider reports no height)", ref.Height)
			}
			if ref.Asset != nil || ref.Artifact != nil {
				t.Errorf("ImageRef = %+v, want the EXTERNAL branch alone", ref)
			}
		})
	}
}

// TestMockTaskAdapter_SuccessPayload_IsDeterministic: the same prompt always produces the
// same image reference, so a replayed dispatch cannot make a Run's output drift.
func TestMockTaskAdapter_SuccessPayload_IsDeterministic(t *testing.T) {
	options := map[string]any{"width": float64(512)}
	first := successPayload("http://provider.test", "a small ember creature", options)
	second := successPayload("http://provider.test", "a small ember creature", options)
	if string(first) != string(second) {
		t.Fatalf("successPayload is not deterministic:\n%s\n%s", first, second)
	}
	other := successPayload("http://provider.test", "a different brief", options)
	if string(other) == string(first) {
		t.Fatalf("successPayload ignores the prompt: both briefs produced %s", first)
	}
}

// TestMockTaskAdapter_Dispatch_ErrorsNeverContainToken: the one-time plaintext callback
// token must not reach an error message, a log line or anything else outside the request
// body itself (07 §1.3, 10 §2).
func TestMockTaskAdapter_Dispatch_ErrorsNeverContainToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A Provider that echoes the request back inside its error body: even then the
		// token must not travel into the Adapter's error.
		body, _ := json.Marshal(map[string]any{"error": "boom", "received": map[string]string{"callbackToken": testToken}})
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)

	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })

	_, err := New(server.URL, server.Client()).Dispatch(
		context.Background(), "a small ember creature", nil, nil, testCallback(), "nr_1")
	if err == nil {
		t.Fatal("Dispatch() error = nil, want error for HTTP 500")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("Dispatch() error contains the callback token: %v", err)
	}
	if strings.Contains(err.Error(), testCallback().URL) {
		t.Fatalf("Dispatch() error contains the callback URL: %v", err)
	}
	if strings.Contains(logged.String(), testToken) {
		t.Fatalf("log output contains the callback token: %s", logged.String())
	}
	if !strings.Contains(err.Error(), ProviderID) {
		t.Fatalf("Dispatch() error = %v, want it to name the provider id %q", err, ProviderID)
	}
}

// TestMockTaskAdapter_Dispatch_TimeoutIsUncertain_4xxIsDefinite pins the classification the
// Execution Service needs for FailNode.Uncertain: a request that was sent but never
// answered may still have created the external task, while a rejected request did not.
func TestMockTaskAdapter_Dispatch_TimeoutIsUncertain_4xxIsDefinite(t *testing.T) {
	t.Run("timeout is uncertain", func(t *testing.T) {
		released := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-released:
			case <-r.Context().Done():
			}
		}))
		// Cleanups run last-registered-first: the handler is released before Close waits
		// for it, so a Provider that never answers cannot wedge the test binary.
		t.Cleanup(server.Close)
		t.Cleanup(func() { close(released) })

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := New(server.URL, server.Client()).Dispatch(ctx, "prompt", nil, nil, testCallback(), "nr_1")
		if err == nil {
			t.Fatal("Dispatch() error = nil, want a timeout error")
		}
		if !isUncertain(err) {
			t.Fatalf("Dispatch() error = %v, want an uncertain failure (the task may have been accepted)", err)
		}
	})

	t.Run("4xx is definite", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"callbackUrl is required"}`))
		}))
		t.Cleanup(server.Close)

		_, err := New(server.URL, server.Client()).Dispatch(context.Background(), "prompt", nil, nil, testCallback(), "nr_1")
		if err == nil {
			t.Fatal("Dispatch() error = nil, want an error for HTTP 400")
		}
		if isUncertain(err) {
			t.Fatalf("Dispatch() error = %v, want a definite failure (the Provider rejected the request)", err)
		}
	})

	t.Run("5xx is uncertain", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(server.Close)

		_, err := New(server.URL, server.Client()).Dispatch(context.Background(), "prompt", nil, nil, testCallback(), "nr_1")
		if err == nil {
			t.Fatal("Dispatch() error = nil, want an error for HTTP 500")
		}
		if !isUncertain(err) {
			t.Fatalf("Dispatch() error = %v, want an uncertain failure", err)
		}
	})
}

// TestMockTaskAdapter_Dispatch_EmptyExternalTaskIDIsUncertain: a 2xx whose body carries no
// task identity means the Provider accepted work Emberling can no longer address.
func TestMockTaskAdapter_Dispatch_EmptyExternalTaskIDIsUncertain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	_, err := New(server.URL, server.Client()).Dispatch(context.Background(), "prompt", nil, nil, testCallback(), "nr_1")
	if err == nil {
		t.Fatal("Dispatch() error = nil, want an error for a response without externalTaskId")
	}
	if !isUncertain(err) {
		t.Fatalf("Dispatch() error = %v, want an uncertain failure (the task was accepted)", err)
	}
}

func TestMockTaskAdapter_Dispatch_RejectsMissingCallback(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"externalTaskId":"provider_task_789"}`))
	}))
	t.Cleanup(server.Close)

	_, err := New(server.URL, server.Client()).Dispatch(context.Background(), "prompt", nil, nil, registry.CallbackContext{}, "nr_1")
	if err == nil {
		t.Fatal("Dispatch() error = nil, want an error for an empty callback context")
	}
	if isUncertain(err) {
		t.Fatalf("Dispatch() error = %v, want a definite failure (nothing was sent)", err)
	}
	if calls != 0 {
		t.Fatalf("provider calls = %d, want 0", calls)
	}
}

// TestMockTaskAdapter_Dispatch_ForwardsReferenceImageRef: an optional Reference Image
// reaches the Provider as the same credential-free domain.ImageRef the `reference` port
// carries (08 §2.2). The Adapter never resolves it: no Asset content, storage key or
// signed URL is fetched or invented on the way out.
func TestMockTaskAdapter_Dispatch_ForwardsReferenceImageRef(t *testing.T) {
	var raw []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"externalTaskId":"provider_task_789"}`))
	}))
	t.Cleanup(server.Close)

	reference := domain.ImageRef{
		Source: domain.ImageSourceAsset,
		Asset:  &domain.AssetRef{AssetID: "asset_123", MediaType: "image/png", SizeBytes: 2048, SHA256: "d0"},
	}
	if _, err := New(server.URL, server.Client()).Dispatch(
		context.Background(), "a small ember creature", &reference, nil, testCallback(), "nr_1"); err != nil {
		t.Fatalf("Dispatch() error = %v, want nil", err)
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode dispatch body: %v", err)
	}
	got, present := body["reference"]
	if !present {
		t.Fatalf("dispatch body carries no reference: %s", raw)
	}
	parsed, err := domain.ParseImageRef(got)
	if err != nil {
		t.Fatalf("dispatched reference is not an ImageRef: %v (%s)", err, got)
	}
	if parsed.Source != domain.ImageSourceAsset || parsed.Asset == nil || parsed.Asset.AssetID != "asset_123" {
		t.Errorf("dispatched reference = %+v, want the ASSET ImageRef the port carried", parsed)
	}
	if strings.Contains(string(raw), "storageKey") {
		t.Errorf("dispatch body carries a storage key: %s", raw)
	}
}

// TestMockTaskAdapter_Dispatch_NoReferenceOmitsTheMember: the optional port is absent far
// more often than not, and an absent Reference Image must not become a null member the
// Provider has to interpret.
func TestMockTaskAdapter_Dispatch_NoReferenceOmitsTheMember(t *testing.T) {
	var body map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode dispatch body: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"externalTaskId":"provider_task_789"}`))
	}))
	t.Cleanup(server.Close)

	if _, err := New(server.URL, server.Client()).Dispatch(
		context.Background(), "a small ember creature", nil, nil, testCallback(), "nr_1"); err != nil {
		t.Fatalf("Dispatch() error = %v, want nil", err)
	}
	if _, present := body["reference"]; present {
		t.Errorf("dispatch body carries a reference member with no Reference Image: %v", body["reference"])
	}
}

// isUncertain reads the uncertainty fact the Adapter reports, the way the Execution Service
// does: through errors.As against the narrow interface, without importing this package.
func isUncertain(err error) bool {
	var reporter interface{ Uncertain() bool }
	if !errors.As(err, &reporter) {
		return false
	}
	return reporter.Uncertain()
}
