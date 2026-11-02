//go:build integration

// Call cost contract: every Node Attempt, Tool Attempt and Agent Turn projects the charge
// its Provider reported as {amount, currency, source} or null, and the Run list item
// carries the exact per-currency total beside the count of external calls whose charge is
// not known. Null and zero are never confused, and no raw Provider usage object, credential
// or numeric amount reaches a response.
package contract

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/fixture/mockprovider"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/reviewasset"
)

// costBody decodes a cost object so an absent key, a null and a value are told apart.
type costBody struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Source   string `json:"source"`
}

type runCostBody struct {
	Totals []struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	} `json:"totals"`
	UnknownCalls int `json:"unknownCalls"`
}

// costOf returns the cost member of one projected call, failing when the key is absent.
func costOf(t *testing.T, call map[string]any, what string) *costBody {
	t.Helper()
	raw, present := call["cost"]
	if !present {
		t.Fatalf("%s has no cost key: %v", what, call)
	}
	if raw == nil {
		return nil
	}
	object, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("%s cost = %v (%T), want an object or null", what, raw, raw)
	}
	for key := range object {
		if key != "amount" && key != "currency" && key != "source" {
			t.Errorf("%s cost carries an unexpected member %q: %v", what, key, object)
		}
	}
	amount, _ := object["amount"].(string)
	if amount == "" {
		t.Errorf("%s cost amount = %v, want decimal text, never a JSON number", what, object["amount"])
	}
	currency, _ := object["currency"].(string)
	source, _ := object["source"].(string)
	return &costBody{Amount: amount, Currency: currency, Source: source}
}

// runListCostOf reads the cost member of runID's Run list item.
func runListCostOf(t *testing.T, env *testEnv, runID string) runCostBody {
	t.Helper()
	page := env.listRuns(t, url.Values{"code": {runID}})
	items, _ := page["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("GET /api/runs?code=%s items = %d, want 1", runID, len(items))
	}
	item, _ := items[0].(map[string]any)
	raw, err := json.Marshal(item["cost"])
	if err != nil || string(raw) == "null" {
		t.Fatalf("run list item cost = %v, want an object: %v", item["cost"], err)
	}
	var cost runCostBody
	if err := json.Unmarshal(raw, &cost); err != nil {
		t.Fatalf("decode run list item cost %s: %v", raw, err)
	}
	if cost.Totals == nil {
		t.Fatalf("run list item cost totals = null, want an array: %s", raw)
	}
	return cost
}

// rawNumericCost matches a Provider-style usage member with a numeric amount, the form a
// response must never carry: amounts travel as text, and raw Provider payloads stay out.
var rawNumericCost = regexp.MustCompile(`"cost"\s*:\s*[0-9]`)

func assertNoRawProviderUsage(t *testing.T, what string, body []byte) {
	t.Helper()
	text := string(body)
	if rawNumericCost.MatchString(text) {
		t.Errorf("%s carries a numeric cost amount: %s", what, text)
	}
	if strings.Contains(text, `"usage":{"cost"`) {
		t.Errorf("%s carries the Provider's raw usage object: %s", what, text)
	}
	lower := strings.ToLower(withoutTokenUsageFieldNames(text))
	for _, secret := range []string{"authorization", "bearer", testCallbackSigningSecret, "token"} {
		if strings.Contains(lower, strings.ToLower(secret)) {
			t.Errorf("%s mentions %q: %s", what, secret, text)
		}
	}
}

// newCostedProviderEnv starts the Mock Provider with test controls, which makes every
// successful task result report a fixed charge, and a Backend wired to it.
func newCostedProviderEnv(t *testing.T) *testEnv {
	t.Helper()
	record, err := mockprovider.OpenRecord(filepath.Join(t.TempDir(), "dispatch.jsonl"))
	if err != nil {
		t.Fatalf("open mock provider record: %v", err)
	}
	callbacks := newCallbackTransport()
	callbacks.release()
	dispatcher := mockprovider.NewDispatcher(&http.Client{Transport: callbacks, Timeout: e2eHTTPTimeout})
	provider := httptest.NewServer(mockprovider.NewServer(dispatcher, mockprovider.WithTestControls(record)))
	t.Cleanup(func() {
		provider.Close()
		close(callbacks.done)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := dispatcher.Shutdown(ctx); err != nil {
			t.Logf("mock provider dispatcher shutdown: %v", err)
		}
		_ = record.Close()
	})
	env := newTestEnvWithOptions(t, testEnvOptions{MockTaskBaseURL: provider.URL})
	callbacks.setTarget(env.server.URL)
	return env
}

// TestRunCost_ProviderReportsCharge_AttemptAndRunListCarryIt is the positive control: the
// image Attempt records the Provider's charge attributed to the image Model ID, the
// caption Attempt records the mock model's known zero, and the Run list total is their
// exact sum with no unknown call. The input and template nodes never leave the process,
// so they carry null without being unknown calls.
func TestRunCost_ProviderReportsCharge_AttemptAndRunListCarryIt(t *testing.T) {
	env := newCostedProviderEnv(t)
	runID := env.createAIGCRun(t, "a small ember creature")
	snapshot := env.waitForTerminal(t, runID, e2eWait)
	run, _ := snapshot["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("AIGC Run status = %q, want COMPLETED: %v", status, snapshot)
	}

	image := env.nodeRunOf(t, runID, "image")
	imageNodeRunID, _ := image["id"].(string)
	detail, body := env.nodeRunDetail(t, runID, imageNodeRunID)
	attempts := attemptsOf(t, detail)
	if len(attempts) != 1 {
		t.Fatalf("image Attempts = %d, want 1: %s", len(attempts), body)
	}
	got := costOf(t, attempts[0], "image Attempt")
	want := costBody{Amount: "0.04", Currency: "USD", Source: mockmodel.ImageModelID}
	if got == nil || *got != want {
		t.Errorf("image Attempt cost = %+v, want %+v", got, want)
	}
	assertNoRawProviderUsage(t, "image NodeRun detail", body)

	caption := env.nodeRunOf(t, runID, "caption")
	captionNodeRunID, _ := caption["id"].(string)
	captionDetail, captionBody := env.nodeRunDetail(t, runID, captionNodeRunID)
	for _, attempt := range attemptsOf(t, captionDetail) {
		textCost := costOf(t, attempt, "caption Attempt")
		if textCost == nil || textCost.Amount != "0" || textCost.Source != mockmodel.ModelID {
			t.Errorf("caption Attempt cost = %+v, want the mock model's known zero attributed to %s: %s", textCost, mockmodel.ModelID, captionBody)
		}
	}
	for _, nodeID := range []string{"brief", "prompt"} {
		nr := env.nodeRunOf(t, runID, nodeID)
		nrID, _ := nr["id"].(string)
		localDetail, _ := env.nodeRunDetail(t, runID, nrID)
		for _, attempt := range attemptsOf(t, localDetail) {
			if got := costOf(t, attempt, nodeID+" Attempt"); got != nil {
				t.Errorf("%s Attempt cost = %+v, want null: the node called no Provider", nodeID, *got)
			}
		}
	}

	cost := runListCostOf(t, env, runID)
	if len(cost.Totals) != 1 || cost.Totals[0].Amount != "0.04" || cost.Totals[0].Currency != "USD" || cost.UnknownCalls != 0 {
		t.Errorf("run list cost = %+v, want one USD total of 0.04 and no unknown call", cost)
	}
}

// TestRunCost_MockProviderWithoutTestControls_RunListCarriesCharge: the fixed charge is
// part of the Mock Provider itself, so a Backend wired to one started without test
// controls, as a plain developer setup is, still records the image task's charge and the
// Runs list shows a known total with no unpriced call.
func TestRunCost_MockProviderWithoutTestControls_RunListCarriesCharge(t *testing.T) {
	callbacks := newCallbackTransport()
	callbacks.release()
	dispatcher := mockprovider.NewDispatcher(&http.Client{Transport: callbacks, Timeout: e2eHTTPTimeout})
	provider := httptest.NewServer(mockprovider.NewServer(dispatcher))
	t.Cleanup(func() {
		provider.Close()
		close(callbacks.done)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := dispatcher.Shutdown(ctx); err != nil {
			t.Logf("mock provider dispatcher shutdown: %v", err)
		}
	})
	env := newTestEnvWithOptions(t, testEnvOptions{MockTaskBaseURL: provider.URL})
	callbacks.setTarget(env.server.URL)

	runID := env.createAIGCRun(t, "a small ember creature")
	snapshot := env.waitForTerminal(t, runID, e2eWait)
	if status, _ := snapshot["run"].(map[string]any)["status"].(string); status != "COMPLETED" {
		t.Fatalf("AIGC Run status = %q, want COMPLETED: %v", status, snapshot)
	}
	image := env.nodeRunOf(t, runID, "image")
	imageNodeRunID, _ := image["id"].(string)
	detail, body := env.nodeRunDetail(t, runID, imageNodeRunID)
	attempts := attemptsOf(t, detail)
	if len(attempts) != 1 {
		t.Fatalf("image Attempts = %d, want 1: %s", len(attempts), body)
	}
	want := costBody{Amount: "0.04", Currency: "USD", Source: mockmodel.ImageModelID}
	if got := costOf(t, attempts[0], "image Attempt"); got == nil || *got != want {
		t.Errorf("image Attempt cost = %+v, want %+v without test controls", got, want)
	}
	cost := runListCostOf(t, env, runID)
	if len(cost.Totals) != 1 || cost.Totals[0].Amount != "0.04" || cost.Totals[0].Currency != "USD" || cost.UnknownCalls != 0 {
		t.Errorf("run list cost = %+v, want one USD total of 0.04 and no unknown call", cost)
	}
}

// TestRunCost_ProviderReportsNoCharge_AttemptNullAndRunCountsItUnknown: a Provider that
// returns the image without any usage leaves the Attempt's cost null, never zero, and the
// Run list counts that one external call as unknown while still summing the caption
// call's known zero. The Mock Provider prices every task result, so the unpriced callback
// is produced by dropping its usage member in transit, the way an unpriced Provider would
// have delivered it.
func TestRunCost_ProviderReportsNoCharge_AttemptNullAndRunCountsItUnknown(t *testing.T) {
	fx := newProviderFixture(t)
	fx.callbacks.rewriteBody(func(body []byte) []byte {
		var envelope struct {
			ExternalTaskID string                     `json:"externalTaskId"`
			Payload        map[string]json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Errorf("callback body %s is not an envelope: %v", body, err)
			return body
		}
		if _, priced := envelope.Payload["usage"]; !priced {
			t.Errorf("callback payload %s carries no usage: the test must strip a charge the Provider reported", body)
		}
		delete(envelope.Payload, "usage")
		rewritten, err := json.Marshal(envelope)
		if err != nil {
			t.Errorf("encode unpriced callback: %v", err)
			return body
		}
		return rewritten
	})
	fx.callbacks.release()
	env := fx.startBackend(t, nil)
	runID := env.createAIGCRun(t, "a quiet harbour")
	snapshot := env.waitForTerminal(t, runID, e2eWait)
	run, _ := snapshot["run"].(map[string]any)
	if status, _ := run["status"].(string); status != "COMPLETED" {
		t.Fatalf("AIGC Run status = %q, want COMPLETED: %v", status, snapshot)
	}

	image := env.nodeRunOf(t, runID, "image")
	imageNodeRunID, _ := image["id"].(string)
	detail, body := env.nodeRunDetail(t, runID, imageNodeRunID)
	attempts := attemptsOf(t, detail)
	if len(attempts) != 1 {
		t.Fatalf("image Attempts = %d, want 1: %s", len(attempts), body)
	}
	if got := costOf(t, attempts[0], "image Attempt"); got != nil {
		t.Errorf("image Attempt cost = %+v, want null when the Provider reported no usage", *got)
	}

	cost := runListCostOf(t, env, runID)
	if len(cost.Totals) != 1 || cost.Totals[0].Amount != "0" || cost.Totals[0].Currency != "USD" {
		t.Errorf("run list cost totals = %+v, want the caption call's known zero in USD", cost.Totals)
	}
	if cost.UnknownCalls != 1 {
		t.Errorf("run list unknownCalls = %d, want 1 for the image call without a reported charge", cost.UnknownCalls)
	}
}

// TestRunCost_UncertainAttempt_NullCostCountedUnknown: an Attempt whose dispatch response
// was lost failed with an uncertain outcome. Whether the Provider charged is not known, so
// its cost is null and the Run list counts it as unknown beside the retry that completed
// without a reported charge. Nothing is estimated into a total.
func TestRunCost_UncertainAttempt_NullCostCountedUnknown(t *testing.T) {
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

	nodeRun := waitForScheduledRetry(t, env, runID, "async", 5*time.Second)
	nodeRunID, _ := nodeRun["id"].(string)
	firstToken := provider.firstToken()
	if firstToken == "" {
		t.Fatal("no credential was handed to the first dispatch")
	}
	clock.advance(time.Minute)
	rec := reconciler.New(reconciler.Config{UoW: env.uow, Executor: env.execution, Clock: clock, BatchLimit: 100})
	if report, err := rec.RunOnce(context.Background()); err != nil || len(report.Errors) != 0 {
		t.Fatalf("reconciler pass: err=%v errors=%v", err, report.Errors)
	}
	env.waitForNodeRunStatus(t, runID, "async", "WAITING_CALLBACK", 5*time.Second)
	resp, body = env.doCallback(t, firstToken, takeoverExternalTaskID, map[string]any{"status": "SUCCEEDED", "output": "original task"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/callbacks status = %d, want 200, body=%s", resp.StatusCode, body)
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
	if uncertain, _ := attempts[0]["uncertain"].(bool); !uncertain {
		t.Fatalf("Attempt 1 uncertain = %v, want true", attempts[0]["uncertain"])
	}
	if got := costOf(t, attempts[0], "uncertain Attempt"); got != nil {
		t.Errorf("uncertain Attempt cost = %+v, want null: whether the Provider charged is not known", *got)
	}
	if got := costOf(t, attempts[1], "retry Attempt"); got != nil {
		t.Errorf("retry Attempt cost = %+v, want null: this Provider reports no usage", *got)
	}
	assertNoRawProviderUsage(t, "NodeRun detail", raw)

	cost := runListCostOf(t, env, runID)
	if len(cost.Totals) != 0 {
		t.Errorf("run list cost totals = %+v, want none: no call reported a charge", cost.Totals)
	}
	if cost.UnknownCalls != 2 {
		t.Errorf("run list unknownCalls = %d, want 2: the uncertain Attempt and the retry without a charge", cost.UnknownCalls)
	}
}

// TestRunCost_AgentLoop_TurnsCarryCostAndRunSumsAcrossTurns: each Turn of a completed
// two-round loop projects the mock model's known zero attributed to the decision Model
// ID, the lookup Tool call (no side effect) carries null without becoming an unknown
// call, and the Run list sums the Turns to one known USD total.
func TestRunCost_AgentLoop_TurnsCarryCostAndRunSumsAcrossTurns(t *testing.T) {
	env := newTestEnv(t)
	runID, snapshot := runCompletedAgentLoop(t, env)
	agentNodeRunID := nodeRunIDOf(t, snapshot, "node_agent")
	status, body := getAgentTrace(t, env, runID, agentNodeRunID)
	if status != http.StatusOK {
		t.Fatalf("GET agent trace status = %d, body=%s", status, body)
	}
	trace := decodeBody[map[string]any](t, body)
	turns, _ := trace["turns"].([]any)
	if len(turns) != 2 {
		t.Fatalf("agent trace has %d turns, want 2: %s", len(turns), body)
	}
	for i, raw := range turns {
		turn, _ := raw.(map[string]any)
		got := costOf(t, turn, "turn")
		want := costBody{Amount: "0", Currency: "USD", Source: mockmodel.ModelID}
		if got == nil || *got != want {
			t.Errorf("turns[%d].cost = %+v, want %+v", i, got, want)
		}
	}
	first, _ := turns[0].(map[string]any)
	toolAttempts, _ := first["toolAttempts"].([]any)
	if len(toolAttempts) != 1 {
		t.Fatalf("turns[0].toolAttempts = %d, want 1", len(toolAttempts))
	}
	attempt, _ := toolAttempts[0].(map[string]any)
	if got := costOf(t, attempt, "lookup Tool Attempt"); got != nil {
		t.Errorf("lookup Tool Attempt cost = %+v, want null: the Tool reports no charge", *got)
	}
	assertNoRawProviderUsage(t, "agent trace", body)

	cost := runListCostOf(t, env, runID)
	if len(cost.Totals) != 1 || cost.Totals[0].Amount != "0" || cost.Totals[0].Currency != "USD" || cost.UnknownCalls != 0 {
		t.Errorf("run list cost = %+v, want one known USD total of 0 and no unknown call: a NONE Tool call is not an external call", cost)
	}
}

// answerPricedVerdict replies with a FINAL verdict and the given Chat Completions usage
// member, the way a Provider that accounts for the call does.
func answerPricedVerdict(verdict, usage string) func(http.ResponseWriter, *http.Request, []byte) {
	return func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		content, _ := json.Marshal(`{"kind":"FINAL","tool":null,"arguments":null,"output":` + verdict + `,"statePatch":null}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-review","choices":[{"message":{"content":`+string(content)+`},"finish_reason":"stop"}],"usage":`+usage+`}`)
	}
}

// reviewToolAttemptOf returns the one review_asset Tool Attempt of a review story's Trace.
func reviewToolAttemptOf(t *testing.T, run reviewRun) map[string]any {
	t.Helper()
	trace := decodeBody[map[string]any](t, run.rawTrace)
	turns, _ := trace["turns"].([]any)
	var reviews []map[string]any
	for _, raw := range turns {
		turn, _ := raw.(map[string]any)
		attempts, _ := turn["toolAttempts"].([]any)
		for _, rawAttempt := range attempts {
			attempt, _ := rawAttempt.(map[string]any)
			if name, _ := attempt["toolName"].(string); name == reviewasset.ToolName {
				reviews = append(reviews, attempt)
			}
		}
	}
	if len(reviews) != 1 {
		t.Fatalf("review Tool Attempts = %d, want 1: %s", len(reviews), run.rawTrace)
	}
	return reviews[0]
}

// TestRunCost_ReviewModelReportsCharge_ToolAttemptAndRunListCarryIt: a review with a real
// review model is an external call like a generation. The charge the model reports is
// recorded on the review Tool Attempt, attributed to the frozen review Model ID, and the
// Run list total is the generation's charge plus the review's, with no unknown call.
func TestRunCost_ReviewModelReportsCharge_ToolAttemptAndRunListCarryIt(t *testing.T) {
	upstream := newVisionUpstream(t, answerPricedVerdict(`{"passed":true,"reasons":[]}`,
		`{"prompt_tokens":1200,"completion_tokens":30,"total_tokens":1230,"cost":0.0068}`))
	env := newPhotoSetEnv(t, withReviewModel(t, upstream, e2eHTTPTimeout))
	run := runReviewStory(t, env)
	if got := runStatusOf(run.snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; trace=%s", got, run.rawTrace)
	}

	got := costOf(t, reviewToolAttemptOf(t, run), "review Tool Attempt")
	want := costBody{Amount: "0.0068", Currency: "USD", Source: contractReviewModelID}
	if got == nil || *got != want {
		t.Errorf("review Tool Attempt cost = %+v, want %+v", got, want)
	}
	assertNoRawProviderUsage(t, "agent trace", run.rawTrace)

	cost := runListCostOf(t, env.testEnv, run.runID)
	if len(cost.Totals) != 1 || cost.Totals[0].Amount != "0.0268" || cost.Totals[0].Currency != "USD" || cost.UnknownCalls != 0 {
		t.Errorf("run list cost = %+v, want one USD total of 0.0268 (0.02 generation + 0.0068 review) and no unknown call", cost)
	}
}

// TestRunCost_ReviewModelReportsNoCharge_ToolAttemptNullAndRunCountsItUnknown: a review
// model that reports no charge leaves the review Tool Attempt's cost null, never zero, and
// the Run list counts that review as an unknown external call while still summing the
// generation's known charge.
func TestRunCost_ReviewModelReportsNoCharge_ToolAttemptNullAndRunCountsItUnknown(t *testing.T) {
	upstream := newVisionUpstream(t, answerVerdict(`{"passed":true,"reasons":[]}`))
	env := newPhotoSetEnv(t, withReviewModel(t, upstream, e2eHTTPTimeout))
	run := runReviewStory(t, env)
	if got := runStatusOf(run.snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED; trace=%s", got, run.rawTrace)
	}

	if got := costOf(t, reviewToolAttemptOf(t, run), "review Tool Attempt"); got != nil {
		t.Errorf("review Tool Attempt cost = %+v, want null when the review model reported no usage", *got)
	}

	cost := runListCostOf(t, env.testEnv, run.runID)
	if len(cost.Totals) != 1 || cost.Totals[0].Amount != "0.02" || cost.Totals[0].Currency != "USD" {
		t.Errorf("run list cost totals = %+v, want the generation's known 0.02 in USD", cost.Totals)
	}
	if cost.UnknownCalls != 1 {
		t.Errorf("run list unknownCalls = %d, want 1 for the review without a reported charge", cost.UnknownCalls)
	}
}
