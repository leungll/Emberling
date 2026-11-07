//go:build integration

// Recovery of a Node Attempt whose process died. A STARTED Attempt with no deadline has no
// timeout to end it, so the process that restarts takes it over from the runner identity
// the claim transaction recorded: the Attempt fails through the ordinary failure use case,
// the registered side-effect policy decides whether a new Attempt may run, and a dispatched
// Attempt is left to its callback, poll or deadline. Every guarantee here is a transaction, a race
// or a restart, so all of it runs against PostgreSQL.
//
// "Crashing" is modelled as the persisted state a crash leaves: Advance committed the
// claim and nothing after it did. Restarting builds a fresh service with a new runner
// identity over the same database.
package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/leungll/Emberling/backend/internal/adapters/mocktask"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/fixture/mockprovider"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/migrations"
	"github.com/leungll/Emberling/backend/test/testdb"
)

const (
	orphanRunnerBefore = "runner-before-restart"
	orphanRunnerAfter  = "runner-after-restart"

	orphanSyncSafeType        = "orphan_sync_safe"
	orphanSyncUnknownType     = "orphan_sync_unknown"
	orphanProviderUnknownType = "orphan_provider_unknown"
	orphanProviderKeyedType   = "orphan_provider_keyed"
	orphanPolledType          = "orphan_polled"

	orphanCodeInterrupted = "EXECUTION_INTERRUPTED"
	orphanCodeUncertain   = "RESULT_UNCERTAIN"

	// orphanLongAfter is further ahead than any backoff these tests can schedule.
	orphanLongAfter = 24 * time.Hour

	// orphanMaxWait is further ahead than orphanLongAfter, so a dispatched Attempt in these
	// tests reaches its bound only when a test means it to.
	orphanMaxWait = 7 * 24 * time.Hour
)

// orphanSyncExecutor is a synchronous Node whose calls are counted, which is how "not
// called again" is observed. With a gate set, a call announces itself and then blocks until
// released, which is a call genuinely in flight in a live process.
type orphanSyncExecutor struct {
	calls    atomic.Int32
	inFlight chan struct{}
	release  chan struct{}
}

func (*orphanSyncExecutor) ValidateSemantics(context.Context, map[string]any) error { return nil }

func (e *orphanSyncExecutor) Execute(ctx context.Context, _ registry.NodeInput, _ map[string]any) (registry.NodeResult, error) {
	e.calls.Add(1)
	if e.inFlight != nil {
		e.inFlight <- struct{}{}
		select {
		case <-e.release:
		case <-ctx.Done():
			return registry.NodeResult{}, ctx.Err()
		}
	}
	return registry.CompletedResult(map[string]json.RawMessage{"text": json.RawMessage(`"done"`)}), nil
}

func orphanSyncRegistration(nodeType string, side domain.SideEffectPolicy, executor *orphanSyncExecutor) registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          nodeType,
			DisplayName:   nodeType,
			Category:      domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs:        []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			Outputs:       []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText}},
			ConfigSchema:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			SideEffect:    side,
		},
		Binding: registry.ExecutorBinding{Executor: executor},
	}
}

// orphanProviderExecutor dispatches through the real task Adapter to the Mock Provider,
// so what the Provider received is read from its own record rather than from the test's
// memory. The `lost` directive makes the Provider accept a task and never call back.
type orphanProviderExecutor struct {
	adapter *mocktask.Adapter
}

func (*orphanProviderExecutor) ValidateSemantics(context.Context, map[string]any) error { return nil }

func (e *orphanProviderExecutor) Execute(ctx context.Context, in registry.NodeInput, _ map[string]any) (registry.NodeResult, error) {
	task, err := e.adapter.Dispatch(ctx, "mock:lost", nil, nil, *in.Callback, in.IdempotencyKey)
	if err != nil {
		return registry.NodeResult{}, err
	}
	return registry.NodeResult{Kind: registry.NodeResultDispatched, ExternalTask: &task}, nil
}

func (*orphanProviderExecutor) OnCallback(_ context.Context, _ registry.NodeAsyncState, payload []byte) (registry.NodeOutput, error) {
	return registry.NodeOutput{Ports: map[string]json.RawMessage{"text": json.RawMessage(strconv.Quote(string(payload)))}}, nil
}

func orphanProviderRegistration(nodeType string, side domain.SideEffectPolicy, executor registry.NodeExecutor) registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          nodeType,
			DisplayName:   nodeType,
			Category:      domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionAsync,
			Inputs:        []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText, Required: true}},
			Outputs:       []domain.PortMetadata{{Name: "text", DataType: domain.PortTypeText}},
			ConfigSchema:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			SideEffect:    side,
		},
		Binding: registry.ExecutorBinding{Executor: executor},
	}
}

// orphanPolledExecutor is a task the Provider answers only through Poll.
type orphanPolledExecutor struct {
	orphanProviderExecutor
	polls atomic.Int32
}

func (e *orphanPolledExecutor) Poll(context.Context, registry.NodeAsyncState) (registry.PollResult, error) {
	e.polls.Add(1)
	return registry.PollResult{Status: registry.PollSucceeded, Output: &registry.NodeOutput{
		Ports: map[string]json.RawMessage{"text": json.RawMessage(`"polled"`)},
	}}, nil
}

// orphanProviderRecord is one line of the Mock Provider's own dispatch record.
type orphanProviderRecord struct {
	Event          string `json:"event"`
	Kind           string `json:"kind"`
	ExternalTaskID string `json:"externalTaskId"`
	Replayed       bool   `json:"replayed"`
}

type orphanHarness struct {
	*capHarness
	safe    *orphanSyncExecutor
	unknown *orphanSyncExecutor
	polled  *orphanPolledExecutor
	adapter *mocktask.Adapter
	// recordPath is the Mock Provider's append-only record of what reached it.
	recordPath string
}

func newOrphanHarness(t *testing.T, limit int) *orphanHarness {
	t.Helper()
	record, err := mockprovider.OpenRecord(filepath.Join(t.TempDir(), "dispatch.jsonl"))
	if err != nil {
		t.Fatalf("open mock provider record: %v", err)
	}
	dispatcher := mockprovider.NewDispatcher(&http.Client{Timeout: 5 * time.Second})
	server := httptest.NewServer(mockprovider.NewServer(dispatcher, mockprovider.WithTestControls(record)))
	t.Cleanup(func() {
		server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := dispatcher.Shutdown(ctx); err != nil {
			t.Logf("mock provider dispatcher shutdown: %v", err)
		}
		_ = record.Close()
	})

	adapter := mocktask.New(server.URL, server.Client(), mocktask.WithProviderID("orphan-provider-v1"))
	h := &orphanHarness{
		capHarness: newCapHarness(t, limit),
		safe:       &orphanSyncExecutor{},
		unknown:    &orphanSyncExecutor{},
		polled:     &orphanPolledExecutor{orphanProviderExecutor: orphanProviderExecutor{adapter: adapter}},
		adapter:    adapter,
		recordPath: record.Path(),
	}
	polled := orphanProviderRegistration(orphanPolledType, domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed}, h.polled)
	polled.Metadata.Poll = &domain.PollPolicy{IntervalMs: 1000, MaxPolls: 3}
	h.extraNodes = []registry.NodeRegistration{
		orphanSyncRegistration(orphanSyncSafeType, domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe}, h.safe),
		orphanSyncRegistration(orphanSyncUnknownType, domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown}, h.unknown),
		orphanProviderRegistration(orphanProviderUnknownType, domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown}, &orphanProviderExecutor{adapter: adapter}),
		orphanProviderRegistration(orphanProviderKeyedType, domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyKeyed}, &orphanProviderExecutor{adapter: adapter}),
		polled,
	}
	h.runnerID = orphanRunnerBefore
	h.maxWait = orphanMaxWait
	h.svc = h.newService()
	return h
}

// restart models a new process over the same database.
func (h *orphanHarness) restart() *reconciler.Reconciler {
	h.t.Helper()
	h.runnerID = orphanRunnerAfter
	h.svc = h.newService()
	return h.reconcilerAs(orphanRunnerAfter, 100)
}

func (h *orphanHarness) reconcilerAs(runnerID string, batchLimit int) *reconciler.Reconciler {
	return reconciler.New(reconciler.Config{UoW: h.uow, Executor: h.svc, Clock: h.clock, BatchLimit: batchLimit, RunnerID: runnerID})
}

// pass runs one reconciliation and fails the test on any error it collected: a recovery
// that rolled back on every pass would otherwise look like a quiet no-op.
func (h *orphanHarness) pass(rec *reconciler.Reconciler) reconciler.Report {
	h.t.Helper()
	report, err := rec.RunOnce(h.ctx)
	if err != nil {
		h.t.Fatalf("reconcile: %v", err)
	}
	if len(report.Errors) != 0 {
		h.t.Fatalf("reconcile collected errors: %v", report.Errors)
	}
	return report
}

// chain saves text_input -> node -> text_output.
func (h *orphanHarness) chain(nodeType, config string, policy *domain.ExecutionPolicy) domain.Definition {
	h.t.Helper()
	if config == "" {
		config = `{}`
	}
	return h.save(domain.Definition{
		WorkflowID: capWorkflowID("orphan"), Version: 1, Name: "Orphan chain", Description: "one Node between input and output",
		Nodes: []domain.Node{
			{ID: "a_input", Type: "text_input", Name: "Brief", Config: json.RawMessage(`{"inputKey":"brief","required":true}`)},
			{ID: "task", Type: nodeType, Name: "task", Config: json.RawMessage(config), ExecutionPolicy: policy},
			{ID: "output", Type: "text_output", Name: "Output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "a_input", SourceHandle: "text", Target: "task", TargetHandle: "text"},
			{ID: "e2", Source: "task", SourceHandle: "text", Target: "output", TargetHandle: "text"},
		},
		CreatedAt: h.clock.Now(),
	})
}

// siblings saves a Run shape where one branch can end the Run while the other holds a
// STARTED Attempt: b_wait waits on a Provider, then z_task, governed by the capacity limit
// and allowed to retry, is claimed.
func (h *orphanHarness) siblings(policy *domain.ExecutionPolicy) domain.Definition {
	h.t.Helper()
	task := capTaskNode("z_task", capModelID)
	task.ExecutionPolicy = policy
	return h.save(domain.Definition{
		WorkflowID: capWorkflowID("orphan_siblings"), Version: 1, Name: "Orphan siblings", Description: "two parallel branches",
		Nodes: []domain.Node{
			{ID: "a_input", Type: "text_input", Name: "Brief", Config: json.RawMessage(`{"inputKey":"brief","required":true}`)},
			capTaskNode("b_wait", capFreeModelID),
			task,
			{ID: "join", Type: capSyncType, Name: "join", Config: json.RawMessage(`{}`)},
			{ID: "output", Type: "text_output", Name: "Output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "a_input", SourceHandle: "text", Target: "b_wait", TargetHandle: "text"},
			{ID: "e2", Source: "a_input", SourceHandle: "text", Target: "z_task", TargetHandle: "text"},
			{ID: "e3", Source: "b_wait", SourceHandle: "text", Target: "join", TargetHandle: "a"},
			{ID: "e4", Source: "z_task", SourceHandle: "text", Target: "join", TargetHandle: "b"},
			{ID: "e5", Source: "join", SourceHandle: "text", Target: "output", TargetHandle: "text"},
		},
		CreatedAt: h.clock.Now(),
	})
}

// claim runs the upstream Nodes and commits the claim of nodeType's Attempt without calling
// it. A process that dies here leaves exactly this state.
func (h *orphanHarness) claim(def domain.Definition, nodeType string) (domain.Run, service.AdvanceOutcome) {
	h.t.Helper()
	run := h.createRun(def)
	return run, h.claimIn(run.ID, nodeType)
}

func (h *orphanHarness) claimIn(runID, nodeType string) service.AdvanceOutcome {
	h.t.Helper()
	for i := 0; i < 10; i++ {
		outcome, err := h.svc.Advance(h.ctx, runID)
		if err != nil {
			h.t.Fatalf("advance: %v", err)
		}
		if !outcome.Claimed {
			h.t.Fatalf("advance claimed nothing before reaching %s", nodeType)
		}
		if outcome.NodeType == nodeType {
			return outcome
		}
		if err := h.svc.Execute(h.ctx, outcome); err != nil {
			h.t.Fatalf("execute %s: %v", outcome.NodeType, err)
		}
	}
	h.t.Fatalf("%s was never claimed", nodeType)
	return service.AdvanceOutcome{}
}

// endRunThroughSibling claims z_task and then fails its waiting sibling, leaving a FAILED
// Run that still holds z_task's STARTED Attempt and Provider slot.
func (h *orphanHarness) endRunThroughSibling(def domain.Definition) (domain.Run, service.AdvanceOutcome) {
	h.t.Helper()
	run, outcome := h.claim(def, capTaskType)
	if outcome.NodeRunID == h.nodeRun(run.ID, "z_task").ID {
		h.t.Fatal("the claim order changed: want b_wait dispatched first")
	}
	if err := h.svc.Execute(h.ctx, outcome); err != nil {
		h.t.Fatalf("dispatch b_wait: %v", err)
	}
	outcome = h.claimIn(run.ID, capTaskType)
	if outcome.NodeRunID != h.nodeRun(run.ID, "z_task").ID {
		h.t.Fatalf("claimed %s, want z_task", outcome.NodeRunID)
	}
	h.callback(h.nodeRun(run.ID, "b_wait").ID, `{"status":"FAILED"}`)
	if status := h.run(run.ID).Status; status != domain.RunFailed {
		h.t.Fatalf("run after the sibling failed = %s, want FAILED", status)
	}
	return run, outcome
}

func (h *orphanHarness) attempt(id string) domain.NodeAttempt {
	h.t.Helper()
	var attempt domain.NodeAttempt
	if err := h.uow.WithinReadTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		attempt, err = tx.NodeAttempts().Get(ctx, id)
		return err
	}); err != nil {
		h.t.Fatalf("get attempt %s: %v", id, err)
	}
	return attempt
}

func (h *orphanHarness) assertFailed(attemptID, code string, uncertain bool) {
	h.t.Helper()
	attempt := h.attempt(attemptID)
	if attempt.Status != domain.NodeAttemptFailed || attempt.Error == nil || attempt.Error.Code != code {
		h.t.Fatalf("attempt = %+v error=%+v, want FAILED with %s", attempt.Status, attempt.Error, code)
	}
	if attempt.Uncertain == nil || *attempt.Uncertain != uncertain {
		h.t.Fatalf("attempt uncertain = %v, want %v", attempt.Uncertain, uncertain)
	}
}

func (h *orphanHarness) eventCount(runID string, typ domain.EventType) int {
	h.t.Helper()
	return len(asyncEventsOfType(h.events(runID), typ))
}

// providerRecord reads what actually reached the Mock Provider.
func (h *orphanHarness) providerRecord() []orphanProviderRecord {
	h.t.Helper()
	f, err := os.Open(h.recordPath)
	if err != nil {
		h.t.Fatalf("open provider record: %v", err)
	}
	defer func() { _ = f.Close() }()
	var lines []orphanProviderRecord
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var line orphanProviderRecord
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			h.t.Fatalf("decode provider record line: %v", err)
		}
		if line.Kind == "task" && line.Event == "responded" {
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		h.t.Fatalf("read provider record: %v", err)
	}
	return lines
}

// holdRunLock takes the Run row and returns the function that lets go of it. Every racer
// that needs the Run aggregate lock queues behind it, which turns "they ran concurrently"
// into something the database reports rather than something a sleep hopes for.
func (h *orphanHarness) holdRunLock(runID string) func() {
	h.t.Helper()
	tx, err := h.pool.Begin(h.ctx)
	if err != nil {
		h.t.Fatalf("begin lock holder: %v", err)
	}
	if _, err := tx.Exec(h.ctx, `SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, runID); err != nil {
		h.t.Fatalf("hold run lock: %v", err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			if err := tx.Rollback(h.ctx); err != nil {
				h.t.Errorf("release run lock: %v", err)
			}
		})
	}
	h.t.Cleanup(release)
	return release
}

// waitForBlocked returns once n backends of this database wait on a lock.
func (h *orphanHarness) waitForBlocked(n int) {
	h.t.Helper()
	const query = `
		SELECT count(*) FROM pg_stat_activity
		 WHERE datname = current_database() AND cardinality(pg_blocking_pids(pid)) > 0`
	deadline := time.Now().Add(blockedWaitTimeout)
	for {
		var blocked int
		if err := h.pool.QueryRow(h.ctx, query).Scan(&blocked); err != nil {
			h.t.Fatalf("query blocked backends: %v", err)
		}
		if blocked >= n {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("%d backends blocked within %s, want %d", blocked, blockedWaitTimeout, n)
		}
		// Poll interval only: the barrier is the observed lock wait.
		time.Sleep(blockedPollInterval)
	}
}

func (h *orphanHarness) listOrphans(current string, limit int) []domain.NodeAttempt {
	h.t.Helper()
	var orphans []domain.NodeAttempt
	if err := h.uow.WithinReadTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		orphans, err = tx.NodeAttempts().ListOrphanStarted(ctx, current, limit)
		return err
	}); err != nil {
		h.t.Fatalf("list orphaned attempts: %v", err)
	}
	return orphans
}

// ---------------------------------------------------------------------------
// Crash states
// ---------------------------------------------------------------------------

func TestNodeOrphan_SyncNodeCrashedWithNoDeadline_RunNoLongerStuck(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, outcome := h.claim(h.chain(orphanSyncSafeType, "", nil), orphanSyncSafeType)
	if got := h.attempt(outcome.AttemptID).RunnerID; got == nil || *got != orphanRunnerBefore {
		t.Fatalf("attempt runner = %v, want the claiming process", got)
	}

	rec := h.restart()
	report := h.pass(rec)

	if report.OrphanNodeAttemptsFound != 1 || report.OrphanNodeAttemptsHandled != 1 {
		t.Fatalf("report = %+v, want the one orphan found and handled", report)
	}
	h.assertFailed(outcome.AttemptID, orphanCodeInterrupted, false)
	if got := h.nodeRun(run.ID, "task").Status; got != domain.NodeRunFailed {
		t.Fatalf("node run = %s, want FAILED", got)
	}
	if got := h.run(run.ID); got.Status != domain.RunFailed || got.Error == nil || got.Error.Code != orphanCodeInterrupted {
		t.Fatalf("run = %s error=%+v, want FAILED with the interruption", got.Status, got.Error)
	}
	if n := h.eventCount(run.ID, domain.EventNodeFailed); n != 1 {
		t.Fatalf("NODE_FAILED events = %d, want 1", n)
	}
}

func TestNodeOrphan_SafeNodeWithAttemptsLeft_RetriesAsNewAttempt(t *testing.T) {
	h := newOrphanHarness(t, 1)
	policy := &domain.ExecutionPolicy{MaxAttempts: 2}
	run, outcome := h.claim(h.chain(orphanSyncSafeType, "", policy), orphanSyncSafeType)

	rec := h.restart()
	h.pass(rec)

	h.assertFailed(outcome.AttemptID, orphanCodeInterrupted, false)
	if nr := h.nodeRun(run.ID, "task"); nr.Status != domain.NodeRunRunning || nr.NextAttemptAt == nil {
		t.Fatalf("node run = %s next=%v, want RUNNING with a retry scheduled", nr.Status, nr.NextAttemptAt)
	}
	if n := h.eventCount(run.ID, domain.EventNodeRetrying); n != 1 {
		t.Fatalf("NODE_RETRYING events = %d, want 1", n)
	}

	h.clock.Advance(orphanLongAfter)
	h.pass(rec)

	if got := h.run(run.ID).Status; got != domain.RunCompleted {
		t.Fatalf("run = %s, want COMPLETED through the second Attempt", got)
	}
	attempts := h.attempts(outcome.NodeRunID)
	if len(attempts) != 2 || attempts[1].RunnerID == nil || *attempts[1].RunnerID != orphanRunnerAfter {
		t.Fatalf("attempts = %+v, want a second one owned by the restarted process", attempts)
	}
	if calls := h.safe.calls.Load(); calls != 1 {
		t.Fatalf("executor calls = %d, want 1: the crashed process never reached it", calls)
	}
}

func TestNodeOrphan_UncertainSyncCall_NotCalledAgain(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, outcome := h.claim(h.chain(orphanSyncUnknownType, "", &domain.ExecutionPolicy{MaxAttempts: 3}), orphanSyncUnknownType)
	h.unknown.calls.Store(1) // The crashed process had the call in flight.

	rec := h.restart()
	h.pass(rec)
	h.clock.Advance(orphanLongAfter)
	h.pass(rec)

	h.assertFailed(outcome.AttemptID, orphanCodeUncertain, true)
	if got := h.run(run.ID).Status; got != domain.RunFailed {
		t.Fatalf("run = %s, want FAILED", got)
	}
	if calls := h.unknown.calls.Load(); calls != 1 {
		t.Fatalf("executor calls = %d, want the crashed call alone", calls)
	}
	if n := len(h.attempts(outcome.NodeRunID)); n != 1 {
		t.Fatalf("attempts = %d, want 1", n)
	}
}

// A registration that is gone leaves no policy to consult, so the outcome counts as unknown
// and no retry is scheduled, whatever the ExecutionPolicy allows.
func TestNodeOrphan_NodeTypeNoLongerRegistered_FailsAsUncertainWithoutRetry(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, outcome := h.claim(h.chain(orphanSyncSafeType, "", &domain.ExecutionPolicy{MaxAttempts: 3}), orphanSyncSafeType)
	h.extraNodes = h.extraNodes[1:]

	h.pass(h.restart())

	h.assertFailed(outcome.AttemptID, orphanCodeUncertain, true)
	if got := h.run(run.ID).Status; got != domain.RunFailed {
		t.Fatalf("run = %s, want FAILED", got)
	}
	if n := h.eventCount(run.ID, domain.EventNodeRetrying); n != 0 {
		t.Fatalf("NODE_RETRYING events = %d, want 0", n)
	}
}

// A process that keeps dying cannot turn recovery into an unbounded loop: each crash uses up
// an Attempt of the ExecutionPolicy.
func TestNodeOrphan_RetriedAttemptCrashesToo_BoundedByMaxAttempts(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, first := h.claim(h.chain(orphanSyncSafeType, "", &domain.ExecutionPolicy{MaxAttempts: 2}), orphanSyncSafeType)

	h.pass(h.restart())
	h.clock.Advance(orphanLongAfter)
	second := h.claimIn(run.ID, orphanSyncSafeType)
	if second.AttemptID == first.AttemptID {
		t.Fatal("the retry reused the first Attempt")
	}

	h.runnerID = "runner-after-second-restart"
	h.svc = h.newService()
	h.pass(h.reconcilerAs(h.runnerID, 100))

	h.assertFailed(second.AttemptID, orphanCodeInterrupted, false)
	if got := h.run(run.ID).Status; got != domain.RunFailed {
		t.Fatalf("run = %s, want FAILED once the Attempts are used up", got)
	}
	if n := len(h.attempts(first.NodeRunID)); n != 2 {
		t.Fatalf("attempts = %d, want 2", n)
	}
}

func TestNodeOrphan_AsyncCrashedBeforeDispatch_FailsAsUncertainAndFreesSlot(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, outcome := h.claim(h.linearDefinition(capModelID, nil), capTaskType)
	h.assertHeld(h.nodeRun(run.ID, "task"))

	h.pass(h.restart())

	// Nothing recorded whether the Provider was reached before the crash, so the outcome
	// is unknown even though in this run of the test it never was.
	h.assertFailed(outcome.AttemptID, orphanCodeUncertain, true)
	h.assertNotHeld(h.nodeRun(run.ID, "task"))
	if got := h.run(run.ID).Status; got != domain.RunFailed {
		t.Fatalf("run = %s, want FAILED", got)
	}
}

// The Provider accepted the task and the process died before the Binding committed. No
// callback can ever route to this Attempt, so nothing but recovery can end it.
func TestNodeOrphan_ProviderAcceptedBeforeBindingCommitted_NotResentAndLateCallbackHasNoRoute(t *testing.T) {
	h := newOrphanHarness(t, 1)
	reg := h.extraNodes[2].Binding.(registry.ExecutorBinding).Executor
	run, outcome := h.claim(h.chain(orphanProviderUnknownType, "", &domain.ExecutionPolicy{MaxAttempts: 3}), orphanProviderUnknownType)
	result, err := reg.Execute(h.ctx, outcome.Input, outcome.Config)
	if err != nil {
		t.Fatalf("dispatch to the mock provider: %v", err)
	}
	if lines := h.providerRecord(); len(lines) != 1 {
		t.Fatalf("provider record before the crash = %+v, want one accepted task", lines)
	}

	rec := h.restart()
	h.pass(rec)
	h.clock.Advance(orphanLongAfter)
	h.pass(rec)
	h.pass(rec)

	h.assertFailed(outcome.AttemptID, orphanCodeUncertain, true)
	if lines := h.providerRecord(); len(lines) != 1 {
		t.Fatalf("provider record after recovery = %+v, want the original task alone", lines)
	}
	if n := len(h.attempts(outcome.NodeRunID)); n != 1 {
		t.Fatalf("attempts = %d, want 1", n)
	}

	callback, err := h.svc.HandleCallback(h.ctx, service.HandleCallback{
		Token: outcome.Input.Callback.Token, ExternalTaskID: result.ExternalTask.ExternalTaskID, Payload: json.RawMessage(`"late"`),
	})
	if err != nil {
		t.Fatalf("late callback: %v", err)
	}
	if !callback.Pending {
		t.Fatalf("late callback = %+v, want stored with no route", callback)
	}
	h.pass(rec)
	if got := h.nodeRun(run.ID, "task").Status; got != domain.NodeRunFailed {
		t.Fatalf("node run after the late callback = %s, want FAILED", got)
	}
}

// A keyed retry is the one repeat the policy allows, and it repeats nothing: the same key
// reaches the Provider, which answers with the task it already accepted.
func TestNodeOrphan_KeyedTaskWithAttemptsLeft_ReplaysTheSameTask(t *testing.T) {
	h := newOrphanHarness(t, 1)
	reg := h.extraNodes[3].Binding.(registry.ExecutorBinding).Executor
	run, outcome := h.claim(h.chain(orphanProviderKeyedType, "", &domain.ExecutionPolicy{MaxAttempts: 2}), orphanProviderKeyedType)
	first, err := reg.Execute(h.ctx, outcome.Input, outcome.Config)
	if err != nil {
		t.Fatalf("dispatch to the mock provider: %v", err)
	}

	rec := h.restart()
	h.pass(rec)
	h.assertFailed(outcome.AttemptID, orphanCodeUncertain, true)
	h.clock.Advance(orphanLongAfter)
	h.pass(rec)

	lines := h.providerRecord()
	if len(lines) != 2 || lines[0].Replayed || !lines[1].Replayed || lines[1].ExternalTaskID != lines[0].ExternalTaskID {
		t.Fatalf("provider record = %+v, want one task and one replay of it", lines)
	}
	if got := h.nodeRun(run.ID, "task").Status; got != domain.NodeRunWaitingCallback {
		t.Fatalf("node run after the replay = %s, want WAITING_CALLBACK", got)
	}

	// The Provider still holds the first Attempt's credential, and may use it.
	if _, err := h.svc.HandleCallback(h.ctx, service.HandleCallback{
		Token: outcome.Input.Callback.Token, ExternalTaskID: first.ExternalTask.ExternalTaskID, Payload: json.RawMessage(`"done"`),
	}); err != nil {
		t.Fatalf("callback with the first credential: %v", err)
	}
	h.pass(rec)
	if got := h.run(run.ID).Status; got != domain.RunCompleted {
		t.Fatalf("run = %s, want COMPLETED", got)
	}
}

// A dispatched Attempt depends on no process: the Binding routes its callback to whichever
// process is alive, so a crash changes nothing about it. It is never an orphan, and what
// ends it when no answer ever comes is the bound dispatch wrote.
func TestNodeOrphan_DispatchedWithNoDeadlineOrPoll_EndsAtTheMaximumWaitNotAsAnOrphan(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, waiting := h.holdSlot(h.linearDefinition(capModelID, nil))
	attemptID := h.attempts(waiting.ID)[0].ID

	rec := h.restart()
	h.clock.Advance(orphanLongAfter)
	report := h.pass(rec)
	h.pass(rec)

	if report.OrphanNodeAttemptsFound != 0 {
		t.Fatalf("report = %+v, want a dispatched Attempt never listed", report)
	}
	if got := h.attempt(attemptID); got.Status != domain.NodeAttemptDispatched {
		t.Fatalf("attempt = %s, want DISPATCHED", got.Status)
	}
	if got := h.nodeRun(run.ID, "task").Status; got != domain.NodeRunWaitingCallback {
		t.Fatalf("node run = %s, want WAITING_CALLBACK", got)
	}
	h.assertHeld(waiting)

	h.clock.Advance(orphanMaxWait - orphanLongAfter)
	report = h.pass(rec)

	if report.OrphanNodeAttemptsFound != 0 || report.AttemptsTimedOut != 1 {
		t.Fatalf("report = %+v, want the timeout scan to end it", report)
	}
	h.assertFailed(attemptID, "TIMEOUT", true)
	if got := h.run(run.ID).Status; got != domain.RunFailed {
		t.Fatalf("run at the bound = %s, want FAILED", got)
	}
	h.assertNotHeld(waiting)
}

func TestNodeOrphan_DispatchedWithPoll_PollResolvesTheOriginalAttempt(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run := h.createRun(h.chain(orphanPolledType, "", nil))
	h.drive(run.ID)
	waiting := h.nodeRun(run.ID, "task")
	if waiting.Status != domain.NodeRunWaitingCallback {
		t.Fatalf("task = %s, want WAITING_CALLBACK", waiting.Status)
	}

	rec := h.restart()
	h.clock.Advance(orphanLongAfter)
	report := h.pass(rec)

	if report.OrphanNodeAttemptsFound != 0 || report.PollsResumed != 1 {
		t.Fatalf("report = %+v, want the poll to resolve it and no orphan", report)
	}
	h.pass(rec) // The output Node the resume made READY.
	if got := h.run(run.ID).Status; got != domain.RunCompleted {
		t.Fatalf("run = %s, want COMPLETED", got)
	}
	if n := len(h.attempts(waiting.ID)); n != 1 {
		t.Fatalf("attempts = %d, want the original alone", n)
	}
}

// ---------------------------------------------------------------------------
// Provider slots
// ---------------------------------------------------------------------------

func TestNodeOrphan_TerminalFailure_FreesSlotForTheQueuedRunWithoutOverAdmission(t *testing.T) {
	h := newOrphanHarness(t, 1)
	def := h.linearDefinition(capModelID, nil)
	crashed, _ := h.claim(def, capTaskType)
	queued, queuedTask := h.queueRun(def)

	h.pass(h.restart())

	h.assertNotHeld(h.nodeRun(crashed.ID, "task"))
	admitted := h.nodeRun(queued.ID, "task")
	if admitted.Status != domain.NodeRunWaitingCallback {
		t.Fatalf("queued task after recovery = %s, want admitted and dispatched", admitted.Status)
	}
	h.assertHeld(queuedTask)
	if held := h.heldCount(); held != 1 {
		t.Fatalf("slots held = %d, want 1", held)
	}
}

func TestNodeOrphan_Retry_KeepsSlotAndAdmitsNothingElse(t *testing.T) {
	h := newOrphanHarness(t, 1)
	def := h.linearDefinition(capModelID, &domain.ExecutionPolicy{MaxAttempts: 2})
	crashed, _ := h.claim(def, capTaskType)
	_, queuedTask := h.queueRun(def)

	rec := h.restart()
	h.pass(rec)
	h.assertHeld(h.nodeRun(crashed.ID, "task"))
	h.assertQueued(queuedTask, 1)

	h.clock.Advance(orphanLongAfter)
	h.pass(rec)
	if got := h.nodeRun(crashed.ID, "task").Status; got != domain.NodeRunWaitingCallback {
		t.Fatalf("retried task = %s, want dispatched under the same slot", got)
	}
	h.assertHeld(h.nodeRun(crashed.ID, "task"))
	h.assertQueued(queuedTask, 1)
	if held := h.heldCount(); held != 1 {
		t.Fatalf("slots held = %d, want 1", held)
	}
}

// ---------------------------------------------------------------------------
// Live runner safety
// ---------------------------------------------------------------------------

func TestNodeOrphan_OwnInFlightAttempt_NotTreatedAsOrphan(t *testing.T) {
	h := newOrphanHarness(t, 1)
	h.safe.inFlight = make(chan struct{}, 1)
	h.safe.release = make(chan struct{})
	run, outcome := h.claim(h.chain(orphanSyncSafeType, "", nil), orphanSyncSafeType)

	executed := make(chan error, 1)
	go func() { executed <- h.svc.Execute(h.ctx, outcome) }()
	<-h.safe.inFlight

	if report := h.pass(h.reconcilerAs(orphanRunnerBefore, 100)); report.OrphanNodeAttemptsFound != 0 {
		t.Fatalf("report = %+v, want this process's own call not listed", report)
	}
	// Even a Reconciler wired with another identity cannot fail it: the use case checks
	// the Attempt's runner against its own.
	if report := h.pass(h.reconcilerAs("runner-misconfigured", 100)); report.OrphanNodeAttemptsFound != 1 {
		t.Fatalf("misconfigured report = %+v, want the Attempt handed over", report)
	}
	if got := h.attempt(outcome.AttemptID).Status; got != domain.NodeAttemptStarted {
		t.Fatalf("attempt while in flight = %s, want STARTED", got)
	}

	close(h.safe.release)
	if err := <-executed; err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := h.attempt(outcome.AttemptID).Status; got != domain.NodeAttemptSucceeded {
		t.Fatalf("attempt after the call returned = %s, want SUCCEEDED", got)
	}
	h.drive(run.ID)
	if got := h.run(run.ID).Status; got != domain.RunCompleted {
		t.Fatalf("run = %s, want COMPLETED", got)
	}
}

func TestNodeOrphan_ReconcilerWithoutRunnerIdentity_SkipsTheScan(t *testing.T) {
	h := newOrphanHarness(t, 1)
	_, outcome := h.claim(h.chain(orphanSyncSafeType, "", nil), orphanSyncSafeType)

	h.restart()
	report := h.pass(h.reconcilerAs("", 100))

	if report.OrphanNodeAttemptsFound != 0 {
		t.Fatalf("report = %+v, want no scan without an identity to compare against", report)
	}
	if got := h.attempt(outcome.AttemptID).Status; got != domain.NodeAttemptStarted {
		t.Fatalf("attempt = %s, want STARTED", got)
	}
}

func TestNodeOrphan_AttemptWithNoRecordedRunner_NeverTaken(t *testing.T) {
	h := newOrphanHarness(t, 1)
	_, outcome := h.claim(h.chain(orphanSyncSafeType, "", nil), orphanSyncSafeType)
	if _, err := h.pool.Exec(h.ctx, `UPDATE node_attempts SET runner_id = NULL WHERE id = $1`, outcome.AttemptID); err != nil {
		t.Fatalf("clear runner: %v", err)
	}

	rec := h.restart()
	if report := h.pass(rec); report.OrphanNodeAttemptsFound != 0 {
		t.Fatalf("report = %+v, want a row with no recorded process not listed", report)
	}
	if err := h.svc.HandleOrphanNodeAttempt(h.ctx, outcome.AttemptID); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if got := h.attempt(outcome.AttemptID).Status; got != domain.NodeAttemptStarted {
		t.Fatalf("attempt = %s, want STARTED", got)
	}
}

// An Attempt with a deadline keeps ending through the Node timeout: its orphan handling
// would race a rule that already bounds it, and would end the Attempt earlier than the
// Definition allows.
func TestNodeOrphan_AttemptWithDeadline_LeftToTheTimeout(t *testing.T) {
	h := newOrphanHarness(t, 1)
	policy := &domain.ExecutionPolicy{TimeoutMs: 60_000}
	_, outcome := h.claim(h.chain(orphanSyncSafeType, "", policy), orphanSyncSafeType)

	rec := h.restart()
	if report := h.pass(rec); report.OrphanNodeAttemptsFound != 0 {
		t.Fatalf("report = %+v, want an Attempt with a deadline not listed", report)
	}
	if err := h.svc.HandleOrphanNodeAttempt(h.ctx, outcome.AttemptID); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if got := h.attempt(outcome.AttemptID).Status; got != domain.NodeAttemptStarted {
		t.Fatalf("attempt before its deadline = %s, want STARTED", got)
	}

	h.clock.Advance(2 * time.Minute)
	if report := h.pass(rec); report.AttemptsTimedOut != 1 {
		t.Fatalf("report = %+v, want the timeout to end it", report)
	}
	h.assertFailed(outcome.AttemptID, "TIMEOUT", false)
}

// ---------------------------------------------------------------------------
// Races
// ---------------------------------------------------------------------------

func TestNodeOrphan_RepeatedScans_HandleOnce(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, outcome := h.claim(h.chain(orphanSyncSafeType, "", nil), orphanSyncSafeType)

	rec := h.restart()
	h.pass(rec)
	eventsAfterFirst := len(h.events(run.ID))
	if report := h.pass(rec); report.OrphanNodeAttemptsFound != 0 {
		t.Fatalf("second report = %+v, want the row gone from the scan", report)
	}
	if err := h.svc.HandleOrphanNodeAttempt(h.ctx, outcome.AttemptID); err != nil {
		t.Fatalf("handle a resolved attempt: %v", err)
	}
	if got := len(h.events(run.ID)); got != eventsAfterFirst {
		t.Fatalf("events = %d, want still %d", got, eventsAfterFirst)
	}
}

func TestNodeOrphan_ConcurrentRecovery_OneWinner(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, outcome := h.claim(h.chain(orphanSyncSafeType, "", &domain.ExecutionPolicy{MaxAttempts: 2}), orphanSyncSafeType)
	h.restart()
	second := h.newService()

	release := h.holdRunLock(run.ID)
	const racers = 3
	errs := make(chan error, racers)
	for i := 0; i < racers; i++ {
		svc := h.svc
		if i%2 == 1 {
			svc = second
		}
		go func() { errs <- svc.HandleOrphanNodeAttempt(h.ctx, outcome.AttemptID) }()
	}
	h.waitForBlocked(racers)
	release()
	for i := 0; i < racers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("racer %d: %v", i, err)
		}
	}

	h.assertFailed(outcome.AttemptID, orphanCodeInterrupted, false)
	if n := h.eventCount(run.ID, domain.EventNodeRetrying); n != 1 {
		t.Fatalf("NODE_RETRYING events = %d, want exactly 1", n)
	}
	if n := len(h.attempts(outcome.NodeRunID)); n != 1 {
		t.Fatalf("attempts = %d, want 1", n)
	}
}

// The single-process premise can be violated by an operator. Even then the old process's
// late result and recovery contend on one conditional update.
func TestNodeOrphan_LateResultOfOldProcessRacingRecovery_OneWinner(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, outcome := h.claim(h.chain(orphanSyncSafeType, "", nil), orphanSyncSafeType)
	old := h.svc
	h.restart()

	release := h.holdRunLock(run.ID)
	errs := make(chan error, 2)
	go func() { errs <- old.Execute(h.ctx, outcome) }()
	go func() { errs <- h.svc.HandleOrphanNodeAttempt(h.ctx, outcome.AttemptID) }()
	h.waitForBlocked(2)
	release()
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("racer %d: %v", i, err)
		}
	}

	failed := h.eventCount(run.ID, domain.EventNodeFailed)
	completed := len(asyncNodeEventsOfType(h.events(run.ID), outcome.NodeRunID, domain.EventNodeCompleted))
	if failed+completed != 1 {
		t.Fatalf("NODE_FAILED=%d NODE_COMPLETED=%d, want exactly one outcome", failed, completed)
	}
	nr := h.nodeRun(run.ID, "task")
	attempt := h.attempt(outcome.AttemptID)
	if failed == 1 && (nr.Status != domain.NodeRunFailed || attempt.Status != domain.NodeAttemptFailed) {
		t.Fatalf("recovery won but node run = %s attempt = %s", nr.Status, attempt.Status)
	}
	if completed == 1 && (nr.Status != domain.NodeRunSucceeded || attempt.Status != domain.NodeAttemptSucceeded) {
		t.Fatalf("the result won but node run = %s attempt = %s", nr.Status, attempt.Status)
	}
}

// A dispatch that commits after recovery finds its Attempt gone: it writes nothing and
// creates no route, so the task's callback has nothing to resume.
func TestNodeOrphan_LateDispatchAfterRecovery_CreatesNoRoute(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, outcome := h.claim(h.linearDefinition(capModelID, nil), capTaskType)
	old := h.svc

	h.pass(h.restart())
	h.assertFailed(outcome.AttemptID, orphanCodeUncertain, true)
	events := len(h.events(run.ID))

	if err := old.Execute(h.ctx, outcome); err != nil {
		t.Fatalf("late dispatch: %v", err)
	}
	if got := len(h.events(run.ID)); got != events {
		t.Fatalf("events after the late dispatch = %d, want still %d", got, events)
	}
	callback := h.callback(outcome.NodeRunID, `{"status":"SUCCEEDED","text":"late"}`)
	if !callback.Pending {
		t.Fatalf("callback = %+v, want stored with no route", callback)
	}
	if got := h.nodeRun(run.ID, "task").Status; got != domain.NodeRunFailed {
		t.Fatalf("node run = %s, want FAILED", got)
	}
}

// Recovery, the old process's dispatch and the Provider's callback all arrive together.
// Whichever of the first two commits decides: a committed dispatch lets the callback
// complete the Attempt, and a committed recovery leaves the callback without a route.
func TestNodeOrphan_CallbackRacingRecovery_OneWinner(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, outcome := h.claim(h.linearDefinition(capModelID, nil), capTaskType)
	old := h.svc
	h.restart()
	externalTaskID := fmt.Sprintf("task-%s-%d", outcome.NodeRunID, outcome.AttemptNo)

	release := h.holdRunLock(run.ID)
	errs := make(chan error, 3)
	go func() { errs <- old.Execute(h.ctx, outcome) }()
	go func() { errs <- h.svc.HandleOrphanNodeAttempt(h.ctx, outcome.AttemptID) }()
	h.waitForBlocked(2)
	go func() {
		_, err := h.svc.HandleCallback(h.ctx, service.HandleCallback{
			Token: outcome.Input.Callback.Token, ExternalTaskID: externalTaskID,
			Payload: json.RawMessage(`{"status":"SUCCEEDED","text":"raced"}`),
		})
		errs <- err
	}()
	release()
	for i := 0; i < 3; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("racer %d: %v", i, err)
		}
	}
	h.pass(h.reconcilerAs(orphanRunnerAfter, 100))

	nr := h.nodeRun(run.ID, "task")
	attempt := h.attempt(outcome.AttemptID)
	switch attempt.Status {
	case domain.NodeAttemptSucceeded:
		if nr.Status != domain.NodeRunSucceeded {
			t.Fatalf("attempt SUCCEEDED but node run = %s", nr.Status)
		}
	case domain.NodeAttemptFailed:
		if nr.Status != domain.NodeRunFailed {
			t.Fatalf("attempt FAILED but node run = %s", nr.Status)
		}
		if status := asyncPendingStatus(h.ctx, t, h.uow, externalTaskID); status != "stored" {
			t.Fatalf("pending callback = %s, want stored and unconsumed", status)
		}
	default:
		t.Fatalf("attempt = %s, want a single terminal outcome", attempt.Status)
	}
	h.assertNotHeld(nr)
	if n := h.eventCount(run.ID, domain.EventNodeFailed) + len(asyncNodeEventsOfType(h.events(run.ID), nr.ID, domain.EventNodeCompleted)); n != 1 {
		t.Fatalf("terminal node events = %d, want 1", n)
	}
}

func TestNodeOrphan_EventInsertFails_RollsBackAndNextPassRecovers(t *testing.T) {
	h := newOrphanHarness(t, 1)
	run, outcome := h.claim(h.linearDefinition(capModelID, nil), capTaskType)
	rec := h.restart()

	const collidingID = "evt_node_orphan_collision"
	execAppendFixedEvent(h.ctx, t, h.uow, run.ID, collidingID, h.clock.Now())
	eventsBefore := len(h.events(run.ID))
	h.ids.ForceNextEventID(collidingID)

	if err := h.svc.HandleOrphanNodeAttempt(h.ctx, outcome.AttemptID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("handle with a forced Event ID collision: err = %v, want domain.ErrConflict", err)
	}
	if got := h.attempt(outcome.AttemptID); got.Status != domain.NodeAttemptStarted || got.Error != nil {
		t.Fatalf("attempt after rollback = %+v, want untouched STARTED", got)
	}
	h.assertHeld(h.nodeRun(run.ID, "task"))
	if got := len(h.events(run.ID)); got != eventsBefore {
		t.Fatalf("events after rollback = %d, want still %d", got, eventsBefore)
	}

	h.pass(rec)
	h.assertFailed(outcome.AttemptID, orphanCodeUncertain, true)
	h.assertNotHeld(h.nodeRun(run.ID, "task"))
}

// ---------------------------------------------------------------------------
// A Run that already ended
// ---------------------------------------------------------------------------

// A failure of an Attempt on a Run another branch already failed must end the Attempt and
// its NodeRun and free the slot, whichever entry reports it. Retrying would need the Run
// back in RUNNING, and nothing claims work of an ended Run anyway.
func TestNodeFailure_OnRunAnotherBranchFailed_EndsNodeWithoutMovingRun(t *testing.T) {
	cases := []struct {
		name    string
		policy  domain.ExecutionPolicy
		restart bool
		report  func(h *orphanHarness, outcome service.AdvanceOutcome)
		code    string
	}{
		{
			name:    "orphan recovery",
			policy:  domain.ExecutionPolicy{MaxAttempts: 3},
			restart: true,
			report: func(h *orphanHarness, _ service.AdvanceOutcome) {
				h.pass(h.reconcilerAs(orphanRunnerAfter, 100))
			},
			code: orphanCodeUncertain,
		},
		{
			name:    "deadline",
			policy:  domain.ExecutionPolicy{MaxAttempts: 3, TimeoutMs: 1000},
			restart: true,
			report: func(h *orphanHarness, _ service.AdvanceOutcome) {
				h.clock.Advance(time.Minute)
				h.pass(h.reconcilerAs(orphanRunnerAfter, 100))
			},
			code: "TIMEOUT",
		},
		{
			name:   "in-process failure",
			policy: domain.ExecutionPolicy{MaxAttempts: 3},
			report: func(h *orphanHarness, outcome service.AdvanceOutcome) {
				if err := h.svc.FailNode(h.ctx, service.FailNode{
					AttemptID: outcome.AttemptID,
					Error:     domain.ExecutionError{Code: "EXECUTOR_ERROR", Message: "failed"},
					Source:    domain.FailureSyncExecution,
				}); err != nil {
					h.t.Fatalf("fail node: %v", err)
				}
			},
			code: "EXECUTOR_ERROR",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOrphanHarness(t, 1)
			run, outcome := h.endRunThroughSibling(h.siblings(&tc.policy))
			before := h.run(run.ID)
			if before.Error == nil || before.Error.Code != "PROVIDER_TASK_FAILED" || before.CompletedAt == nil {
				t.Fatalf("run before = %+v, want ended by the sibling", before)
			}
			h.assertHeld(h.nodeRun(run.ID, "z_task"))
			if tc.restart {
				h.restart()
			}

			tc.report(h, outcome)

			attempt := h.attempt(outcome.AttemptID)
			if attempt.Status != domain.NodeAttemptFailed || attempt.Error == nil || attempt.Error.Code != tc.code {
				t.Fatalf("attempt = %s error=%+v, want FAILED with %s", attempt.Status, attempt.Error, tc.code)
			}
			task := h.nodeRun(run.ID, "z_task")
			if task.Status != domain.NodeRunFailed || task.NextAttemptAt != nil {
				t.Fatalf("node run = %s next=%v, want FAILED with no retry", task.Status, task.NextAttemptAt)
			}
			h.assertNotHeld(task)
			if n := h.eventCount(run.ID, domain.EventNodeRetrying); n != 0 {
				t.Fatalf("NODE_RETRYING events = %d, want 0", n)
			}
			if n := h.eventCount(run.ID, domain.EventRunFailed); n != 1 {
				t.Fatalf("RUN_FAILED events = %d, want the sibling's alone", n)
			}
			if n := h.eventCount(run.ID, domain.EventRunResumed); n != 0 {
				t.Fatalf("RUN_RESUMED events = %d, want 0", n)
			}
			after := h.run(run.ID)
			if after.Status != domain.RunFailed || after.Error == nil || after.Error.Code != "PROVIDER_TASK_FAILED" {
				t.Fatalf("run after = %s error=%+v, want the sibling's failure unchanged", after.Status, after.Error)
			}
			if !after.CompletedAt.Equal(*before.CompletedAt) {
				t.Fatalf("run ended at %v, want still %v", after.CompletedAt, before.CompletedAt)
			}
		})
	}
}

// The scan is not limited to live Runs, and a handled row leaves it, so rows of ended Runs
// can neither be missed nor fill the batch.
func TestNodeOrphan_RowsOfEndedRuns_ListedAndLeaveTheScanOnceHandled(t *testing.T) {
	h := newOrphanHarness(t, 2)
	policy := &domain.ExecutionPolicy{MaxAttempts: 3}
	first, firstOutcome := h.endRunThroughSibling(h.siblings(policy))
	second, secondOutcome := h.endRunThroughSibling(h.siblings(policy))

	rec := h.restart()
	limited := h.reconcilerAs(orphanRunnerAfter, 1)
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		orphans := h.listOrphans(orphanRunnerAfter, 1)
		if len(orphans) != 1 || seen[orphans[0].ID] {
			t.Fatalf("pass %d listed %+v after %v, want one new row", i, orphans, seen)
		}
		seen[orphans[0].ID] = true
		if report := h.pass(limited); report.OrphanNodeAttemptsHandled != 1 {
			t.Fatalf("pass %d report = %+v, want one handled", i, report)
		}
	}
	if report := h.pass(rec); report.OrphanNodeAttemptsFound != 0 {
		t.Fatalf("final report = %+v, want the scan empty", report)
	}
	if !seen[firstOutcome.AttemptID] || !seen[secondOutcome.AttemptID] {
		t.Fatalf("handled %v, want %s and %s", seen, firstOutcome.AttemptID, secondOutcome.AttemptID)
	}
	for _, run := range []domain.Run{first, second} {
		if got := h.nodeRun(run.ID, "z_task").Status; got != domain.NodeRunFailed {
			t.Fatalf("node run of %s = %s, want FAILED", run.ID, got)
		}
	}
	if held := h.heldCount(); held != 0 {
		t.Fatalf("slots held = %d, want none", held)
	}
}

// An orphan that fails the Run reaches the same Run-end handling as any other failure: an
// ACTIVE release occupation is handed to post-task reconciliation in that transaction.
func TestNodeOrphan_EndsRunHoldingAnOccupation_OccupationScheduled(t *testing.T) {
	f := newReleaseFixture(t, releaseFixtureOptions{failingSibling: true})
	action := f.waitOnRelease(t, "wf_node_orphan_occupation")
	outcome := f.claimSibling(t)
	if status := agentRunRow(f.h.ctx, t, f.h.uow, f.run.ID).Status; status.IsTerminal() {
		t.Fatalf("run before the crash = %s, want alive", status)
	}

	restarted := f.restart(t, service.PollHooks{})
	if err := restarted.h.svc.HandleOrphanNodeAttempt(restarted.h.ctx, outcome.AttemptID); err != nil {
		t.Fatalf("handle orphaned sibling: %v", err)
	}

	if status := agentRunRow(f.h.ctx, t, f.h.uow, f.run.ID).Status; status != domain.RunFailed {
		t.Fatalf("run after the orphan = %s, want FAILED", status)
	}
	restarted.assertScheduledByRunEnd(t, action)
}

// ---------------------------------------------------------------------------
// Store and migration
// ---------------------------------------------------------------------------

func TestNodeAttemptStore_ListOrphanStarted_ReturnsOnlyStartedAttemptsOfOtherRunnersWithoutDeadline(t *testing.T) {
	h := newOrphanHarness(t, 10)

	_, orphan := h.claim(h.chain(orphanSyncSafeType, "", nil), orphanSyncSafeType)
	h.clock.Advance(time.Second)
	_, laterOrphan := h.claim(h.chain(orphanSyncSafeType, "", nil), orphanSyncSafeType)
	h.claim(h.chain(orphanSyncSafeType, "", &domain.ExecutionPolicy{TimeoutMs: 1000}), orphanSyncSafeType)
	dispatched := h.createRun(h.linearDefinition(capFreeModelID, nil))
	h.drive(dispatched.ID)
	_, unrecorded := h.claim(h.chain(orphanSyncSafeType, "", nil), orphanSyncSafeType)
	if _, err := h.pool.Exec(h.ctx, `UPDATE node_attempts SET runner_id = NULL WHERE id = $1`, unrecorded.AttemptID); err != nil {
		t.Fatalf("clear runner: %v", err)
	}
	h.runnerID = orphanRunnerAfter
	h.svc = h.newService()
	h.claim(h.chain(orphanSyncSafeType, "", nil), orphanSyncSafeType)

	got := h.listOrphans(orphanRunnerAfter, 10)
	if len(got) != 2 || got[0].ID != orphan.AttemptID || got[1].ID != laterOrphan.AttemptID {
		t.Fatalf("orphans = %+v, want %s then %s", got, orphan.AttemptID, laterOrphan.AttemptID)
	}
	if bounded := h.listOrphans(orphanRunnerAfter, 1); len(bounded) != 1 || bounded[0].ID != orphan.AttemptID {
		t.Fatalf("bounded orphans = %+v, want the oldest", bounded)
	}
}

// The process that applies this migration is the only one running, so a STARTED Attempt
// with no deadline from before it belongs to a process that is gone. Without the backfill
// nothing would ever end those rows.
func TestMigrations_NodeAttemptRunner_BackfillsOnlyStartedAttemptsWithNoDeadline(t *testing.T) {
	ctx := context.Background()
	pool := testdb.OpenUnmigrated(t)

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose dialect: %v", err)
	}
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	if err := goose.UpToContext(ctx, db, ".", 20); err != nil {
		t.Fatalf("apply the schema before the runner column: %v", err)
	}

	uow := postgres.NewUnitOfWork(pool)
	f := seedLegacyRun(ctx, t, pool, uow)
	if _, err := pool.Exec(ctx, `
		INSERT INTO node_attempts (id, node_run_id, attempt_no, status, input, started_at, deadline_at, dispatched_at, completed_at)
		VALUES ('a_stuck',    $1, 1, 'STARTED',    '{}', $2, NULL, NULL, NULL),
		       ('a_deadline', $1, 2, 'STARTED',    '{}', $2, $2,   NULL, NULL),
		       ('a_waiting',  $1, 3, 'DISPATCHED', '{}', $2, NULL, $2,   NULL),
		       ('a_done',     $1, 4, 'SUCCEEDED',  '{}', $2, NULL, NULL, $2)`, f.nodeRunID, fixtureTime); err != nil {
		t.Fatalf("insert attempts: %v", err)
	}

	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var listed []domain.NodeAttempt
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		listed, err = tx.NodeAttempts().ListOrphanStarted(ctx, orphanRunnerAfter, 10)
		return err
	}); err != nil {
		t.Fatalf("list orphans: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != "a_stuck" {
		t.Fatalf("orphans after the migration = %+v, want the stuck row alone", listed)
	}
	var unrecorded int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM node_attempts WHERE runner_id IS NULL`).Scan(&unrecorded); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if unrecorded != 3 {
		t.Fatalf("rows with no recorded process = %d, want the other three", unrecorded)
	}
}
