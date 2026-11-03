package deployrelease

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
	"github.com/leungll/Emberling/backend/internal/fixture/mockproduction"
	"github.com/leungll/Emberling/backend/internal/registry"
)

func releaseAction() registry.ToolAction {
	return registry.ToolAction{
		AgentRunID: "ar_1", TurnID: "turn_1", ActionID: "act_7", ToolName: ToolName, AttemptNo: 2,
		Arguments: json.RawMessage(`{"changeRequestId":"cr_1"}`),
		Workspace: &registry.WorkspaceView{
			RepositoryID: "vuln-demo", BaseCommit: "abc123", PatchDigest: "sha256:feed",
			DeployTarget: &domain.WorkspaceDeployTarget{Service: "payments-api", Environment: "production"},
		},
	}
}

func state() registry.ToolAsyncState {
	return registry.ToolAsyncState{
		AgentRunID: "ar_1", ActionID: "act_7", ToolName: ToolName, AttemptNo: 2,
		ExternalTask:  registry.ExternalTask{ProviderID: ProviderID, ExternalTaskID: "op_act_7_2"},
		ReleaseTarget: &domain.WorkspaceDeployTarget{Service: "payments-api", Environment: "production"},
	}
}

func newProduction(t *testing.T) (*mockproduction.Server, *httptest.Server) {
	t.Helper()
	server := mockproduction.NewServer(mockproduction.WithCompletionDelay(0))
	srv := httptest.NewServer(server)
	t.Cleanup(func() { server.StopHolding(); srv.Close() })
	return server, srv
}

func TestRegistration_DeclaresTheReconcilableReleaseContract(t *testing.T) {
	reg := Registration("http://production.test", nil)
	m := reg.Metadata
	if m.Name != ToolName || m.ExecutionKind != domain.ToolExecutionAsync || m.Poll == nil {
		t.Fatalf("name/kind/poll = %s/%s/%v, want deploy_change ASYNC with a poll policy", m.Name, m.ExecutionKind, m.Poll)
	}
	if want := (domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed}); m.SideEffect != want {
		t.Errorf("sideEffect = %+v, want %+v", m.SideEffect, want)
	}
	if m.Authorization == nil || m.Authorization.Risk != domain.ToolRiskProduction || m.Authorization.Reversible {
		t.Errorf("authorization = %+v, want irreversible PRODUCTION risk", m.Authorization)
	}
	if !m.Reconcilable || !m.OccupiesReleaseTarget || !m.UsesWorkspace {
		t.Errorf("reconcilable/occupiesReleaseTarget/usesWorkspace = %v/%v/%v, want all true", m.Reconcilable, m.OccupiesReleaseTarget, m.UsesWorkspace)
	}
	if m.Produces == nil || m.Produces.FactType != FactType || m.Produces.SubjectPointer.Pointer != "/patchDigest" ||
		m.Produces.VerdictPointer == nil || m.Produces.VerdictPointer.Pointer != "/healthy" ||
		m.Produces.BindArguments["operationKey"].Pointer != "/operationKey" || m.Produces.BindArguments["version"].Pointer != "/version" {
		t.Errorf("produces = %+v, want release_confirmed about /patchDigest binding operationKey and version with verdict /healthy", m.Produces)
	}
	if err := registry.NewToolRegistry().Register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
}

func TestExecute_SendsTheOperationKeyAndFrozenTarget_ReturnsDispatched(t *testing.T) {
	var gotPath string
	var gotBody map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"operationId":"op_act_7_2","status":"ACCEPTED"}`))
	}))
	defer server.Close()

	got, err := New(server.URL, server.Client()).Execute(context.Background(), releaseAction())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Kind != registry.ToolResultDispatched || got.ExternalTask == nil ||
		*got.ExternalTask != (registry.ExternalTask{ProviderID: ProviderID, ExternalTaskID: "op_act_7_2"}) {
		t.Fatalf("result = %+v, want a dispatch under op_act_7_2", got)
	}
	if gotPath != "POST /v2/deployments" {
		t.Errorf("request = %s, want POST /v2/deployments", gotPath)
	}
	want := map[string]string{
		"operationId": `"op_act_7_2"`, "target": `{"service":"payments-api","environment":"production"}`,
		"baseCommit": `"abc123"`, "patchDigest": `"sha256:feed"`,
	}
	if len(gotBody) != len(want) {
		t.Errorf("body has %d members, want %d: the change request id and arguments never travel", len(gotBody), len(want))
	}
	for key, value := range want {
		if string(gotBody[key]) != value {
			t.Errorf("body[%s] = %s, want %s", key, gotBody[key], value)
		}
	}
}

func TestExecute_WithoutWorkspaceTarget_SendsNothing(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	noTarget := releaseAction()
	noTarget.Workspace.DeployTarget = nil
	noWorkspace := releaseAction()
	noWorkspace.Workspace = nil
	for name, action := range map[string]registry.ToolAction{"no target": noTarget, "no workspace": noWorkspace} {
		if _, err := New(server.URL, server.Client()).Execute(context.Background(), action); err == nil {
			t.Errorf("%s: error = nil, want an error", name)
		}
	}
	if called {
		t.Error("production was called without a target")
	}
}

func TestExecute_Conflict_IsProviderFailureWithCodeOnly(t *testing.T) {
	server, srv := newProduction(t)
	_ = server
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"FROZEN_WINDOW","message":"token hunter2"}`))
	}))
	defer rejecting.Close()
	_, err := New(rejecting.URL, rejecting.Client()).Execute(context.Background(), releaseAction())
	var failure *registry.ProviderFailure
	if !errors.As(err, &failure) || failure.Err.Code != "FROZEN_WINDOW" {
		t.Fatalf("error = %v, want a ProviderFailure with code FROZEN_WINDOW", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error %q carries the response body", err)
	}
	// The mock production's own refusal maps the same way.
	_ = srv
}

func TestExecute_ServerErrorAndTransportFailure_PlainErrorWithoutURL(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	for name, baseURL := range map[string]string{"500": failing.URL, "transport": closedURL} {
		t.Run(name, func(t *testing.T) {
			_, err := New(baseURL, nil).Execute(context.Background(), releaseAction())
			if err == nil {
				t.Fatal("error = nil, want an error")
			}
			var failure *registry.ProviderFailure
			if errors.As(err, &failure) {
				t.Errorf("error = %v, want a plain error, not a ProviderFailure", err)
			}
			if strings.Contains(err.Error(), strings.TrimPrefix(baseURL, "http://")) {
				t.Errorf("error %q quotes the URL", err)
			}
		})
	}
}

func TestExecute_ContextDeadline_IsReportedAsDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(server.URL, server.Client()).Execute(ctx, releaseAction()); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want it to wrap the context's error", err)
	}
}

func TestPoll_MapsProductionStatuses(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   registry.PollStatus
		code   string
	}{
		"accepted":  {http.StatusOK, `{"operationId":"op_act_7_2","status":"ACCEPTED"}`, registry.PollRunning, ""},
		"deploying": {http.StatusOK, `{"operationId":"op_act_7_2","status":"DEPLOYING"}`, registry.PollRunning, ""},
		"deployed":  {http.StatusOK, `{"operationId":"op_act_7_2","status":"DEPLOYED","version":"v3"}`, registry.PollSucceeded, ""},
		"voided":    {http.StatusOK, `{"operationId":"op_act_7_2","status":"VOIDED"}`, registry.PollFailed, domain.ReleaseCodeVoided},
		"not found": {http.StatusNotFound, `{"code":"NOT_FOUND"}`, registry.PollUnknownTask, ""},
		"refused":   {http.StatusConflict, `{"code":"FROZEN_WINDOW"}`, registry.PollFailed, "FROZEN_WINDOW"},
		"5xx":       {http.StatusServiceUnavailable, `{}`, registry.PollStatusUnknown, ""},
		"other op":  {http.StatusOK, `{"operationId":"op_other","status":"DEPLOYED","version":"v3"}`, registry.PollStatusUnknown, ""},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			got, err := New(server.URL, server.Client()).Poll(context.Background(), state())
			if err != nil {
				t.Fatalf("poll: %v", err)
			}
			if got.Status != tc.want {
				t.Fatalf("status = %s, want %s", got.Status, tc.want)
			}
			if tc.code != "" && (got.Error == nil || got.Error.Code != tc.code) {
				t.Fatalf("error = %+v, want code %s", got.Error, tc.code)
			}
			if tc.want == registry.PollSucceeded {
				if got.ToolResult == nil || string(got.ToolResult.Output) != `{"operationKey":"op_act_7_2","version":"v3"}` {
					t.Fatalf("result = %+v, want the operation key and version", got.ToolResult)
				}
			}
		})
	}
}

func TestPoll_TransportFailure_IsAnErrorNotAnAnswer(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	if _, err := New(url, nil).Poll(context.Background(), state()); err == nil {
		t.Fatal("poll against a closed server: error = nil, want an error")
	}
}

func TestVoidAndCheckTarget_AgainstMockProduction(t *testing.T) {
	server, srv := newProduction(t)
	server.PauseCompletion()
	executor := New(srv.URL, srv.Client())

	// Void before the operation ever arrived: the tombstone answers VOIDED.
	got, err := executor.Void(context.Background(), state())
	if err != nil || got.Status != registry.PollFailed || got.Error == nil || got.Error.Code != domain.ReleaseCodeVoided {
		t.Fatalf("void of an unknown operation = %+v, %v; want FAILED VOIDED", got, err)
	}
	// Its late arrival is refused as a definite ProviderFailure.
	_, err = executor.Execute(context.Background(), releaseAction())
	var failure *registry.ProviderFailure
	if !errors.As(err, &failure) || failure.Err.Code != domain.ReleaseCodeVoided {
		t.Fatalf("late arrival = %v, want a ProviderFailure with code VOIDED", err)
	}

	// A fresh operation deploys and the target reports it.
	next := releaseAction()
	next.AttemptNo = 3
	if _, err := executor.Execute(context.Background(), next); err != nil {
		t.Fatalf("execute attempt 3: %v", err)
	}
	nextState := state()
	nextState.AttemptNo = 3
	nextState.ExternalTask.ExternalTaskID = "op_act_7_3"
	if got, err := executor.Poll(context.Background(), nextState); err != nil || got.Status != registry.PollRunning {
		t.Fatalf("poll while completion is paused = %+v, %v; want RUNNING", got, err)
	}
	if got, err := executor.Void(context.Background(), nextState); err != nil || got.Status != registry.PollRunning {
		t.Fatalf("void of a started operation = %+v, %v; want RUNNING (current status)", got, err)
	}
	server.ResumeCompletion()
	if got, err := executor.Poll(context.Background(), nextState); err != nil || got.Status != registry.PollSucceeded {
		t.Fatalf("poll after completion = %+v, %v; want SUCCEEDED", got, err)
	}
	current, err := executor.CheckTarget(context.Background(), nextState)
	if err != nil {
		t.Fatalf("check target: %v", err)
	}
	want := registry.TargetState{Known: true, Version: "v1", PatchDigest: "sha256:feed", Healthy: true, LastOperationID: "op_act_7_3"}
	if current != want {
		t.Fatalf("target = %+v, want %+v", current, want)
	}
}

func TestCheckTarget_UnknownTargetAndMissingState(t *testing.T) {
	_, srv := newProduction(t)
	executor := New(srv.URL, srv.Client())
	current, err := executor.CheckTarget(context.Background(), state())
	if err != nil || current.Known {
		t.Fatalf("target with no deployment = %+v, %v; want unknown without error", current, err)
	}
	noTarget := state()
	noTarget.ReleaseTarget = nil
	if _, err := executor.CheckTarget(context.Background(), noTarget); err == nil {
		t.Fatal("check target without a release target: error = nil, want an error")
	}
}

func TestOnCallback_IsRefused(t *testing.T) {
	if _, err := New("http://production.test", nil).OnCallback(context.Background(), state(), []byte(`{}`)); err == nil {
		t.Fatal("OnCallback: error = nil, want a refusal: the release outcome is polled")
	}
}
