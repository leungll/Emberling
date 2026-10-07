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
	expired []domain.NodeAttempt
}

func (r *fakeNodeAttemptRepo) ListExpired(context.Context, time.Time, int) ([]domain.NodeAttempt, error) {
	return r.expired, nil
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
}

func (t *fakeTx) NodeRuns() store.NodeRunRepository                 { return t.nodeRuns }
func (t *fakeTx) NodeAttempts() store.NodeAttemptRepository         { return t.attempts }
func (t *fakeTx) PendingCallbacks() store.PendingCallbackRepository { return t.pending }
func (t *fakeTx) AgentTurns() store.AgentTurnRepository             { return t.agentTurns }
func (t *fakeTx) AgentActions() store.AgentActionRepository         { return t.agentActions }
func (t *fakeTx) AgentRuns() store.AgentRunRepository               { return t.agentRuns }

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

	agentTurnCalls    []agentCall
	agentToolCalls    []agentCall
	agentFinalCalls   []agentCall
	agentTimeoutCalls []string
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

func (e *fakeExecutor) ResumeNode(_ context.Context, req service.ResumeNode) (service.ResumeOutcome, error) {
	e.resumeCalls = append(e.resumeCalls, req)
	if e.resumeFunc != nil {
		return e.resumeFunc(req)
	}
	return service.ResumeOutcome{}, nil
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
