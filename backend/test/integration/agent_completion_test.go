//go:build integration

// The goal-based completion check against PostgreSQL: when an Agent Run with a frozen goal
// ends, the terminating transaction decides the task result from the committed facts,
// writes the verdict and decides how the Agent NodeRun ends. These tests prove what a mock
// repository cannot: the verdict, its Event and the termination commit together and roll
// back together; an INCOMPLETE verdict fails the NodeRun and the Run; a complete PREPARE_ONLY
// task succeeds the NodeRun with the verdict on its port; a timeout records INCOMPLETE; and
// an Agent without a goal is never evaluated.
//
// They share the agentHarness of agent_loop_test.go, the workspace fixtures of
// agent_workspace_test.go, the runner stub of agent_verify_patch_test.go, the change
// request helpers of agent_change_request_test.go and the test task-input node of
// agent_approval_reconcile_test.go.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/asset"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/runtime"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/changerequest"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
	"github.com/leungll/Emberling/backend/internal/tools/verifypatch"
	"github.com/leungll/Emberling/backend/internal/tools/workspace"
)

// completionReport is the FINAL output every scripted Agent below declares completion with.
const completionReport = `{"status":"DONE","advisory":"GO-2025-3553"}`

// completionFixture is one Backend with the test task-input node, the workspace Tools,
// verify_patch against the runner stub and create_change_request registered, bound to a
// generated repository.
type completionFixture struct {
	h         *agentHarness
	repo      *workspaceRepo
	stub      *verifyRunnerStub
	artifacts *asset.ArtifactStore
	run       domain.Run
	outcome   service.AdvanceOutcome
}

type completionFixtureOptions struct {
	notifier       service.EventNotifier
	lookupExecutor registry.ToolExecutor
}

func newCompletionFixture(t *testing.T, opts completionFixtureOptions) *completionFixture {
	t.Helper()
	stub := newVerifyRunnerStub()
	runner := httptest.NewServer(stub)
	t.Cleanup(runner.Close)
	artifacts := asset.NewArtifactStore(t.TempDir(), 1<<20)
	if err := artifacts.VerifyReadWrite(context.Background()); err != nil {
		t.Fatalf("artifact store: %v", err)
	}
	repo := newWorkspaceRepo(t)
	tools := append(workspace.Registrations(workspace.NewLocalGit(map[string]string{wsRepositoryID: repo.path})),
		verifypatch.Registration(runner.URL, runner.Client(), artifacts),
		changerequest.Registration(artifacts),
	)
	h := newAgentHarness(t, agentHarnessOptions{
		Nodes:                 []registry.NodeRegistration{approvalTaskInputRegistration()},
		Tools:                 tools,
		WorkspaceRepositories: []string{wsRepositoryID},
		Notifier:              opts.notifier,
		LookupExecutor:        opts.lookupExecutor,
	})
	return &completionFixture{h: h, repo: repo, stub: stub, artifacts: artifacts}
}

// completionDefinition is the fixture graph: the test task input publishes {goal, question},
// the Agent reads its goal at /goal, is bound to the repository at commit, may call the
// given Tools and answers with a JSON object; its answer feeds a text output.
func completionDefinition(workflowID string, goal domain.AgentGoal, commit string, tools ...string) domain.Definition {
	config := map[string]any{
		"instructions": agentInstructions,
		"modelId":      mockmodel.ModelID,
		"allowedTools": tools,
		"maxTurns":     wsMaxTurns,
		"timeoutMs":    agentTimeoutMs,
		"goalPointer":  "/goal",
		"outputSchema": map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"status": map[string]any{"type": "string"}, "advisory": map[string]any{"type": "string"}},
			"required":             []string{"status", "advisory"},
			"additionalProperties": false,
		},
		"workspace": map[string]any{
			"repositoryId": wsRepositoryID,
			"baseCommit":   commit,
			"allowedPaths": []string{"go.mod", "internal/auth/"},
		},
	}
	encoded, _ := json.Marshal(config)
	task, _ := json.Marshal(map[string]any{"task": map[string]any{"goal": goal, "question": agentQuestion}})
	return domain.Definition{
		WorkflowID:  workflowID,
		Version:     1,
		Name:        "Completion check",
		Description: "task input -> agent -> text_output",
		Nodes: []domain.Node{
			{ID: "node_input", Type: approvalTaskInputType, Name: "Task", Config: task},
			{ID: "node_agent", Type: "agent", Name: "Agent", Config: encoded},
			{ID: "node_output", Type: "text_output", Name: "Output", Config: json.RawMessage(`{}`)},
		},
		Edges: []domain.Edge{
			{ID: "e1", Source: "node_input", SourceHandle: "text", Target: "node_agent", TargetHandle: "input"},
			{ID: "e2", Source: "node_agent", SourceHandle: "text", Target: "node_output", TargetHandle: "text"},
		},
	}
}

// start saves the Definition, creates the Run, claims the Agent NodeRun and scripts the
// calls; the model declares completion with completionReport once they are used up. It then
// executes the first Turn and drains every Turn the loop commits.
func (f *completionFixture) start(t *testing.T, workflowID string, goal domain.AgentGoal, tools []string, calls ...crCall) {
	t.Helper()
	saved := f.h.saveDefinition(completionDefinition(workflowID, goal, f.repo.commit, tools...))
	f.run = f.h.createRun(saved.WorkflowID, saved.Version, `{}`)
	f.outcome = f.h.claimAgentNode(f.run.ID)

	var turn atomic.Int32
	f.h.provider.Script = func(registry.ModelRequest) *mockmodel.Scenario {
		n := int(turn.Add(1))
		if n > len(calls) {
			return &mockmodel.Scenario{Kind: mockmodel.ScenarioFinal, FinalOutput: json.RawMessage(completionReport)}
		}
		call := calls[n-1]()
		call.Kind = mockmodel.ScenarioToolCall
		return &call
	}
	f.h.execute(f.outcome)
}

func (f *completionFixture) agentRunID(t *testing.T) string {
	t.Helper()
	return factAgentRunID(t, f.h, f.outcome)
}

// deliverLatestVerification delivers the runner's callback for the one verification the
// stub accepted, about the workspace's latest patch, and drains the Turns that follow.
func (f *completionFixture) deliverLatestVerification(t *testing.T, passed bool) {
	t.Helper()
	latest := wsLatestPatch(t, f.h, f.agentRunID(t))
	if latest == nil {
		t.Fatalf("no workspace patch to verify")
	}
	token, _ := f.stub.dispatched(t)
	delivered, err := f.h.svc.HandleCallback(f.h.ctx, service.HandleCallback{
		Token: token, ExternalTaskID: verifyExternalTaskID,
		Payload: verifySucceededPayload(f.repo.commit, latest.PatchDigest, "sha256:verified", passed),
	})
	if err != nil || !delivered.Accepted || delivered.Duplicate {
		t.Fatalf("callback = %+v, %v, want accepted", delivered, err)
	}
	if err := f.h.drainTurns(f.h.ctx); err != nil {
		t.Fatalf("drain turns after the verification: %v", err)
	}
}

var completionTools = append(append([]string{}, wsAllTools...), verifypatch.ToolName, changerequest.ToolName)

// completionOf reads the persisted verdict of one Agent Run, or nil when none exists.
func completionOf(t *testing.T, h *agentHarness, agentRunID string) *domain.AgentCompletion {
	t.Helper()
	var completion *domain.AgentCompletion
	err := h.uow.WithinReadTx(h.ctx, func(ctx context.Context, tx store.Tx) error {
		got, err := tx.AgentCompletions().Get(ctx, agentRunID)
		if err != nil {
			return err
		}
		completion = &got
		return nil
	})
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		t.Fatalf("read completion of agent run %s: %v", agentRunID, err)
	}
	return completion
}

func completionMissing(completion *domain.AgentCompletion) []string {
	codes := make([]string, 0, len(completion.Missing))
	for _, item := range completion.Missing {
		codes = append(codes, item.Code)
	}
	return codes
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// completionNodeOutput decodes the verdict the Agent NodeRun published on its text port.
type completionNodeOutput struct {
	Outcome     domain.CompletionOutcome    `json:"outcome"`
	Checks      []domain.CompletionCheck    `json:"checks"`
	Missing     []domain.CompletionMissing  `json:"missing"`
	Evidence    []domain.CompletionEvidence `json:"evidence"`
	AgentReport json.RawMessage             `json:"agentReport"`
}

func decodeCompletionNodeOutput(t *testing.T, nodeRun domain.NodeRun) completionNodeOutput {
	t.Helper()
	var port map[string]string
	if err := json.Unmarshal(nodeRun.Output, &port); err != nil {
		t.Fatalf("node output %s is not a port map: %v", nodeRun.Output, err)
	}
	var out completionNodeOutput
	if err := json.Unmarshal([]byte(port["text"]), &out); err != nil {
		t.Fatalf("text port %q is not the completion verdict: %v", port["text"], err)
	}
	return out
}

// ---------------------------------------------------------------------------
// INCOMPLETE on FINAL
// ---------------------------------------------------------------------------

// TestAgentCompletion_FinalWithoutVerification_IncompleteFailsNodeRun: a PREPARE_ONLY Agent
// writes a patch and declares completion without verifying it. The Agent Run ends with
// FINAL_RESPONSE (the model answered), but the check finds no verification and no change
// request: one INCOMPLETE verdict, one AGENT_COMPLETION_EVALUATED, the Agent NodeRun FAILED
// with TASK_INCOMPLETE naming the missing items, and the Run FAILED. Nothing downstream ran.
func TestAgentCompletion_FinalWithoutVerification_IncompleteFailsNodeRun(t *testing.T) {
	f := newCompletionFixture(t, completionFixtureOptions{})
	f.start(t, "wf-completion-incomplete", domain.AgentGoalPrepareOnly, completionTools,
		fixed(wsWrite(wsTokenPath, wsNewToken)),
	)
	agentRunID := f.agentRunID(t)

	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, f.outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Fatalf("agent termination = %s, want FINAL_RESPONSE: the model did answer", agentTermination(agentRun))
	}
	completion := completionOf(t, f.h, agentRunID)
	if completion == nil {
		t.Fatalf("no completion verdict was recorded for agent run %s", agentRunID)
	}
	if completion.Outcome != domain.CompletionIncomplete || completion.Goal != domain.AgentGoalPrepareOnly {
		t.Errorf("verdict = %s for goal %s, want INCOMPLETE for PREPARE_ONLY", completion.Outcome, completion.Goal)
	}
	missing := completionMissing(completion)
	if !containsString(missing, runtime.MissingPatchVerification) || !containsString(missing, runtime.MissingChangeRequest) {
		t.Errorf("missing = %v, want the verification and the change request named", missing)
	}
	if containsString(missing, runtime.MissingWorkspacePatch) {
		t.Errorf("missing = %v names the workspace patch, which the write recorded", missing)
	}
	latest := wsLatestPatch(t, f.h, agentRunID)
	var patchCheck *domain.CompletionCheck
	for i := range completion.Checks {
		if completion.Checks[i].Name == runtime.CheckWorkspacePatch {
			patchCheck = &completion.Checks[i]
		}
	}
	if latest == nil || patchCheck == nil || !patchCheck.Passed || !containsString(patchCheck.Refs, latest.ID) {
		t.Errorf("workspace patch check = %+v, want it passed and citing patch %+v", patchCheck, latest)
	}

	nodeRun := agentNodeRun(f.h.ctx, t, f.h.uow, f.run.ID)
	if nodeRun.Status != domain.NodeRunFailed || nodeRun.Error == nil || nodeRun.Error.Code != runtime.CodeTaskIncomplete {
		t.Fatalf("agent node run = %s error %+v, want FAILED with TASK_INCOMPLETE", nodeRun.Status, nodeRun.Error)
	}
	for _, code := range missing {
		if !containsSubstring(nodeRun.Error.Message, code) {
			t.Errorf("node run error %q does not name missing item %s", nodeRun.Error.Message, code)
		}
	}
	if len(nodeRun.Output) != 0 {
		t.Errorf("failed agent node run published output %s", nodeRun.Output)
	}
	if run := agentRunRow(f.h.ctx, t, f.h.uow, f.run.ID); run.Status != domain.RunFailed || run.Error == nil || run.Error.Code != runtime.CodeTaskIncomplete {
		t.Errorf("run = %s error %+v, want FAILED with TASK_INCOMPLETE", run.Status, run.Error)
	}
	assertNoDownstreamNodeRun(f.h.ctx, t, f.h.uow, f.run.ID)

	events := listEvents(f.h.ctx, t, f.h.uow, f.run.ID)
	types := agentEventTypesFor(events, f.outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentCompletionEvaluated) != 1 {
		t.Fatalf("AGENT_COMPLETION_EVALUATED events = %d, want exactly one; events=%v", agentCountEventType(types, domain.EventAgentCompletionEvaluated), types)
	}
	completed, evaluated, failed := agentEventIndex(types, domain.EventAgentCompleted), agentEventIndex(types, domain.EventAgentCompletionEvaluated), agentEventIndex(types, domain.EventNodeFailed)
	if completed >= evaluated || evaluated >= failed {
		t.Errorf("event order = %v, want AGENT_COMPLETED before AGENT_COMPLETION_EVALUATED before NODE_FAILED", types)
	}
	if agentCountEventType(types, domain.EventAgentFailed) != 0 || agentCountEventType(types, domain.EventNodeCompleted) != 0 {
		t.Errorf("events = %v, want neither AGENT_FAILED nor NODE_COMPLETED", types)
	}
	payload := agentLastEventPayload(t, events, domain.EventAgentCompletionEvaluated)
	if payload["outcome"] != string(domain.CompletionIncomplete) || payload["agentRunId"] != agentRunID || payload["goal"] != string(domain.AgentGoalPrepareOnly) {
		t.Errorf("AGENT_COMPLETION_EVALUATED payload = %v, want the INCOMPLETE verdict of %s", payload, agentRunID)
	}
	codes, _ := payload["missing"].([]any)
	if len(codes) != len(missing) {
		t.Errorf("payload missing = %v, want the %d codes of the verdict", payload["missing"], len(missing))
	}
	if n, _ := payload["checkCount"].(float64); int(n) != len(completion.Checks) {
		t.Errorf("payload checkCount = %v, want %d", payload["checkCount"], len(completion.Checks))
	}
	for _, key := range []string{"description", "checks"} {
		if _, present := payload[key]; present {
			t.Errorf("payload carries %s; the Event is a bounded summary", key)
		}
	}
}

func containsSubstring(text, want string) bool {
	return len(want) > 0 && len(text) >= len(want) && indexOf(text, want) >= 0
}

func indexOf(text, want string) int {
	for i := 0; i+len(want) <= len(text); i++ {
		if text[i:i+len(want)] == want {
			return i
		}
	}
	return -1
}

// TestAgentCompletion_EventInsertFails_RollsBackVerdictAndTermination proves the verdict
// and its Event commit with the termination or not at all. The AGENT_COMPLETION_EVALUATED
// Event ID is pre-occupied, so the FINAL completion transaction fails after the verdict row
// was inserted: no verdict row, the Action still READY, the Agent Run not terminated, the
// NodeRun still RUNNING. The retried completion then commits the whole outcome once.
func TestAgentCompletion_EventInsertFails_RollsBackVerdictAndTermination(t *testing.T) {
	stop := &agentStopNotifier{}
	f := newCompletionFixture(t, completionFixtureOptions{notifier: stop})
	agentScriptFinal(f.h, mockmodel.Scenario{FinalOutput: json.RawMessage(completionReport)})
	def := f.h.saveDefinition(completionDefinition("wf-completion-rollback", domain.AgentGoalPrepareOnly, f.repo.commit, completionTools...))
	run := f.h.createRun(def.WorkflowID, def.Version, `{}`)
	outcome, action := agentReadyFinalAction(f.h, stop, run.ID)
	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, outcome.NodeRunID)

	// The completion transaction appends AGENT_ACTION_STARTED, AGENT_ACTION_COMPLETED and
	// AGENT_COMPLETED before the verdict's Event, so the collision is placed on the
	// fourth Event ID it asks for.
	const collidingEventID = "ev_agent_completion_collision"
	execAppendFixedEvent(f.h.ctx, t, f.h.uow, run.ID, collidingEventID, f.h.clock.Now())
	f.h.ids.ForceEventIDAfter(3, collidingEventID)

	if err := f.h.svc.CompleteAgentFinal(f.h.ctx, action.ID, domain.ClaimImmediate); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("complete agent final = %v, want a conflict from the duplicated Event ID", err)
	}
	if completion := completionOf(t, f.h, agentRun.ID); completion != nil {
		t.Errorf("a verdict %+v survived the rolled-back completion transaction", completion)
	}
	if rolledBack, _ := agentActionOfTurn(f.h.ctx, t, f.h.uow, outcome.AgentTurnID); rolledBack.Status != domain.AgentActionReady {
		t.Errorf("action status = %s, want READY: the completion transaction rolled back", rolledBack.Status)
	}
	if after, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, outcome.NodeRunID); after.Termination != nil {
		t.Errorf("agent run terminated as %s after a rolled-back transaction", *after.Termination)
	}
	if nodeRun := agentNodeRun(f.h.ctx, t, f.h.uow, run.ID); nodeRun.Status != domain.NodeRunRunning {
		t.Errorf("agent node run status = %s, want RUNNING", nodeRun.Status)
	}
	types := agentEventTypesFor(listEvents(f.h.ctx, t, f.h.uow, run.ID), outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentCompletionEvaluated) != 0 || agentCountEventType(types, domain.EventAgentCompleted) != 0 {
		t.Errorf("events after the rollback = %v, want no completion Events", types)
	}

	if err := f.h.svc.CompleteAgentFinal(f.h.ctx, action.ID, domain.ClaimImmediate); err != nil {
		t.Fatalf("second complete agent final: %v", err)
	}
	completion := completionOf(t, f.h, agentRun.ID)
	if completion == nil || completion.Outcome != domain.CompletionIncomplete {
		t.Fatalf("verdict after the retried completion = %+v, want INCOMPLETE", completion)
	}
	if !containsString(completionMissing(completion), runtime.MissingWorkspacePatch) {
		t.Errorf("missing = %v, want the workspace patch named: nothing was written", completionMissing(completion))
	}
	if nodeRun := agentNodeRun(f.h.ctx, t, f.h.uow, run.ID); nodeRun.Status != domain.NodeRunFailed || nodeRun.Error == nil || nodeRun.Error.Code != runtime.CodeTaskIncomplete {
		t.Errorf("agent node run after the retried completion = %s %+v, want FAILED with TASK_INCOMPLETE", nodeRun.Status, nodeRun.Error)
	}
	types = agentEventTypesFor(listEvents(f.h.ctx, t, f.h.uow, run.ID), outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentCompletionEvaluated) != 1 || agentCountEventType(types, domain.EventAgentCompleted) != 1 {
		t.Errorf("events after the retry = %v, want exactly one AGENT_COMPLETED and one AGENT_COMPLETION_EVALUATED", types)
	}
}

// ---------------------------------------------------------------------------
// READY_FOR_APPROVAL
// ---------------------------------------------------------------------------

// TestAgentCompletion_PrepareOnlyMaterialsComplete_ReadyForApproval: the Agent writes a
// patch, has it verified (the runner's callback passes it), records a change request about
// that patch and that verification, and declares completion. The verdict is
// READY_FOR_APPROVAL, the Agent NodeRun SUCCEEDED with the verdict, its evidence and the
// Agent's own report on its text port, and the Run completes through the downstream node.
func TestAgentCompletion_PrepareOnlyMaterialsComplete_ReadyForApproval(t *testing.T) {
	f := newCompletionFixture(t, completionFixtureOptions{})
	f.start(t, "wf-completion-ready", domain.AgentGoalPrepareOnly, completionTools,
		fixed(wsWrite(wsTokenPath, wsNewToken)),
		func() mockmodel.Scenario {
			return wsCall(verifypatch.ToolName, map[string]any{"patchDigest": wsLatestPatch(t, f.h, f.agentRunID(t)).PatchDigest})
		},
		func() mockmodel.Scenario {
			// Turn 3 is the first request that carries the verification's result, so its
			// Manifest holds the TOOL_RESULT item the basis cites.
			verifyAttempt := agentOnlyToolAttempt(t, f.h, wsActionOfTurn(t, f.h, f.agentRunID(t), 2).ID)
			return wsCall(changerequest.ToolName, changeRequestArguments(wsLatestPatch(t, f.h, f.agentRunID(t)).PatchDigest,
				basisItem("The verification of the patch passed every check.", 3, verifyAttempt.ID)))
		},
	)
	f.deliverLatestVerification(t, true)
	agentDrive(f.h, f.run.ID)
	agentRunID := f.agentRunID(t)

	latest := wsLatestPatch(t, f.h, agentRunID)
	requests := changeRequestsOf(t, f.h, agentRunID)
	facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID)
	if latest == nil || len(requests) != 1 || len(facts) != 1 || facts[0].SubjectRef != latest.PatchDigest {
		t.Fatalf("precondition: patch %+v, requests %+v, facts %+v; want one verified patch with one change request", latest, requests, facts)
	}

	completion := completionOf(t, f.h, agentRunID)
	if completion == nil || completion.Outcome != domain.CompletionReadyForApproval {
		t.Fatalf("verdict = %+v, want READY_FOR_APPROVAL", completion)
	}
	if len(completion.Missing) != 0 {
		t.Errorf("missing = %+v, want nothing", completion.Missing)
	}
	for _, check := range completion.Checks {
		if !check.Passed {
			t.Errorf("check %+v failed in a complete task", check)
		}
	}

	nodeRun := agentNodeRun(f.h.ctx, t, f.h.uow, f.run.ID)
	if nodeRun.Status != domain.NodeRunSucceeded {
		t.Fatalf("agent node run = %s error %+v, want SUCCEEDED", nodeRun.Status, nodeRun.Error)
	}
	output := decodeCompletionNodeOutput(t, nodeRun)
	if output.Outcome != domain.CompletionReadyForApproval || len(output.Checks) != len(completion.Checks) || len(output.Missing) != 0 {
		t.Errorf("node output = %+v, want the READY_FOR_APPROVAL verdict with its checks", output)
	}
	assertSameJSON(t, "agentReport", json.RawMessage(completionReport), output.AgentReport)
	wantEvidence := []domain.CompletionEvidence{
		{Kind: domain.CompletionEvidenceWorkspacePatch, ID: latest.ID},
		{Kind: domain.CompletionEvidenceFact, ID: facts[0].ID},
		{Kind: domain.CompletionEvidenceChangeRequest, ID: requests[0].ID},
		{Kind: domain.CompletionEvidenceArtifact, ID: requests[0].ContentArtifactID},
	}
	for _, want := range wantEvidence {
		found := false
		for _, got := range output.Evidence {
			found = found || got == want
		}
		if !found {
			t.Errorf("evidence %+v lacks %+v", output.Evidence, want)
		}
	}

	if run := agentRunRow(f.h.ctx, t, f.h.uow, f.run.ID); run.Status != domain.RunCompleted {
		t.Errorf("run status = %s, want COMPLETED through the downstream node", run.Status)
	}
	events := listEvents(f.h.ctx, t, f.h.uow, f.run.ID)
	types := agentEventTypesFor(events, f.outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentCompletionEvaluated) != 1 || agentCountEventType(types, domain.EventNodeCompleted) != 1 {
		t.Errorf("events = %v, want one AGENT_COMPLETION_EVALUATED and one NODE_COMPLETED", types)
	}
	payload := agentLastEventPayload(t, events, domain.EventAgentCompletionEvaluated)
	if codes, _ := payload["missing"].([]any); payload["outcome"] != string(domain.CompletionReadyForApproval) || len(codes) != 0 {
		t.Errorf("AGENT_COMPLETION_EVALUATED payload = %v, want READY_FOR_APPROVAL with nothing missing", payload)
	}
}

// ---------------------------------------------------------------------------
// Other terminations
// ---------------------------------------------------------------------------

// TestAgentCompletion_TimeoutTermination_RecordsIncomplete: the Agent deadline expires
// during a Tool call. The timeout transaction terminates the Agent Run TIMEOUT as before
// and, in the same transaction, records the INCOMPLETE verdict naming the facts the goal
// still lacks, the first of them that the Agent never declared completion. The NodeRun
// keeps the timeout's own error.
func TestAgentCompletion_TimeoutTermination_RecordsIncomplete(t *testing.T) {
	paused := newAgentPausedTool()
	f := newCompletionFixture(t, completionFixtureOptions{lookupExecutor: paused})
	agentScriptToolCallThenFinal(f.h, agentToolCallScenario())
	def := f.h.saveDefinition(completionDefinition("wf-completion-timeout", domain.AgentGoalPrepareOnly, f.repo.commit, lookup.ToolName))
	run := f.h.createRun(def.WorkflowID, def.Version, `{}`)
	outcome := f.h.claimAgentNode(run.ID)

	executed := make(chan error, 1)
	go func() { executed <- f.h.svc.Execute(f.h.ctx, outcome) }()
	<-paused.inFlight
	f.h.clock.Advance(agentTimeoutMs*time.Millisecond + time.Second)
	if err := f.h.svc.TimeoutAgentRun(f.h.ctx, outcome.AgentRunID); err != nil {
		t.Fatalf("timeout agent run: %v", err)
	}
	close(paused.release)
	if err := <-executed; err != nil {
		t.Fatalf("the late Tool result transaction returned an error instead of writing nothing: %v", err)
	}

	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Fatalf("agent termination = %s, want TIMEOUT", agentTermination(agentRun))
	}
	completion := completionOf(t, f.h, agentRun.ID)
	if completion == nil || completion.Outcome != domain.CompletionIncomplete {
		t.Fatalf("verdict = %+v, want INCOMPLETE", completion)
	}
	missing := completionMissing(completion)
	if !containsString(missing, runtime.MissingFinalResponse) || !containsString(missing, runtime.MissingWorkspacePatch) {
		t.Errorf("missing = %v, want the undeclared completion and the workspace patch named", missing)
	}
	if nodeRun := agentNodeRun(f.h.ctx, t, f.h.uow, run.ID); nodeRun.Status != domain.NodeRunFailed || nodeRun.Error == nil || nodeRun.Error.Code != "TIMEOUT" {
		t.Errorf("agent node run = %s %+v, want FAILED with the timeout's own error", nodeRun.Status, nodeRun.Error)
	}
	types := agentEventTypesFor(listEvents(f.h.ctx, t, f.h.uow, run.ID), outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentCompletionEvaluated) != 1 {
		t.Errorf("events = %v, want exactly one AGENT_COMPLETION_EVALUATED", types)
	}
	failed, evaluated, nodeFailed := agentEventIndex(types, domain.EventAgentFailed), agentEventIndex(types, domain.EventAgentCompletionEvaluated), agentEventIndex(types, domain.EventNodeFailed)
	if failed >= evaluated || evaluated >= nodeFailed {
		t.Errorf("event order = %v, want AGENT_FAILED before AGENT_COMPLETION_EVALUATED before NODE_FAILED", types)
	}
}

// ---------------------------------------------------------------------------
// No goal
// ---------------------------------------------------------------------------

// TestAgentCompletion_AgentWithoutGoal_NotEvaluated: an Agent whose node configures no goal
// pointer, the shape every existing demo Agent has, is never evaluated: no verdict row, no
// AGENT_COMPLETION_EVALUATED, and its answer is published on the text port exactly as
// before.
func TestAgentCompletion_AgentWithoutGoal_NotEvaluated(t *testing.T) {
	h := newAgentHarness(t, agentHarnessOptions{})
	agentScriptFinal(h, mockmodel.Scenario{Output: "done"})
	def := h.saveDefinition(agentLoopDefinition("wf-completion-no-goal"))
	run := h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := h.claimAgentNode(run.ID)
	h.execute(outcome)

	agentRun, _ := agentRunOfNodeRun(h.ctx, t, h.uow, outcome.NodeRunID)
	if agentRun.Goal != nil {
		t.Fatalf("precondition: agent run froze goal %s, want none", *agentRun.Goal)
	}
	if completion := completionOf(t, h, agentRun.ID); completion != nil {
		t.Errorf("an agent without a goal was evaluated: %+v", completion)
	}
	nodeRun := agentNodeRun(h.ctx, t, h.uow, run.ID)
	if nodeRun.Status != domain.NodeRunSucceeded {
		t.Fatalf("agent node run = %s, want SUCCEEDED", nodeRun.Status)
	}
	assertSameJSON(t, "node output", json.RawMessage(`{"text":"done"}`), nodeRun.Output)
	types := agentEventTypesFor(listEvents(h.ctx, t, h.uow, run.ID), outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentCompletionEvaluated) != 0 {
		t.Errorf("events = %v, want no AGENT_COMPLETION_EVALUATED", types)
	}
}
