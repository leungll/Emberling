package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/textinput"
	"github.com/leungll/Emberling/backend/internal/nodes/textoutput"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/store"
)

// ---------------------------------------------------------------------------
// Poll-aware wrapper over the callback fake store
//
// The callback fake store leaves the poll scheduling methods unimplemented. This wrapper
// adds them with the same conditions the PostgreSQL store applies, and keeps the callback
// fake's rollback model: everything still runs on the transaction's private copy. The
// conditional-claim race itself is proven against PostgreSQL in test/integration.
// ---------------------------------------------------------------------------

type pollFakeStore struct{ *cbStore }

func (s *pollFakeStore) WithinTx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	return s.cbStore.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return fn(ctx, &pollFakeTx{cbTx: tx.(*cbTx)})
	})
}

func (s *pollFakeStore) WithinReadTx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	return s.cbStore.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return fn(ctx, &pollFakeTx{cbTx: tx.(*cbTx)})
	})
}

type pollFakeTx struct{ *cbTx }

func (t *pollFakeTx) NodeAttempts() store.NodeAttemptRepository {
	return &pollFakeAttemptRepo{cbNodeAttemptRepo: &cbNodeAttemptRepo{tx: t.cbTx}}
}

func (t *pollFakeTx) CallbackBindings() store.CallbackBindingRepository {
	return &pollFakeBindingRepo{cbBindingRepo: &cbBindingRepo{tx: t.cbTx}}
}

type pollFakeAttemptRepo struct{ *cbNodeAttemptRepo }

func (r *pollFakeAttemptRepo) ClaimPoll(_ context.Context, attemptID string, now time.Time, interval time.Duration, maxPolls int) (bool, error) {
	if interval <= 0 || maxPolls <= 0 {
		return false, errors.New("node_poll_test: poll bounds must be positive")
	}
	attempt, ok := r.tx.state.attempts[attemptID]
	if !ok || attempt.Status != domain.NodeAttemptDispatched || attempt.NextPollAt == nil ||
		attempt.NextPollAt.After(now) || attempt.PollCount >= maxPolls {
		return false, nil
	}
	attempt.PollCount++
	if attempt.PollCount >= maxPolls {
		attempt.NextPollAt = nil
	} else {
		next := now.Add(interval)
		attempt.NextPollAt = &next
	}
	r.tx.state.attempts[attemptID] = attempt
	return true, nil
}

func (r *pollFakeAttemptRepo) ClearPoll(_ context.Context, attemptID string) (bool, error) {
	attempt, ok := r.tx.state.attempts[attemptID]
	if !ok || attempt.Status != domain.NodeAttemptDispatched || attempt.NextPollAt == nil {
		return false, nil
	}
	attempt.NextPollAt = nil
	r.tx.state.attempts[attemptID] = attempt
	return true, nil
}

type pollFakeBindingRepo struct{ *cbBindingRepo }

func (r *pollFakeBindingRepo) ListByTargets(_ context.Context, targetType domain.CallbackTargetType, ids []string) ([]domain.CallbackBinding, error) {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var out []domain.CallbackBinding
	for _, b := range r.tx.state.bindings {
		if b.TargetType == targetType && want[b.TargetID] {
			out = append(out, b)
		}
	}
	return out, nil
}

// pollFakeExecutor adds a scripted Poll to the callback fake executor.
type pollFakeExecutor struct {
	*cbAsyncExecutor
	poll  func(state registry.NodeAsyncState) (registry.PollResult, error)
	calls int
}

func (e *pollFakeExecutor) Poll(_ context.Context, state registry.NodeAsyncState) (registry.PollResult, error) {
	e.calls++
	if e.poll == nil {
		return registry.PollResult{Status: registry.PollRunning}, nil
	}
	return e.poll(state)
}

const (
	pollTestInterval = 2 * time.Second
	pollTestMax      = 3
)

type pollHarness struct {
	*cbHarness
	poller *pollFakeExecutor
}

var pollTestSideEffect = domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown}

// newPollHarness builds the callback harness over the poll-aware store with a pollable
// registration, seeds the state a committed dispatch leaves behind and advances the
// clock to the first due poll.
func newPollHarness(t *testing.T, hooks PollHooks) *pollHarness {
	t.Helper()
	base := newCbHarness(t, pollTestSideEffect)
	poller := &pollFakeExecutor{cbAsyncExecutor: base.exec}
	reg := cbAsyncRegistration(poller, pollTestSideEffect)
	reg.Metadata.Poll = &domain.PollPolicy{IntervalMs: int64(pollTestInterval / time.Millisecond), MaxPolls: pollTestMax}

	h := &pollHarness{cbHarness: base, poller: poller}
	h.rebuild(t, reg, hooks)

	h.seedWaiting("")
	first := h.clock.Now().Add(pollTestInterval)
	attempt := h.store.state.attempts[cbAttemptID]
	attempt.NextPollAt = &first
	h.store.state.attempts[cbAttemptID] = attempt
	h.clock.now = first
	return h
}

// rebuild replaces the service with one over the poll-aware store and a registry holding
// reg for the async Node Type, as a restarted process with that registration would.
func (h *pollHarness) rebuild(t *testing.T, reg registry.NodeRegistration, hooks PollHooks) {
	t.Helper()
	nodes := registry.NewNodeRegistry()
	for _, r := range []registry.NodeRegistration{textinput.Registration(), textoutput.Registration(), reg} {
		if err := nodes.Register(r); err != nil {
			t.Fatalf("register node type: %v", err)
		}
	}
	h.nodes = nodes
	h.svc = NewExecutionService(Deps{
		UoW:       &pollFakeStore{cbStore: h.store},
		Nodes:     nodes,
		Compiler:  runtime.NewCompiler(nodes, h.clock),
		Clock:     h.clock,
		IDs:       &cbIDs{},
		PollHooks: hooks,
		Callback: CallbackConfig{
			BaseURL:       "https://emberling.test",
			SigningSecret: []byte("unit-test-signing-secret"),
			PendingTTL:    time.Hour,
		},
	})
}

func (h *pollHarness) attempt() domain.NodeAttempt { return h.store.state.attempts[cbAttemptID] }

func (h *pollHarness) poll(t *testing.T) (PollAttemptOutcome, error) {
	t.Helper()
	return h.svc.PollAttempt(context.Background(), PollAttempt{AttemptID: cbAttemptID, NodeRunID: cbTaskNodeRunID, RunID: cbRunID})
}

func pollSucceededResult(text string) registry.PollResult {
	raw, _ := json.Marshal(text)
	return registry.PollResult{
		Status: registry.PollSucceeded,
		Output: &registry.NodeOutput{Ports: map[string]json.RawMessage{"text": raw}},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestPollAttempt_SucceededResult_ResumesThroughProviderPoll proves a due poll is claimed,
// the Provider is queried with state restored from committed facts, and a SUCCEEDED
// answer completes the Attempt through the resume path with completionSource
// PROVIDER_POLL and no NODE_CALLBACK_RECEIVED.
func TestPollAttempt_SucceededResult_ResumesThroughProviderPoll(t *testing.T) {
	h := newPollHarness(t, PollHooks{})
	var seen registry.NodeAsyncState
	h.poller.poll = func(state registry.NodeAsyncState) (registry.PollResult, error) {
		seen = state
		return pollSucceededResult("generated"), nil
	}

	outcome, err := h.poll(t)
	if err != nil {
		t.Fatalf("poll attempt: %v", err)
	}
	if outcome.Kind != PollAttemptResumed || outcome.Status != registry.PollSucceeded || outcome.Failed {
		t.Fatalf("poll outcome: want RESUMED with SUCCEEDED, got %+v", outcome)
	}
	if outcome.RunID != cbRunID || outcome.NodeRunID != cbTaskNodeRunID || outcome.AttemptID != cbAttemptID {
		t.Fatalf("poll outcome identity: got %+v", outcome)
	}
	if seen.ExternalTask.ExternalTaskID != cbExternalTaskID || seen.ExternalTask.ProviderID != cbProviderID ||
		seen.RunID != cbRunID || seen.NodeRunID != cbTaskNodeRunID || seen.AttemptNo != 1 {
		t.Fatalf("poll state: want the bound external task and committed identity, got %+v", seen)
	}
	if got := h.attempt(); got.Status != domain.NodeAttemptSucceeded || got.PollCount != 1 {
		t.Fatalf("attempt after poll completion: want SUCCEEDED with pollCount 1, got %s/%d", got.Status, got.PollCount)
	}
	if _, found := h.eventOfType(domain.EventNodeCallbackReceived); found {
		t.Fatalf("NODE_CALLBACK_RECEIVED after a poll completion: want absent, got %v", h.eventTypes())
	}
	completed, found := h.eventOfType(domain.EventNodeCompleted)
	if !found {
		t.Fatalf("NODE_COMPLETED after poll completion: want present, got %v", h.eventTypes())
	}
	var payload nodeCompletedPayload
	if err := json.Unmarshal(completed.Payload, &payload); err != nil {
		t.Fatalf("decode NODE_COMPLETED payload: %v", err)
	}
	if payload.CompletionSource != domain.CompletionProviderPoll {
		t.Fatalf("completionSource: want PROVIDER_POLL, got %s", payload.CompletionSource)
	}
}

// TestPollAttempt_FailedResult_FailsWithProviderPollSource proves a FAILED answer fails
// the Attempt and NodeRun through the resume path and reports PROVIDER_POLL as source.
func TestPollAttempt_FailedResult_FailsWithProviderPollSource(t *testing.T) {
	h := newPollHarness(t, PollHooks{})
	h.poller.poll = func(registry.NodeAsyncState) (registry.PollResult, error) {
		return registry.PollResult{Status: registry.PollFailed, Error: &domain.ExecutionError{Code: "PROVIDER_TASK_FAILED", Message: "render failed"}}, nil
	}

	outcome, err := h.poll(t)
	if err != nil {
		t.Fatalf("poll attempt: %v", err)
	}
	if outcome.Kind != PollAttemptResumed || !outcome.Failed || outcome.FailureSource != domain.FailureProviderPoll {
		t.Fatalf("poll outcome: want RESUMED failed by PROVIDER_POLL, got %+v", outcome)
	}
	if got := h.attempt().Status; got != domain.NodeAttemptFailed {
		t.Fatalf("attempt status: want FAILED, got %s", got)
	}
	if got := h.store.state.nodeRuns[cbTaskNodeRunID].Status; got != domain.NodeRunFailed {
		t.Fatalf("NodeRun status: want FAILED, got %s", got)
	}
}

// TestPollAttempt_RunningAndUnknown_WritesOnlyTheClaim proves a non-final answer writes
// no Event and no state: only the claim's poll count and next poll time changed.
func TestPollAttempt_RunningAndUnknown_WritesOnlyTheClaim(t *testing.T) {
	for _, status := range []registry.PollStatus{registry.PollRunning, registry.PollStatusUnknown} {
		t.Run(string(status), func(t *testing.T) {
			h := newPollHarness(t, PollHooks{})
			h.poller.poll = func(registry.NodeAsyncState) (registry.PollResult, error) {
				return registry.PollResult{Status: status}, nil
			}
			claimedAt := h.clock.Now()

			outcome, err := h.poll(t)
			if err != nil {
				t.Fatalf("poll attempt: %v", err)
			}
			if outcome.Kind != PollAttemptClaimedNotFinal || outcome.Status != status {
				t.Fatalf("poll outcome: want CLAIMED_NOT_FINAL with %s, got %+v", status, outcome)
			}
			got := h.attempt()
			if got.Status != domain.NodeAttemptDispatched || got.PollCount != 1 {
				t.Fatalf("attempt after non-final poll: want DISPATCHED with pollCount 1, got %s/%d", got.Status, got.PollCount)
			}
			if got.NextPollAt == nil || !got.NextPollAt.Equal(claimedAt.Add(pollTestInterval)) {
				t.Fatalf("next poll time: want claim time + interval, got %v", got.NextPollAt)
			}
			if len(h.store.state.events) != 0 || h.store.state.lastSeq != 7 {
				t.Fatalf("events after non-final poll: want none, got %v (lastSeq %d)", h.eventTypes(), h.store.state.lastSeq)
			}
		})
	}
}

// TestPollAttempt_NotDue_SkipsWithoutQueryingProvider proves a poll whose next poll time
// has not arrived is not claimed and never reaches the Provider.
func TestPollAttempt_NotDue_SkipsWithoutQueryingProvider(t *testing.T) {
	h := newPollHarness(t, PollHooks{})
	h.clock.now = h.clock.now.Add(-time.Millisecond)

	outcome, err := h.poll(t)
	if err != nil {
		t.Fatalf("poll attempt: %v", err)
	}
	if outcome.Kind != PollAttemptSkipped || outcome.SkipReason != PollSkipNotClaimed {
		t.Fatalf("poll outcome: want SKIPPED NOT_CLAIMED, got %+v", outcome)
	}
	if h.poller.calls != 0 || h.attempt().PollCount != 0 {
		t.Fatalf("poll not due: want no Provider query and pollCount 0, got %d queries and pollCount %d", h.poller.calls, h.attempt().PollCount)
	}
}

// TestPollAttempt_AttemptNotDispatched_Skips proves an Attempt that is no longer awaiting
// a result is skipped without error and without a Provider query.
func TestPollAttempt_AttemptNotDispatched_Skips(t *testing.T) {
	h := newPollHarness(t, PollHooks{})
	attempt := h.attempt()
	attempt.Status = domain.NodeAttemptFailed
	h.store.state.attempts[cbAttemptID] = attempt

	outcome, err := h.poll(t)
	if err != nil {
		t.Fatalf("poll attempt: %v", err)
	}
	if outcome.Kind != PollAttemptSkipped || outcome.SkipReason != PollSkipAttemptNotDispatched || h.poller.calls != 0 {
		t.Fatalf("poll outcome: want SKIPPED ATTEMPT_NOT_DISPATCHED with no query, got %+v (%d queries)", outcome, h.poller.calls)
	}
}

// TestPollAttempt_RegistrationWithoutPollPolicy_ClearsScheduleAndSkips proves registry
// drift never fails the Attempt: the schedule is cleared, the Provider is not queried
// and the Attempt stays DISPATCHED for a callback or the deadline.
func TestPollAttempt_RegistrationWithoutPollPolicy_ClearsScheduleAndSkips(t *testing.T) {
	h := newPollHarness(t, PollHooks{})
	h.rebuild(t, cbAsyncRegistration(h.exec, pollTestSideEffect), PollHooks{})

	outcome, err := h.poll(t)
	if err != nil {
		t.Fatalf("poll attempt: %v", err)
	}
	if outcome.Kind != PollAttemptSkipped || outcome.SkipReason != PollSkipRegistryDrift {
		t.Fatalf("poll outcome: want SKIPPED REGISTRY_DRIFT, got %+v", outcome)
	}
	got := h.attempt()
	if got.Status != domain.NodeAttemptDispatched || got.NextPollAt != nil || got.PollCount != 0 {
		t.Fatalf("attempt after drift: want DISPATCHED, no next poll, pollCount 0, got %s/%v/%d", got.Status, got.NextPollAt, got.PollCount)
	}
	if h.poller.calls != 0 || len(h.store.state.events) != 0 {
		t.Fatalf("drift: want no Provider query and no Event, got %d queries and %v", h.poller.calls, h.eventTypes())
	}
}

// TestPollAttempt_BindingRoutesElsewhere_RefusedWithoutClaim proves a poll whose Callback
// Binding does not name the polled Attempt is refused with the mismatch sentinel, claims
// nothing, and clears the poll schedule so discovery stops listing the Attempt.
func TestPollAttempt_BindingRoutesElsewhere_RefusedWithoutClaim(t *testing.T) {
	h := newPollHarness(t, PollHooks{})
	binding := h.store.state.bindings[cbExternalTaskID]
	binding.TargetID = "attempt_other"
	h.store.state.bindings[cbExternalTaskID] = binding

	outcome, err := h.poll(t)
	if !errors.Is(err, ErrPollBindingMismatch) {
		t.Fatalf("poll attempt with a foreign binding: want ErrPollBindingMismatch, got %v", err)
	}
	if outcome.Kind != PollAttemptSkipped || outcome.SkipReason != PollSkipBindingMismatch {
		t.Fatalf("poll outcome: want SKIPPED BINDING_MISMATCH, got %+v", outcome)
	}
	if h.poller.calls != 0 || h.attempt().PollCount != 0 {
		t.Fatalf("binding mismatch: want no claim and no query, got pollCount %d and %d queries", h.attempt().PollCount, h.poller.calls)
	}
	if got := h.attempt(); got.Status != domain.NodeAttemptDispatched || got.NextPollAt != nil {
		t.Fatalf("binding mismatch: want DISPATCHED with the poll schedule cleared, got %s/%v", got.Status, got.NextPollAt)
	}
	if len(h.store.state.events) != 0 {
		t.Fatalf("binding mismatch: want no Event, got %v", h.eventTypes())
	}
}

// TestPollAttempt_HookFailsAfterClaim_ClaimCommittedProviderNotQueried proves the claim
// commits before the Provider is queried: a crash injected between the two leaves the
// poll consumed and the next poll time pushed back, with no Provider query.
func TestPollAttempt_HookFailsAfterClaim_ClaimCommittedProviderNotQueried(t *testing.T) {
	crash := errors.New("injected crash")
	var hooked string
	h := newPollHarness(t, PollHooks{AfterClaim: func(_ context.Context, attemptID string) error {
		hooked = attemptID
		return crash
	}})

	_, err := h.poll(t)
	if !errors.Is(err, crash) {
		t.Fatalf("poll attempt with a failing hook: want the injected error, got %v", err)
	}
	if hooked != cbAttemptID || h.poller.calls != 0 {
		t.Fatalf("hook: want it called for %s before any query, got %q and %d queries", cbAttemptID, hooked, h.poller.calls)
	}
	if got := h.attempt(); got.PollCount != 1 || got.NextPollAt == nil {
		t.Fatalf("claim after injected crash: want committed with pollCount 1, got %d/%v", got.PollCount, got.NextPollAt)
	}
}

// TestPollAttempt_TransportError_WrapsIdentifiersAndWritesNothing proves a Provider that
// gave no answer surfaces a wrapped error naming the Attempt and external task, and
// changes no state beyond the committed claim.
func TestPollAttempt_TransportError_WrapsIdentifiersAndWritesNothing(t *testing.T) {
	h := newPollHarness(t, PollHooks{})
	transport := errors.New("connection refused")
	h.poller.poll = func(registry.NodeAsyncState) (registry.PollResult, error) {
		return registry.PollResult{}, transport
	}

	_, err := h.poll(t)
	if !errors.Is(err, transport) {
		t.Fatalf("poll attempt with a transport failure: want the wrapped error, got %v", err)
	}
	if !strings.Contains(err.Error(), cbAttemptID) || !strings.Contains(err.Error(), cbExternalTaskID) {
		t.Fatalf("transport error: want attempt and external task ids, got %q", err)
	}
	if got := h.attempt(); got.Status != domain.NodeAttemptDispatched || got.PollCount != 1 || len(h.store.state.events) != 0 {
		t.Fatalf("after transport failure: want DISPATCHED, pollCount 1, no Event, got %s/%d/%v", got.Status, got.PollCount, h.eventTypes())
	}
}

// TestPollAttempt_LastAllowedPoll_ClearsSchedule proves the claim that reaches the poll
// limit clears the next poll time and that no further poll is claimed, while the Attempt
// stays DISPATCHED for a callback or the deadline.
func TestPollAttempt_LastAllowedPoll_ClearsSchedule(t *testing.T) {
	h := newPollHarness(t, PollHooks{})
	for i := 1; i <= pollTestMax; i++ {
		outcome, err := h.poll(t)
		if err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
		if outcome.Kind != PollAttemptClaimedNotFinal {
			t.Fatalf("poll %d: want CLAIMED_NOT_FINAL, got %+v", i, outcome)
		}
		h.clock.now = h.clock.now.Add(pollTestInterval)
	}
	got := h.attempt()
	if got.PollCount != pollTestMax || got.NextPollAt != nil || got.Status != domain.NodeAttemptDispatched {
		t.Fatalf("after the last poll: want pollCount %d, no next poll, DISPATCHED, got %d/%v/%s", pollTestMax, got.PollCount, got.NextPollAt, got.Status)
	}
	outcome, err := h.poll(t)
	if err != nil || outcome.SkipReason != PollSkipNotClaimed || h.poller.calls != pollTestMax {
		t.Fatalf("poll past the limit: want NOT_CLAIMED with no query, got %+v, %v, %d queries", outcome, err, h.poller.calls)
	}
}
