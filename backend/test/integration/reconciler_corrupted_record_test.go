//go:build integration

// Reconciler proof for docs/09-testing-and-acceptance.md §3.9 Backend readiness, row
// "首次扫描遇到一条损坏记录" (the first scan encounters one corrupted record): the scan must
// not abort, and must still process the other Run in the same batch. What "corrupted"
// means for an expired Attempt whose Node Type the Registry no longer carries is governed
// by the more specific recovery contract in 09 §3.4 ("恢复已有 Run 时 Node、Model、Tool 或
// Provider 实现缺失 -> 受影响的 NodeRun、Turn 或 Action 明确失败，并保留对应 Event 与
// Trace") and 06 §4 ("缺失或不兼容时必须确定性失败并保留 Trace"): the record is not merely
// logged and left stuck, it is deterministically failed with a retained NODE_FAILED Event,
// exactly like every other recovery entry point that meets a dropped registration. So the
// scan "continuing" here means both Attempts in the batch reach a resolved terminal state
// in the same pass -- the healthy one via the ordinary TIMEOUT path, the drifted one via
// the same NODE_TYPE_NOT_REGISTERED path Execute already uses -- and RunOnce's own return
// stays nil either way. Reuses asyncFakeExecutor, asyncRegistration's shape and the
// getAttempt/getNodeRun/getRun/listEvents helpers from service_async_test.go and
// store_test.go (CLAUDE.md "locate the existing implementation and tests before
// introducing a new abstraction"); the harness itself is bespoke because every existing
// async harness (newAsyncHarness) hardcodes a single Node Type, and this proof needs two
// Runs whose Node Types differ so only one of them can be made "corrupted".
package integration

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/textinput"
	"github.com/leungll/Emberling/backend/internal/nodes/textoutput"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

// corruptedRecordHealthyType and corruptedRecordDriftedType are two distinct async Node
// Types, so a Registry that only drops one of them (a process restart that lost an
// extension's registration, docs/07-extensibility.md §1) can corrupt exactly one Run's
// Attempt while leaving the other's fully recoverable.
const (
	corruptedRecordHealthyType = "async_dispatch_healthy"
	corruptedRecordDriftedType = "async_dispatch_drifted"
)

// corruptedRecordRegistration mirrors asyncRegistration (service_async_test.go) but takes
// the Node Type as a parameter, since this test needs two registrations that share
// asyncFakeExecutor's dispatch/callback behaviour under different Type names.
func corruptedRecordRegistration(nodeType string, executor registry.NodeExecutor, side domain.SideEffectPolicy) registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          nodeType,
			DisplayName:   "Async Dispatch (" + nodeType + ")",
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

// corruptedRecordNodes builds a Registry containing text_input, text_output and exactly
// the async Node Types listed in nodeTypes, all bound to the same executor. Passing a
// subset of the two corruptedRecord* constants is how this test simulates registry drift
// after a restart.
func corruptedRecordNodes(t *testing.T, executor registry.NodeExecutor, side domain.SideEffectPolicy, nodeTypes ...string) *registry.NodeRegistry {
	t.Helper()
	nodes := registry.NewNodeRegistry()
	regs := []registry.NodeRegistration{textinput.Registration(), textoutput.Registration()}
	for _, nt := range nodeTypes {
		regs = append(regs, corruptedRecordRegistration(nt, executor, side))
	}
	for _, reg := range regs {
		if err := nodes.Register(reg); err != nil {
			t.Fatalf("register node type: %v", err)
		}
	}
	return nodes
}

// corruptedRecordDefinition saves a one-Node async Definition (text_input -> nodeType ->
// text_output), matching saveAsyncDefinition's shape (service_async_test.go) but
// parameterized on workflowID/nodeType so this test can create two distinct Definitions
// in the same database.
func corruptedRecordDefinition(t *testing.T, ctx context.Context, uow store.UnitOfWork, clock *execClock, workflowID, nodeType string, executor registry.NodeExecutor, side domain.SideEffectPolicy, policy domain.ExecutionPolicy) domain.Definition {
	t.Helper()
	task := domain.Node{ID: "task", Type: nodeType, Name: "Dispatch", Config: json.RawMessage(`{}`)}
	if policy.MaxAttempts > 0 || policy.TimeoutMs > 0 {
		p := policy
		task.ExecutionPolicy = &p
	}
	def := domain.Definition{
		WorkflowID:  workflowID,
		Version:     1,
		Name:        "Async dispatch (" + nodeType + ")",
		Description: "text_input -> " + nodeType + " -> text_output",
		Nodes: []domain.Node{
			{ID: "input", Type: "text_input", Name: "Brief", Config: json.RawMessage(`{"inputKey":"brief","required":true}`)},
			task,
			{ID: "output", Type: "text_output", Name: "Output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "input", SourceHandle: "text", Target: "task", TargetHandle: "text"},
			{ID: "e2", Source: "task", SourceHandle: "text", Target: "output", TargetHandle: "text"},
		},
		CreatedAt: fixtureTime,
	}

	nodes := corruptedRecordNodes(t, executor, side, nodeType)
	compiler := runtime.NewCompiler(nodes, clock)
	plan, err := compiler.Compile(ctx, def)
	if err != nil {
		t.Fatalf("compile definition %s: %v", workflowID, err)
	}
	def.RunInputSchema = plan.RunInputSchema
	def.Validation = plan.Validation

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.Definitions().Save(ctx, def)
	}); err != nil {
		t.Fatalf("save definition %s: %v", workflowID, err)
	}
	return def
}

// corruptedRecordDispatch advances run until nodeType's Attempt is claimed, dispatches it
// through svc.Execute, and returns the committed NodeRun/Attempt ids -- the single-Node
// equivalent of asyncHarness.dispatchAsyncNode (service_async_test.go), generalized over
// which service and Node Type drive the claim since this test uses two different
// Definitions, each with its own Node Type.
func corruptedRecordDispatch(t *testing.T, ctx context.Context, svc *service.ExecutionService, run domain.Run, nodeType string, externalTaskID string) (nodeRunID, attemptID string) {
	t.Helper()
	var outcome service.AdvanceOutcome
	for i := 0; i < 10; i++ {
		out, err := svc.Advance(ctx, run.ID)
		if err != nil {
			t.Fatalf("advance: %v", err)
		}
		if !out.Claimed {
			t.Fatalf("advance: no claim before reaching node type %s", nodeType)
		}
		if out.NodeType == nodeType {
			outcome = out
			break
		}
		if err := svc.Execute(ctx, out); err != nil {
			t.Fatalf("execute %s: %v", out.NodeType, err)
		}
	}
	if outcome.NodeRunID == "" {
		t.Fatalf("node type %s was never claimed", nodeType)
	}
	if outcome.Input.Callback == nil || outcome.Input.Callback.Token == "" {
		t.Fatalf("claimed async attempt for %s carries no callback credential", nodeType)
	}
	if err := svc.Execute(ctx, outcome); err != nil {
		t.Fatalf("execute async dispatch %s: %v", nodeType, err)
	}
	_ = externalTaskID // set on the shared executor before calling this helper
	return outcome.NodeRunID, outcome.AttemptID
}

// TestReconciler_ExpiredAttemptWithDriftedNodeType_RecordsErrorAndContinuesScan proves the
// 09 §3.9 "首次扫描遇到一条损坏记录" row together with 09 §3.4 / 06 §4's more specific
// recovery rule: a scanned Attempt whose Node Type the reconciling process's Registry no
// longer carries must still resolve to a deterministic FAILED outcome with a retained
// NODE_FAILED Event (error code NODE_TYPE_NOT_REGISTERED), the same way Execute's own
// unregistered-Node-Type path already behaves -- not bubble a bare Go error that would
// abort TimeoutAttempt's guard transaction and leave the row permanently stuck. The
// sibling healthy Attempt, discovered in the very same batch, must still be timed out
// normally via the ordinary TIMEOUT path, and RunOnce's own returned error must stay nil
// so readiness's FirstScan check (cmd/emberling/main.go: `_, err := rec.RunOnce(ctx); return
// err`) is never blocked by a single corrupted record. Because the drifted record is now
// fully (not partially) resolved, it counts as an ordinary AttemptsTimedOut, not a
// Report.Errors entry -- the NODE_FAILED Event and Trace are its durable record, matching
// how the healthy record's own TIMEOUT failure is never itself a Report.Errors entry
// either.
func TestReconciler_ExpiredAttemptWithDriftedNodeType_RecordsErrorAndContinuesScan(t *testing.T) {
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	clock := newExecClock(fixtureTime)
	ids := newExecForcedIDs()
	exec := &asyncFakeExecutor{externalTaskID: "provider-task-healthy"}

	side := domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencyUnknown}
	policy := domain.ExecutionPolicy{TimeoutMs: 60_000, MaxAttempts: 1}

	// The process that creates and dispatches both Runs has both Node Types registered --
	// this is the ordinary, fully-wired Backend before any restart.
	fullNodes := corruptedRecordNodes(t, exec, side, corruptedRecordHealthyType, corruptedRecordDriftedType)
	svc := service.NewExecutionService(service.Deps{
		UoW:      uow,
		Nodes:    fullNodes,
		Compiler: runtime.NewCompiler(fullNodes, clock),
		Clock:    clock,
		IDs:      ids,
		Callback: service.CallbackConfig{
			BaseURL:       asyncBaseURL,
			SigningSecret: []byte(asyncSigningSecret),
			PendingTTL:    time.Hour,
		},
	})

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	defHealthy := corruptedRecordDefinition(t, ctx, uow, clock, "wf_reconcile_healthy_"+suffix, corruptedRecordHealthyType, exec, side, policy)
	defDrifted := corruptedRecordDefinition(t, ctx, uow, clock, "wf_reconcile_drifted_"+suffix, corruptedRecordDriftedType, exec, side, policy)

	runHealthy, err := svc.CreateRun(ctx, service.CreateRun{
		WorkflowID: defHealthy.WorkflowID, DefinitionVersion: defHealthy.Version,
		Input: json.RawMessage(`{"brief":"healthy"}`),
	})
	if err != nil {
		t.Fatalf("create healthy run: %v", err)
	}
	runDrifted, err := svc.CreateRun(ctx, service.CreateRun{
		WorkflowID: defDrifted.WorkflowID, DefinitionVersion: defDrifted.Version,
		Input: json.RawMessage(`{"brief":"drifted"}`),
	})
	if err != nil {
		t.Fatalf("create drifted run: %v", err)
	}

	exec.setExternalTaskID("provider-task-healthy")
	healthyNodeRunID, healthyAttemptID := corruptedRecordDispatch(t, ctx, svc, runHealthy, corruptedRecordHealthyType, "provider-task-healthy")
	exec.setExternalTaskID("provider-task-drifted")
	driftedNodeRunID, driftedAttemptID := corruptedRecordDispatch(t, ctx, svc, runDrifted, corruptedRecordDriftedType, "provider-task-drifted")

	// Both Attempts are now DISPATCHED with a deadline; advance past it so ListExpired
	// finds both in the same scan.
	clock.Advance(2 * time.Minute)

	// Simulate a restart whose Registry lost corruptedRecordDriftedType (an extension that
	// failed to load this run of the process) while corruptedRecordHealthyType is still
	// registered. This is the Reconciler's own Executor from here on -- the Reconciler
	// never mutates business state itself (CLAUDE.md package boundaries), so every effect
	// below flows through this restarted ExecutionService's use cases.
	restartedNodes := corruptedRecordNodes(t, exec, side, corruptedRecordHealthyType)
	restartedSvc := service.NewExecutionService(service.Deps{
		UoW:      uow,
		Nodes:    restartedNodes,
		Compiler: runtime.NewCompiler(restartedNodes, clock),
		Clock:    clock,
		IDs:      ids,
		Callback: service.CallbackConfig{
			BaseURL:       asyncBaseURL,
			SigningSecret: []byte(asyncSigningSecret),
			PendingTTL:    time.Hour,
		},
	})

	rec := reconciler.New(reconciler.Config{UoW: uow, Executor: restartedSvc, Clock: clock, BatchLimit: 100})
	report, err := rec.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce returned a top-level error even though the scan mechanism itself is healthy: %v", err)
	}

	if report.ExpiredAttemptsFound != 2 {
		t.Fatalf("ExpiredAttemptsFound: want 2 (both Attempts discovered despite one being corrupted), got %d", report.ExpiredAttemptsFound)
	}
	if report.AttemptsTimedOut != 2 {
		t.Fatalf("AttemptsTimedOut: want 2 (both records resolved -- registry drift fails deterministically rather than staying stuck), got %d", report.AttemptsTimedOut)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("Errors: want none (the drifted record is fully resolved, not merely logged), got %d: %v", len(report.Errors), report.Errors)
	}

	// The healthy Run's Attempt/NodeRun/Run were advanced by the ordinary TIMEOUT path.
	if got := getAttempt(ctx, t, uow, healthyAttemptID).Status; got != domain.NodeAttemptFailed {
		t.Fatalf("healthy Attempt status: want FAILED, got %s", got)
	}
	if got := getNodeRun(ctx, t, uow, healthyNodeRunID).Status; got != domain.NodeRunFailed {
		t.Fatalf("healthy NodeRun status: want FAILED, got %s", got)
	}
	if got := getRun(ctx, t, uow, runHealthy.ID).Status; got != domain.RunFailed {
		t.Fatalf("healthy Run status: want FAILED, got %s", got)
	}

	// The drifted Run's Attempt/NodeRun/Run were also advanced, via TimeoutAttempt's
	// NODE_TYPE_NOT_REGISTERED path (execution.go's TimeoutAttempt, mirroring Execute):
	// 09 §3.4 / 06 §4 require a deterministic failure with a retained Event, not a record
	// left stuck DISPATCHED/WAITING_CALLBACK forever.
	if got := getAttempt(ctx, t, uow, driftedAttemptID).Status; got != domain.NodeAttemptFailed {
		t.Fatalf("drifted Attempt status: want FAILED, got %s", got)
	}
	if got := getNodeRun(ctx, t, uow, driftedNodeRunID).Status; got != domain.NodeRunFailed {
		t.Fatalf("drifted NodeRun status: want FAILED, got %s", got)
	}
	if got := getRun(ctx, t, uow, runDrifted.ID).Status; got != domain.RunFailed {
		t.Fatalf("drifted Run status: want FAILED, got %s", got)
	}

	driftedEvents := listEvents(ctx, t, uow, runDrifted.ID)
	failedEvents := asyncNodeEventsOfType(driftedEvents, driftedNodeRunID, domain.EventNodeFailed)
	if len(failedEvents) != 1 {
		t.Fatalf("NODE_FAILED events for the drifted NodeRun: want 1, got %d: %v", len(failedEvents), execEventTypes(driftedEvents))
	}
	var payload struct {
		Error domain.ExecutionError `json:"error"`
	}
	if err := json.Unmarshal(failedEvents[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal NODE_FAILED payload: %v", err)
	}
	if payload.Error.Code != "NODE_TYPE_NOT_REGISTERED" {
		t.Fatalf("NODE_FAILED error code: want NODE_TYPE_NOT_REGISTERED, got %s", payload.Error.Code)
	}
	if !strings.Contains(payload.Error.Message, corruptedRecordDriftedType) {
		t.Fatalf("NODE_FAILED error message does not identify the drifted node type %s: %s", corruptedRecordDriftedType, payload.Error.Message)
	}
}
