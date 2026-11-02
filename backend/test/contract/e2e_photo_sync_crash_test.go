//go:build integration

// Photo effect-template story for a synchronous generation interrupted by a Backend
// restart, the counterpart of the restart while a video waits for its callback. The
// generation request reaches the Mock Provider, which generates the image, but its answer
// is held until the Backend stops, so no process ever records the outcome. After the
// restart the Reconciler hands the orphaned call back to the Agent as RESULT_UNCERTAIN;
// the scripted Agent decides to generate again and finishes the set.
package contract

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
	"github.com/leungll/Emberling/backend/internal/tools/reviewasset"
)

// syncCrashInFlightWait bounds how long the test waits for the held call to start; it is
// a safety net, never a synchronization device.
const syncCrashInFlightWait = 20 * time.Second

// heldGenerationTransport lets the first synchronous image generation reach the Mock
// Provider and be answered there, then withholds that answer from the Backend until the
// calling context ends -- the Backend process stopping. Every other request passes
// through unchanged.
type heldGenerationTransport struct {
	base     http.RoundTripper
	once     sync.Once
	inFlight chan struct{}
}

func newHeldGenerationTransport() *heldGenerationTransport {
	return &heldGenerationTransport{base: http.DefaultTransport, inFlight: make(chan struct{})}
}

func (h *heldGenerationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	hold := false
	if req.Method == http.MethodPost && req.URL.Path == "/v1/assets" {
		h.once.Do(func() { hold = true })
	}
	if !hold {
		return h.base.RoundTrip(req)
	}
	resp, err := h.base.RoundTrip(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	close(h.inFlight)
	<-req.Context().Done()
	return nil, req.Context().Err()
}

// syncCrashTrace is the part of the Agent Trace the sync-crash story asserts on.
type syncCrashTrace struct {
	AgentRun struct {
		Termination *string `json:"termination"`
	} `json:"agentRun"`
	Turns []struct {
		TurnNo   int `json:"turnNo"`
		Decision *struct {
			Kind string  `json:"kind"`
			Tool *string `json:"tool"`
		} `json:"decision"`
		Action *struct {
			Status        string  `json:"status"`
			FailureSource *string `json:"failureSource"`
			Error         *struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"action"`
		ToolAttempts []struct {
			ID       string `json:"id"`
			ToolName string `json:"toolName"`
			Status   string `json:"status"`
			Error    *struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"toolAttempts"`
	} `json:"turns"`
	Facts struct {
		Items []struct {
			FactType string            `json:"factType"`
			Subject  string            `json:"subject"`
			Bindings map[string]string `json:"bindings"`
		} `json:"items"`
	} `json:"facts"`
	GenerationBudget struct {
		MaxGenerationCalls  *int `json:"maxGenerationCalls"`
		GenerationCallsUsed int  `json:"generationCallsUsed"`
	} `json:"generationBudget"`
}

func (tr syncCrashTrace) factSubjects(factType string) []string {
	var subjects []string
	for _, fact := range tr.Facts.Items {
		if fact.FactType == factType {
			subjects = append(subjects, fact.Subject)
		}
	}
	slices.Sort(subjects)
	return subjects
}

// toolStatusMessages returns the tool messages of every model request that carry a
// top-level status, decoded, in request order.
func toolStatusMessages(requests []registry.ModelRequest) []map[string]any {
	var out []map[string]any
	seen := map[string]bool{}
	for _, request := range requests {
		for _, message := range request.Messages {
			if message.Role != "tool" || seen[string(message.Content)] {
				continue
			}
			var content map[string]any
			if json.Unmarshal(message.Content, &content) != nil {
				continue
			}
			if _, ok := content["status"].(string); ok {
				seen[string(message.Content)] = true
				out = append(out, content)
			}
		}
	}
	return out
}

// runOrphanScan runs one Reconciler pass as the restarted Backend and requires it to hand
// exactly one orphaned Tool Attempt to the orphan use case.
func runOrphanScan(t *testing.T, env *testEnv, runnerID string) {
	t.Helper()
	rec := reconciler.New(reconciler.Config{UoW: env.uow, Executor: env.execution, BatchLimit: 100, RunnerID: runnerID})
	report, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("reconciler RunOnce: %v", err)
	}
	if len(report.Errors) != 0 || report.OrphanToolAttemptsFound != 1 || report.OrphanToolAttemptsHandled != 1 {
		t.Fatalf("reconciler report = %+v, want one orphaned Tool Attempt found and handed to the Agent", report)
	}
}

// TestE2E_PhotoAgent_SyncGenerationCrash_ResultUncertainThenAgentContinues stops the
// Backend while the first generate_image call is in flight: the Mock Provider has
// generated the image but the Backend never receives the answer. A second Backend over
// the same database and volume runs the Reconciler, which hands the orphaned call back to
// the Agent as RESULT_UNCERTAIN with the generation count. The scripted Agent generates
// that photo again and finishes the set: the Run completes, nothing was resent
// automatically, the uncertain call stays counted, and the Provider record shows every
// generation it performed, the uncertain one included.
func TestE2E_PhotoAgent_SyncGenerationCrash_ResultUncertainThenAgentContinues(t *testing.T) {
	const runnerBefore, runnerAfter = "runner-before-sync-crash", "runner-after-sync-crash"
	held := newHeldGenerationTransport()
	first := newPhotoSetEnv(t, func(_ string, opts *testEnvOptions) {
		opts.TaskClient = &http.Client{Transport: held}
		opts.RunnerID = runnerBefore
	})

	runID, photoIDs := createPhotoSetRun(t, first.testEnv, "photo-set-sync-crash")
	select {
	case <-held.inFlight:
	case <-time.After(syncCrashInFlightWait):
		t.Fatal("the first generation never reached the Mock Provider")
	}
	// The process stops while the call is in flight: nothing commits its outcome.
	first.stop()

	var startedAttemptID string
	if err := first.pool.QueryRow(context.Background(),
		`SELECT id FROM tool_attempts WHERE tool_name = $1 AND status = 'STARTED'`, generateimage.ToolName).Scan(&startedAttemptID); err != nil {
		t.Fatalf("read the STARTED generate_image Attempt left by the stopped Backend: %v", err)
	}
	var factsBefore int
	if err := first.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM execution_facts WHERE run_id = $1`, runID).Scan(&factsBefore); err != nil || factsBefore != 0 {
		t.Fatalf("facts after the crash = %d (err %v), want none: the uncertain call has no result", factsBefore, err)
	}

	second := &photoSetEnv{providerURL: first.providerURL, testEnv: newTestEnvWithOptions(t, testEnvOptions{
		Pool: first.pool, AssetStorageRoot: first.assetRoot, MockTaskBaseURL: first.providerURL, RunnerID: runnerAfter,
	})}
	runOrphanScan(t, second.testEnv, runnerAfter)

	snapshot := second.waitForTerminal(t, runID, photoSetWait)
	if got := runStatusOf(snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED after the Agent continued; snapshot=%v", got, snapshot)
	}
	captionText, _ := nodeRunOutputOf(t, snapshot, "node_result")["caption"].(string)
	var caption struct {
		Accepted    bool `json:"accepted"`
		PassedCount int  `json:"passedCount"`
		PhotoCount  int  `json:"photoCount"`
	}
	if err := json.Unmarshal([]byte(captionText), &caption); err != nil || !caption.Accepted || caption.PassedCount != 3 || caption.PhotoCount != 3 {
		t.Errorf("media_result caption = %q (err %v), want accepted with one reviewed example for each of 3 photos", captionText, err)
	}

	status, traceBody := getAgentTrace(t, second.testEnv, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, traceBody)
	}
	trace := decodeBody[syncCrashTrace](t, traceBody)

	// The orphaned round, then the Agent's own decision to generate the same photo again.
	if len(trace.Turns) != 8 {
		t.Fatalf("Turns = %d, want the 7 scripted Turns plus the inserted regeneration: %s", len(trace.Turns), traceBody)
	}
	uncertain, regenerated := trace.Turns[0], trace.Turns[1]
	if uncertain.Action == nil || uncertain.Action.Status != "FAILED" || uncertain.Action.FailureSource == nil ||
		*uncertain.Action.FailureSource != "RUNNER_LOST" || uncertain.Action.Error == nil || uncertain.Action.Error.Code != "RESULT_UNCERTAIN" {
		t.Errorf("Turn 1 action = %+v, want FAILED with RUNNER_LOST and RESULT_UNCERTAIN", uncertain.Action)
	}
	if len(uncertain.ToolAttempts) != 1 || uncertain.ToolAttempts[0].ID != startedAttemptID || uncertain.ToolAttempts[0].Status != "FAILED" ||
		uncertain.ToolAttempts[0].Error == nil || uncertain.ToolAttempts[0].Error.Code != "RESULT_UNCERTAIN" {
		t.Errorf("Turn 1 attempts = %+v, want the orphaned Attempt %s FAILED with RESULT_UNCERTAIN", uncertain.ToolAttempts, startedAttemptID)
	}
	if regenerated.Decision == nil || regenerated.Decision.Tool == nil || *regenerated.Decision.Tool != generateimage.ToolName ||
		regenerated.Action == nil || regenerated.Action.Status != "SUCCEEDED" {
		t.Errorf("Turn 2 = decision %+v action %+v, want the Agent's new generate_image call SUCCEEDED", regenerated.Decision, regenerated.Action)
	}
	if trace.AgentRun.Termination == nil || *trace.AgentRun.Termination != "FINAL_RESPONSE" {
		t.Errorf("agentRun.termination = %v, want FINAL_RESPONSE", trace.AgentRun.Termination)
	}

	// The restarted Agent was told the outcome is unknown and that the call is counted.
	notices := toolStatusMessages(second.provider.Requests())
	if len(notices) != 1 || notices[0]["status"] != "RESULT_UNCERTAIN" {
		t.Fatalf("tool status messages the Agent received = %v, want one RESULT_UNCERTAIN", notices)
	}
	calls, _ := notices[0]["generationCalls"].(map[string]any)
	if calls["used"] != float64(1) || calls["limit"] != float64(12) {
		t.Errorf("generationCalls in the notice = %v, want 1 of 12 used", calls)
	}
	if message, _ := notices[0]["message"].(string); !strings.Contains(message, "not retried automatically") {
		t.Errorf("notice message = %q, want it to say the call was not retried", message)
	}

	// The uncertain call stays counted; facts exist only for the generations that returned.
	generatedLedger := trace.factSubjects(generateimage.FactType)
	if len(generatedLedger) != 3 || len(trace.factSubjects(reviewasset.FactType)) != 3 {
		t.Errorf("facts = %d image_generated and %d asset_reviewed, want 3 and 3",
			len(generatedLedger), len(trace.factSubjects(reviewasset.FactType)))
	}
	if trace.GenerationBudget.GenerationCallsUsed != 4 {
		t.Errorf("generation calls used = %d, want 4: the three photos plus the uncertain call", trace.GenerationBudget.GenerationCallsUsed)
	}

	// The Provider performed every generation the budget counts, the uncertain one
	// included, and each of them once: the restart resent nothing.
	generatedRecord, videoTasks := second.providerRecord(t)
	if len(generatedRecord) != trace.GenerationBudget.GenerationCallsUsed || len(videoTasks) != 0 {
		t.Errorf("provider record = %d generations and %d video tasks, want %d generations and none",
			len(generatedRecord), len(videoTasks), trace.GenerationBudget.GenerationCallsUsed)
	}
	if !slices.Equal(slices.Compact(slices.Sorted(slices.Values(generatedRecord))), generatedLedger) {
		t.Errorf("provider generations %v and ledger %v name different images", generatedRecord, generatedLedger)
	}
	if !slices.Contains(generatedLedger, generatedRecord[0]) {
		t.Errorf("the uncertain generation %s is not the image the Agent generated again for %s", generatedRecord[0], photoIDs[0])
	}

	events := second.listEvents(t, runID)
	assertContiguousSeq(t, events)
	var after []string
	for _, ev := range events {
		typ, _ := ev["type"].(string)
		if typ == "AGENT_ACTION_FAILED" {
			payload, _ := ev["payload"].(map[string]any)
			if payload["failureSource"] != "RUNNER_LOST" {
				t.Errorf("AGENT_ACTION_FAILED payload = %v, want failureSource RUNNER_LOST", payload)
			}
		}
		switch {
		case typ == "AGENT_ACTION_FAILED" && len(after) == 0:
			after = append(after, typ)
		case len(after) > 0 && len(after) < 3 && (typ == "AGENT_TURN_READY" || typ == "AGENT_DECISION_COMMITTED"):
			after = append(after, typ)
		}
	}
	if want := []string{"AGENT_ACTION_FAILED", "AGENT_TURN_READY", "AGENT_DECISION_COMMITTED"}; !slices.Equal(after, want) {
		t.Errorf("events from the failed Action = %v, want %v", after, want)
	}
}
