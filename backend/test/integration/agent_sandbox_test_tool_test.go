//go:build integration

// The sandbox_test Tool end to end against PostgreSQL: the Agent dispatches it to a real
// in-process sandbox runner, on its mock backend and on its ax backend over the AX
// stand-in, the runner's SUCCEEDED callback resumes the Action and writes the patch_tested
// fact in the resume transaction, and a re-delivery of the same callback is a duplicate
// that writes nothing.
//
// They share the agentHarness of agent_loop_test.go.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/fixture/mockax"
	"github.com/leungll/Emberling/backend/internal/fixture/sandboxrunner"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/tools/sandboxtest"
)

// sandboxPatch changes a comment of the runner's fixture at sandboxBaseCommit; it applies
// under the runner's strict patch applier, and the fixture's test still passes.
const (
	sandboxBaseCommit = "fixture-v1"
	sandboxPatch      = "--- a/greet.go\n+++ b/greet.go\n@@ -5 +5 @@\n" +
		"-// Greeting returns the greeting for name.\n" +
		"+// Greeting returns the greeting for the given name.\n"
)

// sandboxRunnerWait bounds the wait for the runner's callback.
const sandboxRunnerWait = 20 * time.Second

// sandboxRunnerCallback is one callback delivery the runner attempted.
type sandboxRunnerCallback struct {
	token          string
	externalTaskID string
	payload        json.RawMessage
}

// sandboxRunner is a real sandbox runner served in-process. It counts the test requests
// it receives and keeps the callback token of the last one; its callbacks never leave the
// process: a transport captures each delivery and answers 200, so the test itself hands
// the captured callback to the resume use case, once and then again.
type sandboxRunner struct {
	http      *httptest.Server
	callbacks chan sandboxRunnerCallback

	mu    sync.Mutex
	token string
	calls int
}

// newSandboxRunner starts a runner on the mock backend, or with ax on the ax backend over
// an AX stand-in whose default scenario is a passing run of twelve tests.
func newSandboxRunner(t *testing.T, ax bool) *sandboxRunner {
	t.Helper()
	r := &sandboxRunner{callbacks: make(chan sandboxRunnerCallback, 4)}
	var backend sandboxrunner.Backend = sandboxrunner.NewMockBackend()
	if ax {
		fake := mockax.New(mockax.Config{Digest: sandboxrunner.WorkspaceDigest})
		axBackend, err := sandboxrunner.NewAXBackend(sandboxrunner.AXBackendConfig{
			Atespace:    "emberling-sandbox",
			Image:       "registry.local/sandboxsupervisor@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			TestTimeout: sandboxRunnerWait, ReadyTimeout: sandboxRunnerWait,
			Control: fake, Results: fake,
		})
		if err != nil {
			t.Fatalf("new ax backend: %v", err)
		}
		backend = axBackend
	}
	server := sandboxrunner.NewServer(backend, sandboxrunner.WithHTTPClient(&http.Client{Transport: callbackCapture{r.callbacks}}))
	r.http = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost && req.URL.Path == "/v1/tests" {
			raw, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
			var body struct {
				CallbackToken string `json:"callbackToken"`
			}
			_ = json.Unmarshal(raw, &body)
			r.mu.Lock()
			r.token = body.CallbackToken
			r.calls++
			r.mu.Unlock()
			req.Body = io.NopCloser(bytes.NewReader(raw))
		}
		server.ServeHTTP(w, req)
	}))
	t.Cleanup(func() {
		r.http.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	return r
}

// received waits for the runner's callback and returns it, after checking that the runner
// received exactly one test request, that it carried a callback token, and that the
// callback came back with that token.
func (r *sandboxRunner) received(t *testing.T) sandboxRunnerCallback {
	t.Helper()
	var callback sandboxRunnerCallback
	select {
	case callback = <-r.callbacks:
	case <-time.After(sandboxRunnerWait):
		t.Fatalf("the sandbox runner delivered no callback within %s", sandboxRunnerWait)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls != 1 || r.token == "" {
		t.Fatalf("runner received %d requests (token present %v), want exactly one with a token", r.calls, r.token != "")
	}
	if callback.token != r.token {
		t.Fatalf("runner callback carried a different token than the one it was given")
	}
	return callback
}

// callbackCapture is the runner's callback transport: it records each delivery and
// answers 200 without forwarding it.
type callbackCapture struct{ deliveries chan<- sandboxRunnerCallback }

func (c callbackCapture) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	var body struct {
		ExternalTaskID string          `json:"externalTaskId"`
		Payload        json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	select {
	case c.deliveries <- sandboxRunnerCallback{
		token: req.Header.Get("X-Emberling-Callback-Token"), externalTaskID: body.ExternalTaskID, payload: body.Payload,
	}:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"accepted":true}`)), Request: req,
	}, nil
}

func TestAgentSandboxTestTool_SucceededCallback_WritesPatchTestedFactOnce(t *testing.T) {
	for _, variant := range []struct {
		name string
		ax   bool
	}{{"mock_backend", false}, {"ax_backend_on_mockax", true}} {
		t.Run(variant.name, func(t *testing.T) {
			runner := newSandboxRunner(t, variant.ax)

			h := newAgentHarness(t, agentHarnessOptions{
				Tools: []registry.ToolRegistration{sandboxtest.Registration(runner.http.URL, runner.http.Client())},
				Clock: newExecClock(time.Now().UTC().Truncate(time.Millisecond)),
			})
			arguments, _ := json.Marshal(map[string]string{"baseCommit": sandboxBaseCommit, "patch": sandboxPatch})
			factScriptToolCalls(h, mockmodel.Scenario{ToolName: sandboxtest.ToolName, ToolArguments: arguments})
			def := h.saveDefinition(factDefinition("wf-sandbox-test", sandboxtest.ToolName))
			run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
			outcome := h.claimAgentNode(run.ID)
			h.execute(outcome)

			action, _ := agentActionOfTurn(h.ctx, t, h.uow, outcome.AgentTurnID)
			if action.Status != domain.AgentActionWaitingCallback {
				t.Fatalf("precondition: action status = %s, want WAITING_CALLBACK", action.Status)
			}
			delivered := runner.received(t)
			agentRunID := factAgentRunID(t, h, outcome)

			patchDigest := sandboxrunner.PatchDigest(sandboxPatch)
			resultDigest := sandboxrunner.ResultDigest(sandboxBaseCommit, patchDigest, true, 12, 0)
			deliver := func() (service.CallbackOutcome, error) {
				return h.svc.HandleCallback(h.ctx, service.HandleCallback{
					Token: delivered.token, ExternalTaskID: delivered.externalTaskID, Payload: delivered.payload,
				})
			}

			first, err := deliver()
			if err != nil || !first.Accepted || first.Duplicate {
				t.Fatalf("callback = %+v, %v, want accepted", first, err)
			}
			if action = getAgentAction(h.ctx, t, h.uow, action.ID); action.Status != domain.AgentActionSucceeded {
				t.Fatalf("action status = %s, want SUCCEEDED", action.Status)
			}
			attempt := agentOnlyToolAttempt(t, h, action.ID)
			facts := listFactsByAgentRun(h.ctx, t, h.uow, agentRunID)
			if len(facts) != 1 {
				t.Fatalf("facts = %+v, want exactly one", facts)
			}
			fact := facts[0]
			if fact.FactType != sandboxtest.FactType || fact.SubjectRef != patchDigest || fact.ToolAttemptID != attempt.ID {
				t.Errorf("fact = %+v, want %s about %s from attempt %s", fact, sandboxtest.FactType, patchDigest, attempt.ID)
			}
			if fact.Verdict == nil || !*fact.Verdict {
				t.Errorf("fact verdict = %v, want true", fact.Verdict)
			}
			wantBinding, _ := json.Marshal(map[string]string{"baseCommit": sandboxBaseCommit, "resultDigest": resultDigest})
			assertSameJSON(t, "fact binding", wantBinding, fact.Binding)

			before := agentSnapshot(t, h, run, outcome)
			second, err := deliver()
			if err != nil || !second.Accepted || !second.Duplicate {
				t.Fatalf("second callback = %+v, %v, want an accepted duplicate", second, err)
			}
			if after := agentSnapshot(t, h, run, outcome); after != before {
				t.Fatalf("state after the duplicate = %+v, want unchanged %+v", after, before)
			}
			if facts := listFactsByAgentRun(h.ctx, t, h.uow, agentRunID); len(facts) != 1 {
				t.Errorf("facts after the duplicate = %d, want still one", len(facts))
			}
			if n := agentCallbackCompletions(t, listEvents(h.ctx, t, h.uow, run.ID)); n != 1 {
				t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want exactly 1", n)
			}
		})
	}
}
