//go:build integration

package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
	"github.com/leungll/Emberling/backend/internal/tools/remotelookup"
	"github.com/leungll/Emberling/backend/internal/work"
)

// agentAsyncFinalOutput is the scripted FINAL answer of the Turn after the Tool callback.
const agentAsyncFinalOutput = "the answer after the callback"

// agentAsyncCallbackRun is one Agent Run whose ASYNC remote_lookup call was dispatched to
// the real Mock Provider and whose callback the fixture still holds back.
type agentAsyncCallbackRun struct {
	fixture        *providerFixture
	env            *testEnv
	runID          string
	agentNodeRunID string
	token          string
	externalTaskID string
	workers        *agentWorkerGate
}

// agentWorkerGate holds every work.Pool worker in BeforeExecute while it is held, so a
// test can observe what the callback request committed before any worker acts on it.
type agentWorkerGate struct {
	held     atomic.Bool
	released chan struct{}
	once     sync.Once
}

func newAgentWorkerGate() *agentWorkerGate {
	return &agentWorkerGate{released: make(chan struct{})}
}

func (g *agentWorkerGate) hooks() work.Hooks {
	return work.Hooks{BeforeExecute: func(string) {
		if g.held.Load() {
			<-g.released
		}
	}}
}

func (g *agentWorkerGate) hold() { g.held.Store(true) }

func (g *agentWorkerGate) open() {
	g.once.Do(func() {
		g.held.Store(false)
		close(g.released)
	})
}

// startAgentAsyncCallbackRun drives an Agent through a remote_lookup TOOL_CALL until its
// NodeRun is WAITING_CALLBACK. The Provider has accepted the task, but its callback stays
// gated until the test calls fixture.callbacks.release().
func startAgentAsyncCallbackRun(t *testing.T) agentAsyncCallbackRun {
	t.Helper()
	fixture := newProviderFixture(t)
	workers := newAgentWorkerGate()
	env := newTestEnvWithOptions(t, testEnvOptions{
		MockTaskBaseURL: fixture.server.URL,
		TaskClient:      &http.Client{Transport: fixture.tasks, Timeout: e2eHTTPTimeout},
		WorkHooks:       workers.hooks(),
	})
	fixture.callbacks.setTarget(env.server.URL)
	// Registered after the Backend, so it runs before the Pool is stopped: a held worker
	// would otherwise keep Stop waiting.
	t.Cleanup(workers.open)
	runID := createAgentAsyncRun(t, env)

	agentNodeRun := env.waitForNodeRunStatus(t, runID, "node_agent", "WAITING_CALLBACK", agentTraceWait)
	agentNodeRunID, _ := agentNodeRun["id"].(string)

	dispatches := fixture.tasks.dispatches()
	if len(dispatches) != 1 {
		t.Fatalf("provider received %d dispatches, want 1", len(dispatches))
	}
	var sent struct {
		CallbackToken string `json:"callbackToken"`
	}
	if err := json.Unmarshal(dispatches[0], &sent); err != nil || sent.CallbackToken == "" {
		t.Fatalf("dispatch carried no callback token: %v", err)
	}
	attempt := agentAsyncOnlyToolAttempt(t, env, runID, agentNodeRunID)
	if attempt.CallbackBinding == nil || attempt.CallbackBinding.ExternalTaskID == "" {
		t.Fatalf("dispatched tool attempt has no callback binding: %+v", attempt)
	}
	return agentAsyncCallbackRun{
		fixture: fixture, env: env, runID: runID, agentNodeRunID: agentNodeRunID,
		token: sent.CallbackToken, externalTaskID: attempt.CallbackBinding.ExternalTaskID,
		workers: workers,
	}
}

// createAgentAsyncRun scripts env's model to decide one remote_lookup TOOL_CALL and then
// answer FINAL, saves the Agent fixture with remote_lookup as its only allowed Tool, and
// starts a Run against it.
func createAgentAsyncRun(t *testing.T, env *testEnv) string {
	t.Helper()
	var calls atomic.Int32
	env.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		if calls.Add(1) == 1 {
			return &mockmodel.Scenario{
				Kind:          mockmodel.ScenarioToolCall,
				ToolName:      remotelookup.ToolName,
				ToolArguments: json.RawMessage(`{"key":"k1"}`),
			}
		}
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, Output: agentAsyncFinalOutput}
	}

	fx := loadAgentLookupFixture(t)
	for i, node := range fx.Nodes {
		if node.ID == "node_agent" {
			fx.Nodes[i].Config = json.RawMessage(strings.Replace(string(node.Config),
				`["`+lookup.ToolName+`"]`, `["`+remotelookup.ToolName+`"]`, 1))
		}
	}
	resp, raw := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, raw)
	}
	created := decodeBody[map[string]any](t, raw)
	workflowID, _ := created["workflowId"].(string)
	version, _ := created["version"].(float64)
	return createAgentRun(t, env, workflowID, int(version), "what is the answer?")
}

type agentAsyncTraceAttempt struct {
	Status          string          `json:"status"`
	Result          json.RawMessage `json:"result"`
	CompletedAt     *string         `json:"completedAt"`
	CallbackBinding *struct {
		ExternalTaskID string `json:"externalTaskId"`
	} `json:"callbackBinding"`
}

// agentAsyncOnlyToolAttempt reads the first Turn's single Tool Attempt from the Agent Trace.
func agentAsyncOnlyToolAttempt(t *testing.T, env *testEnv, runID, agentNodeRunID string) agentAsyncTraceAttempt {
	t.Helper()
	status, body := getAgentTrace(t, env, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, want %d, body=%s", status, http.StatusOK, body)
	}
	var trace struct {
		Turns []struct {
			ToolAttempts []agentAsyncTraceAttempt `json:"toolAttempts"`
		} `json:"turns"`
	}
	if err := json.Unmarshal(body, &trace); err != nil {
		t.Fatalf("decode agent trace: %v body=%s", err, body)
	}
	if len(trace.Turns) == 0 || len(trace.Turns[0].ToolAttempts) != 1 {
		t.Fatalf("agent trace = %s, want one Tool Attempt on the first Turn", body)
	}
	return trace.Turns[0].ToolAttempts[0]
}

// agentAsyncTurnStatuses reads every Turn's status from the Agent Trace, by turnNo.
func agentAsyncTurnStatuses(t *testing.T, env *testEnv, runID, agentNodeRunID string) map[int]string {
	t.Helper()
	status, body := getAgentTrace(t, env, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, want %d, body=%s", status, http.StatusOK, body)
	}
	var trace struct {
		Turns []struct {
			TurnNo int    `json:"turnNo"`
			Status string `json:"status"`
		} `json:"turns"`
	}
	if err := json.Unmarshal(body, &trace); err != nil {
		t.Fatalf("decode agent trace: %v body=%s", err, body)
	}
	statuses := make(map[int]string, len(trace.Turns))
	for _, turn := range trace.Turns {
		statuses[turn.TurnNo] = turn.Status
	}
	return statuses
}

// agentAsyncCallbackCompletions counts AGENT_ACTION_COMPLETED Events whose completionSource
// is CALLBACK in the Run's public Event list.
func agentAsyncCallbackCompletions(events []map[string]any) int {
	n := 0
	for _, ev := range events {
		if typ, _ := ev["type"].(string); typ != "AGENT_ACTION_COMPLETED" {
			continue
		}
		payload, _ := ev["payload"].(map[string]any)
		if source, _ := payload["completionSource"].(string); source == "CALLBACK" {
			n++
		}
	}
	return n
}

// TestAPI_AgentAsyncToolCallback_ValidToken_Returns200AndLoopCompletes covers the callback
// contract for a Tool Attempt target: the Provider's authenticated callback is answered 200
// accepted as soon as the resume commits, with the next Turn still READY -- the request
// itself runs no model call. A work.Pool worker then carries the Agent Loop through that
// Turn to COMPLETED, and the Agent Trace shows the SUCCEEDED Tool Attempt without the
// callback token or its hash.
func TestAPI_AgentAsyncToolCallback_ValidToken_Returns200AndLoopCompletes(t *testing.T) {
	r := startAgentAsyncCallbackRun(t)

	r.workers.hold()
	r.fixture.callbacks.release()
	delivery := r.fixture.waitDelivery(t, e2eWait)
	if delivery.StatusCode != http.StatusOK {
		t.Fatalf("provider callback status = %d, want %d, body=%s", delivery.StatusCode, http.StatusOK, delivery.Body)
	}
	outcome := decodeBody[callbackResponseDTO](t, delivery.Body)
	if !outcome.Accepted || outcome.Pending || outcome.Duplicate {
		t.Errorf("provider callback outcome = %+v, want accepted", outcome)
	}
	if turns := agentAsyncTurnStatuses(t, r.env, r.runID, r.agentNodeRunID); turns[2] != "READY" {
		t.Fatalf("turn statuses when the callback was answered = %v, want turn 2 READY: the request must not run it", turns)
	}

	r.workers.open()
	snapshot := r.env.waitForTerminal(t, r.runID, agentTraceWait)
	run, _ := snapshot["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("run terminal status = %q, want COMPLETED; snapshot=%v", status, snapshot)
	}
	if n := agentAsyncCallbackCompletions(r.env.listEvents(t, r.runID)); n != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want 1", n)
	}

	attempt := agentAsyncOnlyToolAttempt(t, r.env, r.runID, r.agentNodeRunID)
	if attempt.Status != "SUCCEEDED" || attempt.CompletedAt == nil {
		t.Errorf("tool attempt = %+v, want SUCCEEDED with completedAt", attempt)
	}

	_, body := getAgentTrace(t, r.env, r.runID, r.agentNodeRunID)
	sum := sha256.Sum256([]byte(r.token))
	for _, forbidden := range []string{r.token, hex.EncodeToString(sum[:]), "callbackTokenHash", "callbackToken"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("agent trace after the callback leaks %q", forbidden)
		}
	}
	for _, ev := range r.env.listEvents(t, r.runID) {
		raw, _ := json.Marshal(ev)
		if strings.Contains(string(raw), r.token) || strings.Contains(string(raw), hex.EncodeToString(sum[:])) {
			t.Errorf("event %v leaks the callback token or its hash", ev["type"])
		}
	}
}

// TestAPI_AgentAsyncToolCallback_InvalidToken_Returns401AndActionKeepsWaiting covers the
// callback-token 401 rule: a forged or missing credential for a Tool Attempt's external
// task is answered 401 and changes nothing -- the Agent NodeRun stays WAITING_CALLBACK
// and no Event is written.
func TestAPI_AgentAsyncToolCallback_InvalidToken_Returns401AndActionKeepsWaiting(t *testing.T) {
	r := startAgentAsyncCallbackRun(t)
	eventsBefore := r.env.listEvents(t, r.runID)
	payload := map[string]any{"status": "SUCCEEDED", "key": "k1", "record": "forged"}

	for name, token := range map[string]string{"tampered": tamperToken(r.token), "missing": ""} {
		resp, body := r.env.doCallback(t, token, r.externalTaskID, payload)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s token: status = %d, want %d, body=%s", name, resp.StatusCode, http.StatusUnauthorized, body)
		}
		if strings.Contains(string(body), r.token) {
			t.Errorf("%s token: response echoes the callback token", name)
		}
	}

	if got := len(r.env.listEvents(t, r.runID)); got != len(eventsBefore) {
		t.Errorf("refused callbacks changed the Event count: before=%d after=%d", len(eventsBefore), got)
	}
	r.env.waitForNodeRunStatus(t, r.runID, "node_agent", "WAITING_CALLBACK", agentTraceWait)
	if attempt := agentAsyncOnlyToolAttempt(t, r.env, r.runID, r.agentNodeRunID); attempt.Status != "DISPATCHED" {
		t.Errorf("tool attempt status = %s, want DISPATCHED", attempt.Status)
	}
}

// TestAPI_AgentAsyncToolCallback_Duplicate_Returns200DuplicateWithoutNewEvents covers
// duplicate delivery for a Tool Attempt: re-delivering the Provider's callback after the
// Agent Loop resumed is answered 200 duplicate and commits no Event.
func TestAPI_AgentAsyncToolCallback_Duplicate_Returns200DuplicateWithoutNewEvents(t *testing.T) {
	r := startAgentAsyncCallbackRun(t)
	r.fixture.callbacks.release()
	first := r.fixture.waitDelivery(t, e2eWait)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first delivery status = %d, want %d, body=%s", first.StatusCode, http.StatusOK, first.Body)
	}
	r.env.waitForTerminal(t, r.runID, agentTraceWait)
	eventsBefore := r.env.listEvents(t, r.runID)

	resp, body := r.env.doCallback(t, r.token, r.externalTaskID, map[string]any{"status": "SUCCEEDED", "key": "k1", "record": "again"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("duplicate delivery status = %d, want %d, body=%s", resp.StatusCode, http.StatusOK, body)
	}
	outcome := decodeBody[callbackResponseDTO](t, body)
	if !outcome.Accepted || outcome.Pending || !outcome.Duplicate {
		t.Errorf("duplicate delivery outcome = %+v, want accepted duplicate", outcome)
	}
	eventsAfter := r.env.listEvents(t, r.runID)
	if len(eventsAfter) != len(eventsBefore) {
		t.Errorf("duplicate delivery changed the Event count: before=%d after=%d", len(eventsBefore), len(eventsAfter))
	}
	if n := agentAsyncCallbackCompletions(eventsAfter); n != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want 1", n)
	}
}
