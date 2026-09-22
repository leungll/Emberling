package remotelookup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const (
	testCallbackURL   = "http://emberling.test/api/callbacks"
	testCallbackToken = "plaintext-callback-token-never-logged"
)

// recordingProvider stands in for the Mock Provider's POST /v1/tasks: it captures the one
// request it receives and answers with a fixed status and body.
type recordingProvider struct {
	status int
	answer string

	path string
	body []byte
}

func (p *recordingProvider) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.path = r.URL.Path
		p.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(p.status)
		_, _ = io.WriteString(w, p.answer)
	}))
	t.Cleanup(server.Close)
	return server
}

func toolAction(key string) registry.ToolAction {
	return registry.ToolAction{
		AgentRunID: "ar_1", TurnID: "turn_1", ActionID: "act_1",
		ToolName: ToolName, AttemptNo: 1,
		Arguments: json.RawMessage(`{"key":"` + key + `"}`),
		Callback:  &registry.CallbackContext{URL: testCallbackURL, Token: testCallbackToken},
	}
}

func decodeRequest(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("request body is not JSON: %v (%s)", err, body)
	}
	return request
}

func TestRegistration_AsyncExternalUnknown_RegistersCleanly(t *testing.T) {
	reg := Registration("http://mock-provider.test", nil)
	if reg.Metadata.ExecutionKind != domain.ToolExecutionAsync {
		t.Errorf("executionKind = %s, want ASYNC", reg.Metadata.ExecutionKind)
	}
	want := domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown}
	if reg.Metadata.SideEffect != want {
		t.Errorf("sideEffect = %+v, want %+v", reg.Metadata.SideEffect, want)
	}
	if err := registry.NewToolRegistry().Register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
}

func TestExecute_Dispatch_SendsCallbackInBodyAndReturnsExternalTask(t *testing.T) {
	provider := &recordingProvider{status: http.StatusAccepted, answer: `{"externalTaskId":"provider_task_1"}`}
	server := provider.serve(t)

	result, err := New(server.URL, server.Client()).Execute(context.Background(), toolAction("k1"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Kind != registry.ToolResultDispatched || result.Result != nil {
		t.Fatalf("result = %+v, want DISPATCHED with no completed result", result)
	}
	if result.ExternalTask == nil || *result.ExternalTask != (registry.ExternalTask{ProviderID: ProviderID, ExternalTaskID: "provider_task_1"}) {
		t.Fatalf("external task = %+v, want provider %q task provider_task_1", result.ExternalTask, ProviderID)
	}

	if provider.path != "/v1/tasks" {
		t.Errorf("path = %q, want /v1/tasks", provider.path)
	}
	request := decodeRequest(t, provider.body)
	if request["callbackUrl"] != testCallbackURL || request["callbackToken"] != testCallbackToken {
		t.Errorf("callback in body = (%v, %v), want the Runtime's URL and token unchanged", request["callbackUrl"], request["callbackToken"])
	}
	if _, present := request["idempotencyKey"]; present {
		t.Errorf("request carries an idempotencyKey the Runtime never issued: %v", request["idempotencyKey"])
	}
	payload, _ := request["payload"].(map[string]any)
	if payload["status"] != "SUCCEEDED" || payload["key"] != "k1" || payload["record"] != "record for k1" {
		t.Errorf("success payload = %v, want status SUCCEEDED with key k1 and its record", payload)
	}
}

func TestExecute_Directives_SelectProviderScenario(t *testing.T) {
	cases := []struct {
		key       string
		field     string
		want      any
		noPayload bool
	}{
		{key: "mock:failed", field: "outcome", want: "failed", noPayload: true},
		{key: "mock:lost", field: "delayMs", want: "lost"},
		{key: "mock:duplicate", field: "delayMs", want: "duplicate"},
		{key: "mock:delay:250", field: "delayMs", want: float64(250)},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			provider := &recordingProvider{status: http.StatusAccepted, answer: `{"externalTaskId":"provider_task_2"}`}
			server := provider.serve(t)
			if _, err := New(server.URL, server.Client()).Execute(context.Background(), toolAction(tc.key)); err != nil {
				t.Fatalf("execute: %v", err)
			}
			request := decodeRequest(t, provider.body)
			if request[tc.field] != tc.want {
				t.Errorf("%s = %v, want %v", tc.field, request[tc.field], tc.want)
			}
			if _, present := request["payload"]; present == tc.noPayload {
				t.Errorf("payload present = %v, want %v", present, !tc.noPayload)
			}
		})
	}
}

func TestExecute_Failures_ErrorsNeverCarryTokenURLOrBody(t *testing.T) {
	echo := `{"error":"provider-said-no","echo":"` + testCallbackToken + ` ` + testCallbackURL + `"}`
	cases := []struct {
		name     string
		provider *recordingProvider
	}{
		{name: "rejected", provider: &recordingProvider{status: http.StatusInternalServerError, answer: echo}},
		{name: "undecodable", provider: &recordingProvider{status: http.StatusAccepted, answer: `not json ` + testCallbackToken}},
		{name: "no external task id", provider: &recordingProvider{status: http.StatusAccepted, answer: `{"externalTaskId":""}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := tc.provider.serve(t)
			_, err := New(server.URL, server.Client()).Execute(context.Background(), toolAction("k1"))
			if err == nil {
				t.Fatalf("execute succeeded, want an explicit error")
			}
			for _, secret := range []string{testCallbackToken, testCallbackURL, "record for k1", "provider-said-no"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error %q leaks %q", err.Error(), secret)
				}
			}
		})
	}
}

func TestExecute_NoCallbackContext_FailsWithoutCallingProvider(t *testing.T) {
	provider := &recordingProvider{status: http.StatusAccepted, answer: `{"externalTaskId":"provider_task_3"}`}
	server := provider.serve(t)
	action := toolAction("k1")
	action.Callback = nil
	if _, err := New(server.URL, server.Client()).Execute(context.Background(), action); err == nil {
		t.Fatalf("execute without a callback context succeeded")
	}
	if provider.body != nil {
		t.Errorf("provider was called without a callback credential: %s", provider.body)
	}
}

func asyncState() registry.ToolAsyncState {
	return registry.ToolAsyncState{
		AgentRunID: "ar_1", ActionID: "act_1", ToolName: ToolName, AttemptNo: 1,
		ExternalTask: registry.ExternalTask{ProviderID: ProviderID, ExternalTaskID: "provider_task_1"},
	}
}

func TestOnCallback_Succeeded_NormalisesToOutputSchemaShape(t *testing.T) {
	payload := `{"status":"SUCCEEDED","key":"k1","record":"record for k1","providerPrivate":"x"}`
	result, err := New("http://unused.test", nil).OnCallback(context.Background(), asyncState(), []byte(payload))
	if err != nil {
		t.Fatalf("on callback: %v", err)
	}
	if string(result.Output) != `{"key":"k1","record":"record for k1"}` {
		t.Errorf("output = %s, want only key and record", result.Output)
	}
}

func TestOnCallback_Failed_ReturnsProviderFailure(t *testing.T) {
	payload := `{"status":"FAILED","error":{"code":"PROVIDER_TASK_FAILED","message":"mock provider: task failed by scenario"}}`
	_, err := New("http://unused.test", nil).OnCallback(context.Background(), asyncState(), []byte(payload))
	var failure *registry.ProviderFailure
	if !errors.As(err, &failure) {
		t.Fatalf("on callback error = %v, want a *registry.ProviderFailure", err)
	}
	if failure.Err.Code != "PROVIDER_TASK_FAILED" {
		t.Errorf("failure code = %q, want PROVIDER_TASK_FAILED", failure.Err.Code)
	}
}

func TestOnCallback_FailedWithOversizedMessage_TruncatesToBound(t *testing.T) {
	long := strings.Repeat("x", 4*maxFailureMessageBytes)
	payload := `{"status":"FAILED","error":{"code":"PROVIDER_TASK_FAILED","message":"` + long + `"}}`
	_, err := New("http://unused.test", nil).OnCallback(context.Background(), asyncState(), []byte(payload))
	var failure *registry.ProviderFailure
	if !errors.As(err, &failure) {
		t.Fatalf("on callback error = %v, want a *registry.ProviderFailure", err)
	}
	if got := len(failure.Err.Message); got != maxFailureMessageBytes {
		t.Errorf("failure message length = %d, want truncated to %d bytes", got, maxFailureMessageBytes)
	}
}

func TestOnCallback_Uninterpretable_ReturnsPlainError(t *testing.T) {
	for _, payload := range []string{`not json`, `{"status":"RUNNING"}`, `{"status":"SUCCEEDED","key":"k1"}`} {
		_, err := New("http://unused.test", nil).OnCallback(context.Background(), asyncState(), []byte(payload))
		var failure *registry.ProviderFailure
		if err == nil || errors.As(err, &failure) {
			t.Errorf("payload %s: error = %v, want a plain error that is not a ProviderFailure", payload, err)
		}
	}
}
