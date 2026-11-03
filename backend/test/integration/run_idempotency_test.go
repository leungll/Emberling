//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
)

const idemInput = `{"brief":"a small ember creature"}`

// idemRecordingQueue records every post-COMMIT offer of a Run to the work queue and
// accepts none, so the test drives nothing further and only observes the offers.
type idemRecordingQueue struct {
	mu       sync.Mutex
	advances []string
}

func (q *idemRecordingQueue) EnqueueAdvance(runID string) bool {
	q.mu.Lock()
	q.advances = append(q.advances, runID)
	q.mu.Unlock()
	return false
}

func (q *idemRecordingQueue) EnqueueAgentTurn(string, string) bool   { return false }
func (q *idemRecordingQueue) EnqueueAgentAction(string, string) bool { return false }

func (q *idemRecordingQueue) snapshot() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.advances...)
}

// idemKeyReadBarrier holds every creation transaction right after its read by submission
// key until `parties` transactions have made that read, so all of them miss the key and
// race on the insert. Reads beyond the first `parties` (the loser's read of the winner
// after its rollback) pass straight through.
type idemKeyReadBarrier struct {
	parties int

	mu      sync.Mutex
	arrived int
	all     chan struct{}
}

func newIdemKeyReadBarrier(parties int) *idemKeyReadBarrier {
	return &idemKeyReadBarrier{parties: parties, all: make(chan struct{})}
}

func (b *idemKeyReadBarrier) arrive(ctx context.Context) error {
	b.mu.Lock()
	if b.arrived >= b.parties {
		b.mu.Unlock()
		return nil
	}
	b.arrived++
	if b.arrived == b.parties {
		close(b.all)
	}
	b.mu.Unlock()

	select {
	case <-b.all:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// idemBarrierUoW wraps the real Unit of Work so the Run repository of every transaction
// stops at the barrier after its read by submission key. Everything else is the real
// PostgreSQL Store.
type idemBarrierUoW struct {
	store.UnitOfWork
	barrier *idemKeyReadBarrier
}

func (u idemBarrierUoW) WithinTx(ctx context.Context, fn func(ctx context.Context, tx store.Tx) error) error {
	return u.UnitOfWork.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return fn(ctx, idemBarrierTx{Tx: tx, barrier: u.barrier})
	})
}

type idemBarrierTx struct {
	store.Tx
	barrier *idemKeyReadBarrier
}

func (t idemBarrierTx) Runs() store.RunRepository {
	return idemBarrierRuns{RunRepository: t.Tx.Runs(), barrier: t.barrier}
}

type idemBarrierRuns struct {
	store.RunRepository
	barrier *idemKeyReadBarrier
}

func (r idemBarrierRuns) GetByIdempotencyKey(ctx context.Context, workflowID, key string) (domain.Run, error) {
	run, err := r.RunRepository.GetByIdempotencyKey(ctx, workflowID, key)
	if waitErr := r.barrier.arrive(ctx); waitErr != nil {
		return domain.Run{}, waitErr
	}
	return run, err
}

// idemService builds an ExecutionService over the harness's registries with its own
// Unit of Work and a recording queue.
func idemService(h *execHarness, uow store.UnitOfWork, queue *idemRecordingQueue) *service.ExecutionService {
	return service.NewExecutionService(service.Deps{
		UoW: uow, Nodes: h.nodes, Models: h.models, Compiler: h.compiler,
		Clock: h.clock, IDs: h.ids, Queue: queue,
	})
}

func idemSubmit(ctx context.Context, svc *service.ExecutionService, workflowID string, version int, input, key string) (service.CreateRunResult, error) {
	return svc.SubmitRun(ctx, service.CreateRun{
		WorkflowID: workflowID, DefinitionVersion: version, Input: json.RawMessage(input), IdempotencyKey: key,
	})
}

// idemRunIDs lists every Run of a Workflow, through the Run list read model.
func idemRunIDs(ctx context.Context, t *testing.T, uow store.UnitOfWork, workflowID string) []string {
	t.Helper()
	var ids []string
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		summaries, err := tx.RunList().ListSummaries(ctx, store.RunListFilter{WorkflowID: workflowID}, nil, store.RunsOlder, 100)
		if err != nil {
			return err
		}
		for _, summary := range summaries {
			ids = append(ids, summary.ID)
		}
		return nil
	}); err != nil {
		t.Fatalf("list runs of %s: %v", workflowID, err)
	}
	return ids
}

func idemCountEvents(events []domain.Event, typ domain.EventType) int {
	n := 0
	for _, ev := range events {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

func TestSubmitRun_ConcurrentSameKeySameContent_CreatesOneRunAndOneRunCreated(t *testing.T) {
	h := newExecHarness(t)
	h.saveDefinition(execDocDefinition("wf_idem_concurrent"))

	const parties = 2
	queue := &idemRecordingQueue{}
	svc := idemService(h, idemBarrierUoW{UnitOfWork: h.uow, barrier: newIdemKeyReadBarrier(parties)}, queue)

	ctx, cancel := context.WithTimeout(h.ctx, 30*time.Second)
	defer cancel()

	results := make([]service.CreateRunResult, parties)
	errs := make([]error, parties)
	var done sync.WaitGroup
	done.Add(parties)
	for i := 0; i < parties; i++ {
		go func(i int) {
			defer done.Done()
			results[i], errs[i] = idemSubmit(ctx, svc, "wf_idem_concurrent", 1, idemInput, "client-retry-1")
		}(i)
	}
	done.Wait()

	created, replayed := 0, 0
	for i := 0; i < parties; i++ {
		if errs[i] != nil {
			t.Fatalf("submission %d: unexpected error: %v", i, errs[i])
		}
		if results[i].Replayed {
			replayed++
		} else {
			created++
		}
	}
	if created != 1 || replayed != 1 {
		t.Fatalf("concurrent submissions: want 1 created and 1 replayed, got %d created and %d replayed", created, replayed)
	}
	if results[0].Run.ID != results[1].Run.ID {
		t.Fatalf("concurrent submissions: want the same Run, got %s and %s", results[0].Run.ID, results[1].Run.ID)
	}

	runIDs := idemRunIDs(h.ctx, t, h.uow, "wf_idem_concurrent")
	if len(runIDs) != 1 {
		t.Fatalf("runs persisted: want exactly 1, got %v", runIDs)
	}
	events := listEvents(h.ctx, t, h.uow, runIDs[0])
	if n := idemCountEvents(events, domain.EventRunCreated); n != 1 {
		t.Fatalf("RUN_CREATED events: want exactly 1, got %d (%v)", n, execEventTypes(events))
	}
	if n := idemCountEvents(events, domain.EventNodeReady); n != 1 {
		t.Fatalf("NODE_READY events: want exactly 1 (the loser's rolled back), got %d (%v)", n, execEventTypes(events))
	}
	if offers := queue.snapshot(); len(offers) != 1 || offers[0] != runIDs[0] {
		t.Fatalf("post-COMMIT queue offers: want only the creating submission's, got %v", offers)
	}
}

func TestSubmitRun_ReplaySameKeySameContent_ReturnsOriginalWithoutEventOrEnqueue(t *testing.T) {
	h := newExecHarness(t)
	h.saveDefinition(execDocDefinition("wf_idem_replay"))
	queue := &idemRecordingQueue{}
	svc := idemService(h, h.uow, queue)

	first, err := idemSubmit(h.ctx, svc, "wf_idem_replay", 1, idemInput, "replay-key")
	if err != nil {
		t.Fatalf("first submission: %v", err)
	}
	if first.Replayed {
		t.Fatal("first submission: want a created Run, got a replay")
	}
	eventsBefore := listEvents(h.ctx, t, h.uow, first.Run.ID)

	// Same content, different key order and whitespace.
	second, err := idemSubmit(h.ctx, svc, "wf_idem_replay", 1, ` { "brief" : "a small ember creature" } `, "replay-key")
	if err != nil {
		t.Fatalf("replayed submission: %v", err)
	}
	if !second.Replayed || second.Run.ID != first.Run.ID {
		t.Fatalf("replayed submission: want replay of %s, got replayed=%v id=%s", first.Run.ID, second.Replayed, second.Run.ID)
	}
	if second.Run.Status != domain.RunRunning || second.Run.DefinitionVersion != 1 {
		t.Fatalf("replayed submission: want the stored Run's current state, got %+v", second.Run)
	}

	if eventsAfter := listEvents(h.ctx, t, h.uow, first.Run.ID); len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("events after replay: want %d unchanged, got %d", len(eventsBefore), len(eventsAfter))
	}
	if offers := queue.snapshot(); len(offers) != 1 {
		t.Fatalf("queue offers: want only the first creation's offer, got %v", offers)
	}
}

func TestSubmitRun_SameKeyDifferentContent_MismatchAndNoNewRun(t *testing.T) {
	h := newExecHarness(t)
	h.saveDefinition(execDocDefinition("wf_idem_mismatch"))
	queue := &idemRecordingQueue{}
	svc := idemService(h, h.uow, queue)

	first, err := idemSubmit(h.ctx, svc, "wf_idem_mismatch", 1, idemInput, "mismatch-key")
	if err != nil {
		t.Fatalf("first submission: %v", err)
	}

	_, err = idemSubmit(h.ctx, svc, "wf_idem_mismatch", 1, `{"brief":"a different creature"}`, "mismatch-key")
	var mismatch *service.IdempotencyKeyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("same key, different input: want *service.IdempotencyKeyMismatchError, got %T: %v", err, err)
	}

	if ids := idemRunIDs(h.ctx, t, h.uow, "wf_idem_mismatch"); len(ids) != 1 || ids[0] != first.Run.ID {
		t.Fatalf("runs after mismatch: want only %s, got %v", first.Run.ID, ids)
	}
	if offers := queue.snapshot(); len(offers) != 1 {
		t.Fatalf("queue offers: want only the first creation's offer, got %v", offers)
	}
}

func TestSubmitRun_ReplayAfterNewDefinitionVersion_ReturnsOriginalVersionRun(t *testing.T) {
	h := newExecHarness(t)
	h.saveDefinition(execDocDefinition("wf_idem_versions"))
	svc := idemService(h, h.uow, &idemRecordingQueue{})

	first, err := idemSubmit(h.ctx, svc, "wf_idem_versions", 1, idemInput, "version-key")
	if err != nil {
		t.Fatalf("first submission: %v", err)
	}

	v2 := execDocDefinition("wf_idem_versions")
	v2.Version = 2
	h.saveDefinition(v2)

	replay, err := idemSubmit(h.ctx, svc, "wf_idem_versions", 1, idemInput, "version-key")
	if err != nil {
		t.Fatalf("replay after v2 saved: %v", err)
	}
	if !replay.Replayed || replay.Run.ID != first.Run.ID || replay.Run.DefinitionVersion != 1 {
		t.Fatalf("replay after v2 saved: want replay of %s on v1, got replayed=%v id=%s version=%d",
			first.Run.ID, replay.Replayed, replay.Run.ID, replay.Run.DefinitionVersion)
	}

	// The same key naming the new version is different content, not a new Run.
	_, err = idemSubmit(h.ctx, svc, "wf_idem_versions", 2, idemInput, "version-key")
	var mismatch *service.IdempotencyKeyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("same key naming v2: want *service.IdempotencyKeyMismatchError, got %T: %v", err, err)
	}
	if ids := idemRunIDs(h.ctx, t, h.uow, "wf_idem_versions"); len(ids) != 1 {
		t.Fatalf("runs: want only the original, got %v", ids)
	}
}

func TestSubmitRun_SameKeyDifferentWorkflows_CreatesIndependentRuns(t *testing.T) {
	h := newExecHarness(t)
	h.saveDefinition(execDocDefinition("wf_idem_left"))
	h.saveDefinition(execDocDefinition("wf_idem_right"))
	svc := idemService(h, h.uow, &idemRecordingQueue{})

	left, err := idemSubmit(h.ctx, svc, "wf_idem_left", 1, idemInput, "shared-key")
	if err != nil {
		t.Fatalf("left submission: %v", err)
	}
	right, err := idemSubmit(h.ctx, svc, "wf_idem_right", 1, `{"brief":"another creature"}`, "shared-key")
	if err != nil {
		t.Fatalf("right submission: %v", err)
	}
	if left.Replayed || right.Replayed || left.Run.ID == right.Run.ID {
		t.Fatalf("same key in two Workflows: want two created Runs, got left=%+v right=%+v", left, right)
	}
}

func TestSubmitRun_NoKey_CreatesNewRunEachTime(t *testing.T) {
	h := newExecHarness(t)
	h.saveDefinition(execDocDefinition("wf_idem_unkeyed"))
	queue := &idemRecordingQueue{}
	svc := idemService(h, h.uow, queue)

	first, err := idemSubmit(h.ctx, svc, "wf_idem_unkeyed", 1, idemInput, "")
	if err != nil {
		t.Fatalf("first submission: %v", err)
	}
	second, err := idemSubmit(h.ctx, svc, "wf_idem_unkeyed", 1, idemInput, "")
	if err != nil {
		t.Fatalf("second submission: %v", err)
	}
	if first.Replayed || second.Replayed || first.Run.ID == second.Run.ID {
		t.Fatalf("unkeyed submissions: want two created Runs, got %+v and %+v", first, second)
	}
	if got := getRun(h.ctx, t, h.uow, first.Run.ID); got.IdempotencyKey != "" || got.IdempotencyDigest != "" {
		t.Fatalf("unkeyed Run: want no key or digest stored, got key=%q digest=%q", got.IdempotencyKey, got.IdempotencyDigest)
	}
	if offers := queue.snapshot(); len(offers) != 2 {
		t.Fatalf("queue offers: want one per created Run, got %v", offers)
	}
}
