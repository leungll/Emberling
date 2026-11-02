//go:build integration

// Retry after a lost dispatch response takes over the original external task. The
// Provider accepted Attempt 1's task but the answer never arrived, so Attempt 1 failed as
// uncertain without a Callback Binding. Attempt 2 presents the same idempotency key (the
// NodeRun id), the Provider replays the original task id, and Attempt 2 binds it. The
// original task's callback still carries Attempt 1's credential; for an EXTERNAL+KEYED
// node that credential is accepted for a later Attempt of the same NodeRun, on the live
// callback path, on the early (pending) path and on the Reconciler's pending replay. A
// credential of another NodeRun is still refused, and an expired credential still fails
// verification, leaving polling to finish the Attempt. These are transaction, race and
// recovery guarantees, so they run against real PostgreSQL on the async harness.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mocktask"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/fixture/mockprovider"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
)

var takeoverKeyedSide = domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed}

var takeoverPolicy = domain.ExecutionPolicy{TimeoutMs: 60_000, MaxAttempts: 3, Backoff: domain.BackoffFixed}

const takeoverTaskID = "provider-task-original"

// asyncLostResponseError is a dispatch whose request reached the Provider and whose answer
// was lost: the task may exist, so the failure is uncertain.
type asyncLostResponseError struct{}

func (asyncLostResponseError) Error() string {
	return "provider response lost after the request was sent"
}
func (asyncLostResponseError) Uncertain() bool { return true }

// takeoverFirstAttempt is Attempt 1 after its response was lost: its plaintext credential
// is what the Provider's original task will call back with.
type takeoverFirstAttempt struct {
	run       domain.Run
	nodeRunID string
	attemptID string
	token     string
}

// loseFirstDispatchResponse claims Attempt 1 against a Provider that honours idempotency
// keys, lets the Provider accept the task and loses the answer. The failure is committed
// as uncertain and, because the node is EXTERNAL+KEYED, a retry is scheduled.
func (h *asyncHarness) loseFirstDispatchResponse() takeoverFirstAttempt {
	h.t.Helper()
	h.exec.mu.Lock()
	h.exec.replayKeyed = true
	h.exec.loseNextResponse = true
	h.exec.externalTaskID = takeoverTaskID
	h.exec.mu.Unlock()

	run := h.createRun()
	first := h.claimAsyncNode(run)
	if err := h.svc.Execute(h.ctx, first); err != nil {
		h.t.Fatalf("execute the dispatch whose response is lost: %v", err)
	}
	attempt := getAttempt(h.ctx, h.t, h.uow, first.AttemptID)
	if attempt.Status != domain.NodeAttemptFailed || attempt.Uncertain == nil || !*attempt.Uncertain {
		h.t.Fatalf("Attempt 1 after a lost response: want FAILED and uncertain, got %s / %v", attempt.Status, attempt.Uncertain)
	}
	nodeRun := getNodeRun(h.ctx, h.t, h.uow, first.NodeRunID)
	if nodeRun.Status != domain.NodeRunRunning || nodeRun.NextAttemptAt == nil {
		h.t.Fatalf("NodeRun after a lost keyed response: want RUNNING with a scheduled retry, got %s / %v", nodeRun.Status, nodeRun.NextAttemptAt)
	}
	return takeoverFirstAttempt{run: run, nodeRunID: first.NodeRunID, attemptID: first.AttemptID, token: first.Input.Callback.Token}
}

// dispatchRetry claims and executes the retry Attempt once its backoff has elapsed. The
// Provider replays the original task id.
func (h *asyncHarness) dispatchRetry(first takeoverFirstAttempt) asyncDispatch {
	h.t.Helper()
	outcome := h.claimAsyncNode(first.run)
	if outcome.NodeRunID != first.nodeRunID || outcome.AttemptNo != 2 {
		h.t.Fatalf("retry claim: want Attempt 2 of NodeRun %s, got Attempt %d of %s", first.nodeRunID, outcome.AttemptNo, outcome.NodeRunID)
	}
	token := outcome.Input.Callback.Token
	if err := h.svc.Execute(h.ctx, outcome); err != nil {
		h.t.Fatalf("execute the retry dispatch: %v", err)
	}
	return asyncDispatch{run: first.run, nodeRunID: outcome.NodeRunID, attemptID: outcome.AttemptID, token: token, externalTaskID: takeoverTaskID}
}

func takeoverBinding(ctx context.Context, t *testing.T, uow store.UnitOfWork, externalTaskID string) domain.CallbackBinding {
	t.Helper()
	var binding domain.CallbackBinding
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		binding, err = tx.CallbackBindings().GetByExternalTaskID(ctx, externalTaskID)
		return err
	}); err != nil {
		t.Fatalf("read callback binding %s: %v", externalTaskID, err)
	}
	return binding
}

func takeoverDispatchedReplayed(t *testing.T, events []domain.Event, nodeRunID string) []bool {
	t.Helper()
	var out []bool
	for _, ev := range asyncNodeEventsOfType(events, nodeRunID, domain.EventNodeDispatched) {
		var payload struct {
			Replayed *bool `json:"replayed"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("decode NODE_DISPATCHED payload: %v", err)
		}
		if payload.Replayed == nil {
			t.Fatalf("NODE_DISPATCHED payload carries no replayed member: %s", ev.Payload)
		}
		out = append(out, *payload.Replayed)
	}
	return out
}

func takeoverCallbackAttemptNos(t *testing.T, events []domain.Event, nodeRunID string) []int {
	t.Helper()
	var out []int
	for _, ev := range asyncNodeEventsOfType(events, nodeRunID, domain.EventNodeCallbackReceived) {
		var payload struct {
			AttemptNo int `json:"attemptNo"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("decode NODE_CALLBACK_RECEIVED payload: %v", err)
		}
		out = append(out, payload.AttemptNo)
	}
	return out
}

// assertRetryCompletedOnce checks the facts every takeover leaves: Attempt 1 preserved as
// FAILED, Attempt 2 SUCCEEDED through the original task's Binding, exactly one completion,
// and a retry dispatch recorded as a replay of an accepted task.
func assertRetryCompletedOnce(t *testing.T, h *asyncHarness, first takeoverFirstAttempt, second asyncDispatch, source domain.CompletionSource) {
	t.Helper()
	if got := getAttempt(h.ctx, t, h.uow, first.attemptID).Status; got != domain.NodeAttemptFailed {
		t.Fatalf("Attempt 1 after the takeover: want preserved as FAILED, got %s", got)
	}
	if got := getAttempt(h.ctx, t, h.uow, second.attemptID).Status; got != domain.NodeAttemptSucceeded {
		t.Fatalf("Attempt 2 after the takeover: want SUCCEEDED, got %s", got)
	}
	if got := getNodeRun(h.ctx, t, h.uow, second.nodeRunID).Status; got != domain.NodeRunSucceeded {
		t.Fatalf("NodeRun after the takeover: want SUCCEEDED, got %s", got)
	}
	if binding := takeoverBinding(h.ctx, t, h.uow, takeoverTaskID); binding.TargetID != second.attemptID {
		t.Fatalf("Binding of the original task: want Attempt 2 %s, got %s", second.attemptID, binding.TargetID)
	}
	if got := h.exec.dispatchCount(); got != 1 {
		t.Fatalf("external tasks the Provider created: want 1, got %d", got)
	}

	events := listEvents(h.ctx, t, h.uow, first.run.ID)
	if got := takeoverDispatchedReplayed(t, events, second.nodeRunID); len(got) != 1 || !got[0] {
		t.Fatalf("NODE_DISPATCHED replayed flags: want [true] for the retry only, got %v", got)
	}
	completed := asyncNodeEventsOfType(events, second.nodeRunID, domain.EventNodeCompleted)
	if len(completed) != 1 {
		t.Fatalf("NODE_COMPLETED for the NodeRun: want exactly 1, got %v", execEventTypes(events))
	}
	if got := decodeCompletionSource(t, completed[0]); got != string(source) {
		t.Fatalf("NODE_COMPLETED completionSource: want %s, got %s", source, got)
	}
	callbacks := takeoverCallbackAttemptNos(t, events, second.nodeRunID)
	if source == domain.CompletionCallback {
		if len(callbacks) != 1 || callbacks[0] != 2 {
			t.Fatalf("NODE_CALLBACK_RECEIVED attempt numbers: want [2], got %v", callbacks)
		}
	} else if len(callbacks) != 0 {
		t.Fatalf("NODE_CALLBACK_RECEIVED after a poll completion: want none, got %v", callbacks)
	}
}

// TestCallbackTakeover_ResponseLostEarlyPendingCallback_CompletesRetryAttemptOnce: the
// original task's callback arrives before any Binding exists and is stored as pending
// under Attempt 1's credential. The retry binds the replayed task id, and the pending
// delivery completes Attempt 2 exactly once.
func TestCallbackTakeover_ResponseLostEarlyPendingCallback_CompletesRetryAttemptOnce(t *testing.T) {
	h := newAsyncHarness(t, takeoverKeyedSide, takeoverPolicy)
	first := h.loseFirstDispatchResponse()

	early, err := h.handleCallback(first.token, takeoverTaskID, `{"text":"original task result"}`)
	if err != nil {
		t.Fatalf("early callback of the original task: %v", err)
	}
	if !early.Pending || early.Accepted {
		t.Fatalf("early callback outcome: want Pending, got %+v", early)
	}

	h.clock.Advance(time.Minute)
	second := h.dispatchRetry(first)

	if got := asyncPendingStatus(h.ctx, t, h.uow, takeoverTaskID); got != "consumed" {
		t.Fatalf("pending callback after the retry bound the original task: want consumed, got %s", got)
	}
	assertRetryCompletedOnce(t, h, first, second, domain.CompletionCallback)

	// A Reconciler-style replay of the same stored delivery finds it consumed and writes
	// nothing.
	before := listEvents(h.ctx, t, h.uow, first.run.ID)
	pending := getPending(h.ctx, t, h.uow, takeoverTaskID)
	outcome, err := h.svc.ResumeNode(h.ctx, service.ResumeNode{
		ExternalTaskID: takeoverTaskID,
		Payload:        pending.Payload,
		Source:         domain.CompletionCallback,
		ConsumePending: true,
		PayloadHash:    pending.PayloadHash,
	})
	if err != nil {
		t.Fatalf("replay of the consumed pending callback: %v", err)
	}
	if !outcome.Duplicate {
		t.Fatalf("replay of the consumed pending callback: want Duplicate, got %+v", outcome)
	}
	if after := listEvents(h.ctx, t, h.uow, first.run.ID); len(after) != len(before) {
		t.Fatalf("events after replaying a consumed pending callback: want %d, got %d", len(before), len(after))
	}
}

// TestCallbackTakeover_LiveCallbackWithFirstAttemptToken_CompletesRetryAttemptOnce: the
// original task's callback arrives after the retry bound it, still carrying Attempt 1's
// credential, and completes Attempt 2 exactly once.
func TestCallbackTakeover_LiveCallbackWithFirstAttemptToken_CompletesRetryAttemptOnce(t *testing.T) {
	h := newAsyncHarness(t, takeoverKeyedSide, takeoverPolicy)
	first := h.loseFirstDispatchResponse()
	h.clock.Advance(time.Minute)
	second := h.dispatchRetry(first)

	outcome, err := h.handleCallback(first.token, takeoverTaskID, `{"text":"original task result"}`)
	if err != nil {
		t.Fatalf("live callback with Attempt 1's credential: %v", err)
	}
	if !outcome.Accepted || outcome.Duplicate || outcome.NodeRunID != second.nodeRunID {
		t.Fatalf("live callback outcome: want accepted for NodeRun %s, got %+v", second.nodeRunID, outcome)
	}
	if got := asyncPendingStatus(h.ctx, t, h.uow, takeoverTaskID); got != "absent" {
		t.Fatalf("pending callback after a routed live callback: want none, got %s", got)
	}
	assertRetryCompletedOnce(t, h, first, second, domain.CompletionCallback)
}

// TestCallbackTakeover_DuplicateDelivery_AppendsNoEvent: once Attempt 2 is complete, a
// repeated delivery with Attempt 1's credential and one with Attempt 2's own credential are
// duplicates that consume no Event seq.
func TestCallbackTakeover_DuplicateDelivery_AppendsNoEvent(t *testing.T) {
	h := newAsyncHarness(t, takeoverKeyedSide, takeoverPolicy)
	first := h.loseFirstDispatchResponse()
	h.clock.Advance(time.Minute)
	second := h.dispatchRetry(first)

	if _, err := h.handleCallback(first.token, takeoverTaskID, `{"text":"original task result"}`); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	before := listEvents(h.ctx, t, h.uow, first.run.ID)
	beforeSeq := getRun(h.ctx, t, h.uow, first.run.ID).LastSeq

	for _, token := range []string{first.token, second.token} {
		outcome, err := h.handleCallback(token, takeoverTaskID, `{"text":"original task result"}`)
		if err != nil {
			t.Fatalf("duplicate delivery: %v", err)
		}
		if !outcome.Duplicate {
			t.Fatalf("duplicate delivery outcome: want Duplicate, got %+v", outcome)
		}
	}

	after := listEvents(h.ctx, t, h.uow, first.run.ID)
	if len(after) != len(before) {
		t.Fatalf("events after duplicate deliveries: want %d, got %d (%v)", len(before), len(after), execEventTypes(after))
	}
	if got := getRun(h.ctx, t, h.uow, first.run.ID).LastSeq; got != beforeSeq {
		t.Fatalf("Run lastSeq after duplicate deliveries: want %d, got %d", beforeSeq, got)
	}
	assertRetryCompletedOnce(t, h, first, second, domain.CompletionCallback)
}

// TestCallbackTakeover_TokenAimedAtOtherNodeRunBinding_Rejected: Attempt 1's credential is
// accepted only for its own NodeRun. Presented live at another NodeRun's Binding it is
// refused before anything is written; stored early under another NodeRun's task id, it is
// refused when that NodeRun's dispatch tries to consume it.
func TestCallbackTakeover_TokenAimedAtOtherNodeRunBinding_Rejected(t *testing.T) {
	h := newAsyncHarness(t, takeoverKeyedSide, takeoverPolicy)
	first := h.loseFirstDispatchResponse()

	// Live path: another Run's NodeRun is waiting on its own task.
	other := h.dispatchAsyncNode(h.createRun(), "provider-task-other")
	before := listEvents(h.ctx, t, h.uow, other.run.ID)
	if _, err := h.handleCallback(first.token, other.externalTaskID, `{"text":"misrouted"}`); !errors.Is(err, service.ErrInvalidCallbackCredential) {
		t.Fatalf("credential of another NodeRun at a live Binding: want ErrInvalidCallbackCredential, got %v", err)
	}
	if got := asyncPendingStatus(h.ctx, t, h.uow, other.externalTaskID); got != "absent" {
		t.Fatalf("pending callback after a refused credential: want none, got %s", got)
	}
	if got := getNodeRun(h.ctx, t, h.uow, other.nodeRunID).Status; got != domain.NodeRunWaitingCallback {
		t.Fatalf("other NodeRun after the refused callback: want WAITING_CALLBACK, got %s", got)
	}
	if after := listEvents(h.ctx, t, h.uow, other.run.ID); len(after) != len(before) {
		t.Fatalf("events of the other Run after the refused callback: want %d, got %d", len(before), len(after))
	}

	// Pending path: the same credential names a third NodeRun's task before its Binding
	// exists.
	third := h.createRun()
	h.exec.setExternalTaskID("provider-task-third")
	claimed := h.claimAsyncNode(third)
	early, err := h.handleCallback(first.token, "provider-task-third", `{"text":"misrouted early"}`)
	if err != nil {
		t.Fatalf("early callback with another NodeRun's credential: %v", err)
	}
	if !early.Pending {
		t.Fatalf("early callback outcome: want Pending, got %+v", early)
	}
	if err := h.svc.Execute(h.ctx, claimed); err != nil {
		t.Fatalf("dispatch of the third NodeRun: %v", err)
	}
	if got := getNodeRun(h.ctx, t, h.uow, claimed.NodeRunID).Status; got != domain.NodeRunWaitingCallback {
		t.Fatalf("third NodeRun after its dispatch: want WAITING_CALLBACK, got %s", got)
	}
	pending := getPending(h.ctx, t, h.uow, "provider-task-third")
	if pending.ConsumedAt != nil {
		t.Fatalf("pending callback with another NodeRun's credential: want unconsumed, got %v", pending.ConsumedAt)
	}
	_, err = h.svc.ResumeNode(h.ctx, service.ResumeNode{
		ExternalTaskID: "provider-task-third",
		Payload:        pending.Payload,
		Source:         domain.CompletionCallback,
		ConsumePending: true,
		PayloadHash:    pending.PayloadHash,
	})
	var mismatch *service.PendingCallbackCredentialMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("replay of a pending callback with another NodeRun's credential: want *PendingCallbackCredentialMismatchError, got %v", err)
	}
	if got := getNodeRun(h.ctx, t, h.uow, claimed.NodeRunID).Status; got != domain.NodeRunWaitingCallback {
		t.Fatalf("third NodeRun after the refused replay: want WAITING_CALLBACK, got %s", got)
	}
}

// TestCallbackTakeover_FirstAttemptTokenExpired_CallbackRejectedAndPollCompletes: Attempt
// 1's credential lives until its own deadline plus the pending retention window, which can
// end before Attempt 2's deadline. Once it has expired the original task's callback fails
// verification and stores nothing, and the Provider poll completes Attempt 2. The Events
// show the replayed dispatch and a PROVIDER_POLL completion with no callback received.
func TestCallbackTakeover_FirstAttemptTokenExpired_CallbackRejectedAndPollCompletes(t *testing.T) {
	h, poller := newPollingHarness(t, takeoverKeyedSide, takeoverPolicy)
	first := h.loseFirstDispatchResponse()

	// The retry is claimed after Attempt 1's deadline plus the retention window.
	h.clock.Advance(2 * time.Hour)
	second := h.dispatchRetry(first)

	if _, err := h.handleCallback(first.token, takeoverTaskID, `{"text":"original task result"}`); !errors.Is(err, service.ErrInvalidCallbackCredential) {
		t.Fatalf("callback with Attempt 1's expired credential: want ErrInvalidCallbackCredential, got %v", err)
	}
	if got := asyncPendingStatus(h.ctx, t, h.uow, takeoverTaskID); got != "absent" {
		t.Fatalf("pending callback after an expired credential: want none, got %s", got)
	}
	if got := getAttempt(h.ctx, t, h.uow, second.attemptID).Status; got != domain.NodeAttemptDispatched {
		t.Fatalf("Attempt 2 after the refused callback: want DISPATCHED, got %s", got)
	}

	poller.pollMu.Lock()
	poller.pollFn = pollReturning(pollSucceeded("polled original task"))
	poller.pollMu.Unlock()
	h.clock.Advance(pollAttemptInterval)
	outcome, err := pollAttemptOf(h.ctx, h.svc, second)
	if err != nil {
		t.Fatalf("poll Attempt 2: %v", err)
	}
	if outcome.Kind != service.PollAttemptResumed {
		t.Fatalf("poll Attempt 2: want RESUMED, got %+v", outcome)
	}
	assertRetryCompletedOnce(t, h, first, second, domain.CompletionProviderPoll)
}

// TestCallbackTakeover_CrashBeforeRetryAttemptCreated_ReconcilerRecoversSameFlow: the
// process stops after Attempt 1's failure committed and before Attempt 2 exists, while the
// original task's callback is already stored as pending. A fresh service and Reconciler
// find the NodeRun through its persisted next_attempt_at, dispatch the retry, which the
// Provider answers with a replay, and the pending delivery completes Attempt 2 once.
func TestCallbackTakeover_CrashBeforeRetryAttemptCreated_ReconcilerRecoversSameFlow(t *testing.T) {
	h := newAsyncHarness(t, takeoverKeyedSide, takeoverPolicy)
	first := h.loseFirstDispatchResponse()
	if _, err := h.handleCallback(first.token, takeoverTaskID, `{"text":"original task result"}`); err != nil {
		t.Fatalf("early callback of the original task: %v", err)
	}

	// Restart: fresh registry, plan cache and Executor instance. The Provider is external
	// and keeps its own record of the accepted task and its idempotency key.
	h.exec.mu.Lock()
	provider := &asyncFakeExecutor{externalTaskID: takeoverTaskID, replayKeyed: true, tasksByKey: map[string]string{}, dispatched: append([]string(nil), h.exec.dispatched...)}
	for key, id := range h.exec.tasksByKey {
		provider.tasksByKey[key] = id
	}
	h.exec.mu.Unlock()
	h.exec = provider
	h.svc = h.newService(takeoverKeyedSide, provider)
	rec := reconciler.New(reconciler.Config{UoW: h.uow, Executor: h.svc, Clock: h.clock, BatchLimit: 100})

	// Before next_attempt_at nothing is due.
	report, err := rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("reconciler before the retry is due: %v", err)
	}
	if report.ReadyOrRetryableFound != 0 || len(report.Errors) != 0 {
		t.Fatalf("reconciler before the retry is due: want nothing found, got %+v", report)
	}

	h.clock.Advance(time.Minute)
	report, err = rec.RunOnce(h.ctx)
	if err != nil {
		t.Fatalf("reconciler after the retry is due: %v", err)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("reconciler errors: want none, got %v", report.Errors)
	}

	attempts := listNodeAttempts(h.ctx, t, h.uow, first.nodeRunID)
	if len(attempts) != 2 {
		t.Fatalf("Attempts of the recovered NodeRun: want 2, got %d", len(attempts))
	}
	second := asyncDispatch{run: first.run, nodeRunID: first.nodeRunID, attemptID: attempts[1].ID, externalTaskID: takeoverTaskID}
	if got := asyncPendingStatus(h.ctx, t, h.uow, takeoverTaskID); got != "consumed" {
		t.Fatalf("pending callback after recovery: want consumed, got %s", got)
	}
	assertRetryCompletedOnce(t, h, first, second, domain.CompletionCallback)

	for i := 0; i < 10 && getRun(h.ctx, t, h.uow, first.run.ID).Status == domain.RunRunning; i++ {
		if _, err := rec.RunOnce(h.ctx); err != nil {
			t.Fatalf("drain reconciler: %v", err)
		}
	}
	if got := getRun(h.ctx, t, h.uow, first.run.ID).Status; got != domain.RunCompleted {
		t.Fatalf("Run after recovery: want COMPLETED, got %s", got)
	}
}

func listNodeAttempts(ctx context.Context, t *testing.T, uow store.UnitOfWork, nodeRunID string) []domain.NodeAttempt {
	t.Helper()
	var attempts []domain.NodeAttempt
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		attempts, err = tx.NodeAttempts().ListByNodeRun(ctx, nodeRunID)
		return err
	}); err != nil {
		t.Fatalf("list attempts of %s: %v", nodeRunID, err)
	}
	return attempts
}

// TestMockTaskAdapter_KeyedRedispatchToMockProvider_ReportsReplayedTask runs the Adapter
// against the deterministic Mock Provider: the first dispatch under an idempotency key
// creates the task, a second dispatch under the same key (a retry whose predecessor lost
// its response) is answered with the same task id and reported as a replay.
func TestMockTaskAdapter_KeyedRedispatchToMockProvider_ReportsReplayedTask(t *testing.T) {
	dispatcher := mockprovider.NewDispatcher(nil)
	provider := httptest.NewServer(mockprovider.NewServer(dispatcher))
	t.Cleanup(func() {
		provider.Close()
		_ = dispatcher.Shutdown(context.Background())
	})
	adapter := mocktask.New(provider.URL, &http.Client{Timeout: 5 * time.Second})
	// The lost-callback scenario keeps the Provider from calling back into a host that
	// does not exist in this test.
	const prompt = "mock:lost"

	created, err := adapter.Dispatch(context.Background(), prompt, nil, nil,
		registry.CallbackContext{URL: asyncBaseURL + "/api/callbacks", Token: "attempt-one-token"}, "nr_takeover")
	if err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	replayed, err := adapter.Dispatch(context.Background(), prompt, nil, nil,
		registry.CallbackContext{URL: asyncBaseURL + "/api/callbacks", Token: "attempt-two-token"}, "nr_takeover")
	if err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	if created.Replayed {
		t.Fatalf("first dispatch: want a created task, got Replayed")
	}
	if !replayed.Replayed || replayed.ExternalTaskID != created.ExternalTaskID {
		t.Fatalf("second dispatch under the same key: want a replay of %s, got %+v", created.ExternalTaskID, replayed)
	}
}
