//go:build integration

// Photo effect-template fault stories over the public API: a restart while the Run waits
// for the video Provider's callback, and a definite image Provider failure. Both run the
// same fixture as the main photo-set story against the real Mock Provider.
package contract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/mockprovider"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
	"github.com/leungll/Emberling/backend/internal/tools/generatevideo"
)

// heldPhotoProvider is a Mock Provider with test controls whose callbacks stay gated until
// the test releases them, plus a capture of the callback body the Provider sends, so a
// duplicate delivery can be replayed byte for byte.
type heldPhotoProvider struct {
	url       string
	callbacks *callbackTransport
	body      atomic.Value // []byte
}

func newHeldPhotoProvider(t *testing.T) *heldPhotoProvider {
	t.Helper()
	record, err := mockprovider.OpenRecord(filepath.Join(t.TempDir(), "dispatch.jsonl"))
	if err != nil {
		t.Fatalf("open mock provider record: %v", err)
	}
	held := &heldPhotoProvider{callbacks: newCallbackTransport()}
	held.callbacks.rewriteBody(func(body []byte) []byte {
		held.body.Store(append([]byte(nil), body...))
		return body
	})
	dispatcher := mockprovider.NewDispatcher(&http.Client{Transport: held.callbacks, Timeout: e2eHTTPTimeout})
	server := httptest.NewServer(mockprovider.NewServer(dispatcher, mockprovider.WithTestControls(record)))
	held.url = server.URL
	t.Cleanup(func() {
		server.Close()
		close(held.callbacks.done)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := dispatcher.Shutdown(ctx); err != nil {
			t.Logf("mock provider dispatcher shutdown: %v", err)
		}
		_ = record.Close()
	})
	return held
}

// createPhotoSetRun saves the photo fixture bound to script, uploads three photos and
// starts a Run over them, returning the Run id and the photo asset ids.
func createPhotoSetRun(t *testing.T, env *testEnv, script string) (string, []string) {
	t.Helper()
	workflowID, version := savePhotoSetDefinition(t, env, script, 0)
	photos := make([]any, 0, 3)
	photoIDs := make([]string, 0, 3)
	for _, content := range []string{"photo-one", "photo-two", "photo-three"} {
		ref := env.uploadPNG(t, []byte("\x89PNG\r\n\x1a\n"+content))
		photos = append(photos, ref)
		id, _ := ref["assetId"].(string)
		photoIDs = append(photoIDs, id)
	}
	resp, body := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version,
		"input": map[string]any{"brief": "one warm film look for the whole set", "photos": photos},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs (photo set) status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)
	return runID, photoIDs
}

// TestE2E_PhotoSet_RestartWhileWaitingForVideoCallback_CallbackResumesOriginalActionOnce
// stops the Backend while the generate_video Action waits for the Provider's callback. A
// second Backend over the same database receives the callback and a duplicate of it: the
// original Tool Attempt resumes once, the model-scripted trajectory continues from the
// committed Context, the Run completes with one reviewed example per photo, and no second
// video task, second Attempt or repeated Event appears.
func TestE2E_PhotoSet_RestartWhileWaitingForVideoCallback_CallbackResumesOriginalActionOnce(t *testing.T) {
	provider := newHeldPhotoProvider(t)
	first := newTestEnvWithOptions(t, testEnvOptions{MockTaskBaseURL: provider.url})
	provider.callbacks.setTarget(first.server.URL)

	runID, _ := createPhotoSetRun(t, first, "photo-set")
	waiting := first.waitForNodeRunStatus(t, runID, "node_agent", "WAITING_CALLBACK", photoSetWait)
	agentNodeRunID, _ := waiting["id"].(string)

	status, body := getAgentTrace(t, first, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace while waiting: status = %d, body=%s", status, body)
	}
	before := decodePhotoSetTrace(t, body)
	last := before.Turns[len(before.Turns)-1]
	if last.Action == nil || last.Action.Status != "WAITING_CALLBACK" ||
		len(last.ToolAttempts) != 1 || last.ToolAttempts[0].ToolName != generatevideo.ToolName ||
		last.ToolAttempts[0].CallbackBinding == nil {
		t.Fatalf("last Turn while waiting = %+v, want one generate_video Attempt bound and its Action WAITING_CALLBACK", last)
	}
	videoAttemptID := last.ToolAttempts[0].ID
	externalTaskID := last.ToolAttempts[0].CallbackBinding.ExternalTaskID
	turnsBefore := len(before.Turns)

	// --- restart: same database, new Backend; the Provider's callback was never released.
	pool := first.pool
	first.stop()
	second := newTestEnvWithOptions(t, testEnvOptions{Pool: pool, MockTaskBaseURL: provider.url})
	provider.callbacks.setTarget(second.server.URL)
	provider.callbacks.release()

	delivery := waitPhotoDelivery(t, provider)
	if delivery.StatusCode != http.StatusOK || delivery.ExternalTaskID != externalTaskID {
		t.Fatalf("callback after the restart = status %d task %q, want 200 for task %q; body=%s",
			delivery.StatusCode, delivery.ExternalTaskID, externalTaskID, delivery.Body)
	}
	if outcome := decodeBody[callbackResponseDTO](t, delivery.Body); !outcome.Accepted || outcome.Pending || outcome.Duplicate {
		t.Errorf("callback outcome = %+v, want accepted", outcome)
	}

	snapshot := second.waitForTerminal(t, runID, photoSetWait)
	if got := runStatusOf(snapshot); got != "COMPLETED" {
		t.Fatalf("Run status after the callback = %q, want COMPLETED; snapshot=%v", got, snapshot)
	}

	// The same callback delivered again is idempotently acknowledged and changes nothing.
	raw, _ := provider.body.Load().([]byte)
	resp, dupBody := second.doCallbackRaw(t, delivery.Token, raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("duplicate callback status = %d, want 200, body=%s", resp.StatusCode, dupBody)
	}
	if outcome := decodeBody[callbackResponseDTO](t, dupBody); !outcome.Accepted || !outcome.Duplicate {
		t.Errorf("duplicate callback outcome = %+v, want accepted duplicate", outcome)
	}

	captionText, _ := nodeRunOutputOf(t, snapshot, "node_result")["caption"].(string)
	var caption struct {
		Accepted    bool `json:"accepted"`
		PassedCount int  `json:"passedCount"`
		PhotoCount  int  `json:"photoCount"`
	}
	if err := json.Unmarshal([]byte(captionText), &caption); err != nil {
		t.Fatalf("media_result caption %q is not JSON: %v", captionText, err)
	}
	if !caption.Accepted || caption.PassedCount != 3 || caption.PhotoCount != 3 {
		t.Errorf("media_result caption = %+v, want accepted with one reviewed example for each of 3 photos", caption)
	}

	status, body = getAgentTrace(t, second, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace after completion: status = %d, body=%s", status, body)
	}
	after := decodePhotoSetTrace(t, body)
	videoAttempts := 0
	for _, turn := range after.Turns {
		for _, attempt := range turn.ToolAttempts {
			if attempt.ToolName != generatevideo.ToolName {
				continue
			}
			videoAttempts++
			if attempt.ID != videoAttemptID || attempt.Status != "SUCCEEDED" {
				t.Errorf("video Attempt = %+v, want the original %s SUCCEEDED", attempt, videoAttemptID)
			}
		}
	}
	if videoAttempts != 1 {
		t.Errorf("generate_video Attempts = %d, want exactly 1", videoAttempts)
	}
	if after.GenerationBudget.GenerationCallsUsed != 6 || len(after.Turns) <= turnsBefore {
		t.Errorf("generation calls used = %d over %d Turns (was %d), want 6 and a continued trajectory",
			after.GenerationBudget.GenerationCallsUsed, len(after.Turns), turnsBefore)
	}
	if _, videoTasks := (&photoSetEnv{testEnv: second, providerURL: provider.url}).providerRecord(t); len(videoTasks) != 1 {
		t.Errorf("provider video tasks = %v, want exactly one: the restart must not re-dispatch", videoTasks)
	}

	events := second.listEvents(t, runID)
	assertContiguousSeq(t, events)
	if n := agentAsyncCallbackCompletions(events); n != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want 1", n)
	}
	for _, typ := range []string{"AGENT_ACTION_WAITING", "RUN_PAUSED", "RUN_RESUMED", "RUN_COMPLETED"} {
		n := 0
		for _, ev := range events {
			if got, _ := ev["type"].(string); got == typ {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s events = %d, want exactly 1", typ, n)
		}
	}
	if strings.Contains(string(body), delivery.Token) {
		t.Errorf("agent trace leaks the callback token")
	}
}

func waitPhotoDelivery(t *testing.T, provider *heldPhotoProvider) callbackDelivery {
	t.Helper()
	select {
	case d := <-provider.callbacks.deliveries:
		return d
	case <-time.After(e2eWait):
		t.Fatalf("no callback delivery was recorded within %s", e2eWait)
		return callbackDelivery{}
	}
}

func decodePhotoSetTrace(t *testing.T, body []byte) photoSetFaultTrace {
	t.Helper()
	var trace photoSetFaultTrace
	if err := json.Unmarshal(body, &trace); err != nil {
		t.Fatalf("decode agent trace: %v body=%s", err, body)
	}
	return trace
}

// photoSetFaultTrace is the slice of the Agent Trace the fault stories assert on.
type photoSetFaultTrace struct {
	AgentRun struct {
		Termination *string `json:"termination"`
	} `json:"agentRun"`
	Turns []struct {
		TurnNo int `json:"turnNo"`
		Action *struct {
			Status string `json:"status"`
			Error  *struct {
				Code    string          `json:"code"`
				Message string          `json:"message"`
				Details json.RawMessage `json:"details"`
			} `json:"error"`
		} `json:"action"`
		ToolAttempts []struct {
			ID              string `json:"id"`
			ToolName        string `json:"toolName"`
			Status          string `json:"status"`
			CallbackBinding *struct {
				ExternalTaskID string `json:"externalTaskId"`
			} `json:"callbackBinding"`
		} `json:"toolAttempts"`
	} `json:"turns"`
	GenerationBudget struct {
		GenerationCallsUsed int `json:"generationCallsUsed"`
	} `json:"generationBudget"`
}

// TestE2E_PhotoSet_ImageProviderFailure_FailsAgentAndRunWithoutReachingTheResult scripts
// the first generation with the Mock Provider's fail control and a settings value that
// must never be quoted back. The Provider refuses definitely: the Action ends in a Tool
// error, the Agent and the Run fail, the error names neither arguments nor URLs, the model
// is not asked again, and the downstream media_result Node is never created.
func TestE2E_PhotoSet_ImageProviderFailure_FailsAgentAndRunWithoutReachingTheResult(t *testing.T) {
	const secretSetting = "settings-secret-canary-4417"
	env := newPhotoSetEnv(t)

	var photoID atomic.Value // string, published before the Run is created
	var modelCalls atomic.Int32
	env.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		modelCalls.Add(1)
		id, _ := photoID.Load().(string)
		arguments, _ := json.Marshal(map[string]any{
			"photoAssetId": id,
			"settings":     map[string]any{"mock": "fail", "style": "film", "note": secretSetting},
		})
		return &mockmodel.Scenario{Kind: mockmodel.ScenarioToolCall, ToolName: generateimage.ToolName, ToolArguments: arguments}
	}

	workflowID, version := savePhotoSetDefinition(t, env.testEnv, "photo-set", 0)
	ref := env.uploadPNG(t, []byte("\x89PNG\r\n\x1a\nphoto-one"))
	id, _ := ref["assetId"].(string)
	photoID.Store(id)
	resp, body := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version,
		"input": map[string]any{"brief": "one warm film look for the whole set", "photos": []any{ref}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs status = %d, want %d, body=%s", resp.StatusCode, http.StatusCreated, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)

	snapshot := env.waitForTerminal(t, runID, photoSetWait)
	if got := runStatusOf(snapshot); got != "FAILED" {
		t.Fatalf("Run status = %q, want FAILED; snapshot=%v", got, snapshot)
	}
	if got := nodeRunStatusOf(snapshot, "node_agent"); got != "FAILED" {
		t.Errorf("Agent NodeRun status = %q, want FAILED", got)
	}
	if got := nodeRunStatusOf(snapshot, "node_result"); got != "" {
		t.Errorf("media_result NodeRun status = %q, want no NodeRun at all", got)
	}

	status, traceBody := getAgentTrace(t, env.testEnv, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, traceBody)
	}
	trace := decodePhotoSetTrace(t, traceBody)
	if trace.AgentRun.Termination == nil || *trace.AgentRun.Termination != "TOOL_ERROR" {
		t.Errorf("agentRun.termination = %v, want TOOL_ERROR", trace.AgentRun.Termination)
	}
	if len(trace.Turns) != 1 || trace.Turns[0].Action == nil || trace.Turns[0].Action.Status != "FAILED" {
		t.Fatalf("Turns = %+v, want one Turn whose Action FAILED", trace.Turns)
	}
	if got := modelCalls.Load(); got != 1 {
		t.Errorf("model calls = %d, want 1: a Tool error is not handed back to the model", got)
	}

	events, _ := json.Marshal(env.listEvents(t, runID))
	for label, surface := range map[string][]byte{"agent trace": traceBody, "events": events, "snapshot": mustMarshal(t, snapshot)} {
		for _, forbidden := range []string{secretSetting, "http://", "https://", env.providerURL} {
			if strings.Contains(string(surface), forbidden) {
				t.Errorf("%s quotes %q", label, forbidden)
			}
		}
	}
	if generated, videoTasks := env.providerRecord(t); len(generated) != 0 || len(videoTasks) != 0 {
		t.Errorf("provider record has %d generations and %d video tasks, want a refused generation and none", len(generated), len(videoTasks))
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// nodeRunStatusOf returns the status of nodeID's NodeRun in a Snapshot, or "" when the
// Run has no such NodeRun.
func nodeRunStatusOf(snapshot map[string]any, nodeID string) string {
	nodeRuns, _ := snapshot["nodeRuns"].([]any)
	for _, nr := range nodeRuns {
		m, _ := nr.(map[string]any)
		if id, _ := m["nodeId"].(string); id == nodeID {
			status, _ := m["status"].(string)
			return status
		}
	}
	return ""
}
