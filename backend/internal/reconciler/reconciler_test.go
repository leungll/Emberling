package reconciler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
)

// ---------------------------------------------------------------------------------------
// Fakes. These are deliberately minimal: they cover only the store.Tx / store.UnitOfWork
// surface RunOnce touches (NodeRuns, NodeAttempts, PendingCallbacks) and embed the real
// interfaces (nil) for everything else, so an accidental call to an un-stubbed method
// panics instead of silently returning zero values. CLAUDE.md's testing standard reserves
// PostgreSQL integration tests for transaction/locking/constraint proofs; these unit tests
// prove RunOnce's own orchestration -- which rows it lists, which use case it calls with
// which arguments, how it classifies outcomes -- against a fake, not a transaction
// guarantee.
// ---------------------------------------------------------------------------------------

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type fakeNodeRunRepo struct {
	store.NodeRunRepository
	readyOrRetryable []domain.NodeRun
}

func (r *fakeNodeRunRepo) ListReadyOrRetryable(context.Context, time.Time, int) ([]domain.NodeRun, error) {
	return r.readyOrRetryable, nil
}

type fakeNodeAttemptRepo struct {
	store.NodeAttemptRepository
	expired  []domain.NodeAttempt
	duePolls []store.DuePoll
	// duePollLimits records the limit of every ListDuePolls call; pollWrites records any
	// attempt to claim or clear a poll schedule, which only the service may do.
	duePollLimits []int
	pollWrites    []string
}

func (r *fakeNodeAttemptRepo) ListExpired(context.Context, time.Time, int) ([]domain.NodeAttempt, error) {
	return r.expired, nil
}

func (r *fakeNodeAttemptRepo) ListDuePolls(_ context.Context, _ time.Time, limit int) ([]store.DuePoll, error) {
	r.duePollLimits = append(r.duePollLimits, limit)
	return r.duePolls, nil
}

func (r *fakeNodeAttemptRepo) ClaimPoll(_ context.Context, attemptID string, _ time.Time, _ time.Duration, _ int) (bool, error) {
	r.pollWrites = append(r.pollWrites, "ClaimPoll "+attemptID)
	return false, nil
}

func (r *fakeNodeAttemptRepo) ClearPoll(_ context.Context, attemptID string) (bool, error) {
	r.pollWrites = append(r.pollWrites, "ClearPoll "+attemptID)
	return false, nil
}

type fakePendingCallbackRepo struct {
	store.PendingCallbackRepository
	consumable         []store.PendingForWaiting
	deleteExpiredCount int64
	deleteExpiredCalls int
}

func (r *fakePendingCallbackRepo) ListConsumableForWaiting(context.Context, time.Time, int) ([]store.PendingForWaiting, error) {
	return r.consumable, nil
}

func (r *fakePendingCallbackRepo) DeleteExpired(context.Context, time.Time, int) (int64, error) {
	r.deleteExpiredCalls++
	return r.deleteExpiredCount, nil
}

type fakeTx struct {
	store.Tx
	nodeRuns     *fakeNodeRunRepo
	attempts     *fakeNodeAttemptRepo
	pending      *fakePendingCallbackRepo
	agentTurns   *fakeAgentTurnRepo
	agentActions *fakeAgentActionRepo
	agentRuns    *fakeAgentRunRepo
	toolAttempts *fakeToolAttemptRepo
	approvals    *fakeApprovalRepo
	humanWaits   *fakeHumanWaitRepo
	occupations  *fakeOccupationRepo
}

func (t *fakeTx) HumanWaits() store.HumanWaitRepository                 { return t.humanWaits }
func (t *fakeTx) ReleaseOccupations() store.ReleaseOccupationRepository { return t.occupations }

// fakeHumanWaitRepo serves the human wait expiry scan's lister; any other call panics, so a
// scan that closed a wait itself would be caught.
type fakeHumanWaitRepo struct {
	store.HumanWaitRepository
	expired []domain.HumanWait
}

func (r *fakeHumanWaitRepo) ListExpiredOpen(context.Context, time.Time, int) ([]domain.HumanWait, error) {
	return r.expired, nil
}

// fakeOccupationRepo serves the post-task query scan's lister; claiming or releasing an
// occupation through it panics, because only the service may.
type fakeOccupationRepo struct {
	store.ReleaseOccupationRepository
	due []domain.ReleaseOccupation
}

func (r *fakeOccupationRepo) ListDueQueries(context.Context, time.Time, int) ([]domain.ReleaseOccupation, error) {
	return r.due, nil
}

func (t *fakeTx) ToolAttempts() store.ToolAttemptRepository { return t.toolAttempts }

func (t *fakeTx) NodeRuns() store.NodeRunRepository                     { return t.nodeRuns }
func (t *fakeTx) NodeAttempts() store.NodeAttemptRepository             { return t.attempts }
func (t *fakeTx) PendingCallbacks() store.PendingCallbackRepository     { return t.pending }
func (t *fakeTx) AgentTurns() store.AgentTurnRepository                 { return t.agentTurns }
func (t *fakeTx) AgentTurnManifests() store.AgentTurnManifestRepository { return nil }
func (t *fakeTx) AgentActions() store.AgentActionRepository             { return t.agentActions }
func (t *fakeTx) AgentRuns() store.AgentRunRepository                   { return t.agentRuns }
func (t *fakeTx) Approvals() store.ApprovalRepository                   { return t.approvals }

// fakeApprovalRepo serves the approval expiry scan's lister. Any other call panics on the
// embedded nil interface, so a scan that decided an approval itself would be caught.
type fakeApprovalRepo struct {
	store.ApprovalRepository
	expired []domain.Approval
}

func (r *fakeApprovalRepo) ListExpiredPending(context.Context, time.Time, int) ([]domain.Approval, error) {
	return r.expired, nil
}

// The three Agent scans of RunOnce need a lister each. Only the list methods are
// implemented: the Reconciler is not allowed to change Agent state itself, so any other
// call through these fakes is a bug and panics on the embedded nil interface.
type fakeAgentTurnRepo struct {
	store.AgentTurnRepository
	ready []domain.AgentTurn
}

func (r *fakeAgentTurnRepo) ListReady(context.Context, int) ([]domain.AgentTurn, error) {
	return r.ready, nil
}

type fakeAgentActionRepo struct {
	store.AgentActionRepository
	ready []domain.AgentAction
}

func (r *fakeAgentActionRepo) ListReady(context.Context, int) ([]domain.AgentAction, error) {
	return r.ready, nil
}

type fakeAgentRunRepo struct {
	store.AgentRunRepository
	expired []domain.AgentRun
}

func (r *fakeAgentRunRepo) ListExpired(context.Context, time.Time, int) ([]domain.AgentRun, error) {
	return r.expired, nil
}

// fakeToolAttemptRepo serves the orphan scan's lister, recording the runner it was asked
// about, and the due Tool poll lister. Like the other fakes, any other call panics on the
// embedded nil interface, so a scan that claimed or cleared a poll itself would be caught.
type fakeToolAttemptRepo struct {
	store.ToolAttemptRepository
	orphans    []domain.ToolAttempt
	runnerSeen []string
	duePolls   []store.DueToolPoll
	// duePollLimits records the limit of every ListDuePolls call.
	duePollLimits []int
	dueConfirm    []store.DueConfirmDeadline
}

func (r *fakeToolAttemptRepo) ListDueConfirmDeadlines(_ context.Context, _ time.Time, _ int) ([]store.DueConfirmDeadline, error) {
	return r.dueConfirm, nil
}

func (r *fakeToolAttemptRepo) ListOrphanStarted(_ context.Context, currentRunnerID string, _ time.Time, _ int) ([]domain.ToolAttempt, error) {
	r.runnerSeen = append(r.runnerSeen, currentRunnerID)
	return r.orphans, nil
}

func (r *fakeToolAttemptRepo) ListDuePolls(_ context.Context, _ time.Time, limit int) ([]store.DueToolPoll, error) {
	r.duePollLimits = append(r.duePollLimits, limit)
	return r.duePolls, nil
}

type fakeUoW struct{ tx *fakeTx }

func (u *fakeUoW) WithinTx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	return fn(ctx, u.tx)
}

func (u *fakeUoW) WithinReadTx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	return fn(ctx, u.tx)
}

// fakeExecutor implements Advancer. Advance/Execute/TimeoutAttempt are no-ops that report
// no claim / no error: every test in this file drives only the Pending Callback scan, so
// the NodeRun and Attempt lists in fakeTx are left empty.
type fakeExecutor struct {
	resumeCalls []service.ResumeNode
	resumeFunc  func(service.ResumeNode) (service.ResumeOutcome, error)

	pollCalls []service.PollAttempt
	pollFunc  func(service.PollAttempt) (service.PollAttemptOutcome, error)

	toolPollCalls []service.PollToolAttempt
	toolPollFunc  func(service.PollToolAttempt) (service.PollToolAttemptOutcome, error)

	agentTurnCalls    []agentCall
	agentToolCalls    []agentCall
	agentFinalCalls   []agentCall
	agentTimeoutCalls []string
	orphanCalls       []string
	expireCalls       []string
	humanWaitCalls    []string
	waitExpireCalls   []string
	occupationCalls   []string
	occupationFunc    func(string) (service.ReleaseReconcileOutcome, error)
}

// agentCall records which Agent work item the Reconciler handed to which use case, and
// with which claim source.
type agentCall struct {
	id     string
	source domain.ClaimSource
}

func (e *fakeExecutor) Advance(context.Context, string) (service.AdvanceOutcome, error) {
	return service.AdvanceOutcome{}, nil
}

func (e *fakeExecutor) Execute(context.Context, service.AdvanceOutcome) error { return nil }

func (e *fakeExecutor) TimeoutAttempt(context.Context, string) error { return nil }

func (e *fakeExecutor) AdvanceAgentTurn(_ context.Context, turnID string, source domain.ClaimSource) error {
	e.agentTurnCalls = append(e.agentTurnCalls, agentCall{id: turnID, source: source})
	return nil
}

func (e *fakeExecutor) ExecuteAgentAction(_ context.Context, actionID string, source domain.ClaimSource) error {
	e.agentToolCalls = append(e.agentToolCalls, agentCall{id: actionID, source: source})
	return nil
}

func (e *fakeExecutor) CompleteAgentFinal(_ context.Context, actionID string, source domain.ClaimSource) error {
	e.agentFinalCalls = append(e.agentFinalCalls, agentCall{id: actionID, source: source})
	return nil
}

func (e *fakeExecutor) TimeoutAgentRun(_ context.Context, agentRunID string) error {
	e.agentTimeoutCalls = append(e.agentTimeoutCalls, agentRunID)
	return nil
}

func (e *fakeExecutor) HandleOrphanToolAttempt(_ context.Context, attemptID string) error {
	e.orphanCalls = append(e.orphanCalls, attemptID)
	return nil
}

func (e *fakeExecutor) ExpireApproval(_ context.Context, approvalID string) error {
	e.expireCalls = append(e.expireCalls, approvalID)
	return nil
}

func (e *fakeExecutor) OpenReleaseHumanWait(_ context.Context, attemptID string) error {
	e.humanWaitCalls = append(e.humanWaitCalls, attemptID)
	return nil
}

func (e *fakeExecutor) ExpireHumanWait(_ context.Context, humanWaitID string) error {
	e.waitExpireCalls = append(e.waitExpireCalls, humanWaitID)
	return nil
}

func (e *fakeExecutor) ReconcileReleaseOccupation(_ context.Context, occupationID string) (service.ReleaseReconcileOutcome, error) {
	e.occupationCalls = append(e.occupationCalls, occupationID)
	if e.occupationFunc != nil {
		return e.occupationFunc(occupationID)
	}
	return service.ReleaseReconcileOutcome{}, nil
}

func (e *fakeExecutor) ResumeNode(_ context.Context, req service.ResumeNode) (service.ResumeOutcome, error) {
	e.resumeCalls = append(e.resumeCalls, req)
	if e.resumeFunc != nil {
		return e.resumeFunc(req)
	}
	return service.ResumeOutcome{}, nil
}

func (e *fakeExecutor) PollAttempt(_ context.Context, req service.PollAttempt) (service.PollAttemptOutcome, error) {
	e.pollCalls = append(e.pollCalls, req)
	if e.pollFunc != nil {
		return e.pollFunc(req)
	}
	return service.PollAttemptOutcome{Kind: service.PollAttemptSkipped, SkipReason: service.PollSkipNotClaimed}, nil
}

func (e *fakeExecutor) PollToolAttempt(_ context.Context, req service.PollToolAttempt) (service.PollToolAttemptOutcome, error) {
	e.toolPollCalls = append(e.toolPollCalls, req)
	if e.toolPollFunc != nil {
		return e.toolPollFunc(req)
	}
	return service.PollToolAttemptOutcome{Kind: service.PollAttemptSkipped, SkipReason: service.PollSkipNotClaimed}, nil
}

func newTestReconciler(t *testing.T, exec *fakeExecutor, tx *fakeTx, logBuf *bytes.Buffer) *Reconciler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return New(Config{
		UoW:        &fakeUoW{tx: tx},
		Executor:   exec,
		Clock:      fixedClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		BatchLimit: 50,
		Logger:     logger,
	})
}

func emptyFakeTx() *fakeTx {
	return &fakeTx{
		nodeRuns:     &fakeNodeRunRepo{},
		attempts:     &fakeNodeAttemptRepo{},
		pending:      &fakePendingCallbackRepo{},
		agentTurns:   &fakeAgentTurnRepo{},
		agentActions: &fakeAgentActionRepo{},
		agentRuns:    &fakeAgentRunRepo{},
		approvals:    &fakeApprovalRepo{},
		toolAttempts: &fakeToolAttemptRepo{},
		humanWaits:   &fakeHumanWaitRepo{},
		occupations:  &fakeOccupationRepo{},
	}
}

// The release rows of the scan only discover work: every expired human wait goes to the
// expiry use case and every due occupation query to the reconciliation use case, whose
// outcomes are counted and whose errors are collected without stopping the pass.
func TestReconciler_RunOnce_ExpiredHumanWaitsAndDueOccupationQueries_HandedToUseCases(t *testing.T) {
	tx := emptyFakeTx()
	tx.humanWaits.expired = []domain.HumanWait{{ID: "hwait_1"}, {ID: "hwait_2"}}
	tx.occupations.due = []domain.ReleaseOccupation{{ID: "occupation_1"}, {ID: "occupation_2"}, {ID: "occupation_3"}}
	exec := &fakeExecutor{occupationFunc: func(id string) (service.ReleaseReconcileOutcome, error) {
		switch id {
		case "occupation_1":
			return service.ReleaseReconcileOutcome{Claimed: true, Released: true, Reason: domain.ReleaseReasonVoided}, nil
		case "occupation_2":
			return service.ReleaseReconcileOutcome{Claimed: true}, errors.New("production gave no answer")
		default:
			return service.ReleaseReconcileOutcome{}, nil
		}
	}}
	var logs bytes.Buffer
	report, err := newTestReconciler(t, exec, tx, &logs).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if strings.Join(exec.waitExpireCalls, ",") != "hwait_1,hwait_2" || report.ExpiredHumanWaitsFound != 2 || report.HumanWaitsExpired != 2 {
		t.Errorf("human wait expiry calls %v, report found %d expired %d, want both waits handed over",
			exec.waitExpireCalls, report.ExpiredHumanWaitsFound, report.HumanWaitsExpired)
	}
	if strings.Join(exec.occupationCalls, ",") != "occupation_1,occupation_2,occupation_3" {
		t.Errorf("occupation calls = %v, want all three", exec.occupationCalls)
	}
	if report.DueOccupationQueriesFound != 3 || report.OccupationQueriesClaimed != 2 || report.OccupationsReleased != 1 {
		t.Errorf("report found %d claimed %d released %d, want 3/2/1",
			report.DueOccupationQueriesFound, report.OccupationQueriesClaimed, report.OccupationsReleased)
	}
	if len(report.Errors) != 1 || !strings.Contains(report.Errors[0].Error(), "occupation_2") {
		t.Errorf("errors = %v, want the one unanswered query", report.Errors)
	}
}

// TestReconciler_RunOnce_ResumesConsumablePendingCallbacksThroughResumeNode proves the
// Pending Callback consumption scan:
// every row ListConsumableForWaiting returns is replayed through the same idempotent
// ResumeNode use case a live callback or Provider Poll enters, with
// ConsumePending=true, Source=CALLBACK and the stored PayloadHash forwarded verbatim (never
// recomputed from the JSONB-round-tripped payload -- service.ResumeNode.hash's own doc
// comment explains why re-hashing would not match what the Provider actually sent).
func TestReconciler_RunOnce_ResumesConsumablePendingCallbacksThroughResumeNode(t *testing.T) {
	tx := emptyFakeTx()
	payload := json.RawMessage(`{"text":"resumed"}`)
	tx.pending.consumable = []store.PendingForWaiting{
		{
			Pending: domain.PendingCallback{
				ExternalTaskID: "task-1",
				Payload:        payload,
				PayloadHash:    "stored-hash-1",
			},
			Binding:   domain.CallbackBinding{ID: "cb_1", TargetID: "at_1"},
			RunID:     "run_1",
			NodeRunID: "nr_1",
			AttemptID: "at_1",
		},
	}
	exec := &fakeExecutor{}
	var logBuf bytes.Buffer
	rec := newTestReconciler(t, exec, tx, &logBuf)

	report, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if len(exec.resumeCalls) != 1 {
		t.Fatalf("ResumeNode calls: want 1, got %d", len(exec.resumeCalls))
	}
	got := exec.resumeCalls[0]
	if got.ExternalTaskID != "task-1" {
		t.Fatalf("ResumeNode.ExternalTaskID: want %q, got %q", "task-1", got.ExternalTaskID)
	}
	if string(got.Payload) != string(payload) {
		t.Fatalf("ResumeNode.Payload: want %s, got %s", payload, got.Payload)
	}
	if got.PayloadHash != "stored-hash-1" {
		t.Fatalf("ResumeNode.PayloadHash: want the stored hash forwarded verbatim (%q), got %q", "stored-hash-1", got.PayloadHash)
	}
	if got.Source != domain.CompletionCallback {
		t.Fatalf("ResumeNode.Source: want %s, got %s", domain.CompletionCallback, got.Source)
	}
	if !got.ConsumePending {
		t.Fatal("ResumeNode.ConsumePending: want true, got false")
	}
	if report.ConsumablePendingFound != 1 {
		t.Fatalf("report.ConsumablePendingFound: want 1, got %d", report.ConsumablePendingFound)
	}
	if report.PendingCallbacksResumed != 1 {
		t.Fatalf("report.PendingCallbacksResumed: want 1, got %d", report.PendingCallbacksResumed)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("report.Errors: want none, got %v", report.Errors)
	}
}

// TestReconciler_RunOnce_PayloadRejectedPendingCallback_LogsAndContinues proves that a
// stored early callback the registered Executor cannot interpret does not abort the pass
// or count as an operational error: an uninterpretable payload leaves the NodeRun
// WAITING_CALLBACK, so the Reconciler logs at warn -- without the token, hash or payload -- and
// moves on, leaving the row for the next tick or for retention to eventually delete.
func TestReconciler_RunOnce_PayloadRejectedPendingCallback_LogsAndContinues(t *testing.T) {
	tx := emptyFakeTx()
	const secretPayload = `{"text":"top-secret-body"}`
	const secretHash = "super-secret-hash-value"
	tx.pending.consumable = []store.PendingForWaiting{
		{
			Pending: domain.PendingCallback{
				ExternalTaskID: "task-rejected",
				Payload:        json.RawMessage(secretPayload),
				PayloadHash:    secretHash,
			},
			Binding:   domain.CallbackBinding{ID: "cb_2", TargetID: "at_2"},
			RunID:     "run_2",
			NodeRunID: "nr_2",
			AttemptID: "at_2",
		},
	}
	exec := &fakeExecutor{
		resumeFunc: func(req service.ResumeNode) (service.ResumeOutcome, error) {
			return service.ResumeOutcome{}, &service.CallbackPayloadRejectedError{
				ExternalTaskID: req.ExternalTaskID,
				Err:            errors.New("undecodable callback payload"),
			}
		},
	}
	var logBuf bytes.Buffer
	rec := newTestReconciler(t, exec, tx, &logBuf)

	report, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("report.Errors after a rejected payload: want none (not an operational error), got %v", report.Errors)
	}
	if report.PendingCallbacksResumed != 0 {
		t.Fatalf("report.PendingCallbacksResumed after a rejected payload: want 0, got %d", report.PendingCallbacksResumed)
	}

	logged := logBuf.String()
	if !strings.Contains(logged, "level=WARN") {
		t.Fatalf("log output: want a WARN entry for the rejected payload, got %q", logged)
	}
	if !strings.Contains(logged, "task-rejected") {
		t.Fatalf("log output: want the external task id for correlation, got %q", logged)
	}
	if strings.Contains(logged, secretPayload) || strings.Contains(logged, secretHash) {
		t.Fatalf("log output leaked the payload or its hash: %q", logged)
	}

	// A second RunOnce sees the same still-unconsumed row (this fake never removes it, the
	// same way a real Postgres row survives an untouched pass) and rejects it again --
	// proving the Reconciler retries it every tick rather than giving up after one warning,
	// exactly as CLAUDE.md's completion checklist requires for a payload that
	// stays uninterpretable: nothing advances until the row expires and retention deletes
	// it.
	if _, err := rec.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if len(exec.resumeCalls) != 2 {
		t.Fatalf("ResumeNode calls across two ticks: want 2 (retried, not abandoned), got %d", len(exec.resumeCalls))
	}
}

// TestReconciler_RunOnce_PendingCallbackCredentialMismatch_LogsAndContinues proves that a
// stored early callback whose credential ResumeNode finds does not belong to the Attempt
// it was about to advance (service.PendingCallbackCredentialMismatchError,
// an unmatched Pending Callback has no right to advance Execution) is classified
// exactly like a rejected payload: not an operational error, not counted as resumed, logged
// at warn with only the external task id and attempt id (never a hash), and the pass
// continues to the next row instead of aborting.
func TestReconciler_RunOnce_PendingCallbackCredentialMismatch_LogsAndContinues(t *testing.T) {
	tx := emptyFakeTx()
	const secretHash = "super-secret-token-hash"
	tx.pending.consumable = []store.PendingForWaiting{
		{
			Pending: domain.PendingCallback{
				ExternalTaskID:    "task-mismatch",
				Payload:           json.RawMessage(`{"text":"hijacked"}`),
				PayloadHash:       "hash-mismatch",
				CallbackTokenHash: secretHash,
			},
			Binding:   domain.CallbackBinding{ID: "cb_3", TargetID: "at_3"},
			RunID:     "run_3",
			NodeRunID: "nr_3",
			AttemptID: "at_3",
		},
		{
			Pending: domain.PendingCallback{
				ExternalTaskID: "task-ok",
				Payload:        json.RawMessage(`{"text":"resumed"}`),
				PayloadHash:    "hash-ok",
			},
			Binding:   domain.CallbackBinding{ID: "cb_4", TargetID: "at_4"},
			RunID:     "run_4",
			NodeRunID: "nr_4",
			AttemptID: "at_4",
		},
	}
	exec := &fakeExecutor{
		resumeFunc: func(req service.ResumeNode) (service.ResumeOutcome, error) {
			if req.ExternalTaskID == "task-mismatch" {
				return service.ResumeOutcome{}, &service.PendingCallbackCredentialMismatchError{
					ExternalTaskID: req.ExternalTaskID,
					AttemptID:      "at_3",
				}
			}
			return service.ResumeOutcome{}, nil
		},
	}
	var logBuf bytes.Buffer
	rec := newTestReconciler(t, exec, tx, &logBuf)

	report, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(exec.resumeCalls) != 2 {
		t.Fatalf("ResumeNode calls: want 2 (the mismatch must not stop the scan), got %d", len(exec.resumeCalls))
	}
	if len(report.Errors) != 0 {
		t.Fatalf("report.Errors after a credential mismatch: want none (not an operational error), got %v", report.Errors)
	}
	if report.PendingCallbacksResumed != 1 {
		t.Fatalf("report.PendingCallbacksResumed: want 1 (only the matching row), got %d", report.PendingCallbacksResumed)
	}

	logged := logBuf.String()
	if !strings.Contains(logged, "level=WARN") {
		t.Fatalf("log output: want a WARN entry for the mismatched credential, got %q", logged)
	}
	if !strings.Contains(logged, "task-mismatch") || !strings.Contains(logged, "at_3") {
		t.Fatalf("log output: want the external task id and attempt id for correlation, got %q", logged)
	}
	if strings.Contains(logged, secretHash) || strings.Contains(logged, "hash-mismatch") {
		t.Fatalf("log output leaked the pending callback's token hash or payload hash: %q", logged)
	}
}

// TestReconciler_RunOnce_DeletesExpiredPendingCallbacks proves the retention scan:
// DeleteExpired runs every tick and its count is only surfaced through the report
// and a debug log, never as an operational error, and never logged when there was nothing
// to delete.
func TestReconciler_RunOnce_DeletesExpiredPendingCallbacks(t *testing.T) {
	tx := emptyFakeTx()
	tx.pending.deleteExpiredCount = 3
	exec := &fakeExecutor{}
	var logBuf bytes.Buffer
	rec := newTestReconciler(t, exec, tx, &logBuf)

	report, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if tx.pending.deleteExpiredCalls != 1 {
		t.Fatalf("DeleteExpired calls: want 1, got %d", tx.pending.deleteExpiredCalls)
	}
	if report.ExpiredPendingDeleted != 3 {
		t.Fatalf("report.ExpiredPendingDeleted: want 3, got %d", report.ExpiredPendingDeleted)
	}
	if !strings.Contains(logBuf.String(), "level=DEBUG") {
		t.Fatalf("log output: want a DEBUG entry when count > 0, got %q", logBuf.String())
	}

	// Invert: when nothing is deleted, nothing is logged at all (not even at debug).
	tx2 := emptyFakeTx()
	tx2.pending.deleteExpiredCount = 0
	var logBuf2 bytes.Buffer
	rec2 := newTestReconciler(t, &fakeExecutor{}, tx2, &logBuf2)
	if _, err := rec2.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce (zero deleted): %v", err)
	}
	if logBuf2.Len() != 0 {
		t.Fatalf("log output when nothing was deleted: want empty, got %q", logBuf2.String())
	}
}

// TestReconciler_DuePolls_OnlyCallsPollAttempt proves the due-poll scan only discovers
// work: every row ListDuePolls returns is handed to the PollAttempt use case with its
// persisted identifiers, the Reconciler never claims or clears a poll itself, each outcome
// kind lands in its own Report count, and an error from one Attempt is collected without
// stopping the polls of the others.
func TestReconciler_DuePolls_OnlyCallsPollAttempt(t *testing.T) {
	tx := emptyFakeTx()
	tx.attempts.duePolls = []store.DuePoll{
		{AttemptID: "at_running", NodeRunID: "nr_1", RunID: "run_1", PollCount: 0},
		{AttemptID: "at_broken", NodeRunID: "nr_2", RunID: "run_2", PollCount: 4},
		{AttemptID: "at_resumed", NodeRunID: "nr_3", RunID: "run_3", PollCount: 1},
		{AttemptID: "at_duplicate", NodeRunID: "nr_4", RunID: "run_4", PollCount: 2},
		{AttemptID: "at_skipped", NodeRunID: "nr_5", RunID: "run_5", PollCount: 3},
	}
	providerDown := errors.New("provider connection refused")
	exec := &fakeExecutor{
		pollFunc: func(req service.PollAttempt) (service.PollAttemptOutcome, error) {
			switch req.AttemptID {
			case "at_running":
				return service.PollAttemptOutcome{Kind: service.PollAttemptClaimedNotFinal}, nil
			case "at_broken":
				return service.PollAttemptOutcome{Kind: service.PollAttemptClaimedNotFinal}, providerDown
			case "at_resumed":
				return service.PollAttemptOutcome{Kind: service.PollAttemptResumed}, nil
			case "at_duplicate":
				return service.PollAttemptOutcome{Kind: service.PollAttemptDuplicate}, nil
			default:
				return service.PollAttemptOutcome{Kind: service.PollAttemptSkipped, SkipReason: service.PollSkipNotClaimed}, nil
			}
		},
	}
	var logBuf bytes.Buffer
	rec := newTestReconciler(t, exec, tx, &logBuf)

	report, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if len(exec.pollCalls) != len(tx.attempts.duePolls) {
		t.Fatalf("PollAttempt calls: want %d (one error must not stop the scan), got %d", len(tx.attempts.duePolls), len(exec.pollCalls))
	}
	for i, row := range tx.attempts.duePolls {
		want := service.PollAttempt{AttemptID: row.AttemptID, NodeRunID: row.NodeRunID, RunID: row.RunID}
		if exec.pollCalls[i] != want {
			t.Fatalf("PollAttempt call %d: want %+v, got %+v", i, want, exec.pollCalls[i])
		}
	}
	if len(tx.attempts.duePollLimits) != 1 || tx.attempts.duePollLimits[0] != 50 {
		t.Fatalf("ListDuePolls: want one call bounded by the batch limit 50, got %v", tx.attempts.duePollLimits)
	}
	if len(tx.attempts.pollWrites) != 0 {
		t.Fatalf("poll schedule writes by the Reconciler: want none, got %v", tx.attempts.pollWrites)
	}
	if len(exec.resumeCalls) != 0 {
		t.Fatalf("ResumeNode calls: want none (only PollAttempt enters the resume path), got %d", len(exec.resumeCalls))
	}

	if report.DuePollsFound != 5 || report.PollsClaimed != 2 || report.PollsResumed != 1 ||
		report.PollsDuplicate != 1 || report.PollsSkipped != 1 {
		t.Fatalf("report: want found 5, claimed 2, resumed 1, duplicate 1, skipped 1, got %+v", report)
	}
	if len(report.Errors) != 1 || !errors.Is(report.Errors[0], providerDown) || !strings.Contains(report.Errors[0].Error(), "at_broken") {
		t.Fatalf("report.Errors: want only the at_broken error, got %v", report.Errors)
	}
}

// TestReconciler_OrphanScan_HandsEachOrphanToUseCase proves the orphan scan only discovers
// work: it lists with this process's runner identity and hands every Attempt to the
// orphaned Tool Attempt use case, after the deadline scan and before the READY Turn scan.
func TestReconciler_OrphanScan_HandsEachOrphanToUseCase(t *testing.T) {
	exec := &fakeExecutor{}
	tx := emptyFakeTx()
	tx.toolAttempts = &fakeToolAttemptRepo{orphans: []domain.ToolAttempt{{ID: "ta_1"}, {ID: "ta_2"}}}
	tx.agentRuns.expired = []domain.AgentRun{{ID: "ar_expired"}}
	tx.agentTurns.ready = []domain.AgentTurn{{ID: "turn_ready"}}

	var logBuf bytes.Buffer
	rec := newTestReconciler(t, exec, tx, &logBuf)
	rec.cfg.RunnerID = "runner-current"
	report, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := tx.toolAttempts.runnerSeen; len(got) != 1 || got[0] != "runner-current" {
		t.Errorf("orphan scan runner = %v, want [runner-current]", got)
	}
	if len(exec.orphanCalls) != 2 || exec.orphanCalls[0] != "ta_1" || exec.orphanCalls[1] != "ta_2" {
		t.Errorf("orphan use case calls = %v, want [ta_1 ta_2]", exec.orphanCalls)
	}
	if report.OrphanToolAttemptsFound != 2 || report.OrphanToolAttemptsHandled != 2 {
		t.Errorf("report = %+v, want 2 found and 2 handled", report)
	}
	if len(exec.agentTimeoutCalls) != 1 || len(exec.agentTurnCalls) != 1 {
		t.Errorf("timeout calls %v, turn calls %v, want one each", exec.agentTimeoutCalls, exec.agentTurnCalls)
	}
}

// TestReconciler_ExpiredApprovalScan_HandsEachApprovalToUseCase proves the approval expiry
// scan only discovers work: every PENDING approval the lister returns is handed to the
// expiry use case, before the Agent deadline scan, and the Reconciler decides nothing itself.
func TestReconciler_ExpiredApprovalScan_HandsEachApprovalToUseCase(t *testing.T) {
	exec := &fakeExecutor{}
	tx := emptyFakeTx()
	tx.approvals.expired = []domain.Approval{{ID: "approval_1"}, {ID: "approval_2"}}
	tx.agentRuns.expired = []domain.AgentRun{{ID: "ar_expired"}}

	var logBuf bytes.Buffer
	report, err := newTestReconciler(t, exec, tx, &logBuf).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(exec.expireCalls) != 2 || exec.expireCalls[0] != "approval_1" || exec.expireCalls[1] != "approval_2" {
		t.Errorf("expiry use case calls = %v, want [approval_1 approval_2]", exec.expireCalls)
	}
	if report.ExpiredApprovalsFound != 2 || report.ApprovalsExpired != 2 {
		t.Errorf("report = %+v, want 2 found and 2 expired", report)
	}
	if len(exec.agentTimeoutCalls) != 1 {
		t.Errorf("timeout calls %v, want the expired agent run still handled", exec.agentTimeoutCalls)
	}
}

// TestReconciler_OrphanScan_SkippedWithoutRunnerID proves a Reconciler with no runner
// identity never scans for orphans: the orphan lister is never asked about any runner.
func TestReconciler_OrphanScan_SkippedWithoutRunnerID(t *testing.T) {
	exec := &fakeExecutor{}
	tx := emptyFakeTx()
	tx.toolAttempts.orphans = []domain.ToolAttempt{{ID: "ta_1"}}
	var logBuf bytes.Buffer
	report, err := newTestReconciler(t, exec, tx, &logBuf).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(tx.toolAttempts.runnerSeen) != 0 || len(exec.orphanCalls) != 0 || report.OrphanToolAttemptsFound != 0 {
		t.Errorf("runners asked = %v, orphan calls = %v, report = %+v, want no orphan scan", tx.toolAttempts.runnerSeen, exec.orphanCalls, report)
	}
}

// TestReconciler_DueToolPolls_OnlyCallsPollToolAttempt proves the Tool row of the due-poll
// scan only discovers work: every row the Tool Attempt lister returns is handed to the Tool
// poll use case with its persisted identifiers, bounded by the batch limit, each outcome
// kind lands in its own Report count, the Reconciler never claims or clears a poll itself,
// and an error from one Attempt is collected without stopping the others.
func TestReconciler_DueToolPolls_OnlyCallsPollToolAttempt(t *testing.T) {
	tx := emptyFakeTx()
	tx.toolAttempts.duePolls = []store.DueToolPoll{
		{AttemptID: "ta_running", ActionID: "act_1"},
		{AttemptID: "ta_broken", ActionID: "act_2"},
		{AttemptID: "ta_resumed", ActionID: "act_3"},
		{AttemptID: "ta_returned", ActionID: "act_4"},
		{AttemptID: "ta_duplicate", ActionID: "act_5"},
		{AttemptID: "ta_skipped", ActionID: "act_6"},
	}
	providerDown := errors.New("provider connection refused")
	exec := &fakeExecutor{
		toolPollFunc: func(req service.PollToolAttempt) (service.PollToolAttemptOutcome, error) {
			switch req.AttemptID {
			case "ta_running":
				return service.PollToolAttemptOutcome{Kind: service.PollAttemptClaimedNotFinal}, nil
			case "ta_broken":
				return service.PollToolAttemptOutcome{Kind: service.PollAttemptClaimedNotFinal}, providerDown
			case "ta_resumed":
				return service.PollToolAttemptOutcome{Kind: service.PollAttemptResumed}, nil
			case "ta_returned":
				return service.PollToolAttemptOutcome{Kind: service.PollAttemptReturned}, nil
			case "ta_duplicate":
				return service.PollToolAttemptOutcome{Kind: service.PollAttemptDuplicate}, nil
			default:
				return service.PollToolAttemptOutcome{Kind: service.PollAttemptSkipped, SkipReason: service.PollSkipNotClaimed}, nil
			}
		},
	}
	var logBuf bytes.Buffer
	rec := newTestReconciler(t, exec, tx, &logBuf)

	report, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(exec.toolPollCalls) != len(tx.toolAttempts.duePolls) {
		t.Fatalf("PollToolAttempt calls: want %d (one error must not stop the scan), got %d", len(tx.toolAttempts.duePolls), len(exec.toolPollCalls))
	}
	for i, row := range tx.toolAttempts.duePolls {
		want := service.PollToolAttempt{AttemptID: row.AttemptID, ActionID: row.ActionID}
		if exec.toolPollCalls[i] != want {
			t.Fatalf("PollToolAttempt call %d: want %+v, got %+v", i, want, exec.toolPollCalls[i])
		}
	}
	if len(tx.toolAttempts.duePollLimits) != 1 || tx.toolAttempts.duePollLimits[0] != 50 {
		t.Fatalf("ListDuePolls: want one call bounded by the batch limit 50, got %v", tx.toolAttempts.duePollLimits)
	}
	if len(exec.resumeCalls) != 0 || len(exec.pollCalls) != 0 {
		t.Fatalf("ResumeNode calls %d, PollAttempt calls %d: want none, only PollToolAttempt enters the Tool resume path", len(exec.resumeCalls), len(exec.pollCalls))
	}
	if report.DueToolPollsFound != 6 || report.ToolPollsClaimed != 3 || report.ToolPollsResumed != 1 || report.ToolPollsReturned != 1 ||
		report.ToolPollsDuplicate != 1 || report.ToolPollsSkipped != 1 {
		t.Fatalf("report: want found 6, claimed 3, resumed 1, returned 1, duplicate 1, skipped 1, got %+v", report)
	}
	if len(report.Errors) != 1 || !errors.Is(report.Errors[0], providerDown) || !strings.Contains(report.Errors[0].Error(), "ta_broken") {
		t.Fatalf("report.Errors: want only the ta_broken error, got %v", report.Errors)
	}
}
