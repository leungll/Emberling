//go:build integration

// The Provider poll use case against real PostgreSQL. A poll is claimed by a conditional
// update that only reschedules the next poll, the Provider is queried outside every
// transaction, and a final answer enters the single idempotent resume path, where the
// conditional update from DISPATCHED elects one winner against callbacks, timeouts and
// other polls. Every scenario here is a transaction, recovery or race guarantee, so the
// fake repositories of the service unit tests cannot stand in for it.
package integration

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mocktask"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/mockprovider"
	"github.com/leungll/Emberling/backend/internal/nodes/textinput"
	"github.com/leungll/Emberling/backend/internal/nodes/textoutput"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
)

const (
	pollAttemptInterval = 2 * time.Second
	pollAttemptMax      = 3
)

// pollScriptExecutor is the async fake Node with a scripted Poll. Each Poll call is
// counted, which is how "no Provider query" is observed from the Provider's side.
type pollScriptExecutor struct {
	*asyncFakeExecutor

	pollMu sync.Mutex
	pollFn func(ctx context.Context, state registry.NodeAsyncState) (registry.PollResult, error)
	polls  []registry.NodeAsyncState
}

func newPollScriptExecutor(base *asyncFakeExecutor, fn func(context.Context, registry.NodeAsyncState) (registry.PollResult, error)) *pollScriptExecutor {
	return &pollScriptExecutor{asyncFakeExecutor: base, pollFn: fn}
}

func (e *pollScriptExecutor) Poll(ctx context.Context, state registry.NodeAsyncState) (registry.PollResult, error) {
	e.pollMu.Lock()
	e.polls = append(e.polls, state)
	fn := e.pollFn
	e.pollMu.Unlock()
	if fn == nil {
		return registry.PollResult{Status: registry.PollRunning}, nil
	}
	return fn(ctx, state)
}

func (e *pollScriptExecutor) pollCalls() int {
	e.pollMu.Lock()
	defer e.pollMu.Unlock()
	return len(e.polls)
}

func pollReturning(result registry.PollResult) func(context.Context, registry.NodeAsyncState) (registry.PollResult, error) {
	return func(context.Context, registry.NodeAsyncState) (registry.PollResult, error) { return result, nil }
}

// pollingRegistration is the async fake Node registration with a poll policy declared.
func pollingRegistration(executor registry.PollableAsyncNodeExecutor, side domain.SideEffectPolicy) registry.NodeRegistration {
	reg := asyncRegistration(executor, side)
	reg.Metadata.Poll = &domain.PollPolicy{IntervalMs: int64(pollAttemptInterval / time.Millisecond), MaxPolls: pollAttemptMax}
	return reg
}

// pollService builds an ExecutionService over uow with reg as the async Node
// registration. Calling it again models a process restart with that registration: the
// registry and Executor instance are fresh and PostgreSQL is the only carried-over state.
func (h *asyncHarness) pollService(uow store.UnitOfWork, reg registry.NodeRegistration, hooks service.PollHooks) *service.ExecutionService {
	h.t.Helper()
	nodes := registry.NewNodeRegistry()
	for _, r := range []registry.NodeRegistration{textinput.Registration(), textoutput.Registration(), reg} {
		if err := nodes.Register(r); err != nil {
			h.t.Fatalf("register node type: %v", err)
		}
	}
	return service.NewExecutionService(service.Deps{
		UoW:       uow,
		Nodes:     nodes,
		Compiler:  runtime.NewCompiler(nodes, h.clock),
		Clock:     h.clock,
		IDs:       h.ids,
		PollHooks: hooks,
		Callback: service.CallbackConfig{
			BaseURL:       asyncBaseURL,
			SigningSecret: []byte(asyncSigningSecret),
			PendingTTL:    time.Hour,
		},
	})
}

// newPollingHarness is the async harness whose service dispatches through a pollable
// registration, so every dispatch schedules its first poll.
func newPollingHarness(t *testing.T, side domain.SideEffectPolicy, policy domain.ExecutionPolicy) (*asyncHarness, *pollScriptExecutor) {
	t.Helper()
	h := newAsyncHarness(t, side, policy)
	poller := newPollScriptExecutor(h.exec, nil)
	h.svc = h.pollService(h.uow, pollingRegistration(poller, side), service.PollHooks{})
	return h, poller
}

func pollAttemptOf(ctx context.Context, svc *service.ExecutionService, dispatch asyncDispatch) (service.PollAttemptOutcome, error) {
	return svc.PollAttempt(ctx, service.PollAttempt{AttemptID: dispatch.attemptID, NodeRunID: dispatch.nodeRunID, RunID: dispatch.run.ID})
}

func duePollAttemptIDs(ctx context.Context, t *testing.T, uow store.UnitOfWork, now time.Time) []string {
	t.Helper()
	var ids []string
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		due, err := tx.NodeAttempts().ListDuePolls(ctx, now, 100)
		for _, d := range due {
			ids = append(ids, d.AttemptID)
		}
		return err
	}); err != nil {
		t.Fatalf("list due polls: %v", err)
	}
	return ids
}

func containsID(ids []string, id string) bool {
	for _, got := range ids {
		if got == id {
			return true
		}
	}
	return false
}

// pollArmedBarrierUoW passes every transaction straight through until it is armed, and
// from then on holds each state-changing transaction at a shared barrier. Arming it from
// the post-claim hook lets the claim commit freely and stops only the poll's resume
// transaction.
type pollArmedBarrierUoW struct {
	store.UnitOfWork
	armed   atomic.Bool
	arrived chan struct{}
	release chan struct{}
}

func (u *pollArmedBarrierUoW) WithinTx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	if u.armed.Load() {
		u.arrived <- struct{}{}
		<-u.release
	}
	return u.UnitOfWork.WithinTx(ctx, fn)
}

// TestPollAttempt_DispatchWithPollPolicy_WritesFirstNextPollAt proves the dispatch
// transaction schedules the first poll one interval after dispatch for a registration
// that declares a poll policy, schedules none without one, and writes no extra Event.
func TestPollAttempt_DispatchWithPollPolicy_WritesFirstNextPollAt(t *testing.T) {
	h := newAsyncHarness(t, pollUnknownSide, domain.ExecutionPolicy{})
	plainSvc := h.svc
	poller := newPollScriptExecutor(h.exec, nil)
	pollingSvc := h.pollService(h.uow, pollingRegistration(poller, pollUnknownSide), service.PollHooks{})

	h.svc = plainSvc
	plainRun := h.createRun()
	plain := h.dispatchAsyncNode(plainRun, "provider-task-poll-plain")
	h.svc = pollingSvc
	pollingRun := h.createRun()
	polling := h.dispatchAsyncNode(pollingRun, "provider-task-poll-first")

	attempt := getAttempt(h.ctx, t, h.uow, polling.attemptID)
	if attempt.Status != domain.NodeAttemptDispatched || attempt.DispatchedAt == nil {
		t.Fatalf("polled Attempt after dispatch: want DISPATCHED with dispatched_at, got %s/%v", attempt.Status, attempt.DispatchedAt)
	}
	if attempt.NextPollAt == nil || !attempt.NextPollAt.Equal(attempt.DispatchedAt.Add(pollAttemptInterval)) {
		t.Fatalf("first next_poll_at: want dispatched_at + %s = %v, got %v", pollAttemptInterval, attempt.DispatchedAt.Add(pollAttemptInterval), attempt.NextPollAt)
	}
	if attempt.PollCount != 0 {
		t.Fatalf("poll_count after dispatch: want 0, got %d", attempt.PollCount)
	}
	if got := getAttempt(h.ctx, t, h.uow, plain.attemptID).NextPollAt; got != nil {
		t.Fatalf("next_poll_at without a poll policy: want NULL, got %v", got)
	}

	plainEvents := execEventTypes(listEvents(h.ctx, t, h.uow, plainRun.ID))
	pollingEvents := execEventTypes(listEvents(h.ctx, t, h.uow, pollingRun.ID))
	if len(plainEvents) != len(pollingEvents) {
		t.Fatalf("events of a polled dispatch: want the same as without a policy %v, got %v", plainEvents, pollingEvents)
	}
	for i := range plainEvents {
		if plainEvents[i] != pollingEvents[i] {
			t.Fatalf("events of a polled dispatch: want %v, got %v", plainEvents, pollingEvents)
		}
	}
	if poller.pollCalls() != 0 {
		t.Fatalf("Provider polls during dispatch: want 0, got %d", poller.pollCalls())
	}
}

// TestPollAttempt_RacesCallback_ExactlyOneWinner holds a claimed SUCCEEDED poll and a
// callback at a barrier just before their state-changing transactions, after both have
// routed to the DISPATCHED Attempt, and releases them together. The conditional update
// elects exactly one winner: one SUCCEEDED, one NODE_COMPLETED, the loser a duplicate, and
// a winning poll records PROVIDER_POLL with no NODE_CALLBACK_RECEIVED.
func TestPollAttempt_RacesCallback_ExactlyOneWinner(t *testing.T) {
	h, poller := newPollingHarness(t, pollUnknownSide, domain.ExecutionPolicy{})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-poll-attempt-race")
	h.clock.Advance(pollAttemptInterval)

	arrived := make(chan struct{})
	release := make(chan struct{})
	pollUoW := &pollArmedBarrierUoW{UnitOfWork: h.uow, arrived: arrived, release: release}
	poller.pollFn = pollReturning(pollSucceeded("polled"))
	pollSvc := h.pollService(pollUoW, pollingRegistration(poller, pollUnknownSide), service.PollHooks{
		AfterClaim: func(context.Context, string) error {
			pollUoW.armed.Store(true)
			return nil
		},
	})
	callbackSvc := h.serviceOver(&pollBarrierUoW{UnitOfWork: h.uow, arrived: arrived, release: release}, pollUnknownSide)

	var wg sync.WaitGroup
	var pollOutcome service.PollAttemptOutcome
	var callbackOutcome service.CallbackOutcome
	var pollErr, callbackErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		pollOutcome, pollErr = pollAttemptOf(h.ctx, pollSvc, dispatch)
	}()
	go func() {
		defer wg.Done()
		callbackOutcome, callbackErr = callbackSvc.HandleCallback(h.ctx, service.HandleCallback{
			Token: dispatch.token, ExternalTaskID: dispatch.externalTaskID, Payload: []byte(`{"text":"called back"}`),
		})
	}()
	<-arrived
	<-arrived
	close(release)
	wg.Wait()

	if pollErr != nil || callbackErr != nil {
		t.Fatalf("racing deliveries: poll err %v, callback err %v", pollErr, callbackErr)
	}
	pollWon := pollOutcome.Kind == service.PollAttemptResumed
	if pollWon == !callbackOutcome.Duplicate || (!pollWon && pollOutcome.Kind != service.PollAttemptDuplicate) {
		t.Fatalf("racing deliveries: want exactly one winner, got poll %+v and callback %+v", pollOutcome, callbackOutcome)
	}

	attempt := getAttempt(h.ctx, t, h.uow, dispatch.attemptID)
	if attempt.Status != domain.NodeAttemptSucceeded || attempt.PollCount != 1 {
		t.Fatalf("Attempt after the race: want SUCCEEDED with one claimed poll, got %s/%d", attempt.Status, attempt.PollCount)
	}
	events := listEvents(h.ctx, t, h.uow, run.ID)
	completed := asyncNodeEventsOfType(events, dispatch.nodeRunID, domain.EventNodeCompleted)
	received := asyncNodeEventsOfType(events, dispatch.nodeRunID, domain.EventNodeCallbackReceived)
	if len(completed) != 1 {
		t.Fatalf("NODE_COMPLETED after the race: want exactly 1, got %v", execEventTypes(events))
	}
	source := decodeCompletionSource(t, completed[0])
	if pollWon && (source != string(domain.CompletionProviderPoll) || len(received) != 0) {
		t.Fatalf("poll won: want PROVIDER_POLL and no NODE_CALLBACK_RECEIVED, got %q and %v", source, execEventTypes(events))
	}
	if !pollWon && (source != string(domain.CompletionCallback) || len(received) != 1) {
		t.Fatalf("callback won: want CALLBACK and one NODE_CALLBACK_RECEIVED, got %q and %v", source, execEventTypes(events))
	}
}

// TestPollAttempt_CrashAfterClaimCommitBeforeHTTP_RediscoveredAfterRestart injects a
// crash after the claim committed and before the Provider was queried. The claim stays
// committed: one poll consumed and the next poll time pushed back. A fresh service, as
// after a restart, does not rediscover the Attempt before that time, then rediscovers it
// from PostgreSQL alone, claims again and completes it.
func TestPollAttempt_CrashAfterClaimCommitBeforeHTTP_RediscoveredAfterRestart(t *testing.T) {
	h, poller := newPollingHarness(t, pollUnknownSide, domain.ExecutionPolicy{})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-poll-crash")
	h.clock.Advance(pollAttemptInterval)

	crash := errors.New("process killed after the poll claim committed")
	crashing := h.pollService(h.uow, pollingRegistration(poller, pollUnknownSide), service.PollHooks{
		AfterClaim: func(context.Context, string) error { return crash },
	})
	if _, err := pollAttemptOf(h.ctx, crashing, dispatch); !errors.Is(err, crash) {
		t.Fatalf("poll with an injected crash: want the crash error, got %v", err)
	}
	claimedAt := h.clock.Now()
	attempt := getAttempt(h.ctx, t, h.uow, dispatch.attemptID)
	if attempt.PollCount != 1 || attempt.NextPollAt == nil || !attempt.NextPollAt.Equal(claimedAt.Add(pollAttemptInterval)) {
		t.Fatalf("claim after the crash: want poll_count 1 and next poll at claim + interval, got %d/%v", attempt.PollCount, attempt.NextPollAt)
	}
	if poller.pollCalls() != 0 {
		t.Fatalf("Provider polls before the crash point: want 0, got %d", poller.pollCalls())
	}

	restarted := newPollScriptExecutor(&asyncFakeExecutor{}, pollReturning(pollSucceeded("after restart")))
	fresh := h.pollService(h.uow, pollingRegistration(restarted, pollUnknownSide), service.PollHooks{})

	if containsID(duePollAttemptIDs(h.ctx, t, h.uow, h.clock.Now()), dispatch.attemptID) {
		t.Fatal("discovery before the pushed-back poll time: want the Attempt not due")
	}
	early, err := pollAttemptOf(h.ctx, fresh, dispatch)
	if err != nil || early.Kind != service.PollAttemptSkipped || early.SkipReason != service.PollSkipNotClaimed {
		t.Fatalf("poll before the pushed-back time: want SKIPPED NOT_CLAIMED, got %+v / %v", early, err)
	}

	h.clock.Advance(pollAttemptInterval)
	if !containsID(duePollAttemptIDs(h.ctx, t, h.uow, h.clock.Now()), dispatch.attemptID) {
		t.Fatal("discovery after restart: want the Attempt due again from persisted facts")
	}
	outcome, err := pollAttemptOf(h.ctx, fresh, dispatch)
	if err != nil || outcome.Kind != service.PollAttemptResumed {
		t.Fatalf("poll after restart: want RESUMED, got %+v / %v", outcome, err)
	}
	attempt = getAttempt(h.ctx, t, h.uow, dispatch.attemptID)
	if attempt.Status != domain.NodeAttemptSucceeded || attempt.PollCount != 2 {
		t.Fatalf("Attempt after the restarted poll: want SUCCEEDED with poll_count 2, got %s/%d", attempt.Status, attempt.PollCount)
	}
	if restarted.pollCalls() != 1 {
		t.Fatalf("Provider polls after restart: want 1, got %d", restarted.pollCalls())
	}
	completed := asyncNodeEventsOfType(listEvents(h.ctx, t, h.uow, run.ID), dispatch.nodeRunID, domain.EventNodeCompleted)
	if len(completed) != 1 || decodeCompletionSource(t, completed[0]) != string(domain.CompletionProviderPoll) {
		t.Fatalf("NODE_COMPLETED after restart: want one with PROVIDER_POLL, got %d", len(completed))
	}
}

// TestPollAttempt_RunningThenCallback_CallbackCompletes proves a RUNNING answer writes no
// Event and no business state, the callback then completes the Attempt, and a later poll
// claims nothing and never reaches the Provider.
func TestPollAttempt_RunningThenCallback_CallbackCompletes(t *testing.T) {
	h, poller := newPollingHarness(t, pollUnknownSide, domain.ExecutionPolicy{})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-poll-running")
	h.clock.Advance(pollAttemptInterval)
	before := h.pollFacts(dispatch)

	poller.pollFn = pollReturning(registry.PollResult{Status: registry.PollRunning})
	outcome, err := pollAttemptOf(h.ctx, h.svc, dispatch)
	if err != nil || outcome.Kind != service.PollAttemptClaimedNotFinal || outcome.Status != registry.PollRunning {
		t.Fatalf("RUNNING poll: want CLAIMED_NOT_FINAL with RUNNING, got %+v / %v", outcome, err)
	}
	if after := h.pollFacts(dispatch); after != before {
		t.Fatalf("facts after a RUNNING poll: want unchanged %+v, got %+v", before, after)
	}

	final, err := h.handleCallback(dispatch.token, dispatch.externalTaskID, `{"text":"called back"}`)
	if err != nil || !final.Accepted || final.Duplicate {
		t.Fatalf("callback after a RUNNING poll: want accepted, got %+v / %v", final, err)
	}
	completed := asyncNodeEventsOfType(listEvents(h.ctx, t, h.uow, run.ID), dispatch.nodeRunID, domain.EventNodeCompleted)
	if len(completed) != 1 || decodeCompletionSource(t, completed[0]) != string(domain.CompletionCallback) {
		t.Fatalf("NODE_COMPLETED after the callback: want one with CALLBACK, got %d", len(completed))
	}

	h.clock.Advance(pollAttemptInterval)
	var claimed bool
	if err := h.uow.WithinTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		claimed, err = tx.NodeAttempts().ClaimPoll(ctx, dispatch.attemptID, h.clock.Now(), pollAttemptInterval, pollAttemptMax)
		return err
	}); err != nil {
		t.Fatalf("claim a poll after the callback: %v", err)
	}
	if claimed {
		t.Fatal("poll claim after the callback completed the Attempt: want 0 rows")
	}
	later, err := pollAttemptOf(h.ctx, h.svc, dispatch)
	if err != nil || later.Kind != service.PollAttemptSkipped {
		t.Fatalf("poll after the callback: want SKIPPED, got %+v / %v", later, err)
	}
	if poller.pollCalls() != 1 {
		t.Fatalf("Provider polls: want only the RUNNING one, got %d", poller.pollCalls())
	}
	if got := getAttempt(h.ctx, t, h.uow, dispatch.attemptID).PollCount; got != 1 {
		t.Fatalf("poll_count after the callback: want 1, got %d", got)
	}
}

// TestPollAttempt_StaleAttemptLatePollResult_NoEffect lets the timeout fail the Attempt
// after the poll was claimed and before its answer arrives. The late SUCCEEDED answer is
// a duplicate: no state change, no Event, no seq.
func TestPollAttempt_StaleAttemptLatePollResult_NoEffect(t *testing.T) {
	h, poller := newPollingHarness(t, pollUnknownSide, domain.ExecutionPolicy{TimeoutMs: 60_000, MaxAttempts: 1})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-poll-stale-attempt")
	h.clock.Advance(pollAttemptInterval)

	var before pollWaitingFacts
	poller.pollFn = pollReturning(pollSucceeded("too late"))
	pollSvc := h.pollService(h.uow, pollingRegistration(poller, pollUnknownSide), service.PollHooks{
		AfterClaim: func(ctx context.Context, attemptID string) error {
			h.clock.Advance(2 * time.Minute)
			if err := h.svc.TimeoutAttempt(ctx, attemptID); err != nil {
				return err
			}
			before = h.pollFacts(dispatch)
			return nil
		},
	})

	outcome, err := pollAttemptOf(h.ctx, pollSvc, dispatch)
	if err != nil {
		t.Fatalf("late poll: %v", err)
	}
	if before.attemptStatus != domain.NodeAttemptFailed {
		t.Fatalf("precondition: Attempt after timeout want FAILED, got %s", before.attemptStatus)
	}
	if outcome.Kind != service.PollAttemptDuplicate || outcome.Status != registry.PollSucceeded {
		t.Fatalf("late poll outcome: want DUPLICATE with SUCCEEDED, got %+v", outcome)
	}
	if after := h.pollFacts(dispatch); after != before {
		t.Fatalf("facts after a late poll result: want unchanged %+v, got %+v", before, after)
	}
	if got := getAttempt(h.ctx, t, h.uow, dispatch.attemptID).Error; got == nil || got.Code != "TIMEOUT" {
		t.Fatalf("Attempt error after a late poll result: want TIMEOUT kept, got %+v", got)
	}
}

// TestPollAttempt_MaxPollsReachedBeforeDeadline_TimeoutTerminates proves reaching the
// poll limit clears the schedule without failing the Attempt, no further Provider query
// is made, and the deadline still terminates the Attempt through the timeout use case.
func TestPollAttempt_MaxPollsReachedBeforeDeadline_TimeoutTerminates(t *testing.T) {
	h, poller := newPollingHarness(t, pollUnknownSide, domain.ExecutionPolicy{TimeoutMs: 60_000, MaxAttempts: 1})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-poll-limit")

	for i := 1; i <= pollAttemptMax; i++ {
		h.clock.Advance(pollAttemptInterval)
		outcome, err := pollAttemptOf(h.ctx, h.svc, dispatch)
		if err != nil || outcome.Kind != service.PollAttemptClaimedNotFinal {
			t.Fatalf("poll %d: want CLAIMED_NOT_FINAL, got %+v / %v", i, outcome, err)
		}
	}
	attempt := getAttempt(h.ctx, t, h.uow, dispatch.attemptID)
	if attempt.Status != domain.NodeAttemptDispatched || attempt.PollCount != pollAttemptMax || attempt.NextPollAt != nil {
		t.Fatalf("Attempt at the poll limit: want DISPATCHED, poll_count %d, next_poll_at NULL, got %s/%d/%v",
			pollAttemptMax, attempt.Status, attempt.PollCount, attempt.NextPollAt)
	}

	h.clock.Advance(pollAttemptInterval)
	if containsID(duePollAttemptIDs(h.ctx, t, h.uow, h.clock.Now()), dispatch.attemptID) {
		t.Fatal("discovery past the poll limit: want the Attempt not due")
	}
	past, err := pollAttemptOf(h.ctx, h.svc, dispatch)
	if err != nil || past.Kind != service.PollAttemptSkipped || past.SkipReason != service.PollSkipNotClaimed {
		t.Fatalf("poll past the limit: want SKIPPED NOT_CLAIMED, got %+v / %v", past, err)
	}
	if poller.pollCalls() != pollAttemptMax {
		t.Fatalf("Provider polls: want exactly %d, got %d", pollAttemptMax, poller.pollCalls())
	}

	h.clock.Advance(2 * time.Minute)
	if err := h.svc.TimeoutAttempt(h.ctx, dispatch.attemptID); err != nil {
		t.Fatalf("timeout after the deadline: %v", err)
	}
	attempt = getAttempt(h.ctx, t, h.uow, dispatch.attemptID)
	if attempt.Status != domain.NodeAttemptFailed || attempt.Error == nil || attempt.Error.Code != "TIMEOUT" {
		t.Fatalf("Attempt after the deadline: want FAILED TIMEOUT, got %s/%+v", attempt.Status, attempt.Error)
	}
	if got := getNodeRun(h.ctx, t, h.uow, dispatch.nodeRunID).Status; got != domain.NodeRunFailed {
		t.Fatalf("NodeRun after the deadline: want FAILED, got %s", got)
	}
}

// TestPollAttempt_ProviderRestartedBetweenDispatchAndPoll_NoStateChange queries a Mock
// Provider that restarted and lost the task: it answers 404, the Adapter normalizes that
// to UNKNOWN, and nothing changes beyond the claim. A later callback still decides.
func TestPollAttempt_ProviderRestartedBetweenDispatchAndPoll_NoStateChange(t *testing.T) {
	h, _ := newPollingHarness(t, pollUnknownSide, domain.ExecutionPolicy{})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-poll-provider-restarted")
	h.clock.Advance(pollAttemptInterval)

	dispatcher := mockprovider.NewDispatcher(nil)
	provider := httptest.NewServer(mockprovider.NewServer(dispatcher))
	t.Cleanup(func() {
		provider.Close()
		_ = dispatcher.Shutdown(context.Background())
	})
	adapter := mocktask.New(provider.URL, &http.Client{Timeout: 5 * time.Second})
	restartedProvider := newPollScriptExecutor(h.exec, func(ctx context.Context, state registry.NodeAsyncState) (registry.PollResult, error) {
		status, _, err := adapter.Poll(ctx, state.ExternalTask.ExternalTaskID)
		if err != nil {
			return registry.PollResult{}, err
		}
		return registry.PollResult{Status: status}, nil
	})
	svc := h.pollService(h.uow, pollingRegistration(restartedProvider, pollUnknownSide), service.PollHooks{})
	before := h.pollFacts(dispatch)

	outcome, err := pollAttemptOf(h.ctx, svc, dispatch)
	if err != nil {
		t.Fatalf("poll a restarted Provider: %v", err)
	}
	if outcome.Kind != service.PollAttemptClaimedNotFinal || outcome.Status != registry.PollStatusUnknown {
		t.Fatalf("poll a restarted Provider: want CLAIMED_NOT_FINAL with UNKNOWN, got %+v", outcome)
	}
	if after := h.pollFacts(dispatch); after != before {
		t.Fatalf("facts after polling a restarted Provider: want unchanged %+v, got %+v", before, after)
	}
	if restartedProvider.pollCalls() != 1 {
		t.Fatalf("Provider polls: want 1, got %d", restartedProvider.pollCalls())
	}

	final, err := h.handleCallback(dispatch.token, dispatch.externalTaskID, `{"text":"called back"}`)
	if err != nil || !final.Accepted || final.Duplicate {
		t.Fatalf("callback after an UNKNOWN poll: want accepted, got %+v / %v", final, err)
	}
}

// TestPollAttempt_RegistryDrift_ClearsPollAndSkips restarts with a registration that no
// longer declares polling. The poll is skipped without a Provider query, the schedule is
// cleared so the Attempt is no longer discovered, nothing fails, and the callback still
// completes the Attempt.
func TestPollAttempt_RegistryDrift_ClearsPollAndSkips(t *testing.T) {
	h, poller := newPollingHarness(t, pollUnknownSide, domain.ExecutionPolicy{})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-poll-drift")
	h.clock.Advance(pollAttemptInterval)
	before := h.pollFacts(dispatch)

	drifted := h.pollService(h.uow, asyncRegistration(h.exec, pollUnknownSide), service.PollHooks{})
	outcome, err := pollAttemptOf(h.ctx, drifted, dispatch)
	if err != nil {
		t.Fatalf("poll after registry drift: want no error, got %v", err)
	}
	if outcome.Kind != service.PollAttemptSkipped || outcome.SkipReason != service.PollSkipRegistryDrift {
		t.Fatalf("poll after registry drift: want SKIPPED REGISTRY_DRIFT, got %+v", outcome)
	}
	attempt := getAttempt(h.ctx, t, h.uow, dispatch.attemptID)
	if attempt.Status != domain.NodeAttemptDispatched || attempt.NextPollAt != nil || attempt.PollCount != 0 {
		t.Fatalf("Attempt after registry drift: want DISPATCHED, next_poll_at NULL, poll_count 0, got %s/%v/%d",
			attempt.Status, attempt.NextPollAt, attempt.PollCount)
	}
	if after := h.pollFacts(dispatch); after != before {
		t.Fatalf("facts after registry drift: want unchanged %+v, got %+v", before, after)
	}
	if containsID(duePollAttemptIDs(h.ctx, t, h.uow, h.clock.Now()), dispatch.attemptID) {
		t.Fatal("discovery after registry drift: want the Attempt no longer due")
	}
	if poller.pollCalls() != 0 {
		t.Fatalf("Provider polls after registry drift: want 0, got %d", poller.pollCalls())
	}

	final, err := drifted.HandleCallback(h.ctx, service.HandleCallback{
		Token: dispatch.token, ExternalTaskID: dispatch.externalTaskID, Payload: []byte(`{"text":"called back"}`),
	})
	if err != nil || !final.Accepted || final.Duplicate {
		t.Fatalf("callback after registry drift: want accepted, got %+v / %v", final, err)
	}
}

// TestPollAttempt_BindingNamesOtherAttempt_Skips repoints the current Attempt's Callback
// Binding at the superseded Attempt of a keyed retry. A poll carries no callback token, so
// the Binding is its only route: the poll is refused with the mismatch sentinel before any
// claim, nothing is queried, and the only write is the cleared poll schedule, so discovery
// stops listing the Attempt and the deadline decides.
func TestPollAttempt_BindingNamesOtherAttempt_Skips(t *testing.T) {
	h, poller := newPollingHarness(t,
		domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed},
		domain.ExecutionPolicy{TimeoutMs: 60_000, MaxAttempts: 3, Backoff: domain.BackoffFixed})
	run := h.createRun()

	first := h.claimAsyncNode(run)
	h.exec.setDispatchErr(errors.New("provider connection reset while accepting the task"))
	if err := h.svc.Execute(h.ctx, first); err != nil {
		t.Fatalf("execute the failing dispatch: %v", err)
	}
	h.exec.setDispatchErr(nil)
	h.clock.Advance(time.Minute)
	second := h.dispatchAsyncNode(run, "provider-task-poll-misbound")

	if _, err := h.pool.Exec(h.ctx,
		`UPDATE callback_bindings SET target_id = $1 WHERE external_task_id = $2`,
		first.AttemptID, second.externalTaskID); err != nil {
		t.Fatalf("repoint callback binding: %v", err)
	}
	h.clock.Advance(pollAttemptInterval)
	before := h.pollFacts(second)
	beforeAttempt := getAttempt(h.ctx, t, h.uow, second.attemptID)

	outcome, err := pollAttemptOf(h.ctx, h.svc, second)
	if !errors.Is(err, service.ErrPollBindingMismatch) {
		t.Fatalf("poll with a binding naming another attempt: want ErrPollBindingMismatch, got %v", err)
	}
	if outcome.Kind != service.PollAttemptSkipped || outcome.SkipReason != service.PollSkipBindingMismatch {
		t.Fatalf("poll with a binding naming another attempt: want SKIPPED BINDING_MISMATCH, got %+v", outcome)
	}
	if beforeAttempt.NextPollAt == nil {
		t.Fatal("precondition: want a scheduled poll before the refused poll")
	}
	afterAttempt := getAttempt(h.ctx, t, h.uow, second.attemptID)
	if afterAttempt.PollCount != beforeAttempt.PollCount || afterAttempt.NextPollAt != nil {
		t.Fatalf("poll schedule after a refused poll: want poll_count %d kept and next_poll_at NULL, got %d/%v",
			beforeAttempt.PollCount, afterAttempt.PollCount, afterAttempt.NextPollAt)
	}
	if containsID(duePollAttemptIDs(h.ctx, t, h.uow, h.clock.Now()), second.attemptID) {
		t.Fatal("discovery after a refused poll: want the Attempt no longer due")
	}
	if after := h.pollFacts(second); after != before {
		t.Fatalf("facts after a refused poll: want unchanged %+v, got %+v", before, after)
	}
	if poller.pollCalls() != 0 {
		t.Fatalf("Provider polls for a refused poll: want 0, got %d", poller.pollCalls())
	}
}

// TestPollAttempt_NodeRunNotWaitingCallback_Skips proves a poll is only claimed for a
// NodeRun that is waiting for its external result: otherwise it is skipped without error,
// claims nothing and queries nothing. The inconsistent row is forced with SQL because no
// use case produces a DISPATCHED Attempt under a NodeRun that is not waiting.
func TestPollAttempt_NodeRunNotWaitingCallback_Skips(t *testing.T) {
	h, poller := newPollingHarness(t, pollUnknownSide, domain.ExecutionPolicy{})
	run := h.createRun()
	dispatch := h.dispatchAsyncNode(run, "provider-task-poll-not-waiting")
	if _, err := h.pool.Exec(h.ctx, `UPDATE node_runs SET status = 'RUNNING' WHERE id = $1`, dispatch.nodeRunID); err != nil {
		t.Fatalf("force node run status: %v", err)
	}
	h.clock.Advance(pollAttemptInterval)
	before := h.pollFacts(dispatch)

	outcome, err := pollAttemptOf(h.ctx, h.svc, dispatch)
	if err != nil {
		t.Fatalf("poll a NodeRun that is not waiting: want no error, got %v", err)
	}
	if outcome.Kind != service.PollAttemptSkipped || outcome.SkipReason != service.PollSkipNodeRunNotWaiting {
		t.Fatalf("poll a NodeRun that is not waiting: want SKIPPED NODE_RUN_NOT_WAITING_CALLBACK, got %+v", outcome)
	}
	if got := getAttempt(h.ctx, t, h.uow, dispatch.attemptID).PollCount; got != 0 {
		t.Fatalf("poll_count after a skipped poll: want 0, got %d", got)
	}
	if after := h.pollFacts(dispatch); after != before {
		t.Fatalf("facts after a skipped poll: want unchanged %+v, got %+v", before, after)
	}
	if poller.pollCalls() != 0 {
		t.Fatalf("Provider polls for a skipped poll: want 0, got %d", poller.pollCalls())
	}
}

func equalTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
