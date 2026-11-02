//go:build integration

package contract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const takeoverNodeType = "test_async_keyed"

const takeoverExternalTaskID = "keyed-task-original"

// takeoverClock is the system clock plus an offset the test moves forward, so a retry
// backoff can elapse without real time passing.
type takeoverClock struct{ offset atomic.Int64 }

func (c *takeoverClock) Now() time.Time {
	return time.Now().UTC().Add(time.Duration(c.offset.Load()))
}

func (c *takeoverClock) advance(d time.Duration) { c.offset.Add(int64(d)) }

// takeoverProvider stands in for a Provider that honours idempotency keys and whose first
// answer is lost on the way back: the first dispatch under a key accepts the task and then
// fails as uncertain, and a later dispatch under the same key replays the task id.
type takeoverProvider struct {
	mu     sync.Mutex
	tasks  map[string]string
	tokens []string
}

type takeoverLostResponse struct{}

func (takeoverLostResponse) Error() string   { return "provider response lost" }
func (takeoverLostResponse) Uncertain() bool { return true }

func (p *takeoverProvider) ValidateSemantics(context.Context, map[string]any) error { return nil }

func (p *takeoverProvider) Execute(_ context.Context, input registry.NodeInput, _ map[string]any) (registry.NodeResult, error) {
	if input.Callback == nil || input.IdempotencyKey == "" {
		return registry.NodeResult{}, fmt.Errorf("%s: a keyed asynchronous dispatch needs a callback and an idempotency key", takeoverNodeType)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokens = append(p.tokens, input.Callback.Token)
	if existing, ok := p.tasks[input.IdempotencyKey]; ok {
		return registry.NodeResult{
			Kind:         registry.NodeResultDispatched,
			ExternalTask: &registry.ExternalTask{ProviderID: "test-keyed-provider", ExternalTaskID: existing, Replayed: true},
		}, nil
	}
	p.tasks[input.IdempotencyKey] = takeoverExternalTaskID
	return registry.NodeResult{}, takeoverLostResponse{}
}

func (p *takeoverProvider) OnCallback(ctx context.Context, state registry.NodeAsyncState, payload []byte) (registry.NodeOutput, error) {
	return testAsyncExecutor{}.OnCallback(ctx, state, payload)
}

func (p *takeoverProvider) firstToken() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.tokens) == 0 {
		return ""
	}
	return p.tokens[0]
}

func takeoverRegistration(provider *takeoverProvider) registry.NodeRegistration {
	reg := testAsyncNodeRegistration(nil)
	reg.Metadata.Type = takeoverNodeType
	reg.Metadata.DisplayName = "Test Async Keyed"
	reg.Metadata.SideEffect = domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed}
	reg.Binding = registry.ExecutorBinding{Executor: provider}
	return reg
}

// TestAPI_Callback_EarlierAttemptCredentialAfterReplayedDispatch_Returns200AndRunCompletes
// covers the callback endpoint after a lost dispatch response: Attempt 1's task was
// accepted but its answer lost, the retry replayed the same task id, and the original
// task's callback -- still carrying Attempt 1's credential -- is answered 200 and
// completes Attempt 2. The Node detail and Events show the takeover.
func TestAPI_Callback_EarlierAttemptCredentialAfterReplayedDispatch_Returns200AndRunCompletes(t *testing.T) {
	clock := &takeoverClock{}
	provider := &takeoverProvider{tasks: map[string]string{}}
	env := newTestEnvWithOptions(t, testEnvOptions{
		Clock:                  clock,
		ExtraNodeRegistrations: []registry.NodeRegistration{takeoverRegistration(provider)},
	})

	definition := asyncEchoDefinitionRequest()
	nodes, _ := definition["nodes"].([]map[string]any)
	nodes[1]["type"] = takeoverNodeType
	nodes[1]["executionPolicy"] = map[string]any{"timeoutMs": 60_000, "maxAttempts": 3, "backoff": "FIXED"}
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", definition)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create keyed definition: status = %d, body=%s", resp.StatusCode, body)
	}
	created := decodeBody[map[string]any](t, body)
	resp, body = env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": created["workflowId"], "definitionVersion": created["version"], "input": map[string]any{"prompt": "hello"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, body=%s", resp.StatusCode, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)

	// Attempt 1 fails as uncertain and the keyed retry is scheduled.
	nodeRun := waitForScheduledRetry(t, env, runID, "async", 5*time.Second)
	nodeRunID, _ := nodeRun["id"].(string)
	firstToken := provider.firstToken()
	if firstToken == "" {
		t.Fatal("no credential was handed to the first dispatch")
	}

	// The backoff elapses; one Reconciler pass claims and dispatches the retry.
	clock.advance(time.Minute)
	rec := reconciler.New(reconciler.Config{UoW: env.uow, Executor: env.execution, Clock: clock, BatchLimit: 100})
	report, err := rec.RunOnce(context.Background())
	if err != nil || len(report.Errors) != 0 {
		t.Fatalf("reconciler pass: err=%v errors=%v", err, report.Errors)
	}
	env.waitForNodeRunStatus(t, runID, "async", "WAITING_CALLBACK", 5*time.Second)

	resp, body = env.doCallback(t, firstToken, takeoverExternalTaskID, map[string]any{"status": "SUCCEEDED", "output": "original task"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/callbacks with Attempt 1's credential status = %d, want 200, body=%s", resp.StatusCode, body)
	}
	if outcome := decodeBody[callbackResponseDTO](t, body); !outcome.Accepted || outcome.Pending || outcome.Duplicate {
		t.Fatalf("callback outcome = %+v, want accepted", outcome)
	}

	resp, body = env.doCallback(t, firstToken, takeoverExternalTaskID, map[string]any{"status": "SUCCEEDED", "output": "original task"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("duplicate delivery status = %d, want 200, body=%s", resp.StatusCode, body)
	}
	if outcome := decodeBody[callbackResponseDTO](t, body); !outcome.Duplicate {
		t.Fatalf("duplicate delivery outcome = %+v, want duplicate", outcome)
	}

	snapshot := env.waitForTerminal(t, runID, 5*time.Second)
	run, _ := snapshot["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("run status = %q, want COMPLETED", status)
	}

	detail, raw := env.nodeRunDetail(t, runID, nodeRunID)
	attempts := attemptsOf(t, detail)
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2: %s", len(attempts), raw)
	}
	if status, _ := attempts[0]["status"].(string); status != "FAILED" {
		t.Errorf("Attempt 1 status = %q, want FAILED", status)
	}
	if uncertain, _ := attempts[0]["uncertain"].(bool); !uncertain {
		t.Errorf("Attempt 1 uncertain = %v, want true", attempts[0]["uncertain"])
	}
	if status, _ := attempts[1]["status"].(string); status != "SUCCEEDED" {
		t.Errorf("Attempt 2 status = %q, want SUCCEEDED", status)
	}
	binding, _ := attempts[1]["callbackBinding"].(map[string]any)
	if id, _ := binding["externalTaskId"].(string); id != takeoverExternalTaskID {
		t.Errorf("Attempt 2 binding externalTaskId = %q, want the original task %q", id, takeoverExternalTaskID)
	}

	events := env.eventsForNodeRun(t, runID, nodeRunID)
	dispatched := eventsOfType(events, "NODE_DISPATCHED")
	if len(dispatched) != 1 {
		t.Fatalf("NODE_DISPATCHED events = %d, want 1", len(dispatched))
	}
	if replayed, _ := payloadOf(t, dispatched[0])["replayed"].(bool); !replayed {
		t.Errorf("NODE_DISPATCHED replayed = %v, want true", payloadOf(t, dispatched[0])["replayed"])
	}
	received := eventsOfType(events, "NODE_CALLBACK_RECEIVED")
	if len(received) != 1 {
		t.Fatalf("NODE_CALLBACK_RECEIVED events = %d, want 1", len(received))
	}
	if attemptNo, _ := payloadOf(t, received[0])["attemptNo"].(float64); attemptNo != 2 {
		t.Errorf("NODE_CALLBACK_RECEIVED attemptNo = %v, want 2", attemptNo)
	}
	if got := len(eventsOfType(events, "NODE_COMPLETED")); got != 1 {
		t.Errorf("NODE_COMPLETED events = %d, want 1", got)
	}
	if strings.Contains(string(raw), firstToken) {
		t.Error("Node detail echoes a callback token")
	}
}

// waitForScheduledRetry polls the Run snapshot until the NodeRun for nodeID is RUNNING
// with a persisted next attempt time, the committed fact a failed Attempt with a retry
// left behind.
func waitForScheduledRetry(t *testing.T, env *testEnv, runID, nodeID string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, raw := env.doJSON(t, http.MethodGet, "/api/runs/"+runID, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/runs/%s: status %d body %s", runID, resp.StatusCode, raw)
		}
		var snap struct {
			NodeRuns []map[string]any `json:"nodeRuns"`
		}
		if err := json.Unmarshal(raw, &snap); err != nil {
			t.Fatalf("decode snapshot: %v", err)
		}
		for _, nr := range snap.NodeRuns {
			if id, _ := nr["nodeId"].(string); id == nodeID && nr["status"] == "RUNNING" && nr["nextAttemptAt"] != nil {
				return nr
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("node %q in run %s had no scheduled retry within %s", nodeID, runID, timeout)
	return nil
}
