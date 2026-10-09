//go:build integration

// The sandbox_test Tool end to end against PostgreSQL: the Agent dispatches it to a runner
// stub, the runner's SUCCEEDED callback resumes the Action and writes the patch_tested
// fact in the resume transaction, and a re-delivery of the same callback is a duplicate
// that writes nothing.
//
// They share the agentHarness of agent_loop_test.go.
package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/sandboxrunner"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/tools/sandboxtest"
)

const (
	sandboxTestID     = "test_0123456789abcdef"
	sandboxBaseCommit = "fixture-v1"
	sandboxPatch      = "--- a/greet.go\n+++ b/greet.go\n@@ -5 +5 @@\n-// old\n+// new\n"
)

// sandboxRunnerStub accepts one test request and records the callback token it was given,
// as the real runner would before delivering its result.
type sandboxRunnerStub struct {
	mu    sync.Mutex
	token string
	calls int
}

func (s *sandboxRunnerStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CallbackToken string `json:"callbackToken"`
		BaseCommit    string `json:"baseCommit"`
		Patch         string `json:"patch"`
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if r.Method != http.MethodPost || r.URL.Path != "/v1/tests" || json.Unmarshal(raw, &body) != nil ||
		body.BaseCommit != sandboxBaseCommit || body.Patch != sandboxPatch {
		http.Error(w, "unexpected request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.token = body.CallbackToken
	s.calls++
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = io.WriteString(w, `{"testId":"`+sandboxTestID+`"}`)
}

func (s *sandboxRunnerStub) received(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls != 1 || s.token == "" {
		t.Fatalf("runner stub received %d requests (token present %v), want exactly one with a token", s.calls, s.token != "")
	}
	return s.token
}

func TestAgentSandboxTestTool_SucceededCallback_WritesPatchTestedFactOnce(t *testing.T) {
	stub := &sandboxRunnerStub{}
	runner := httptest.NewServer(stub)
	t.Cleanup(runner.Close)

	h := newAgentHarness(t, agentHarnessOptions{
		Tools: []registry.ToolRegistration{sandboxtest.Registration(runner.URL, runner.Client())},
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
	token := stub.received(t)
	agentRunID := factAgentRunID(t, h, outcome)

	patchDigest := sandboxrunner.PatchDigest(sandboxPatch)
	resultDigest := sandboxrunner.ResultDigest(sandboxBaseCommit, patchDigest, true, 12, 0)
	callback, _ := json.Marshal(map[string]any{
		"status": "SUCCEEDED", "passed": true, "baseCommit": sandboxBaseCommit,
		"patchDigest": patchDigest, "resultDigest": resultDigest,
		"summary": map[string]int{"total": 12, "failed": 0},
	})
	deliver := func() (service.CallbackOutcome, error) {
		return h.svc.HandleCallback(h.ctx, service.HandleCallback{
			Token: token, ExternalTaskID: sandboxTestID, Payload: callback,
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
}
