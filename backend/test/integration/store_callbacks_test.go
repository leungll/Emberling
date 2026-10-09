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
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// ---------------------------------------------------------------------------
// Callback Binding (external_task_id is globally unique; the binding, the DISPATCHED
// Attempt and the waiting state commit in the same transaction)
// ---------------------------------------------------------------------------

func TestCallbackBindings_DuplicateExternalTaskID_Rejected(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	f := seedRun(ctx, t, uow)
	_, attemptID := seedAttempt(ctx, t, uow, f, "one", domain.NodeRunWaitingCallback, true)
	_, otherAttemptID := seedAttempt(ctx, t, uow, f, "two", domain.NodeRunWaitingCallback, true)

	createBinding(ctx, t, uow, newBinding("binding_one", "provider_task_1", domain.CallbackTargetNodeAttempt, attemptID))

	// A second dispatch must never be able to steal an external identity that already
	// routes to another Attempt: the route would become ambiguous.
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.CallbackBindings().Create(ctx,
			newBinding("binding_two", "provider_task_1", domain.CallbackTargetNodeAttempt, otherAttemptID))
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Create duplicate external_task_id: want wrapped domain.ErrConflict, got %v", err)
	}

	got := getBinding(ctx, t, uow, "provider_task_1")
	if got.TargetID != attemptID {
		t.Fatalf("binding target after rejected duplicate: want %s, got %s", attemptID, got.TargetID)
	}
}

func TestCallbackBindings_BindingAndDispatchedAttempt_CommitTogether(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	f := seedRun(ctx, t, uow)
	_, attemptID := seedAttempt(ctx, t, uow, f, "one", domain.NodeRunRunning, false)

	// The dispatch transaction fails after both writes. Neither may survive: a
	// DISPATCHED Attempt without its binding is unroutable, and a binding without a
	// DISPATCHED Attempt routes a callback at a state that never waited for it.
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.NodeAttempts().MarkDispatched(ctx, attemptID, fixtureTime, nil); err != nil {
			return err
		}
		if err := tx.CallbackBindings().Create(ctx,
			newBinding("binding_one", "provider_task_1", domain.CallbackTargetNodeAttempt, attemptID)); err != nil {
			return err
		}
		return tx.CallbackBindings().Create(ctx,
			newBinding("binding_two", "provider_task_1", domain.CallbackTargetNodeAttempt, attemptID))
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("dispatch transaction: want wrapped domain.ErrConflict, got %v", err)
	}

	attempt := getAttempt(ctx, t, uow, attemptID)
	if attempt.Status != domain.NodeAttemptStarted {
		t.Fatalf("Attempt status after rollback: want STARTED, got %s", attempt.Status)
	}
	if attempt.DispatchedAt != nil {
		t.Fatalf("Attempt dispatched_at after rollback: want nil, got %v", attempt.DispatchedAt)
	}

	err = uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.CallbackBindings().GetByExternalTaskID(ctx, "provider_task_1")
		return err
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetByExternalTaskID after rollback: want wrapped domain.ErrNotFound, got %v", err)
	}
}

func TestCallbackBindings_ListByTargets_ReturnsOnlyRequestedAttempts(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	f := seedRun(ctx, t, uow)
	_, wantedA := seedAttempt(ctx, t, uow, f, "one", domain.NodeRunWaitingCallback, true)
	_, wantedB := seedAttempt(ctx, t, uow, f, "two", domain.NodeRunWaitingCallback, true)
	_, unwanted := seedAttempt(ctx, t, uow, f, "three", domain.NodeRunWaitingCallback, true)

	createBinding(ctx, t, uow, newBinding("binding_a", "provider_task_a", domain.CallbackTargetNodeAttempt, wantedA))
	createBinding(ctx, t, uow, newBinding("binding_b", "provider_task_b", domain.CallbackTargetNodeAttempt, wantedB))
	createBinding(ctx, t, uow, newBinding("binding_c", "provider_task_c", domain.CallbackTargetNodeAttempt, unwanted))
	// A Tool Attempt sharing the requested id must not leak into a Node projection.
	createBinding(ctx, t, uow, newBinding("binding_d", "provider_task_d", domain.CallbackTargetToolAttempt, wantedA))

	var got []domain.CallbackBinding
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		got, err = tx.CallbackBindings().ListByTargets(ctx, domain.CallbackTargetNodeAttempt,
			[]string{wantedA, wantedB})
		return err
	}); err != nil {
		t.Fatalf("ListByTargets: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("ListByTargets returned %d bindings, want 2: %+v", len(got), got)
	}
	for _, binding := range got {
		if binding.TargetType != domain.CallbackTargetNodeAttempt {
			t.Fatalf("ListByTargets returned target type %s, want NODE_ATTEMPT", binding.TargetType)
		}
		if binding.TargetID != wantedA && binding.TargetID != wantedB {
			t.Fatalf("ListByTargets returned unrequested target %s", binding.TargetID)
		}
		if binding.ProviderID == "" || binding.CreatedAt.IsZero() {
			t.Fatalf("ListByTargets returned an incompletely mapped binding: %+v", binding)
		}
	}
}

// ---------------------------------------------------------------------------
// Pending Callback (a repeated arrival only updates duplicate_count and the last received
// time without overwriting the first valid payload; consumption is a conditional update)
// ---------------------------------------------------------------------------

func TestPendingCallbacks_SecondArrival_IncrementsDuplicateCountAndKeepsFirstPayload(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	first := newPending("provider_task_1", `{"imageUrl":"https://example.test/first.png"}`, "sha256:first", "sha256:token_first")
	duplicate, err := recordPending(ctx, uow, first)
	if err != nil {
		t.Fatalf("Record first arrival: %v", err)
	}
	if duplicate {
		t.Fatal("Record first arrival: want duplicate=false, got true")
	}

	second := newPending("provider_task_1", `{"imageUrl":"https://example.test/second.png"}`, "sha256:second", "sha256:token_second")
	second.ReceivedAt = first.ReceivedAt.Add(2 * time.Second)
	duplicate, err = recordPending(ctx, uow, second)
	if err != nil {
		t.Fatalf("Record second arrival: %v", err)
	}
	if !duplicate {
		t.Fatal("Record second arrival: want duplicate=true, got false")
	}

	got := getPending(ctx, t, uow, "provider_task_1")
	if got.PayloadHash != "sha256:first" {
		t.Fatalf("payload_hash after duplicate: want the first payload's hash, got %s", got.PayloadHash)
	}
	// JSONB normalises whitespace, so the stored payload is compared by value.
	var storedPayload map[string]string
	if err := json.Unmarshal(got.Payload, &storedPayload); err != nil {
		t.Fatalf("unmarshal stored payload: %v", err)
	}
	if storedPayload["imageUrl"] != "https://example.test/first.png" {
		t.Fatalf("payload after duplicate: want the first payload, got %s", got.Payload)
	}
	if got.CallbackTokenHash != "sha256:token_first" {
		t.Fatalf("callback_token_hash after duplicate: want the first token hash, got %s", got.CallbackTokenHash)
	}
	if got.DuplicateCount != 1 {
		t.Fatalf("duplicate_count after one duplicate: want 1, got %d", got.DuplicateCount)
	}
	if !got.ReceivedAt.Equal(second.ReceivedAt) {
		t.Fatalf("received_at after duplicate: want %v, got %v", second.ReceivedAt, got.ReceivedAt)
	}
	if got.ConsumedAt != nil {
		t.Fatalf("consumed_at after duplicate: want nil, got %v", got.ConsumedAt)
	}
}

func TestPendingCallbacks_ConcurrentConsume_SucceedsOnce(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	if _, err := recordPending(ctx, uow, newPending("provider_task_1", `{"ok":true}`, "sha256:p", "sha256:t")); err != nil {
		t.Fatalf("Record: %v", err)
	}

	const consumers = 8
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(consumers)

	consumed := make([]bool, consumers)
	failures := make([]error, consumers)
	now := fixtureTime.Add(time.Minute)

	for i := range consumers {
		go func() {
			defer done.Done()
			// Barrier: the callback handler and the post-commit check race here without
			// a sleep manufacturing the ordering.
			start.Wait()

			failures[i] = uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
				pending, ok, err := tx.PendingCallbacks().ConsumeOnce(ctx, "provider_task_1", now)
				if err != nil {
					return err
				}
				if ok && pending.ExternalTaskID != "provider_task_1" {
					t.Errorf("ConsumeOnce returned external task id %q", pending.ExternalTaskID)
				}
				consumed[i] = ok
				return nil
			})
		}()
	}

	start.Done()
	done.Wait()

	winners := 0
	for i := range consumers {
		if failures[i] != nil {
			t.Fatalf("consumer %d: unexpected error %v", i, failures[i])
		}
		if consumed[i] {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("ConsumeOnce winners: want exactly 1, got %d", winners)
	}

	got := getPending(ctx, t, uow, "provider_task_1")
	if got.ConsumedAt == nil {
		t.Fatal("consumed_at after consumption: want set, got nil")
	}
	if !got.ConsumedAt.Equal(now) {
		t.Fatalf("consumed_at: want %v, got %v", now, *got.ConsumedAt)
	}
}

func TestPendingCallbacks_ConsumeOnce_ExpiredRow_NotConsumed(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	pending := newPending("provider_task_1", `{"ok":true}`, "sha256:p", "sha256:t")
	pending.ExpiresAt = fixtureTime.Add(time.Minute)
	if _, err := recordPending(ctx, uow, pending); err != nil {
		t.Fatalf("Record: %v", err)
	}

	after := pending.ExpiresAt.Add(time.Second)

	var ok bool
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		_, ok, err = tx.PendingCallbacks().ConsumeOnce(ctx, "provider_task_1", after)
		return err
	}); err != nil {
		t.Fatalf("ConsumeOnce on an expired row: %v", err)
	}
	if ok {
		t.Fatal("ConsumeOnce on an expired row: want ok=false, got true")
	}

	// The row stays untouched so it can still be audited before retention removes it.
	got := getPending(ctx, t, uow, "provider_task_1")
	if got.ConsumedAt != nil {
		t.Fatalf("consumed_at on an expired row: want nil, got %v", got.ConsumedAt)
	}

	var deleted int64
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		deleted, err = tx.PendingCallbacks().DeleteExpired(ctx, after, 10)
		return err
	}); err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("DeleteExpired removed %d rows, want 1", deleted)
	}

	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.PendingCallbacks().GetByExternalTaskID(ctx, "provider_task_1")
		return err
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetByExternalTaskID after retention: want wrapped domain.ErrNotFound, got %v", err)
	}
}

func TestPendingCallbacks_ListConsumableForWaiting_ReturnsOnlyRowsWithDispatchedAttemptAndWaitingNodeRun(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	f := seedRun(ctx, t, uow)
	now := fixtureTime.Add(time.Minute)

	// (1) The only row the Reconciler may hand to resume.
	matchNodeRunID, matchAttemptID := seedAttempt(ctx, t, uow, f, "match", domain.NodeRunWaitingCallback, true)
	createBinding(ctx, t, uow, newBinding("binding_match", "task_match", domain.CallbackTargetNodeAttempt, matchAttemptID))
	if _, err := recordPending(ctx, uow, newPending("task_match", `{"ok":true}`, "sha256:m", "sha256:t")); err != nil {
		t.Fatalf("Record matching pending: %v", err)
	}

	// (2) NodeRun never reached WAITING_CALLBACK: the dispatch transaction did not commit.
	_, runningAttemptID := seedAttempt(ctx, t, uow, f, "running", domain.NodeRunRunning, true)
	createBinding(ctx, t, uow, newBinding("binding_running", "task_running", domain.CallbackTargetNodeAttempt, runningAttemptID))
	if _, err := recordPending(ctx, uow, newPending("task_running", `{"ok":true}`, "sha256:r", "sha256:t")); err != nil {
		t.Fatalf("Record running pending: %v", err)
	}

	// (3) Already consumed: resume owns it, a second hand-off would be a double resume.
	_, consumedAttemptID := seedAttempt(ctx, t, uow, f, "consumed", domain.NodeRunWaitingCallback, true)
	createBinding(ctx, t, uow, newBinding("binding_consumed", "task_consumed", domain.CallbackTargetNodeAttempt, consumedAttemptID))
	if _, err := recordPending(ctx, uow, newPending("task_consumed", `{"ok":true}`, "sha256:c", "sha256:t")); err != nil {
		t.Fatalf("Record consumed pending: %v", err)
	}
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, ok, err := tx.PendingCallbacks().ConsumeOnce(ctx, "task_consumed", now)
		if err == nil && !ok {
			t.Error("seed: ConsumeOnce(task_consumed) did not consume")
		}
		return err
	}); err != nil {
		t.Fatalf("seed consume: %v", err)
	}

	// (4) Expired: past its retention window, it may not advance an Execution.
	_, expiredAttemptID := seedAttempt(ctx, t, uow, f, "expired", domain.NodeRunWaitingCallback, true)
	createBinding(ctx, t, uow, newBinding("binding_expired", "task_expired", domain.CallbackTargetNodeAttempt, expiredAttemptID))
	expired := newPending("task_expired", `{"ok":true}`, "sha256:e", "sha256:t")
	expired.ExpiresAt = now.Add(-time.Second)
	if _, err := recordPending(ctx, uow, expired); err != nil {
		t.Fatalf("Record expired pending: %v", err)
	}

	// (5) No binding at all: it is kept for audit only, it never routes.
	if _, err := recordPending(ctx, uow, newPending("task_unbound", `{"ok":true}`, "sha256:u", "sha256:t")); err != nil {
		t.Fatalf("Record unbound pending: %v", err)
	}

	var got []store.PendingForWaiting
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		got, err = tx.PendingCallbacks().ListConsumableForWaiting(ctx, now, 50)
		return err
	}); err != nil {
		t.Fatalf("ListConsumableForWaiting: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("ListConsumableForWaiting returned %d rows, want 1: %+v", len(got), got)
	}
	row := got[0]
	if row.Pending.ExternalTaskID != "task_match" {
		t.Fatalf("row external task id: want task_match, got %s", row.Pending.ExternalTaskID)
	}
	if row.Binding.ID != "binding_match" {
		t.Fatalf("row binding id: want binding_match, got %s", row.Binding.ID)
	}
	if row.RunID != f.runID || row.NodeRunID != matchNodeRunID || row.AttemptID != matchAttemptID {
		t.Fatalf("row routing: want (%s, %s, %s), got (%s, %s, %s)",
			f.runID, matchNodeRunID, matchAttemptID, row.RunID, row.NodeRunID, row.AttemptID)
	}
	if row.Pending.CallbackTokenHash != "sha256:t" {
		t.Fatalf("row callback token hash: want the persisted hash, got %q", row.Pending.CallbackTokenHash)
	}
}

// TestPendingCallbacks_ListConsumableForWaiting_ToolBranch_RequiresDispatchedAttemptWaitingActionAndNodeRun
// covers the TOOL_ATTEMPT branch of the Reconciler's Pending Callback discovery. A stored
// early callback bound to a Tool Attempt may be replayed only while all three facts the
// resume transaction conditionally updates still hold: the Tool Attempt is DISPATCHED,
// its Agent Action is WAITING_CALLBACK and the Agent NodeRun that owns the Action is
// WAITING_CALLBACK. A row whose dispatch transaction never committed, or whose Action was
// already resolved, must never be handed to resume. The returned route names the Agent
// NodeRun and the Tool Attempt.
func TestPendingCallbacks_ListConsumableForWaiting_ToolBranch_RequiresDispatchedAttemptWaitingActionAndNodeRun(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	f := seedRun(ctx, t, uow)
	now := fixtureTime.Add(time.Minute)

	cases := []struct {
		suffix         string
		nodeRunWaiting bool
		actionWaiting  bool
		dispatched     bool
	}{
		{suffix: "match", nodeRunWaiting: true, actionWaiting: true, dispatched: true},
		// The Action is still RUNNING: nothing on it is waiting for a callback.
		{suffix: "action_running", nodeRunWaiting: true, actionWaiting: false, dispatched: true},
		// The Agent NodeRun is not WAITING_CALLBACK: the Run is not paused on this Action.
		{suffix: "node_run_running", nodeRunWaiting: false, actionWaiting: true, dispatched: true},
		// The Tool Attempt is still STARTED: its external task was never confirmed.
		{suffix: "attempt_started", nodeRunWaiting: true, actionWaiting: true, dispatched: false},
	}
	var matchNodeRunID, matchAttemptID string
	for _, c := range cases {
		nodeRunID, attemptID := seedToolAttempt(ctx, t, uow, f, c.suffix, c.nodeRunWaiting, c.actionWaiting, c.dispatched)
		if c.suffix == "match" {
			matchNodeRunID, matchAttemptID = nodeRunID, attemptID
		}
		createBinding(ctx, t, uow, newBinding("binding_"+c.suffix, "task_"+c.suffix, domain.CallbackTargetToolAttempt, attemptID))
		if _, err := recordPending(ctx, uow, newPending("task_"+c.suffix, `{"status":"SUCCEEDED"}`, "sha256:"+c.suffix, "sha256:t")); err != nil {
			t.Fatalf("Record pending %s: %v", c.suffix, err)
		}
	}

	got := listConsumableForWaiting(ctx, t, uow, now, 50)
	if len(got) != 1 {
		t.Fatalf("ListConsumableForWaiting returned %d rows, want only task_match: %+v", len(got), got)
	}
	row := got[0]
	if row.Pending.ExternalTaskID != "task_match" || row.Binding.ID != "binding_match" {
		t.Fatalf("row = (%s, %s), want (task_match, binding_match)", row.Pending.ExternalTaskID, row.Binding.ID)
	}
	if row.Binding.TargetType != domain.CallbackTargetToolAttempt || row.Binding.TargetID != matchAttemptID {
		t.Fatalf("row binding target = (%s, %s), want (TOOL_ATTEMPT, %s)", row.Binding.TargetType, row.Binding.TargetID, matchAttemptID)
	}
	if row.RunID != f.runID || row.NodeRunID != matchNodeRunID || row.AttemptID != matchAttemptID {
		t.Fatalf("row routing = (%s, %s, %s), want (%s, %s, %s)",
			row.RunID, row.NodeRunID, row.AttemptID, f.runID, matchNodeRunID, matchAttemptID)
	}
}

// TestPendingCallbacks_ListConsumableForWaiting_MixedTargets_OrderedGloballyByReceivedAt
// covers the bound on one Reconciler batch: the Node and Tool branches form one
// result set under a single ORDER BY received_at and LIMIT, so the oldest rediscovered
// callback is replayed first whatever its target type, and a small batch never starves
// Tool callbacks behind Node ones.
func TestPendingCallbacks_ListConsumableForWaiting_MixedTargets_OrderedGloballyByReceivedAt(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	f := seedRun(ctx, t, uow)
	now := fixtureTime.Add(time.Minute)

	_, nodeAttemptID := seedAttempt(ctx, t, uow, f, "node", domain.NodeRunWaitingCallback, true)
	createBinding(ctx, t, uow, newBinding("binding_node", "task_node", domain.CallbackTargetNodeAttempt, nodeAttemptID))
	nodePending := newPending("task_node", `{"ok":true}`, "sha256:n", "sha256:t")
	nodePending.ReceivedAt = fixtureTime.Add(2 * time.Second)
	if _, err := recordPending(ctx, uow, nodePending); err != nil {
		t.Fatalf("Record node pending: %v", err)
	}

	_, toolAttemptID := seedToolAttempt(ctx, t, uow, f, "tool", true, true, true)
	createBinding(ctx, t, uow, newBinding("binding_tool", "task_tool", domain.CallbackTargetToolAttempt, toolAttemptID))
	toolPending := newPending("task_tool", `{"status":"SUCCEEDED"}`, "sha256:o", "sha256:t")
	toolPending.ReceivedAt = fixtureTime.Add(time.Second)
	if _, err := recordPending(ctx, uow, toolPending); err != nil {
		t.Fatalf("Record tool pending: %v", err)
	}

	first := listConsumableForWaiting(ctx, t, uow, now, 1)
	if len(first) != 1 || first[0].Pending.ExternalTaskID != "task_tool" {
		t.Fatalf("LIMIT 1 batch = %+v, want only the older Tool callback task_tool", first)
	}
	both := listConsumableForWaiting(ctx, t, uow, now, 10)
	if len(both) != 2 || both[0].Pending.ExternalTaskID != "task_tool" || both[1].Pending.ExternalTaskID != "task_node" {
		t.Fatalf("full batch = %+v, want [task_tool, task_node] by received_at", both)
	}
	if both[0].Binding.TargetType != domain.CallbackTargetToolAttempt || both[1].Binding.TargetType != domain.CallbackTargetNodeAttempt {
		t.Fatalf("target types = (%s, %s), want (TOOL_ATTEMPT, NODE_ATTEMPT)", both[0].Binding.TargetType, both[1].Binding.TargetType)
	}
}

// ---------------------------------------------------------------------------
// Conditional resume (UPDATE node_runs SET status='SUCCEEDED'
// WHERE id=$1 AND status='WAITING_CALLBACK')
// ---------------------------------------------------------------------------

func TestNodeRuns_MarkSucceeded_FromWaitingCallback_ConcurrentCompleters_ExactlyOneWins(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))

	f := seedRun(ctx, t, uow)
	nodeRunID, attemptID := seedAttempt(ctx, t, uow, f, "waiting", domain.NodeRunWaitingCallback, true)

	const completers = 8
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(completers)

	results := make([]error, completers)
	now := fixtureTime.Add(time.Minute)

	for i := range completers {
		go func() {
			defer done.Done()
			// Duplicate callback, Provider poll and Reconciler all arrive at once.
			start.Wait()

			results[i] = uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
				if _, err := tx.Runs().LockForUpdate(ctx, f.runID); err != nil {
					return err
				}
				if err := tx.NodeAttempts().MarkSucceeded(ctx, attemptID,
					domain.NodeAttemptDispatched, now, json.RawMessage(`{"imageUrl":"https://example.test/a.png"}`)); err != nil {
					return err
				}
				return tx.NodeRuns().MarkSucceeded(ctx, nodeRunID, domain.NodeRunWaitingCallback, now,
					store.NodeRunOutcome{Output: json.RawMessage(`{"imageUrl":"https://example.test/a.png"}`)})
			})
		}()
	}

	start.Done()
	done.Wait()

	winners := 0
	for i := range completers {
		switch {
		case results[i] == nil:
			winners++
		case errors.Is(results[i], domain.ErrStaleClaim):
		default:
			t.Fatalf("completer %d: want nil or domain.ErrStaleClaim, got %v", i, results[i])
		}
	}
	if winners != 1 {
		t.Fatalf("WAITING_CALLBACK -> SUCCEEDED winners: want exactly 1, got %d", winners)
	}

	nr := getNodeRun(ctx, t, uow, nodeRunID)
	if nr.Status != domain.NodeRunSucceeded {
		t.Fatalf("NodeRun status: want SUCCEEDED, got %s", nr.Status)
	}
	attempt := getAttempt(ctx, t, uow, attemptID)
	if attempt.Status != domain.NodeAttemptSucceeded {
		t.Fatalf("Attempt status: want SUCCEEDED, got %s", attempt.Status)
	}
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// seedAttempt commits one NodeRun advanced to nodeRunStatus plus its first Attempt,
// dispatched when dispatched is true. Every transition goes through the repositories so
// the fixture exercises the same state machine production code does.
func seedAttempt(
	ctx context.Context,
	t *testing.T,
	uow store.UnitOfWork,
	f fixture,
	suffix string,
	nodeRunStatus domain.NodeRunStatus,
	dispatched bool,
) (nodeRunID, attemptID string) {
	t.Helper()

	nodeRunID = "nr_" + suffix
	attemptID = "attempt_" + suffix

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.NodeRuns().Create(ctx, newNodeRun(nodeRunID, f.runID, "node_"+suffix)); err != nil {
			return err
		}
		if nodeRunStatus != domain.NodeRunReady {
			if _, err := tx.NodeRuns().ClaimReady(ctx, nodeRunID, fixtureTime); err != nil {
				return err
			}
		}
		if nodeRunStatus == domain.NodeRunWaitingCallback {
			if err := tx.NodeRuns().MarkWaiting(ctx, nodeRunID, fixtureTime); err != nil {
				return err
			}
		}
		if err := tx.NodeAttempts().Create(ctx, newNodeAttempt(attemptID, nodeRunID, 1)); err != nil {
			return err
		}
		if dispatched {
			return tx.NodeAttempts().MarkDispatched(ctx, attemptID, fixtureTime, nil)
		}
		return nil
	}); err != nil {
		t.Fatalf("seed attempt %s: %v", suffix, err)
	}
	return nodeRunID, attemptID
}

func newBinding(id, externalTaskID string, targetType domain.CallbackTargetType, targetID string) domain.CallbackBinding {
	return domain.CallbackBinding{
		ID:             id,
		ProviderID:     "mock-async-provider-v1",
		ExternalTaskID: externalTaskID,
		TargetType:     targetType,
		TargetID:       targetID,
		CreatedAt:      fixtureTime,
	}
}

func newPending(externalTaskID, payload, payloadHash, tokenHash string) domain.PendingCallback {
	return domain.PendingCallback{
		ExternalTaskID:    externalTaskID,
		Payload:           json.RawMessage(payload),
		PayloadHash:       payloadHash,
		CallbackTokenHash: tokenHash,
		ReceivedAt:        fixtureTime,
		ExpiresAt:         fixtureTime.Add(24 * time.Hour),
	}
}

func createBinding(ctx context.Context, t *testing.T, uow store.UnitOfWork, binding domain.CallbackBinding) {
	t.Helper()
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.CallbackBindings().Create(ctx, binding)
	}); err != nil {
		t.Fatalf("create binding %s: %v", binding.ID, err)
	}
}

func getBinding(ctx context.Context, t *testing.T, uow store.UnitOfWork, externalTaskID string) domain.CallbackBinding {
	t.Helper()
	var binding domain.CallbackBinding
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		binding, err = tx.CallbackBindings().GetByExternalTaskID(ctx, externalTaskID)
		return err
	}); err != nil {
		t.Fatalf("get binding %s: %v", externalTaskID, err)
	}
	return binding
}

func recordPending(ctx context.Context, uow store.UnitOfWork, pending domain.PendingCallback) (bool, error) {
	var duplicate bool
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		duplicate, err = tx.PendingCallbacks().Record(ctx, pending)
		return err
	})
	return duplicate, err
}

func getPending(ctx context.Context, t *testing.T, uow store.UnitOfWork, externalTaskID string) domain.PendingCallback {
	t.Helper()
	var pending domain.PendingCallback
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		pending, err = tx.PendingCallbacks().GetByExternalTaskID(ctx, externalTaskID)
		return err
	}); err != nil {
		t.Fatalf("get pending callback %s: %v", externalTaskID, err)
	}
	return pending
}

func getAttempt(ctx context.Context, t *testing.T, uow store.UnitOfWork, attemptID string) domain.NodeAttempt {
	t.Helper()
	var attempt domain.NodeAttempt
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		attempt, err = tx.NodeAttempts().Get(ctx, attemptID)
		return err
	}); err != nil {
		t.Fatalf("get attempt %s: %v", attemptID, err)
	}
	return attempt
}

// seedToolAttempt commits one Agent NodeRun with its own Agent Run, a decided Turn, its
// TOOL_CALL Action claimed to RUNNING and one Tool Attempt, advancing the NodeRun and the
// Action to WAITING_CALLBACK and the Attempt to DISPATCHED only where asked. Every
// transition goes through the repositories, as in seedAttempt.
func seedToolAttempt(
	ctx context.Context,
	t *testing.T,
	uow store.UnitOfWork,
	f fixture,
	suffix string,
	nodeRunWaiting, actionWaiting, dispatched bool,
) (nodeRunID, attemptID string) {
	t.Helper()

	nodeRunID = "nr_agent_" + suffix
	agentRunID := "ar_" + suffix
	turnID := "turn_" + suffix
	decisionID := "decision_" + suffix
	actionID := "action_" + suffix
	attemptID = "tool_attempt_" + suffix

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		nodeRun := newNodeRun(nodeRunID, f.runID, "node_agent_"+suffix)
		nodeRun.NodeType = "agent"
		if err := tx.NodeRuns().Create(ctx, nodeRun); err != nil {
			return err
		}
		if _, err := tx.NodeRuns().ClaimReady(ctx, nodeRunID, fixtureTime); err != nil {
			return err
		}
		if err := tx.AgentRuns().Create(ctx, newAgentRun(agentRunID, nodeRunID)); err != nil {
			return err
		}
		if err := tx.AgentTurns().Create(ctx, newAgentTurn(turnID, agentRunID, 1)); err != nil {
			return err
		}
		if _, err := tx.AgentTurns().ClaimReady(ctx, turnID, fixtureTime); err != nil {
			return err
		}
		if err := tx.AgentTurns().MarkCompleted(ctx, turnID, fixtureTime, json.RawMessage(`{"kind":"TOOL_CALL"}`), nil); err != nil {
			return err
		}
		if err := tx.AgentDecisions().Create(ctx, newAgentDecision(decisionID, turnID)); err != nil {
			return err
		}
		if err := tx.AgentActions().Create(ctx, newAgentAction(actionID, turnID, decisionID)); err != nil {
			return err
		}
		if _, err := tx.AgentActions().ClaimReady(ctx, actionID, fixtureTime); err != nil {
			return err
		}
		if err := tx.ToolAttempts().Create(ctx, newToolAttempt(attemptID, actionID, 1)); err != nil {
			return err
		}
		if dispatched {
			if err := tx.ToolAttempts().MarkDispatched(ctx, attemptID, fixtureTime); err != nil {
				return err
			}
		}
		if actionWaiting {
			if err := tx.AgentActions().MarkWaiting(ctx, actionID, fixtureTime); err != nil {
				return err
			}
		}
		if nodeRunWaiting {
			return tx.NodeRuns().MarkWaiting(ctx, nodeRunID, fixtureTime)
		}
		return nil
	}); err != nil {
		t.Fatalf("seed tool attempt %s: %v", suffix, err)
	}
	return nodeRunID, attemptID
}

func listConsumableForWaiting(ctx context.Context, t *testing.T, uow store.UnitOfWork, now time.Time, limit int) []store.PendingForWaiting {
	t.Helper()
	var got []store.PendingForWaiting
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		got, err = tx.PendingCallbacks().ListConsumableForWaiting(ctx, now, limit)
		return err
	}); err != nil {
		t.Fatalf("ListConsumableForWaiting(limit %d): %v", limit, err)
	}
	return got
}
