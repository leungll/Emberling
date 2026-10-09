package generatevideo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/mockprovider"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const testCallbackToken = "plaintext-callback-token-never-logged"

// callbackSink receives the Mock Provider's callbacks and hands each payload to the test.
type callbackSink struct {
	server   *httptest.Server
	payloads chan json.RawMessage
}

func newCallbackSink(t *testing.T) *callbackSink {
	t.Helper()
	sink := &callbackSink{payloads: make(chan json.RawMessage, 4)}
	sink.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Payload json.RawMessage `json:"payload"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sink.payloads <- body.Payload
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(sink.server.Close)
	return sink
}

func (s *callbackSink) next(t *testing.T) json.RawMessage {
	t.Helper()
	select {
	case payload := <-s.payloads:
		return payload
	case <-time.After(5 * time.Second):
		t.Fatal("no callback delivered within 5s")
		return nil
	}
}

func newProvider(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(mockprovider.NewServer(mockprovider.NewDispatcher(nil)))
	t.Cleanup(server.Close)
	return server
}

func action(callbackURL, settings string) registry.ToolAction {
	return registry.ToolAction{
		AgentRunID: "ar_1", TurnID: "turn_1", ActionID: "act_1",
		ToolName: ToolName, AttemptNo: 1,
		Arguments: json.RawMessage(`{"assetRef":"img_0011223344556677","photoAssetId":"asset_photo_1","settings":` + settings + `}`),
		Callback:  &registry.CallbackContext{URL: callbackURL, Token: testCallbackToken},
	}
}

func dispatch(t *testing.T, executor *Executor, toolAction registry.ToolAction) registry.ToolAsyncState {
	t.Helper()
	got, err := executor.Execute(context.Background(), toolAction)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Kind != registry.ToolResultDispatched || got.Result != nil || got.ExternalTask == nil {
		t.Fatalf("result = %+v, want DISPATCHED with an external task", got)
	}
	if got.ExternalTask.ProviderID != ProviderID || got.ExternalTask.ExternalTaskID == "" {
		t.Fatalf("external task = %+v, want provider %q and an id", got.ExternalTask, ProviderID)
	}
	return registry.ToolAsyncState{AgentRunID: "ar_1", ActionID: "act_1", ToolName: ToolName, AttemptNo: 1, ExternalTask: *got.ExternalTask}
}

func TestRegistration_AsyncConsumerRequiringPassedReview_RegistersCleanly(t *testing.T) {
	reg := Registration("http://mock-provider.test", nil)
	if reg.Metadata.ExecutionKind != domain.ToolExecutionAsync {
		t.Errorf("executionKind = %s, want ASYNC", reg.Metadata.ExecutionKind)
	}
	if want := (domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown}); reg.Metadata.SideEffect != want {
		t.Errorf("sideEffect = %+v, want %+v", reg.Metadata.SideEffect, want)
	}
	requires := reg.Metadata.Requires
	if len(requires) != 1 || requires[0].FactType != reviewedFactType || requires[0].RequireVerdict == nil || !*requires[0].RequireVerdict {
		t.Fatalf("requires = %+v, want one passed %q requirement", requires, reviewedFactType)
	}
	if err := registry.NewToolRegistry().Register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
}

func TestExecute_ProviderCallbackSucceeds_OnCallbackReturnsVideoURL(t *testing.T) {
	provider, sink := newProvider(t), newCallbackSink(t)
	executor := New(provider.URL, provider.Client())

	state := dispatch(t, executor, action(sink.server.URL, `{"durationSeconds":4}`))
	got, err := executor.OnCallback(context.Background(), state, sink.next(t))
	if err != nil {
		t.Fatalf("on callback: %v", err)
	}

	schema, err := registry.CompileSchema(json.RawMessage(outputSchema))
	if err != nil {
		t.Fatalf("compile output schema: %v", err)
	}
	var value map[string]any
	if err := json.Unmarshal(got.Output, &value); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if err := registry.ValidateValue(schema, value); err != nil {
		t.Fatalf("output %s does not conform to the output schema: %v", got.Output, err)
	}
	if url, _ := value["videoUrl"].(string); !strings.HasPrefix(url, provider.URL+"/v1/videos/") {
		t.Errorf("videoUrl = %q, want a video URL on the Provider", url)
	}
}

func TestExecute_FailedControl_OnCallbackReturnsProviderFailure(t *testing.T) {
	provider, sink := newProvider(t), newCallbackSink(t)
	executor := New(provider.URL, provider.Client())

	state := dispatch(t, executor, action(sink.server.URL, `{"mock":"failed"}`))
	_, err := executor.OnCallback(context.Background(), state, sink.next(t))

	var failure *registry.ProviderFailure
	if !errors.As(err, &failure) || failure.Err.Code != defaultFailureCode {
		t.Fatalf("on callback error = %v, want a ProviderFailure with code %s", err, defaultFailureCode)
	}
}

func TestBuildRequest_MockControl_SelectsProviderScenario(t *testing.T) {
	callback := registry.CallbackContext{URL: "http://emberling.test/cb", Token: testCallbackToken}
	tests := []struct {
		control     any
		wantOutcome string
		wantDelay   any
	}{
		{control: nil},
		{control: "failed", wantOutcome: "failed"},
		{control: "lost", wantDelay: "lost"},
		{control: "duplicate", wantDelay: "duplicate"},
		{control: "delay:1500", wantDelay: 1500},
		{control: "delay:-1"},
		{control: 7},
	}
	for _, tt := range tests {
		settings := map[string]any{"durationSeconds": 4}
		if tt.control != nil {
			settings[mockControlKey] = tt.control
		}
		got := buildRequest(settings, callback)
		if got.Media != mediaVideo || got.Outcome != tt.wantOutcome || got.DelayMs != tt.wantDelay {
			t.Errorf("buildRequest(mock=%v) = %+v, want media video, outcome %q, delay %v", tt.control, got, tt.wantOutcome, tt.wantDelay)
		}
		if got.CallbackURL != callback.URL || got.CallbackToken != callback.Token {
			t.Errorf("buildRequest(mock=%v) dropped the callback", tt.control)
		}
	}
}

func TestExecute_ProviderRejectsOrArgumentsInvalid_FailsWithoutLeakingCallback(t *testing.T) {
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(body)
	}))
	t.Cleanup(rejecting.Close)
	executor := New(rejecting.URL, rejecting.Client())

	_, err := executor.Execute(context.Background(), action("http://emberling.test/cb", `{}`))
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("execute error = %v, want the Provider's rejection status", err)
	}
	if strings.Contains(err.Error(), testCallbackToken) || strings.Contains(err.Error(), "emberling.test") {
		t.Fatalf("error %q leaks the callback token or URL", err)
	}

	noCallback := action("http://emberling.test/cb", `{}`)
	noCallback.Callback = nil
	if _, err := executor.Execute(context.Background(), noCallback); err == nil {
		t.Error("execute without callback error = nil, want an explicit error")
	}
	missingSettings := action("http://emberling.test/cb", `{}`)
	missingSettings.Arguments = json.RawMessage(`{"assetRef":"img_1","photoAssetId":"p"}`)
	if _, err := executor.Execute(context.Background(), missingSettings); err == nil {
		t.Error("execute without settings error = nil, want an explicit error")
	}
}

func TestOnCallback_UninterpretablePayload_IsNotAProviderFailure(t *testing.T) {
	state := registry.ToolAsyncState{ExternalTask: registry.ExternalTask{ProviderID: ProviderID, ExternalTaskID: "task_1"}}
	for _, payload := range []string{`[]`, `{"status":"SUCCEEDED"}`, `{"status":"RUNNING"}`} {
		_, err := New("http://unused.test", nil).OnCallback(context.Background(), state, []byte(payload))
		var failure *registry.ProviderFailure
		if err == nil || errors.As(err, &failure) {
			t.Errorf("OnCallback(%s) error = %v, want a plain error", payload, err)
		}
	}
}
