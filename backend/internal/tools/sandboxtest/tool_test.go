package sandboxtest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const (
	testCallbackToken = "plaintext-callback-token-never-logged"
	testCallbackURL   = "http://backend.invalid/api/callbacks"
	testPatch         = "--- a/greet.go\n+++ b/greet.go\n@@ -1 +1 @@\n-secret patch line\n+secret patch line 2\n"
)

func action(arguments string) registry.ToolAction {
	return registry.ToolAction{
		AgentRunID: "ar_1", TurnID: "turn_1", ActionID: "act_1",
		ToolName: ToolName, AttemptNo: 1,
		Arguments: json.RawMessage(arguments),
		Callback:  &registry.CallbackContext{URL: testCallbackURL, Token: testCallbackToken},
	}
}

func validArguments() string {
	encoded, _ := json.Marshal(map[string]string{"baseCommit": "fixture-v1", "patch": testPatch, "mock": "fail"})
	return string(encoded)
}

// runnerStub answers POST /v1/tests with the given status and body and hands each request
// body to the test.
func runnerStub(t *testing.T, status int, body string) (*httptest.Server, chan map[string]any) {
	t.Helper()
	requests := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != testsPath {
			http.NotFound(w, r)
			return
		}
		var decoded map[string]any
		if err := json.NewDecoder(r.Body).Decode(&decoded); err != nil {
			t.Errorf("decode dispatch body: %v", err)
		}
		requests <- decoded
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, requests
}

func assertNoSecrets(t *testing.T, err error, server *httptest.Server) {
	t.Helper()
	message := err.Error()
	for _, secret := range []string{testCallbackToken, testCallbackURL, server.URL, "secret patch line", testsPath} {
		if strings.Contains(message, secret) {
			t.Fatalf("error %q leaks %q", message, secret)
		}
	}
}

func TestExecute_Accepted_SendsRequestAndReturnsDispatched(t *testing.T) {
	server, requests := runnerStub(t, http.StatusAccepted, `{"testId":"test_42"}`)

	got, err := New(server.URL+"/", nil).Execute(context.Background(), action(validArguments()))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Kind != registry.ToolResultDispatched || got.Result != nil {
		t.Fatalf("result = %+v, want DISPATCHED without a result", got)
	}
	if got.ExternalTask == nil || *got.ExternalTask != (registry.ExternalTask{ProviderID: ProviderID, ExternalTaskID: "test_42"}) {
		t.Fatalf("external task = %+v, want sandboxrunner/test_42", got.ExternalTask)
	}

	body := <-requests
	want := map[string]any{
		"callbackUrl":   testCallbackURL,
		"callbackToken": testCallbackToken,
		"baseCommit":    "fixture-v1",
		"patch":         testPatch,
		"mock":          "fail",
	}
	if len(body) != len(want) {
		t.Fatalf("dispatch body = %v, want exactly %v", body, want)
	}
	for key, value := range want {
		if body[key] != value {
			t.Fatalf("dispatch body[%q] = %v, want %v", key, body[key], value)
		}
	}
}

func TestExecute_NoMock_OmitsMockMember(t *testing.T) {
	server, requests := runnerStub(t, http.StatusAccepted, `{"testId":"test_1"}`)

	if _, err := New(server.URL, nil).Execute(context.Background(), action(`{"baseCommit":"fixture-v1","patch":"p"}`)); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, ok := (<-requests)["mock"]; ok {
		t.Fatal("dispatch body carries mock although the arguments name none")
	}
}

func TestExecute_NonAcceptedStatus_ReturnsErrorWithoutSecrets(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusInternalServerError} {
		// The runner echoes the request into its error body; none of it may surface.
		server, _ := runnerStub(t, status, `{"error":"`+testCallbackToken+` secret patch line"}`)

		_, err := New(server.URL, nil).Execute(context.Background(), action(validArguments()))
		if err == nil {
			t.Fatalf("status %d: execute succeeded, want an error", status)
		}
		var failure *registry.ProviderFailure
		if errors.As(err, &failure) {
			t.Fatalf("status %d: error is a ProviderFailure, want a plain dispatch error", status)
		}
		assertNoSecrets(t, err, server)
	}
}

func TestExecute_TransportFailure_ReturnsErrorWithoutSecrets(t *testing.T) {
	server, _ := runnerStub(t, http.StatusAccepted, `{"testId":"x"}`)
	server.Close()

	_, err := New(server.URL, nil).Execute(context.Background(), action(validArguments()))
	if err == nil {
		t.Fatal("execute succeeded against a closed runner, want an error")
	}
	assertNoSecrets(t, err, server)
}

func TestExecute_AcceptedWithoutTestID_ReturnsError(t *testing.T) {
	server, _ := runnerStub(t, http.StatusAccepted, `{}`)

	if _, err := New(server.URL, nil).Execute(context.Background(), action(validArguments())); err == nil {
		t.Fatal("execute succeeded without a testId, want an error")
	}
}

func TestExecute_MissingCallbackOrArguments_SendsNothing(t *testing.T) {
	server, requests := runnerStub(t, http.StatusAccepted, `{"testId":"x"}`)
	executor := New(server.URL, nil)

	noCallback := action(validArguments())
	noCallback.Callback = nil
	cases := map[string]registry.ToolAction{
		"no callback":    noCallback,
		"no patch":       action(`{"baseCommit":"fixture-v1"}`),
		"no base commit": action(`{"patch":"p"}`),
		"not an object":  action(`[]`),
	}
	for name, toolAction := range cases {
		if _, err := executor.Execute(context.Background(), toolAction); err == nil {
			t.Fatalf("%s: execute succeeded, want an error", name)
		}
	}
	select {
	case body := <-requests:
		t.Fatalf("runner received %v, want no request", body)
	default:
	}
}

func asyncState() registry.ToolAsyncState {
	return registry.ToolAsyncState{
		AgentRunID: "ar_1", ActionID: "act_1", ToolName: ToolName, AttemptNo: 1,
		ExternalTask: registry.ExternalTask{ProviderID: ProviderID, ExternalTaskID: "test_42"},
	}
}

func TestOnCallback_Succeeded_RepublishesExactlyTheResultFields(t *testing.T) {
	payload := `{"status":"SUCCEEDED","passed":false,"baseCommit":"fixture-v1",
		"patchDigest":"sha256:aa","resultDigest":"sha256:bb",
		"summary":{"total":12,"failed":3,"skipped":1},"log":"provider-private"}`

	got, err := New("http://unused", nil).OnCallback(context.Background(), asyncState(), []byte(payload))
	if err != nil {
		t.Fatalf("on callback: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(got.Output, &decoded); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	want := map[string]any{
		"passed":       false,
		"baseCommit":   "fixture-v1",
		"patchDigest":  "sha256:aa",
		"resultDigest": "sha256:bb",
		"summary":      map[string]any{"total": float64(12), "failed": float64(3)},
	}
	if len(decoded) != len(want) {
		t.Fatalf("output = %s, want exactly the members %v", got.Output, want)
	}
	for key, value := range want {
		if key == "summary" {
			summary, _ := decoded[key].(map[string]any)
			if len(summary) != 2 || summary["total"] != float64(12) || summary["failed"] != float64(3) {
				t.Fatalf("output summary = %v, want total 12 failed 3 only", decoded[key])
			}
			continue
		}
		if decoded[key] != value {
			t.Fatalf("output[%q] = %v, want %v", key, decoded[key], value)
		}
	}

	schema, err := registry.CompileSchema(json.RawMessage(outputSchema))
	if err != nil {
		t.Fatalf("compile output schema: %v", err)
	}
	if err := registry.ValidateValue(schema, got.Output); err != nil {
		t.Fatalf("output %s does not match the output schema: %v", got.Output, err)
	}
}

func TestOnCallback_SucceededMissingMember_ReturnsPlainError(t *testing.T) {
	for name, payload := range map[string]string{
		"no passed":        `{"status":"SUCCEEDED","baseCommit":"b","patchDigest":"p","resultDigest":"r","summary":{"total":1,"failed":0}}`,
		"no summary":       `{"status":"SUCCEEDED","passed":true,"baseCommit":"b","patchDigest":"p","resultDigest":"r"}`,
		"no result digest": `{"status":"SUCCEEDED","passed":true,"baseCommit":"b","patchDigest":"p","summary":{"total":1,"failed":0}}`,
		"negative count":   `{"status":"SUCCEEDED","passed":true,"baseCommit":"b","patchDigest":"p","resultDigest":"r","summary":{"total":1,"failed":-1}}`,
		"unknown status":   `{"status":"RUNNING"}`,
		"not json":         `nope`,
	} {
		_, err := New("http://unused", nil).OnCallback(context.Background(), asyncState(), []byte(payload))
		var failure *registry.ProviderFailure
		if err == nil || errors.As(err, &failure) {
			t.Fatalf("%s: err = %v, want a plain error", name, err)
		}
	}
}

func TestOnCallback_Failed_ReturnsProviderFailure(t *testing.T) {
	cases := map[string]struct {
		payload string
		want    domain.ExecutionError
	}{
		"code and message": {
			payload: `{"status":"FAILED","error":{"code":"TIMEOUT","message":"test run exceeded its deadline"}}`,
			want:    domain.ExecutionError{Code: "TIMEOUT", Message: "test run exceeded its deadline"},
		},
		"no error member": {
			payload: `{"status":"FAILED"}`,
			want:    domain.ExecutionError{Code: defaultFailureCode, Message: "sandbox runner reported the test run failed"},
		},
	}
	for name, tc := range cases {
		_, err := New("http://unused", nil).OnCallback(context.Background(), asyncState(), []byte(tc.payload))
		var failure *registry.ProviderFailure
		if !errors.As(err, &failure) {
			t.Fatalf("%s: err = %v, want a ProviderFailure", name, err)
		}
		if failure.Err.Code != tc.want.Code || failure.Err.Message != tc.want.Message {
			t.Fatalf("%s: failure = %+v, want %+v", name, failure.Err, tc.want)
		}
	}
}

func TestOnCallback_FailedOversizedMessage_IsBounded(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"status": "FAILED", "error": map[string]string{"message": strings.Repeat("é", 600)}})

	_, err := New("http://unused", nil).OnCallback(context.Background(), asyncState(), payload)
	var failure *registry.ProviderFailure
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v, want a ProviderFailure", err)
	}
	if len(failure.Err.Message) > maxFailureMessageBytes {
		t.Fatalf("message length %d exceeds %d", len(failure.Err.Message), maxFailureMessageBytes)
	}
}

func TestRegistration_DeclaresAsyncExternalPatchTestedFact(t *testing.T) {
	reg := Registration("http://runner.invalid", nil)
	meta := reg.Metadata

	if meta.Name != ToolName || meta.ExecutionKind != domain.ToolExecutionAsync {
		t.Fatalf("name/kind = %q/%q, want %q/ASYNC", meta.Name, meta.ExecutionKind, ToolName)
	}
	if meta.SideEffect != (domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown}) {
		t.Fatalf("side effect = %+v, want EXTERNAL/UNKNOWN", meta.SideEffect)
	}
	if meta.CountsTowardGenerationLimit {
		t.Fatal("sandbox_test counts toward the generation limit, want it not to")
	}
	if len(meta.Requires) != 0 {
		t.Fatalf("requires = %+v, want none", meta.Requires)
	}
	result := domain.FactPointer{Source: domain.FactPointerResult}
	produces := meta.Produces
	if produces == nil || produces.FactType != FactType {
		t.Fatalf("produces = %+v, want fact type %q", produces, FactType)
	}
	if produces.SubjectPointer != (domain.FactPointer{Source: result.Source, Pointer: "/patchDigest"}) {
		t.Fatalf("subject = %+v, want RESULT /patchDigest", produces.SubjectPointer)
	}
	wantBindings := map[string]string{"baseCommit": "/baseCommit", "resultDigest": "/resultDigest"}
	if len(produces.BindArguments) != len(wantBindings) {
		t.Fatalf("bindings = %+v, want %v", produces.BindArguments, wantBindings)
	}
	for name, pointer := range wantBindings {
		if produces.BindArguments[name] != (domain.FactPointer{Source: result.Source, Pointer: pointer}) {
			t.Fatalf("binding %q = %+v, want RESULT %s", name, produces.BindArguments[name], pointer)
		}
	}
	if produces.VerdictPointer == nil || *produces.VerdictPointer != (domain.FactPointer{Source: result.Source, Pointer: "/passed"}) {
		t.Fatalf("verdict = %+v, want RESULT /passed", produces.VerdictPointer)
	}
	if _, ok := reg.Executor.(registry.AsyncToolExecutor); !ok {
		t.Fatal("executor does not implement AsyncToolExecutor")
	}

	tools := registry.NewToolRegistry()
	if err := tools.Register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := registry.ValidateFactDeclarations(tools.ListMetadata(), nil); err != nil {
		t.Fatalf("validate fact declarations: %v", err)
	}
}
