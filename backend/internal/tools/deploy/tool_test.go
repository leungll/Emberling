package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/mockproduction"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const validArguments = `{"target":{"service":"checkout","environment":"prod"},"baseCommit":"abc123","patchDigest":"sha256:feed","parameters":{"replicas":3,"flags":{"canary":true}}}`

func action(arguments string) registry.ToolAction {
	return registry.ToolAction{
		AgentRunID: "ar_1", TurnID: "turn_1", ActionID: "act_7",
		ToolName: ToolName, AttemptNo: 2, Arguments: json.RawMessage(arguments),
	}
}

func validate(t *testing.T, schemaText string, document string) error {
	t.Helper()
	schema, err := registry.CompileSchema(json.RawMessage(schemaText))
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	var value any
	if err := json.Unmarshal([]byte(document), &value); err != nil {
		t.Fatalf("document is not JSON: %v", err)
	}
	return registry.ValidateValue(schema, value)
}

func TestRegistration_SyncExternalUnknownWithoutFacts_RegistersCleanly(t *testing.T) {
	reg := Registration("http://production.test", nil)
	m := reg.Metadata
	if m.Name != ToolName || m.ExecutionKind != domain.ToolExecutionSync {
		t.Errorf("name/kind = %s/%s, want deploy/SYNC", m.Name, m.ExecutionKind)
	}
	if want := (domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown}); m.SideEffect != want {
		t.Errorf("sideEffect = %+v, want %+v", m.SideEffect, want)
	}
	if m.Produces != nil || len(m.Requires) != 0 || m.CountsTowardGenerationLimit {
		t.Errorf("produces/requires/countsTowardGenerationLimit = %+v/%+v/%v, want none", m.Produces, m.Requires, m.CountsTowardGenerationLimit)
	}
	if err := registry.NewToolRegistry().Register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
}

func TestInputSchema_RequiresTargetCommitAndDigest_ParametersOptionalOpenObject(t *testing.T) {
	for name, tc := range map[string]struct {
		document string
		valid    bool
	}{
		"full":                {validArguments, true},
		"without parameters":  {`{"target":{"service":"s","environment":"e"},"baseCommit":"c","patchDigest":"d"}`, true},
		"missing target":      {`{"baseCommit":"c","patchDigest":"d"}`, false},
		"missing environment": {`{"target":{"service":"s"},"baseCommit":"c","patchDigest":"d"}`, false},
		"missing baseCommit":  {`{"target":{"service":"s","environment":"e"},"patchDigest":"d"}`, false},
		"missing patchDigest": {`{"target":{"service":"s","environment":"e"},"baseCommit":"c"}`, false},
		"parameters array":    {`{"target":{"service":"s","environment":"e"},"baseCommit":"c","patchDigest":"d","parameters":[]}`, false},
		"unknown member":      {`{"target":{"service":"s","environment":"e"},"baseCommit":"c","patchDigest":"d","operationId":"x"}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validate(t, inputSchema, tc.document); (err == nil) != tc.valid {
				t.Errorf("validate = %v, want valid=%v", err, tc.valid)
			}
		})
	}
}

func TestOperationID_CombinesActionAndAttempt(t *testing.T) {
	if got := OperationID("act_7", 2); got != "op_act_7_2" {
		t.Errorf("OperationID = %q, want op_act_7_2", got)
	}
}

func TestExecute_MockProductionDeploys_ReturnsOperationAndVersion(t *testing.T) {
	production := httptest.NewServer(mockproduction.NewServer())
	defer production.Close()

	got, err := New(production.URL+"/", production.Client()).Execute(context.Background(), action(validArguments))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Kind != registry.ToolResultCompleted || got.Result == nil {
		t.Fatalf("result = %+v, want a completed result", got)
	}
	if want := `{"operationId":"op_act_7_2","version":"v1"}`; string(got.Result.Output) != want {
		t.Errorf("output = %s, want %s", got.Result.Output, want)
	}
	if err := validate(t, outputSchema, string(got.Result.Output)); err != nil {
		t.Errorf("output does not conform to the output schema: %v", err)
	}
}

func TestExecute_RequestBody_CarriesOperationIDAndArgumentsWithoutApproval(t *testing.T) {
	var gotMethod, gotPath, gotContentType string
	var gotBody map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotContentType = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"operationId":"op_act_7_2","version":"v9","deployedAt":"2026-01-01T00:00:00Z"}`))
	}))
	defer server.Close()

	if _, err := New(server.URL, server.Client()).Execute(context.Background(), action(validArguments)); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/deployments" || gotContentType != "application/json" {
		t.Errorf("request = %s %s (%s), want POST /v1/deployments (application/json)", gotMethod, gotPath, gotContentType)
	}
	want := map[string]string{
		"operationId": `"op_act_7_2"`,
		"target":      `{"service":"checkout","environment":"prod"}`,
		"baseCommit":  `"abc123"`,
		"patchDigest": `"sha256:feed"`,
		"parameters":  `{"replicas":3,"flags":{"canary":true}}`,
	}
	if len(gotBody) != len(want) {
		t.Errorf("body members = %v, want exactly %v", keys(gotBody), keys(want))
	}
	for key, value := range want {
		if string(gotBody[key]) != value {
			t.Errorf("body[%s] = %s, want %s", key, gotBody[key], value)
		}
	}
}

func TestExecute_WithoutParameters_OmitsThem(t *testing.T) {
	var gotBody map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"operationId":"op_act_7_2","version":"v1"}`))
	}))
	defer server.Close()

	_, err := New(server.URL, server.Client()).Execute(context.Background(),
		action(`{"target":{"service":"s","environment":"e"},"baseCommit":"c","patchDigest":"d"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, ok := gotBody["parameters"]; ok {
		t.Errorf("body carries parameters: %v", keys(gotBody))
	}
}

func TestExecute_Conflict_IsProviderFailureWithoutBodyText(t *testing.T) {
	production := httptest.NewServer(mockproduction.NewServer())
	defer production.Close()

	_, err := New(production.URL, production.Client()).Execute(context.Background(),
		action(`{"target":{"service":"s","environment":"e"},"baseCommit":"c","patchDigest":"d","parameters":{"mock":"reject"}}`))
	var failure *registry.ProviderFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want *registry.ProviderFailure", err)
	}
	if failure.Err.Code != "REJECTED" {
		t.Errorf("code = %q, want REJECTED", failure.Err.Code)
	}
	if strings.Contains(err.Error(), production.URL) || strings.Contains(err.Error(), "rejected by production") {
		t.Errorf("error %q quotes the URL or the response body", err)
	}
}

func TestExecute_ConflictWithUnboundedCode_FallsBackToRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"secret value hunter2","message":"hunter2"}`))
	}))
	defer server.Close()

	_, err := New(server.URL, server.Client()).Execute(context.Background(), action(validArguments))
	var failure *registry.ProviderFailure
	if !errors.As(err, &failure) || failure.Err.Code != "REJECTED" {
		t.Fatalf("error = %v, want ProviderFailure with code REJECTED", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error %q quotes the response body", err)
	}
}

func TestExecute_ServerErrorAndTransportFailure_PlainErrorWithoutURLOrArguments(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"INTERNAL","message":"database password hunter2"}`))
	}))
	defer failing.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	for name, baseURL := range map[string]string{"500": failing.URL, "transport": closedURL} {
		t.Run(name, func(t *testing.T) {
			_, err := New(baseURL, nil).Execute(context.Background(), action(validArguments))
			if err == nil {
				t.Fatal("execute: error = nil, want an error")
			}
			var failure *registry.ProviderFailure
			if errors.As(err, &failure) {
				t.Errorf("error = %v, want a plain error, not a ProviderFailure", err)
			}
			for _, leak := range []string{baseURL, strings.TrimPrefix(baseURL, "http://"), "hunter2", "replicas", "abc123"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("error %q contains %q", err, leak)
				}
			}
		})
	}
}

func TestExecute_ContextDeadline_IsReportedAsDeadline(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(server.URL, server.Client()).Execute(ctx, action(validArguments))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want it to wrap the context's error", err)
	}
	if strings.Contains(err.Error(), server.URL) {
		t.Errorf("error %q quotes the URL", err)
	}
}

func TestExecute_ResponseForAnotherOperation_IsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"operationId":"op_other_1","version":"v1"}`))
	}))
	defer server.Close()
	if _, err := New(server.URL, server.Client()).Execute(context.Background(), action(validArguments)); err == nil {
		t.Fatal("execute: error = nil, want an error for a response naming another operation")
	}
}

func TestExecute_InvalidArgumentsOrAttempt_FailsWithoutCalling(t *testing.T) {
	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called.Store(true) }))
	defer server.Close()

	noAttempt := action(validArguments)
	noAttempt.AttemptNo = 0
	for name, a := range map[string]registry.ToolAction{
		"missing target": action(`{"baseCommit":"c","patchDigest":"d"}`),
		"not json":       action(`{`),
		"no attempt":     noAttempt,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(server.URL, server.Client()).Execute(context.Background(), a); err == nil {
				t.Fatal("execute: error = nil, want an error")
			}
		})
	}
	if called.Load() {
		t.Error("production was called for an invalid request")
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
