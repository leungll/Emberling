//go:build integration

// Software delivery stories over the public API: an Agent tests a patch through the
// sandbox runner's asynchronous callback, then deploys it once through the mock
// production service. Both external services run in-process with their test controls, so
// a test can hold a request at their barrier and compare the Runtime's facts with what
// each service recorded.
package contract

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/mockcontrol"
	"github.com/leungll/Emberling/backend/internal/mockproduction"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/sandboxrunner"
	"github.com/leungll/Emberling/backend/internal/tools/deploy"
	"github.com/leungll/Emberling/backend/internal/tools/sandboxtest"
)

// deliveryPatch is the patch every story submits; only its digest is ever compared.
const deliveryPatch = "--- a/greet.go\n+++ b/greet.go\n@@ -1 +1 @@\n-hello\n+hello, world\n"

// deliveryBaseCommit, deliveryService and deliveryEnvironment complete the task.
const (
	deliveryBaseCommit  = "fixture-v1"
	deliveryService     = "checkout"
	deliveryEnvironment = "staging"
)

// deliveryRunner is one in-process sandbox runner with its record and redelivery store.
// Stopping it and starting another over the same record path is a runner restart.
type deliveryRunner struct {
	server   *sandboxrunner.Server
	http     *httptest.Server
	record   *mockcontrol.Record
	store    *sandboxrunner.RedeliveryStore
	stopOnce sync.Once
}

func (r *deliveryRunner) stop(t *testing.T) {
	r.stopOnce.Do(func() {
		r.server.StopHolding()
		r.http.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := r.server.Shutdown(ctx); err != nil {
			t.Logf("sandbox runner shutdown: %v", err)
		}
		_ = r.record.Close()
		_ = r.store.Close()
	})
}

// deliveryFixture is the sandbox runner and the mock production service one story runs
// against. Both outlive an Emberling restart: a real external service does not restart
// because Emberling did.
type deliveryFixture struct {
	callbacks      *callbackTransport
	runnerRecord   string
	runner         *deliveryRunner
	production     *httptest.Server
	productionHeld chan string
}

func newDeliveryFixture(t *testing.T) *deliveryFixture {
	t.Helper()
	f := &deliveryFixture{
		callbacks:      newCallbackTransport(),
		runnerRecord:   filepath.Join(t.TempDir(), "runner.jsonl"),
		productionHeld: make(chan string, 4),
	}
	// Holding is done at the runner's own barrier, never at this transport, so a story
	// observes the hold exactly as the demo does.
	f.callbacks.release()
	t.Cleanup(func() { close(f.callbacks.done) })

	f.startRunner(t)
	t.Cleanup(func() { f.runner.stop(t) })

	record, err := mockproduction.OpenRecord(filepath.Join(t.TempDir(), "production.jsonl"))
	if err != nil {
		t.Fatalf("open mock production record: %v", err)
	}
	production := mockproduction.NewServer(mockproduction.WithTestControls(record), mockproduction.WithHeldNotify(f.productionHeld))
	f.production = httptest.NewServer(production)
	t.Cleanup(func() {
		production.StopHolding()
		f.production.Close()
		_ = record.Close()
	})
	return f
}

// startRunner starts a sandbox runner over the fixture's record path and delivers, once
// more, every result an earlier runner on that path stored.
func (f *deliveryFixture) startRunner(t *testing.T) {
	t.Helper()
	record, err := mockcontrol.OpenRecord(f.runnerRecord)
	if err != nil {
		t.Fatalf("open sandbox runner record: %v", err)
	}
	store, stored, err := sandboxrunner.OpenRedeliveryStore(sandboxrunner.RedeliveryPath(f.runnerRecord))
	if err != nil {
		t.Fatalf("open sandbox runner redelivery store: %v", err)
	}
	server := sandboxrunner.NewServer(sandboxrunner.NewMockBackend(),
		sandboxrunner.WithHTTPClient(&http.Client{Transport: f.callbacks, Timeout: e2eHTTPTimeout}),
		sandboxrunner.WithTestControls(record, store))
	f.runner = &deliveryRunner{server: server, http: httptest.NewServer(server), record: record, store: store}
	server.Redeliver(stored)
}

// startBackend wires one Emberling Backend against this fixture; pool == nil provisions a
// fresh database, a previous Backend's pool restarts over the same facts.
func (f *deliveryFixture) startBackend(t *testing.T, opts testEnvOptions) *testEnv {
	t.Helper()
	opts.ExtraToolRegistrations = []registry.ToolRegistration{
		sandboxtest.Registration(f.runner.http.URL, nil),
		deploy.Registration(f.production.URL, nil),
	}
	env := newTestEnvWithOptions(t, opts)
	f.callbacks.setTarget(env.server.URL)
	return env
}

// control POSTs one barrier control request to a service's /control routes.
func control(t *testing.T, baseURL, action, body string) {
	t.Helper()
	resp, err := (&http.Client{Timeout: e2eHTTPTimeout}).Post(baseURL+"/control/"+action, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /control/%s: %v", action, err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /control/%s status = %d, body=%s", action, resp.StatusCode, raw)
	}
}

// serviceRecord reads a service's whole request record, one decoded object per line.
func serviceRecord(t *testing.T, baseURL string) []map[string]any {
	t.Helper()
	resp, err := (&http.Client{Timeout: e2eHTTPTimeout}).Get(baseURL + "/control/record?after=0")
	if err != nil {
		t.Fatalf("GET /control/record: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /control/record status = %d, body=%s", resp.StatusCode, body)
	}
	var lines []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("decode record line %q: %v", scanner.Text(), err)
		}
		lines = append(lines, line)
	}
	return lines
}

// recordLines returns the lines of one event and kind.
func recordLines(lines []map[string]any, event, kind string) []map[string]any {
	var out []map[string]any
	for _, line := range lines {
		if line["event"] == event && line["kind"] == kind {
			out = append(out, line)
		}
	}
	return out
}

// waitRecordLine waits until a service's record holds a line of event and kind that
// matches, and returns it. The record is the service's own observation; polling it is a
// condition wait, not a sleep standing in for one.
func waitRecordLine(t *testing.T, baseURL, event, kind string, match func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(e2eWait)
	for time.Now().Before(deadline) {
		for _, line := range recordLines(serviceRecord(t, baseURL), event, kind) {
			if match == nil || match(line) {
				return line
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("record at %s has no %s %s line within %s", baseURL, event, kind, e2eWait)
	return nil
}

// waitRunnerHeld waits until the runner's barrier holds a request of kind.
func waitRunnerHeld(t *testing.T, f *deliveryFixture, kind string) {
	t.Helper()
	deadline := time.Now().Add(e2eWait)
	for time.Now().Before(deadline) {
		resp, err := (&http.Client{Timeout: e2eHTTPTimeout}).Get(f.runner.http.URL + "/control/barrier")
		if err != nil {
			t.Fatalf("GET /control/barrier: %v", err)
		}
		var state struct {
			Held []struct {
				Kind string `json:"kind"`
			} `json:"held"`
		}
		err = json.NewDecoder(resp.Body).Decode(&state)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("decode barrier state: %v", err)
		}
		for _, held := range state.Held {
			if held.Kind == kind {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the sandbox runner held no %s request within %s", kind, e2eWait)
}

// nextDelivery returns the next callback delivery the runner completed.
func (f *deliveryFixture) nextDelivery(t *testing.T) callbackDelivery {
	t.Helper()
	select {
	case d := <-f.callbacks.deliveries:
		return d
	case <-time.After(e2eWait):
		t.Fatalf("no sandbox runner callback delivery was recorded within %s", e2eWait)
		return callbackDelivery{}
	}
}

// saveDeliveryDefinition saves the software delivery fixture with its Agent bound to
// script and returns the workflow id and version.
func saveDeliveryDefinition(t *testing.T, env *testEnv, script string) (string, int) {
	t.Helper()
	raw, err := os.ReadFile("../fixtures/definitions/software_delivery.json")
	if err != nil {
		t.Fatalf("read software_delivery.json: %v", err)
	}
	var fx documentProcessingFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("unmarshal software_delivery.json: %v", err)
	}
	for i, node := range fx.Nodes {
		if node.ID != "node_agent" {
			continue
		}
		var config map[string]any
		if err := json.Unmarshal(node.Config, &config); err != nil {
			t.Fatalf("decode node_agent config: %v", err)
		}
		config["modelConfig"] = map[string]any{"script": script}
		if fx.Nodes[i].Config, err = json.Marshal(config); err != nil {
			t.Fatalf("encode node_agent config: %v", err)
		}
	}
	resp, body := env.doJSON(t, http.MethodPost, "/api/definitions", map[string]any{
		"name": fx.Name, "description": fx.Description, "nodes": fx.Nodes, "edges": fx.Edges,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/definitions (software delivery) status = %d, body=%s", resp.StatusCode, body)
	}
	created := decodeBody[map[string]any](t, body)
	workflowID, _ := created["workflowId"].(string)
	version, _ := created["version"].(float64)
	return workflowID, int(version)
}

// createDeliveryRun saves the fixture bound to script and starts a Run over the task.
func createDeliveryRun(t *testing.T, env *testEnv, script string) string {
	t.Helper()
	workflowID, version := saveDeliveryDefinition(t, env, script)
	task := string(mustMarshal(t, map[string]any{
		"baseCommit": deliveryBaseCommit,
		"patch":      deliveryPatch,
		"target":     map[string]any{"service": deliveryService, "environment": deliveryEnvironment},
	}))
	resp, body := env.doJSON(t, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflowID, "definitionVersion": version, "input": map[string]any{"task": task},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/runs (software delivery) status = %d, body=%s", resp.StatusCode, body)
	}
	runID, _ := decodeBody[map[string]any](t, body)["id"].(string)
	return runID
}

// runHeldUntilWaiting starts a delivery Run with the runner's callback held at its barrier
// until the Agent waits on the committed Callback Binding, then releases it. The mock
// runner finishes a test at once, so without the hold its callback could arrive before
// the Binding commits and be answered as an early, pending callback instead.
func runHeldUntilWaiting(t *testing.T, f *deliveryFixture, env *testEnv, script string) string {
	t.Helper()
	control(t, f.runner.http.URL, "pause", `{"kinds":["callback"]}`)
	runID := createDeliveryRun(t, env, script)
	env.waitForNodeRunStatus(t, runID, "node_agent", "WAITING_CALLBACK", e2eWait)
	waitRunnerHeld(t, f, "callback")
	control(t, f.runner.http.URL, "release", "")
	return runID
}

// deliveryTrace is the slice of the Agent Trace the delivery stories assert on.
type deliveryTrace struct {
	AgentRun struct {
		Termination *string `json:"termination"`
	} `json:"agentRun"`
	Turns []struct {
		TurnNo int `json:"turnNo"`
		Action *struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Status string `json:"status"`
			Error  *struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"action"`
		ToolAttempts []deliveryToolAttempt `json:"toolAttempts"`
	} `json:"turns"`
	Facts struct {
		Items []struct {
			FactType      string          `json:"factType"`
			Subject       string          `json:"subject"`
			Bindings      json.RawMessage `json:"bindings"`
			Verdict       *bool           `json:"verdict"`
			ToolAttemptID string          `json:"toolAttemptId"`
		} `json:"items"`
	} `json:"facts"`
}

type deliveryToolAttempt struct {
	ID              string `json:"id"`
	ToolName        string `json:"toolName"`
	AttemptNo       int    `json:"attemptNo"`
	Status          string `json:"status"`
	CallbackBinding *struct {
		ProviderID     string `json:"providerId"`
		ExternalTaskID string `json:"externalTaskId"`
	} `json:"callbackBinding"`
}

// toolCall is one Tool Attempt with the Action that made it.
type toolCall struct {
	actionID     string
	actionStatus string
	actionError  string
	attempt      deliveryToolAttempt
}

// callsOf returns every Tool Attempt of toolName in Turn order.
func (tr deliveryTrace) callsOf(toolName string) []toolCall {
	var out []toolCall
	for _, turn := range tr.Turns {
		if turn.Action == nil {
			continue
		}
		for _, attempt := range turn.ToolAttempts {
			if attempt.ToolName != toolName {
				continue
			}
			call := toolCall{actionID: turn.Action.ID, actionStatus: turn.Action.Status, attempt: attempt}
			if turn.Action.Error != nil {
				call.actionError = turn.Action.Error.Code
			}
			out = append(out, call)
		}
	}
	return out
}

func loadDeliveryTrace(t *testing.T, env *testEnv, runID, agentNodeRunID string) deliveryTrace {
	t.Helper()
	status, body := getAgentTrace(t, env, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, body)
	}
	return decodeBody[deliveryTrace](t, body)
}

// deliveryResult is the FINAL document the Agent hands to the result Node.
type deliveryResult struct {
	OperationID string `json:"operationId"`
	Version     string `json:"version"`
	PatchDigest string `json:"patchDigest"`
}

// completedDelivery is what the happy path leaves behind, for the stories that continue
// from it.
type completedDelivery struct {
	runID          string
	testID         string
	testAttemptID  string
	operationID    string
	agentNodeRunID string
}

// assertDeliveredOnce checks a COMPLETED delivery Run against the Agent Trace and both
// service records: one test, one CALLBACK completion, one deployment whose operationId
// is derived from the deploy Action and Attempt, and a result naming exactly that.
func assertDeliveredOnce(t *testing.T, env *testEnv, f *deliveryFixture, runID string, snapshot map[string]any) completedDelivery {
	t.Helper()
	if got := runStatusOf(snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; snapshot=%v", got, snapshot)
	}
	agentNodeRunID := nodeRunIDOf(t, snapshot, "node_agent")
	trace := loadDeliveryTrace(t, env, runID, agentNodeRunID)

	tests := trace.callsOf(sandboxtest.ToolName)
	if len(tests) != 1 {
		t.Fatalf("sandbox_test Attempts = %+v, want exactly 1", tests)
	}
	test := tests[0]
	if test.attempt.Status != "SUCCEEDED" || test.actionStatus != "SUCCEEDED" || test.attempt.CallbackBinding == nil ||
		test.attempt.CallbackBinding.ProviderID != sandboxtest.ProviderID {
		t.Fatalf("sandbox_test call = %+v, want a SUCCEEDED Attempt bound to the sandbox runner", test)
	}
	testID := test.attempt.CallbackBinding.ExternalTaskID

	deploys := trace.callsOf(deploy.ToolName)
	if len(deploys) != 1 || deploys[0].attempt.Status != "SUCCEEDED" || deploys[0].attempt.AttemptNo != 1 {
		t.Fatalf("deploy Attempts = %+v, want exactly one SUCCEEDED first Attempt", deploys)
	}
	operationID := deploy.OperationID(deploys[0].actionID, 1)

	patchDigest := sandboxrunner.PatchDigest(deliveryPatch)
	text, _ := nodeRunOutputOf(t, snapshot, "node_result")["text"].(string)
	var result deliveryResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("delivery result %q is not JSON: %v", text, err)
	}
	if result != (deliveryResult{OperationID: operationID, Version: "v1", PatchDigest: patchDigest}) {
		t.Errorf("delivery result = %+v, want operation %s at v1 for %s", result, operationID, patchDigest)
	}

	if len(trace.Facts.Items) != 1 {
		t.Fatalf("facts = %+v, want exactly one patch_tested fact", trace.Facts.Items)
	}
	fact := trace.Facts.Items[0]
	var bindings struct {
		BaseCommit   string `json:"baseCommit"`
		ResultDigest string `json:"resultDigest"`
	}
	_ = json.Unmarshal(fact.Bindings, &bindings)
	if fact.FactType != sandboxtest.FactType || fact.Subject != patchDigest || fact.Verdict == nil || !*fact.Verdict ||
		fact.ToolAttemptID != test.attempt.ID || bindings.BaseCommit != deliveryBaseCommit ||
		bindings.ResultDigest != sandboxrunner.ResultDigest(deliveryBaseCommit, patchDigest, true, 12, 0) {
		t.Errorf("fact = %+v bindings %s, want a passing patch_tested fact for %s on %s", fact, fact.Bindings, patchDigest, deliveryBaseCommit)
	}

	// The sandbox runner ran exactly the bound test.
	runnerLines := serviceRecord(t, f.runner.http.URL)
	started := recordLines(runnerLines, "started", "test")
	if len(started) != 1 || started[0]["testId"] != testID {
		t.Errorf("runner started lines = %v, want one for test %s", started, testID)
	}
	completed := recordLines(runnerLines, "completed", "test")
	if len(completed) != 1 || completed[0]["patchDigest"] != patchDigest || completed[0]["passed"] != true {
		t.Errorf("runner completed lines = %v, want one passing test of %s", completed, patchDigest)
	}

	// Production deployed exactly the operation the ledger derives, once.
	productionLines := serviceRecord(t, f.production.URL)
	deployed := recordLines(productionLines, "deployed", "deploy")
	if len(deployed) != 1 {
		t.Fatalf("production deployed lines = %v, want exactly 1", deployed)
	}
	want := map[string]any{
		"operationId": operationID, "service": deliveryService, "environment": deliveryEnvironment,
		"baseCommit": deliveryBaseCommit, "patchDigest": patchDigest, "version": "v1", "replayed": false,
	}
	for key, value := range want {
		if deployed[0][key] != value {
			t.Errorf("production deployed %s = %v, want %v", key, deployed[0][key], value)
		}
	}
	if arrived := recordLines(productionLines, "arrived", "deploy"); len(arrived) != 1 {
		t.Errorf("production deploy arrivals = %d, want exactly 1", len(arrived))
	}

	events := env.listEvents(t, runID)
	assertContiguousSeq(t, events)
	if n := agentAsyncCallbackCompletions(events); n != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want 1", n)
	}
	return completedDelivery{
		runID: runID, testID: testID, testAttemptID: test.attempt.ID,
		operationID: operationID, agentNodeRunID: agentNodeRunID,
	}
}

// TestE2E_SoftwareDelivery_HappyPath_TestsThenDeploysOnce runs the scripted delivery: the
// patch is tested through the runner's callback, then deployed once. The Run completes
// with a result naming the deployment, and the Agent Trace, the sandbox runner record and
// the production record agree on one test and one deployment.
func TestE2E_SoftwareDelivery_HappyPath_TestsThenDeploysOnce(t *testing.T) {
	f := newDeliveryFixture(t)
	env := f.startBackend(t, testEnvOptions{})

	runID := runHeldUntilWaiting(t, f, env, "software-delivery")
	snapshot := env.waitForTerminal(t, runID, e2eWait)
	delivered := assertDeliveredOnce(t, env, f, runID, snapshot)

	delivery := f.nextDelivery(t)
	if delivery.StatusCode != http.StatusOK || delivery.ExternalTaskID != delivered.testID {
		t.Fatalf("callback = status %d test %q, want 200 for %q; body=%s",
			delivery.StatusCode, delivery.ExternalTaskID, delivered.testID, delivery.Body)
	}
	if outcome := decodeBody[callbackResponseDTO](t, delivery.Body); !outcome.Accepted || outcome.Duplicate {
		t.Errorf("callback outcome = %+v, want accepted and not a duplicate", outcome)
	}
	callbacks := recordLines(serviceRecord(t, f.runner.http.URL), "callback", "callback")
	if len(callbacks) != 1 || callbacks[0]["httpStatus"] != float64(http.StatusOK) || callbacks[0]["attempt"] != float64(1) {
		t.Errorf("runner callback lines = %v, want one first delivery answered 200", callbacks)
	}
}

// TestE2E_SoftwareDelivery_RunnerRedeliversCallback_BackendAnswersDuplicateWithoutNewEvent
// completes a delivery, then restarts the sandbox runner, which delivers its stored result
// once more. The Backend answers the redelivery as a duplicate of the consumed Binding and
// writes nothing: the Event log and the Run's terminal status are unchanged.
func TestE2E_SoftwareDelivery_RunnerRedeliversCallback_BackendAnswersDuplicateWithoutNewEvent(t *testing.T) {
	f := newDeliveryFixture(t)
	env := f.startBackend(t, testEnvOptions{})

	runID := runHeldUntilWaiting(t, f, env, "software-delivery")
	snapshot := env.waitForTerminal(t, runID, e2eWait)
	delivered := assertDeliveredOnce(t, env, f, runID, snapshot)
	if first := f.nextDelivery(t); first.StatusCode != http.StatusOK {
		t.Fatalf("first callback status = %d, want 200; body=%s", first.StatusCode, first.Body)
	}
	eventsBefore := env.listEvents(t, runID)

	// --- runner restart: same record path, so the new runner finds the stored result.
	f.runner.stop(t)
	f.startRunner(t)

	redelivery := f.nextDelivery(t)
	if redelivery.StatusCode != http.StatusOK || redelivery.ExternalTaskID != delivered.testID {
		t.Fatalf("redelivered callback = status %d test %q, want 200 for %q; body=%s",
			redelivery.StatusCode, redelivery.ExternalTaskID, delivered.testID, redelivery.Body)
	}
	if outcome := decodeBody[callbackResponseDTO](t, redelivery.Body); !outcome.Accepted || !outcome.Duplicate || outcome.Pending {
		t.Errorf("redelivered callback outcome = %+v, want an accepted duplicate", outcome)
	}
	waitRecordLine(t, f.runner.http.URL, "callback", "callback", func(line map[string]any) bool {
		return line["testId"] == delivered.testID && line["attempt"] == float64(2) && line["httpStatus"] == float64(http.StatusOK)
	})
	if redelivered := recordLines(serviceRecord(t, f.runner.http.URL), "redelivered", "callback"); len(redelivered) != 1 ||
		redelivered[0]["testId"] != delivered.testID {
		t.Errorf("runner redelivered lines = %v, want one for test %s", redelivered, delivered.testID)
	}

	eventsAfter := env.listEvents(t, runID)
	if len(eventsAfter) != len(eventsBefore) {
		t.Errorf("events after the redelivery = %d, want the %d before it: a duplicate writes nothing",
			len(eventsAfter), len(eventsBefore))
	}
	if got := runStatusOf(env.runSnapshot(t, runID)); got != "COMPLETED" {
		t.Errorf("Run status after the redelivery = %q, want COMPLETED", got)
	}
	trace := loadDeliveryTrace(t, env, runID, delivered.agentNodeRunID)
	if tests := trace.callsOf(sandboxtest.ToolName); len(tests) != 1 || tests[0].attempt.ID != delivered.testAttemptID {
		t.Errorf("sandbox_test Attempts after the redelivery = %+v, want only %s", tests, delivered.testAttemptID)
	}
	if started := recordLines(serviceRecord(t, f.runner.http.URL), "started", "test"); len(started) != 1 {
		t.Errorf("runner started lines = %v, want 1: a redelivery runs no new test", started)
	}
}
