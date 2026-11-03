//go:build integration

// The verify_patch Tool end to end against PostgreSQL: the Runtime refuses a stale patch
// digest before any Attempt exists, dispatches the current patch to a runner stub, turns
// the runner's SUCCEEDED callback into the patch_verified fact and a text log Artifact in
// the resume transaction, applies a re-delivered callback once, and hands a verification
// the runner lost back to the Agent as interrupted work.
//
// They share the agentHarness of agent_loop_test.go and the workspace fixtures of
// agent_workspace_test.go.
package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/verifypatch"
)

const (
	verifyExternalTaskID = "verify_0123456789abcdef"
	verifyPollInterval   = 5 * time.Second
	verifyLogText        = "$ go test -json ./...\nok example.com/tokenservice/tests\n"
)

// verifyRunnerStub accepts verification dispatches and answers status queries. It records
// the dispatch body, the callback token included, as the real runner would before
// delivering its result.
type verifyRunnerStub struct {
	mu         sync.Mutex
	token      string
	dispatches []map[string]any
	statusCode int
	statusBody string
	pollCount  int
}

func newVerifyRunnerStub() *verifyRunnerStub {
	return &verifyRunnerStub{statusCode: http.StatusNotFound, statusBody: `{"error":"unknown verificationId"}`}
}

func (s *verifyRunnerStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/verifications":
		var body map[string]any
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if json.Unmarshal(raw, &body) != nil {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.token, _ = body["callbackToken"].(string)
		s.dispatches = append(s.dispatches, body)
		s.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"verificationId":"`+verifyExternalTaskID+`"}`)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/verifications/"+verifyExternalTaskID:
		s.mu.Lock()
		s.pollCount++
		code, body := s.statusCode, s.statusBody
		s.mu.Unlock()
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	default:
		http.NotFound(w, r)
	}
}

func (s *verifyRunnerStub) dispatched(t *testing.T) (string, map[string]any) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.dispatches) != 1 || s.token == "" {
		t.Fatalf("runner stub received %d dispatches (token present %v), want exactly one with a token", len(s.dispatches), s.token != "")
	}
	return s.token, s.dispatches[0]
}

func (s *verifyRunnerStub) dispatchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.dispatches)
}

// verifyFixture is one Backend with the workspace Tools and verify_patch registered against
// the stub, and the artifact store the Tool writes logs to.
type verifyFixture struct {
	h         *agentHarness
	repo      *workspaceRepo
	stub      *verifyRunnerStub
	artifacts *asset.ArtifactStore
}

func newVerifyFixture(t *testing.T) *verifyFixture {
	t.Helper()
	stub := newVerifyRunnerStub()
	runner := httptest.NewServer(stub)
	t.Cleanup(runner.Close)
	artifacts := asset.NewArtifactStore(t.TempDir(), 1<<20)
	if err := artifacts.VerifyReadWrite(context.Background()); err != nil {
		t.Fatalf("artifact store: %v", err)
	}
	repo := newWorkspaceRepo(t)
	h := newWorkspaceHarness(t, workspaceHarnessOptions{
		repo:       repo,
		extraTools: []registry.ToolRegistration{verifypatch.Registration(runner.URL, runner.Client(), artifacts)},
	})
	return &verifyFixture{h: h, repo: repo, stub: stub, artifacts: artifacts}
}

// start scripts one verify_patch call naming patchDigest (the model answers FINAL
// afterwards), creates the Run and executes the first Turn.
func (f *verifyFixture) start(t *testing.T, workflowID, patchDigest string) (domain.Run, service.AdvanceOutcome) {
	t.Helper()
	tools := append(append([]string{}, wsAllTools...), verifypatch.ToolName)
	run, outcome := startWorkspaceRun(f.h, workspaceDefinition(workflowID, f.repo.commit, true, tools...),
		wsCall(verifypatch.ToolName, map[string]any{"patchDigest": patchDigest}))
	f.h.execute(outcome)
	return run, outcome
}

// baselineDigest is the digest of the empty patch: what the workspace holds before any
// write, and what a verification of the base commit itself names.
func baselineDigest() string { return domain.WorkspacePatchDigest("") }

func verifySucceededPayload(baseCommit, patchDigest, resultDigest string, passed bool) json.RawMessage {
	payload, _ := json.Marshal(map[string]any{
		"status": "SUCCEEDED", "passed": passed, "testsPassed": true, "scanClean": passed, "dependenciesClean": true,
		"baseCommit": baseCommit, "patchDigest": patchDigest, "resultDigest": resultDigest,
		"summary": map[string]any{
			"tests": map[string]int{"total": 9, "failed": 0}, "scanRan": true, "scanner": "govulncheck",
			"scanFindings": []string{}, "vulnerableDependencies": []string{}, "snapshotDigest": "sha256:snapshot",
		},
		"log": verifyLogText, "logTruncated": false,
	})
	return payload
}

// A verify_patch call naming a digest other than the workspace's current patch is handed
// back to the Agent as STALE_PATCH in the claim transaction: no Attempt, no dispatch.
func TestVerifyPatch_StalePatchDigest_ReturnedWithoutDispatch(t *testing.T) {
	f := newVerifyFixture(t)
	stale := domain.WorkspacePatchDigest("a patch the workspace never recorded")
	_, outcome := f.start(t, "wf-verify-stale", stale)
	agentRunID := factAgentRunID(t, f.h, outcome)

	action := wsActionOfTurn(t, f.h, agentRunID, 1)
	if action.Status != domain.AgentActionFailed || action.Error == nil || action.Error.Code != string(domain.ReturnStalePatch) {
		t.Fatalf("action = status %s error %+v, want FAILED with STALE_PATCH", action.Status, action.Error)
	}
	if attempts := agentToolAttempts(f.h.ctx, t, f.h.uow, action.ID); len(attempts) != 0 {
		t.Errorf("tool attempts of the refused call = %+v, want none", attempts)
	}
	if n := f.stub.dispatchCount(); n != 0 {
		t.Errorf("runner received %d dispatches, want none", n)
	}
	message := wsToolMessage(t, f.h, agentRunID, 1, action.ID)
	if message["status"] != "REJECTED" || message["code"] != string(domain.ReturnStalePatch) {
		t.Errorf("tool message = %v, want REJECTED STALE_PATCH", message)
	}
	if text, _ := message["message"].(string); !strings.Contains(text, stale) || !strings.Contains(text, baselineDigest()) {
		t.Errorf("tool message %q should name the stale digest and the current one", text)
	}
	if !agentTurnNoExists(f.h.ctx, t, f.h.uow, agentRunID, 2) || agentTurnNoExists(f.h.ctx, t, f.h.uow, agentRunID, 3) {
		t.Errorf("want exactly one Turn to follow the refusal")
	}
	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Errorf("agent termination = %s, want FINAL_RESPONSE after the Agent answered in the next Turn", agentTermination(agentRun))
	}
	if facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID); len(facts) != 0 {
		t.Errorf("facts = %+v, want none", facts)
	}
}

// dispatchBaseline starts a verification of the base commit and returns what the stub saw.
func dispatchBaseline(t *testing.T, f *verifyFixture, workflowID string) (domain.Run, service.AdvanceOutcome, domain.AgentAction, string) {
	t.Helper()
	run, outcome := f.start(t, workflowID, baselineDigest())
	agentRunID := factAgentRunID(t, f.h, outcome)
	action := wsActionOfTurn(t, f.h, agentRunID, 1)
	if action.Status != domain.AgentActionWaitingCallback {
		t.Fatalf("precondition: action status = %s, want WAITING_CALLBACK", action.Status)
	}
	token, body := f.stub.dispatched(t)
	if body["repositoryId"] != wsRepositoryID || body["baseCommit"] != f.repo.commit || body["patch"] != "" {
		t.Fatalf("dispatch body = %v, want the frozen repository, base commit and the empty patch", body)
	}
	return run, outcome, action, token
}

func TestVerifyPatch_Callback_WritesLogAsTextArtifactAndPatchVerifiedFact(t *testing.T) {
	f := newVerifyFixture(t)
	run, outcome, action, token := dispatchBaseline(t, f, "wf-verify-callback")
	agentRunID := factAgentRunID(t, f.h, outcome)

	const resultDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000001"
	delivered, err := f.h.svc.HandleCallback(f.h.ctx, service.HandleCallback{
		Token: token, ExternalTaskID: verifyExternalTaskID,
		Payload: verifySucceededPayload(f.repo.commit, baselineDigest(), resultDigest, false),
	})
	if err != nil || !delivered.Accepted || delivered.Duplicate {
		t.Fatalf("callback = %+v, %v, want accepted", delivered, err)
	}
	if action = getAgentAction(f.h.ctx, t, f.h.uow, action.ID); action.Status != domain.AgentActionSucceeded {
		t.Fatalf("action status = %s, want SUCCEEDED", action.Status)
	}
	attempt := agentOnlyToolAttempt(t, f.h, action.ID)
	var result struct {
		Passed        bool    `json:"passed"`
		ScanClean     bool    `json:"scanClean"`
		LogArtifactID string  `json:"logArtifactId"`
		Log           *string `json:"log"`
	}
	if err := json.Unmarshal(attempt.Result, &result); err != nil || result.LogArtifactID == "" || result.Log != nil {
		t.Fatalf("attempt result = %s (%v), want a logArtifactId and no log text", attempt.Result, err)
	}
	if result.Passed || result.ScanClean {
		t.Errorf("result = %+v, want passed false with scanClean false", result)
	}

	// The artifact row and its link to the Attempt committed with the result.
	var record store.ArtifactRecord
	if err := f.h.uow.WithinReadTx(f.h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		record, err = tx.Artifacts().GetForRun(ctx, run.ID, result.LogArtifactID)
		return err
	}); err != nil {
		t.Fatalf("artifact %s for run %s: %v", result.LogArtifactID, run.ID, err)
	}
	if record.Artifact.MediaType != domain.ArtifactMediaTypeTextPlain || record.Artifact.SizeBytes != int64(len(verifyLogText)) {
		t.Errorf("artifact = %+v, want %s of %d bytes", record.Artifact, domain.ArtifactMediaTypeTextPlain, len(verifyLogText))
	}
	content, err := f.artifacts.Open(f.h.ctx, record.StorageKey)
	if err != nil {
		t.Fatalf("open artifact content: %v", err)
	}
	stored, _ := io.ReadAll(content)
	_ = content.Close()
	if string(stored) != verifyLogText {
		t.Errorf("stored log = %q, want the runner's log", stored)
	}

	facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID)
	if len(facts) != 1 {
		t.Fatalf("facts = %+v, want exactly one", facts)
	}
	fact := facts[0]
	if fact.FactType != verifypatch.FactType || fact.SubjectRef != baselineDigest() || fact.ToolAttemptID != attempt.ID {
		t.Errorf("fact = %+v, want %s about %s from attempt %s", fact, verifypatch.FactType, baselineDigest(), attempt.ID)
	}
	if fact.Verdict == nil || *fact.Verdict {
		t.Errorf("fact verdict = %v, want false: a vulnerable scan fails the verification", fact.Verdict)
	}
	wantBinding, _ := json.Marshal(map[string]string{"baseCommit": f.repo.commit, "resultDigest": resultDigest})
	assertSameJSON(t, "fact binding", wantBinding, fact.Binding)
	if strings.Contains(string(fact.Binding), verifyLogText[:12]) {
		t.Errorf("fact binding %s carries the log text, want the artifact reference only", fact.Binding)
	}
}

func TestVerifyPatch_DuplicateCallback_AppliedOnce(t *testing.T) {
	f := newVerifyFixture(t)
	run, outcome, action, token := dispatchBaseline(t, f, "wf-verify-duplicate")
	agentRunID := factAgentRunID(t, f.h, outcome)

	payload := verifySucceededPayload(f.repo.commit, baselineDigest(), "sha256:dup", true)
	deliver := func() (service.CallbackOutcome, error) {
		return f.h.svc.HandleCallback(f.h.ctx, service.HandleCallback{Token: token, ExternalTaskID: verifyExternalTaskID, Payload: payload})
	}
	first, err := deliver()
	if err != nil || !first.Accepted || first.Duplicate {
		t.Fatalf("callback = %+v, %v, want accepted", first, err)
	}
	before := agentSnapshot(t, f.h, run, outcome)
	second, err := deliver()
	if err != nil || !second.Accepted || !second.Duplicate {
		t.Fatalf("second callback = %+v, %v, want an accepted duplicate", second, err)
	}
	if after := agentSnapshot(t, f.h, run, outcome); after != before {
		t.Fatalf("state after the duplicate = %+v, want unchanged %+v", after, before)
	}
	if facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID); len(facts) != 1 {
		t.Errorf("facts after the duplicate = %d, want still one", len(facts))
	}
	if n := agentCallbackCompletions(t, listEvents(f.h.ctx, t, f.h.uow, run.ID)); n != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want exactly 1", n)
	}
	if attempts := agentToolAttempts(f.h.ctx, t, f.h.uow, action.ID); len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptSucceeded {
		t.Errorf("attempts = %+v, want one SUCCEEDED Attempt", attempts)
	}
}

// A runner that no longer knows the verification answers the status poll with 404; the
// Tool is SAFE to repeat, so the Runtime hands the call back to the Agent as
// EXECUTION_INTERRUPTED with one new Turn, instead of waiting for a callback that will
// never come.
func TestVerifyPatch_RunnerForgotTask_ReturnedAsInterrupted(t *testing.T) {
	f := newVerifyFixture(t)
	run, outcome, action, _ := dispatchBaseline(t, f, "wf-verify-lost")
	agentRunID := factAgentRunID(t, f.h, outcome)
	attempt := agentOnlyToolAttempt(t, f.h, action.ID)

	f.h.clock.Advance(verifyPollInterval)
	got := pollToolAttempt(t, f.h, attempt)
	if got.Kind != service.PollAttemptReturned || !got.Failed || got.FailureSource != domain.FailureProviderPoll {
		t.Fatalf("poll = %+v, want the lost call handed back to the Agent", got)
	}
	f.stub.mu.Lock()
	polls := f.stub.pollCount
	f.stub.mu.Unlock()
	if polls != 1 {
		t.Errorf("runner status queries = %d, want exactly one", polls)
	}
	attempt = getToolAttempt(t, f.h, attempt.ID)
	if attempt.Status != domain.ToolAttemptFailed || attempt.Error == nil || attempt.Error.Code != "EXECUTION_INTERRUPTED" {
		t.Errorf("attempt = status %s error %+v, want FAILED with EXECUTION_INTERRUPTED", attempt.Status, attempt.Error)
	}
	if attempt.Uncertain == nil || *attempt.Uncertain {
		t.Errorf("attempt uncertain = %v, want false: verifying is SAFE to repeat", attempt.Uncertain)
	}
	if action = getAgentAction(f.h.ctx, t, f.h.uow, action.ID); action.Status != domain.AgentActionFailed || action.Error == nil || action.Error.Code != "EXECUTION_INTERRUPTED" {
		t.Errorf("action = status %s error %+v, want FAILED with EXECUTION_INTERRUPTED", action.Status, action.Error)
	}
	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, outcome.NodeRunID)
	if agentRun.Termination != nil {
		t.Fatalf("agent run terminated as %s, want it to continue", *agentRun.Termination)
	}
	if !agentTurnNoExists(f.h.ctx, t, f.h.uow, agentRunID, 2) || agentTurnNoExists(f.h.ctx, t, f.h.uow, agentRunID, 3) {
		t.Errorf("want exactly one Turn to follow the handback")
	}
	if facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID); len(facts) != 0 {
		t.Errorf("facts = %+v, want none: a lost verification establishes nothing", facts)
	}
	if n := f.stub.dispatchCount(); n != 1 {
		t.Errorf("runner dispatches = %d, want exactly one: the Runtime never re-dispatches on its own", n)
	}
	if persisted := agentRunRow(f.h.ctx, t, f.h.uow, run.ID); persisted.Status != domain.RunRunning {
		t.Errorf("run status = %s, want RUNNING", persisted.Status)
	}
}
