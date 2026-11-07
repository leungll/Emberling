//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// ---------------------------------------------------------------------------
// Release target occupation, human waits and the release facts of a Tool Attempt. The
// partial unique index on ACTIVE target keys is the only arbiter of occupation
// uniqueness; every settlement is a conditional update with a single winner.
// ---------------------------------------------------------------------------

func newReleaseOccupation(id, runID, actionID, operationKey string) domain.ReleaseOccupation {
	return domain.ReleaseOccupation{
		ID: id, TargetKey: "payments-api/production", RunID: runID, ActionID: actionID,
		OperationKey: operationKey, Status: domain.ReleaseOccupationActive, AcquiredAt: fixtureTime,
	}
}

func acquireOccupation(ctx context.Context, t *testing.T, uow store.UnitOfWork, occupation domain.ReleaseOccupation) bool {
	t.Helper()
	var acquired bool
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		acquired, err = tx.ReleaseOccupations().Acquire(ctx, occupation)
		return err
	}); err != nil {
		t.Fatalf("acquire occupation %s: %v", occupation.ID, err)
	}
	return acquired
}

func TestReleaseOccupations_Acquire_SecondActiveOnSameTarget_NotAcquired(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_occ", "turn_occ", "decision_occ", "action_occ")

	if !acquireOccupation(ctx, t, uow, newReleaseOccupation("occupation_1", f.runID, "action_occ", "op_action_occ_1")) {
		t.Fatal("first Acquire: want acquired, got not acquired")
	}
	if acquireOccupation(ctx, t, uow, newReleaseOccupation("occupation_2", f.runID, "action_occ", "op_other_1")) {
		t.Fatal("second Acquire on an ACTIVE target: want not acquired, got acquired")
	}

	var active domain.ReleaseOccupation
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		active, err = tx.ReleaseOccupations().GetActiveByTarget(ctx, "payments-api/production")
		return err
	}); err != nil {
		t.Fatalf("GetActiveByTarget: %v", err)
	}
	if active.ID != "occupation_1" || active.OperationKey != "op_action_occ_1" {
		t.Fatalf("active occupation = %s/%s, want occupation_1/op_action_occ_1", active.ID, active.OperationKey)
	}
}

func TestReleaseOccupations_Release_SecondSettlement_StaleAndTargetFree(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_occ", "turn_occ", "decision_occ", "action_occ")
	acquireOccupation(ctx, t, uow, newReleaseOccupation("occupation_1", f.runID, "action_occ", "op_action_occ_1"))

	released := fixtureTime.Add(time.Minute)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ReleaseOccupations().Release(ctx, "occupation_1", domain.ReleaseReasonDeployed, released)
	}); err != nil {
		t.Fatalf("Release: %v", err)
	}
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ReleaseOccupations().Release(ctx, "occupation_1", domain.ReleaseReasonVoided, released.Add(time.Second))
	})
	if !errors.Is(err, domain.ErrStaleClaim) {
		t.Fatalf("second Release: want ErrStaleClaim, got %v", err)
	}

	var got domain.ReleaseOccupation
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := tx.ReleaseOccupations().GetActiveByTarget(ctx, "payments-api/production"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("GetActiveByTarget after release: want ErrNotFound, got %v", err)
		}
		var err error
		got, err = tx.ReleaseOccupations().Get(ctx, "occupation_1")
		return err
	}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != domain.ReleaseOccupationReleased || got.ReleaseReason == nil || *got.ReleaseReason != domain.ReleaseReasonDeployed ||
		got.ReleasedAt == nil || !got.ReleasedAt.Equal(released) {
		t.Fatalf("released occupation = %+v, want RELEASED/DEPLOYED at %v", got, released)
	}
	// A released target can be occupied again.
	if !acquireOccupation(ctx, t, uow, newReleaseOccupation("occupation_2", f.runID, "action_occ", "op_action_occ_2")) {
		t.Fatal("Acquire after release: want acquired, got not acquired")
	}
}

func TestReleaseOccupations_SetOperationKey_OnReleased_Stale(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_occ", "turn_occ", "decision_occ", "action_occ")
	acquireOccupation(ctx, t, uow, newReleaseOccupation("occupation_1", f.runID, "action_occ", "op_action_occ_1"))

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ReleaseOccupations().SetOperationKey(ctx, "occupation_1", "op_action_occ_2")
	}); err != nil {
		t.Fatalf("SetOperationKey on ACTIVE: %v", err)
	}
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ReleaseOccupations().Release(ctx, "occupation_1", domain.ReleaseReasonNotDeployed, fixtureTime)
	}); err != nil {
		t.Fatalf("Release: %v", err)
	}
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ReleaseOccupations().SetOperationKey(ctx, "occupation_1", "op_action_occ_3")
	})
	if !errors.Is(err, domain.ErrStaleClaim) {
		t.Fatalf("SetOperationKey on RELEASED: want ErrStaleClaim, got %v", err)
	}
	var list []domain.ReleaseOccupation
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		list, err = tx.ReleaseOccupations().ListByActionID(ctx, "action_occ")
		return err
	}); err != nil {
		t.Fatalf("ListByActionID: %v", err)
	}
	if len(list) != 1 || list[0].OperationKey != "op_action_occ_2" {
		t.Fatalf("occupations of action = %+v, want one with op_action_occ_2", list)
	}
}

func newHumanWait(id, runID, actionID string) domain.HumanWait {
	return domain.HumanWait{
		ID: id, ActionID: actionID, RunID: runID, Status: domain.HumanWaitOpen,
		OpenedAt: fixtureTime, Deadline: fixtureTime.Add(time.Hour),
	}
}

func TestHumanWaits_Create_SecondForSameAction_Conflict(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_hw", "turn_hw", "decision_hw", "action_hw")

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.HumanWaits().Create(ctx, newHumanWait("hwait_1", f.runID, "action_hw"))
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.HumanWaits().Create(ctx, newHumanWait("hwait_2", f.runID, "action_hw"))
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second Create for the same Action: want ErrConflict, got %v", err)
	}
}

func TestHumanWaits_Close_SecondClosing_ReportsFalseAndKeepsFirst(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_hw", "turn_hw", "decision_hw", "action_hw")
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.HumanWaits().Create(ctx, newHumanWait("hwait_1", f.runID, "action_hw"))
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	deployed := "DEPLOYED"
	stopped := "STOPPED"
	operator := "release-operator"
	var first, second bool
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		if first, err = tx.HumanWaits().Close(ctx, "hwait_1", domain.HumanWaitResolved, fixtureTime.Add(time.Minute), nil, &deployed); err != nil {
			return err
		}
		second, err = tx.HumanWaits().Close(ctx, "hwait_1", domain.HumanWaitStopped, fixtureTime.Add(2*time.Minute), &operator, &stopped)
		return err
	}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !first || second {
		t.Fatalf("Close results = %v, %v; want true, false", first, second)
	}
	var wait domain.HumanWait
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		wait, err = tx.HumanWaits().GetByActionID(ctx, "action_hw")
		return err
	}); err != nil {
		t.Fatalf("GetByActionID: %v", err)
	}
	if wait.Status != domain.HumanWaitResolved || wait.ClosedBy != nil || wait.Outcome == nil || *wait.Outcome != deployed {
		t.Fatalf("closed wait = %+v, want RESOLVED by nobody with outcome DEPLOYED", wait)
	}
}

func newReleaseAttempt(id, actionID string, attemptNo int, confirm time.Time) domain.ToolAttempt {
	attempt := newToolAttempt(id, actionID, attemptNo)
	attempt.ToolName = "deploy_change"
	key := "op_" + actionID + "_" + string(rune('0'+attemptNo))
	attempt.OperationKey = &key
	attempt.ConfirmDeadline = &confirm
	return attempt
}

func TestToolAttempts_Create_ReleaseFacts_RoundTripAndUniqueOperationKey(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_rel", "turn_rel", "decision_rel", "action_rel")
	confirm := fixtureTime.Add(30 * time.Minute)

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ToolAttempts().Create(ctx, newReleaseAttempt("tool_attempt_rel_1", "action_rel", 1, confirm))
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := getToolAttemptRow(ctx, t, uow, "tool_attempt_rel_1")
	if got.OperationKey == nil || *got.OperationKey != "op_action_rel_1" || got.ConfirmDeadline == nil || !got.ConfirmDeadline.Equal(confirm) ||
		got.VoidRequestedAt != nil || got.OutcomeUnknownSince != nil {
		t.Fatalf("release attempt facts = key %v confirm %v void %v unknown %v; want op_action_rel_1, %v, nil, nil",
			got.OperationKey, got.ConfirmDeadline, got.VoidRequestedAt, got.OutcomeUnknownSince, confirm)
	}

	duplicate := newReleaseAttempt("tool_attempt_rel_2", "action_rel", 2, confirm)
	key := "op_action_rel_1"
	duplicate.OperationKey = &key
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ToolAttempts().Create(ctx, duplicate)
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Create with a reused operation key: want ErrConflict, got %v", err)
	}
}

func TestToolAttempts_MarkOutcomeUnknown_Repeated_SetsUnknownOnceAndKeepsTime(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_rel", "turn_rel", "decision_rel", "action_rel")
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ToolAttempts().Create(ctx, newReleaseAttempt("tool_attempt_rel_1", "action_rel", 1, fixtureTime.Add(time.Hour)))
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	first := fixtureTime.Add(time.Minute)
	var firstSet, secondSet bool
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		if firstSet, err = tx.ToolAttempts().MarkOutcomeUnknown(ctx, "tool_attempt_rel_1", first, first); err != nil {
			return err
		}
		secondSet, err = tx.ToolAttempts().MarkOutcomeUnknown(ctx, "tool_attempt_rel_1", first.Add(time.Minute), first.Add(2*time.Minute))
		return err
	}); err != nil {
		t.Fatalf("MarkOutcomeUnknown: %v", err)
	}
	if !firstSet || secondSet {
		t.Fatalf("MarkOutcomeUnknown first flags = %v, %v; want true, false", firstSet, secondSet)
	}
	got := getToolAttemptRow(ctx, t, uow, "tool_attempt_rel_1")
	if got.Status != domain.ToolAttemptDispatched || got.OutcomeUnknownSince == nil || !got.OutcomeUnknownSince.Equal(first) ||
		got.NextPollAt == nil || !got.NextPollAt.Equal(first.Add(2*time.Minute)) || got.DispatchedAt == nil {
		t.Fatalf("attempt after two unknown marks = %+v; want DISPATCHED, unknown since %v, next poll %v", got, first, first.Add(2*time.Minute))
	}

	// A settled Attempt is never put back onto the query path.
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ToolAttempts().MarkFailedDefinite(ctx, "tool_attempt_rel_1", domain.ToolAttemptDispatched, first.Add(3*time.Minute),
			domain.ExecutionError{Code: "VOIDED", Message: "production confirmed the operation void"})
	}); err != nil {
		t.Fatalf("MarkFailedDefinite: %v", err)
	}
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := tx.ToolAttempts().MarkOutcomeUnknown(ctx, "tool_attempt_rel_1", first, first)
		return err
	})
	if !errors.Is(err, domain.ErrStaleClaim) {
		t.Fatalf("MarkOutcomeUnknown on a FAILED attempt: want ErrStaleClaim, got %v", err)
	}
	got = getToolAttemptRow(ctx, t, uow, "tool_attempt_rel_1")
	if got.Uncertain == nil || *got.Uncertain {
		t.Fatalf("definite failure uncertain = %v, want false", got.Uncertain)
	}
}

func TestToolAttempts_MarkVoidRequested_Repeated_KeepsFirstTime(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_rel", "turn_rel", "decision_rel", "action_rel")
	first := fixtureTime.Add(time.Minute)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.ToolAttempts().Create(ctx, newReleaseAttempt("tool_attempt_rel_1", "action_rel", 1, fixtureTime.Add(time.Hour))); err != nil {
			return err
		}
		if _, err := tx.ToolAttempts().MarkOutcomeUnknown(ctx, "tool_attempt_rel_1", first, first); err != nil {
			return err
		}
		if err := tx.ToolAttempts().MarkVoidRequested(ctx, "tool_attempt_rel_1", first.Add(time.Minute)); err != nil {
			return err
		}
		return tx.ToolAttempts().MarkVoidRequested(ctx, "tool_attempt_rel_1", first.Add(5*time.Minute))
	}); err != nil {
		t.Fatalf("seed and request void: %v", err)
	}
	got := getToolAttemptRow(ctx, t, uow, "tool_attempt_rel_1")
	if got.VoidRequestedAt == nil || !got.VoidRequestedAt.Equal(first.Add(time.Minute)) {
		t.Fatalf("void_requested_at = %v, want the first request time %v", got.VoidRequestedAt, first.Add(time.Minute))
	}
}

func TestToolAttempts_ListDueConfirmDeadlines_OnlyWaitingActionsPastDeadline(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_rel", "turn_rel", "decision_rel", "action_rel")
	due := fixtureTime.Add(10 * time.Minute)

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := tx.AgentActions().ClaimReady(ctx, "action_rel", fixtureTime); err != nil {
			return err
		}
		if err := tx.ToolAttempts().Create(ctx, newReleaseAttempt("tool_attempt_rel_1", "action_rel", 1, due)); err != nil {
			return err
		}
		return tx.AgentActions().MarkWaiting(ctx, "action_rel", fixtureTime)
	}); err != nil {
		t.Fatalf("seed waiting release: %v", err)
	}

	list := func(now time.Time) []store.DueConfirmDeadline {
		var out []store.DueConfirmDeadline
		if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			out, err = tx.ToolAttempts().ListDueConfirmDeadlines(ctx, now, 10)
			return err
		}); err != nil {
			t.Fatalf("ListDueConfirmDeadlines: %v", err)
		}
		return out
	}
	if got := list(due.Add(-time.Second)); len(got) != 0 {
		t.Fatalf("before the deadline: want nothing due, got %+v", got)
	}
	if got := list(due); len(got) != 1 || got[0].AttemptID != "tool_attempt_rel_1" || got[0].ActionID != "action_rel" {
		t.Fatalf("at the deadline: want tool_attempt_rel_1/action_rel, got %+v", got)
	}

	// Once the Action is in a human wait, the deadline is spent and the scan skips it.
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentActions().MarkWaitingHuman(ctx, "action_rel", domain.AgentActionWaitingCallback, due)
	}); err != nil {
		t.Fatalf("MarkWaitingHuman: %v", err)
	}
	if got := list(due.Add(time.Hour)); len(got) != 0 {
		t.Fatalf("after WAITING_HUMAN: want nothing due, got %+v", got)
	}
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentActions().MarkWaitingHuman(ctx, "action_rel", domain.AgentActionWaitingCallback, due)
	})
	if !errors.Is(err, domain.ErrStaleClaim) {
		t.Fatalf("second MarkWaitingHuman: want ErrStaleClaim, got %v", err)
	}
}

func TestAgentActions_ResumeForRelease_OnlyFromWaitingCallback(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_rel", "turn_rel", "decision_rel", "action_rel")

	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.AgentActions().ResumeForRelease(ctx, "action_rel", fixtureTime)
	})
	if !errors.Is(err, domain.ErrStaleClaim) {
		t.Fatalf("ResumeForRelease from READY: want ErrStaleClaim, got %v", err)
	}
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := tx.AgentActions().ClaimReady(ctx, "action_rel", fixtureTime); err != nil {
			return err
		}
		if err := tx.AgentActions().MarkWaiting(ctx, "action_rel", fixtureTime); err != nil {
			return err
		}
		return tx.AgentActions().ResumeForRelease(ctx, "action_rel", fixtureTime.Add(time.Minute))
	}); err != nil {
		t.Fatalf("ResumeForRelease from WAITING_CALLBACK: %v", err)
	}
	var action domain.AgentAction
	if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		action, err = tx.AgentActions().Get(ctx, "action_rel")
		return err
	}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if action.Status != domain.AgentActionRunning {
		t.Fatalf("action status = %s, want RUNNING", action.Status)
	}
}

// ScheduleQueriesOfRun runs in the transaction that ends a Run: it writes the first query
// onto the ACTIVE occupations of that Run alone, keeps a schedule that is already moving,
// and leaves released occupations out of the post-task scan.
func TestReleaseOccupations_ScheduleQueriesOfRun_OnlyActiveWithoutSchedule(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testdb.Open(t))
	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_sched", "turn_sched", "decision_sched", "action_sched")

	unscheduled := newReleaseOccupation("occupation_unscheduled", f.runID, "action_sched", "op_unscheduled_1")
	scheduled := newReleaseOccupation("occupation_scheduled", f.runID, "action_sched", "op_scheduled_1")
	scheduled.TargetKey = "payments-api/staging"
	released := newReleaseOccupation("occupation_released", f.runID, "action_sched", "op_released_1")
	released.TargetKey = "payments-api/canary"
	for _, occupation := range []domain.ReleaseOccupation{unscheduled, scheduled, released} {
		if !acquireOccupation(ctx, t, uow, occupation) {
			t.Fatalf("acquire %s: not acquired", occupation.ID)
		}
	}
	earlier := fixtureTime.Add(time.Minute)
	runEnd := fixtureTime.Add(time.Hour)
	repeat := runEnd.Add(time.Hour)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.ReleaseOccupations().ScheduleQuery(ctx, scheduled.ID, earlier); err != nil {
			return err
		}
		if err := tx.ReleaseOccupations().Release(ctx, released.ID, domain.ReleaseReasonDeployed, earlier); err != nil {
			return err
		}
		if err := tx.ReleaseOccupations().ScheduleQueriesOfRun(ctx, f.runID, runEnd); err != nil {
			return err
		}
		return tx.ReleaseOccupations().ScheduleQueriesOfRun(ctx, f.runID, repeat)
	}); err != nil {
		t.Fatalf("schedule queries of run: %v", err)
	}

	read := func(id string) domain.ReleaseOccupation {
		t.Helper()
		var occupation domain.ReleaseOccupation
		if err := uow.WithinReadTx(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			occupation, err = tx.ReleaseOccupations().Get(ctx, id)
			return err
		}); err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		return occupation
	}
	if got := read(unscheduled.ID).NextQueryAt; got == nil || !got.Equal(runEnd) {
		t.Errorf("first query of the unscheduled occupation = %v, want %v", got, runEnd)
	}
	if got := read(scheduled.ID).NextQueryAt; got == nil || !got.Equal(earlier) {
		t.Errorf("schedule already moving = %v, want it kept at %v", got, earlier)
	}
	if got := read(released.ID).NextQueryAt; got != nil {
		t.Errorf("released occupation got a query at %v", got)
	}
}
