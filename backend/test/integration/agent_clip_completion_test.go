//go:build integration

// The clip delivery completion check against PostgreSQL and the in-process media task
// service: when an Agent Run frozen to DELIVER_CLIP ends, the terminating transaction
// decides from the committed media facts whether the declared clip was delivered, writes
// the verdict and decides how the Agent NodeRun ends. These tests prove the failure rows:
// a declared clip that was never checked, a declared digest that no render produced, a
// reviewed variant without a passed review and a Run that ended on its turn bound or its
// deadline are INCOMPLETE and fail the NodeRun; a clip whose newest check passed after an
// earlier failure is CLIP_DELIVERED with the verdict and the Agent's report on the port; a
// reasoned CANNOT_SATISFY succeeds the NodeRun without a clip and an unreasoned one is
// INCOMPLETE; and a completion transaction that cannot commit leaves neither the
// termination nor the verdict behind.
//
// They share the clipFixture of agent_revision_limit_test.go and the completion helpers
// of agent_completion_test.go.
package integration

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/runtime"
)

// assertClipIncomplete proves the verdict is INCOMPLETE naming the given missing codes, that
// the Agent NodeRun and the Run failed with TASK_INCOMPLETE and that nothing ran downstream.
func assertClipIncomplete(t *testing.T, f *clipFixture, run domain.Run, agentRunID string, wantMissing ...string) *domain.AgentCompletion {
	t.Helper()
	completion := completionOf(t, f.h, agentRunID)
	if completion == nil {
		t.Fatalf("no completion verdict was recorded for agent run %s", agentRunID)
	}
	if completion.Outcome != domain.CompletionIncomplete || completion.Goal != domain.AgentGoalDeliverClip {
		t.Errorf("verdict = %s for goal %s, want INCOMPLETE for DELIVER_CLIP", completion.Outcome, completion.Goal)
	}
	missing := completionMissing(completion)
	for _, code := range wantMissing {
		if !containsString(missing, code) {
			t.Errorf("missing = %v, want %s named", missing, code)
		}
	}
	nodeRun := agentNodeRun(f.h.ctx, t, f.h.uow, run.ID)
	if nodeRun.Status != domain.NodeRunFailed || nodeRun.Error == nil || nodeRun.Error.Code != runtime.CodeTaskIncomplete {
		t.Fatalf("agent node run = %s error %+v, want FAILED with TASK_INCOMPLETE", nodeRun.Status, nodeRun.Error)
	}
	if len(nodeRun.Output) != 0 {
		t.Errorf("failed agent node run published output %s", nodeRun.Output)
	}
	if row := agentRunRow(f.h.ctx, t, f.h.uow, run.ID); row.Status != domain.RunFailed {
		t.Errorf("run status = %s, want FAILED", row.Status)
	}
	assertNoDownstreamNodeRun(f.h.ctx, t, f.h.uow, run.ID)
	return completion
}

// renderedAndChecked drives the fixture through a passed timeline check, a render and the
// scripted clip checks, delivering each service result as the Agent reaches it. The
// injected clock moves between deliveries so each fact is recorded later than the one
// before, as it would be on a running clock: the check reads the newest fact by its
// recorded time.
func renderedAndChecked(t *testing.T, f *clipFixture, agentRunID string, timeline string, clipVerdicts ...bool) {
	t.Helper()
	_, checkTask := f.waitingAction(t, agentRunID, 1)
	f.deliver(t, checkTask, timelineCheckedPayload(timeline, true))
	f.h.clock.Advance(time.Second)
	_, renderTask := f.waitingAction(t, agentRunID, 2)
	f.deliver(t, renderTask, renderedPayload(clipReportSha, timelineDigest(timeline)))
	for i, passed := range clipVerdicts {
		f.h.clock.Advance(time.Second)
		_, clipTask := f.waitingAction(t, agentRunID, 3+i)
		f.deliver(t, clipTask, clipCheckedPayload(clipReportSha, timelineDigest(timeline), passed))
	}
}

// The Agent rendered the clip and declared it delivered without ever checking it: the
// verdict is INCOMPLETE naming the missing clip check, and the NodeRun fails.
func TestClipCompletion_DeclaredDeliveredButClipUnchecked_Incomplete(t *testing.T) {
	f := newClipFixture(t, nil, agentHarnessOptions{})
	timeline := canonicalTimelineFor("S12", "S13")
	run, outcome := f.start(t, "wf-clip-unchecked", clipMaxTurns, nil, clipTools,
		timelineCheckCall("S12", "S13"), renderCall(timelineDigest(timeline)), deliveredFinal(clipReportSha))
	agentRunID := factAgentRunID(t, f.h, outcome)
	renderedAndChecked(t, f, agentRunID, timeline)

	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationFinalResponse {
		t.Fatalf("agent termination = %s, want FINAL_RESPONSE: the model did answer", agentTermination(agentRun))
	}
	completion := assertClipIncomplete(t, f, run, agentRunID, runtime.MissingClipCheckPassed)
	if missing := completionMissing(completion); containsString(missing, runtime.MissingClipRendered) {
		t.Errorf("missing = %v names the render, which was recorded", missing)
	}
	facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID)
	rendered := factsOfType(facts, domain.FactTypeClipRendered)
	var renderCheck *domain.CompletionCheck
	for i := range completion.Checks {
		if completion.Checks[i].Name == runtime.CheckClipRendered {
			renderCheck = &completion.Checks[i]
		}
	}
	if len(rendered) != 1 || renderCheck == nil || !renderCheck.Passed || !containsString(renderCheck.Refs, rendered[0].ID) {
		t.Errorf("render check = %+v, want it passed and citing the clip_rendered fact %+v", renderCheck, rendered)
	}
	types := agentEventTypesFor(listEvents(f.h.ctx, t, f.h.uow, run.ID), outcome.NodeRunID)
	completed, evaluated, failed := agentEventIndex(types, domain.EventAgentCompleted), agentEventIndex(types, domain.EventAgentCompletionEvaluated), agentEventIndex(types, domain.EventNodeFailed)
	if completed >= evaluated || evaluated >= failed {
		t.Errorf("event order = %v, want AGENT_COMPLETED before AGENT_COMPLETION_EVALUATED before NODE_FAILED", types)
	}
}

// The Agent declared a digest no render produced: INCOMPLETE naming the missing render,
// whatever else was checked.
func TestClipCompletion_DeclaredDigestNotRendered_Incomplete(t *testing.T) {
	f := newClipFixture(t, nil, agentHarnessOptions{})
	timeline := canonicalTimelineFor("S12", "S13")
	run, outcome := f.start(t, "wf-clip-not-rendered", clipMaxTurns, nil, clipTools,
		timelineCheckCall("S12", "S13"), renderCall(timelineDigest(timeline)), clipCheckCall(clipReportSha), deliveredFinal(clipOtherSha))
	agentRunID := factAgentRunID(t, f.h, outcome)
	renderedAndChecked(t, f, agentRunID, timeline, true)

	completion := assertClipIncomplete(t, f, run, agentRunID, runtime.MissingClipRendered)
	if missing := completionMissing(completion); containsString(missing, runtime.MissingClipCheckPassed) {
		t.Errorf("missing = %v: nothing past the missing render is checked", missing)
	}
}

// The reviewed variant: the Agent may call the review Tool, so delivery needs a passed
// content review of the timeline the clip was rendered from. Without one the verdict is
// INCOMPLETE even though the clip was rendered and its check passed.
func TestClipCompletion_ReviewedVariantWithoutPassedReview_Incomplete(t *testing.T) {
	f := newClipFixture(t, []bool{false}, agentHarnessOptions{})
	timeline := canonicalTimelineFor("S12", "S13")
	timelineSha := timelineDigest(timeline)
	run, outcome := f.start(t, "wf-clip-review-missing", clipMaxTurns, nil, clipReviewedTools,
		timelineCheckCall("S12", "S13"), renderCall(timelineSha), clipCheckCall(clipReportSha), contentReviewCall(timelineSha), deliveredFinal(clipReportSha))
	agentRunID := factAgentRunID(t, f.h, outcome)
	renderedAndChecked(t, f, agentRunID, timeline, true)

	completion := assertClipIncomplete(t, f, run, agentRunID, runtime.MissingContentReviewPassed)
	if missing := completionMissing(completion); len(missing) != 1 {
		t.Errorf("missing = %v, want only the review named", missing)
	}
	reviews := factsOfType(listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID), domain.FactTypeContentReviewed)
	if len(reviews) != 1 || reviews[0].Verdict == nil || *reviews[0].Verdict {
		t.Fatalf("content_reviewed facts = %+v, want the one failed review", reviews)
	}
	for _, check := range completion.Checks {
		if check.Name == runtime.CheckContentReviewed && (check.Passed || !containsString(check.Refs, reviews[0].ID)) {
			t.Errorf("review check = %+v, want it failed citing the rejected review", check)
		}
	}
}

// The clip check failed once and passed on the second check: the newest verdict decides,
// so the task is CLIP_DELIVERED, the NodeRun succeeds with the verdict, its evidence and the
// Agent's report on its text port, and the Run completes through the downstream node.
func TestClipCompletion_ClipCheckFailedThenPassed_ClipDeliveredByNewestVerdict(t *testing.T) {
	f := newClipFixture(t, nil, agentHarnessOptions{})
	timeline := canonicalTimelineFor("S12", "S13")
	run, outcome := f.start(t, "wf-clip-delivered", clipMaxTurns, map[string]int{domain.FactTypeMediaChecked: 3}, clipTools,
		timelineCheckCall("S12", "S13"), renderCall(timelineDigest(timeline)), clipCheckCall(clipReportSha), clipCheckCall(clipReportSha), deliveredFinal(clipReportSha))
	agentRunID := factAgentRunID(t, f.h, outcome)
	renderedAndChecked(t, f, agentRunID, timeline, false, true)

	completion := completionOf(t, f.h, agentRunID)
	if completion == nil || completion.Outcome != domain.CompletionClipDelivered || completion.Goal != domain.AgentGoalDeliverClip {
		t.Fatalf("verdict = %+v, want CLIP_DELIVERED for DELIVER_CLIP", completion)
	}
	if len(completion.Missing) != 0 {
		t.Errorf("missing = %+v, want nothing", completion.Missing)
	}
	facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID)
	checks := factsOfType(facts, domain.FactTypeMediaChecked)
	if len(checks) != 3 {
		t.Fatalf("media_checked facts = %+v, want the timeline check and two clip checks", checks)
	}
	newest := checks[2]
	for _, check := range completion.Checks {
		if !check.Passed {
			t.Errorf("check %+v failed in a delivered task", check)
		}
		if check.Name == runtime.CheckClipChecked && (len(check.Refs) != 1 || check.Refs[0] != newest.ID) {
			t.Errorf("clip check refs = %v, want only the newest passed check %s", check.Refs, newest.ID)
		}
	}

	nodeRun := agentNodeRun(f.h.ctx, t, f.h.uow, run.ID)
	if nodeRun.Status != domain.NodeRunSucceeded {
		t.Fatalf("agent node run = %s error %+v, want SUCCEEDED", nodeRun.Status, nodeRun.Error)
	}
	output := decodeCompletionNodeOutput(t, nodeRun)
	if output.Outcome != domain.CompletionClipDelivered || len(output.Checks) != len(completion.Checks) || len(output.Missing) != 0 {
		t.Errorf("node output = %+v, want the CLIP_DELIVERED verdict with its checks", output)
	}
	var report struct {
		Outcome             string  `json:"outcome"`
		ClipSha256          *string `json:"clipSha256"`
		CannotSatisfyReason *string `json:"cannotSatisfyReason"`
	}
	if err := json.Unmarshal(output.AgentReport, &report); err != nil || report.Outcome != "DELIVERED" || report.ClipSha256 == nil || *report.ClipSha256 != clipReportSha || report.CannotSatisfyReason != nil {
		t.Errorf("agentReport = %s (%v), want the Agent's DELIVERED report naming the clip", output.AgentReport, err)
	}
	rendered := factsOfType(facts, domain.FactTypeClipRendered)
	for _, want := range []domain.CompletionEvidence{
		{Kind: domain.CompletionEvidenceFact, ID: rendered[0].ID},
		{Kind: domain.CompletionEvidenceFact, ID: newest.ID},
	} {
		found := false
		for _, got := range output.Evidence {
			found = found || got == want
		}
		if !found {
			t.Errorf("evidence %+v lacks %+v", output.Evidence, want)
		}
	}
	agentDrive(f.h, run.ID)
	if row := agentRunRow(f.h.ctx, t, f.h.uow, run.ID); row.Status != domain.RunCompleted {
		t.Errorf("run status = %s, want COMPLETED through the downstream node", row.Status)
	}
	events := listEvents(f.h.ctx, t, f.h.uow, run.ID)
	payload := agentLastEventPayload(t, events, domain.EventAgentCompletionEvaluated)
	if codes, _ := payload["missing"].([]any); payload["outcome"] != string(domain.CompletionClipDelivered) || len(codes) != 0 {
		t.Errorf("AGENT_COMPLETION_EVALUATED payload = %v, want CLIP_DELIVERED with nothing missing", payload)
	}
}

// A reasoned CANNOT_SATISFY needs no fact: the verdict is CANNOT_SATISFY, the NodeRun
// succeeds with the report carrying the reason and no clip, and the Run completes.
func TestClipCompletion_CannotSatisfyWithReason_SucceedsWithoutClip(t *testing.T) {
	f := newClipFixture(t, nil, agentHarnessOptions{})
	reason := "completing the claim needs 84 seconds, over the 60 second target"
	run, outcome := f.start(t, "wf-clip-cannot-satisfy", clipMaxTurns, nil, clipTools, cannotSatisfyFinal(&reason))
	agentRunID := factAgentRunID(t, f.h, outcome)

	completion := completionOf(t, f.h, agentRunID)
	if completion == nil || completion.Outcome != domain.CompletionCannotSatisfy || len(completion.Missing) != 0 {
		t.Fatalf("verdict = %+v, want CANNOT_SATISFY with nothing missing", completion)
	}
	nodeRun := agentNodeRun(f.h.ctx, t, f.h.uow, run.ID)
	if nodeRun.Status != domain.NodeRunSucceeded {
		t.Fatalf("agent node run = %s error %+v, want SUCCEEDED", nodeRun.Status, nodeRun.Error)
	}
	output := decodeCompletionNodeOutput(t, nodeRun)
	var report struct {
		Outcome             string  `json:"outcome"`
		ClipSha256          *string `json:"clipSha256"`
		CannotSatisfyReason *string `json:"cannotSatisfyReason"`
	}
	if err := json.Unmarshal(output.AgentReport, &report); err != nil || output.Outcome != domain.CompletionCannotSatisfy ||
		report.Outcome != "CANNOT_SATISFY" || report.ClipSha256 != nil || report.CannotSatisfyReason == nil || *report.CannotSatisfyReason != reason {
		t.Errorf("node output = %+v (%v), want CANNOT_SATISFY with the reasoned report and no clip", output, err)
	}
	if facts := listFactsByAgentRun(f.h.ctx, t, f.h.uow, agentRunID); len(facts) != 0 {
		t.Errorf("facts = %+v, want none", facts)
	}
	if submits, _ := f.service.counts(); submits != 0 {
		t.Errorf("the media service saw %d submits, want none", submits)
	}
	agentDrive(f.h, run.ID)
	if row := agentRunRow(f.h.ctx, t, f.h.uow, run.ID); row.Status != domain.RunCompleted {
		t.Errorf("run status = %s, want COMPLETED", row.Status)
	}
}

// CANNOT_SATISFY without a reason is not a result a person can act on: INCOMPLETE.
func TestClipCompletion_CannotSatisfyWithoutReason_Incomplete(t *testing.T) {
	f := newClipFixture(t, nil, agentHarnessOptions{})
	run, outcome := f.start(t, "wf-clip-cannot-satisfy-unreasoned", clipMaxTurns, nil, clipTools, cannotSatisfyFinal(nil))
	agentRunID := factAgentRunID(t, f.h, outcome)
	assertClipIncomplete(t, f, run, agentRunID, runtime.MissingCannotSatisfyReason)
}

// The turn bound ends the Agent Run before it declared anything: the termination
// transaction records INCOMPLETE naming the undeclared completion, and the NodeRun keeps
// the MAX_TURNS error.
func TestClipCompletion_MaxTurnsTermination_Incomplete(t *testing.T) {
	f := newClipFixture(t, nil, agentHarnessOptions{})
	timeline := canonicalTimelineFor("S12", "S13")
	run, outcome := f.start(t, "wf-clip-max-turns", 1, nil, clipTools, timelineCheckCall("S12", "S13"))
	agentRunID := factAgentRunID(t, f.h, outcome)
	_, checkTask := f.waitingAction(t, agentRunID, 1)
	f.deliver(t, checkTask, timelineCheckedPayload(timeline, true))

	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationMaxTurns {
		t.Fatalf("agent termination = %s, want MAX_TURNS", agentTermination(agentRun))
	}
	completion := completionOf(t, f.h, agentRunID)
	if completion == nil || completion.Outcome != domain.CompletionIncomplete {
		t.Fatalf("verdict = %+v, want INCOMPLETE", completion)
	}
	if missing := completionMissing(completion); !containsString(missing, runtime.MissingFinalResponse) {
		t.Errorf("missing = %v, want the undeclared completion named", missing)
	}
	if nodeRun := agentNodeRun(f.h.ctx, t, f.h.uow, run.ID); nodeRun.Status != domain.NodeRunFailed || nodeRun.Error == nil || nodeRun.Error.Code != "MAX_TURNS" {
		t.Errorf("agent node run = %s %+v, want FAILED with the turn bound's own error", nodeRun.Status, nodeRun.Error)
	}
	types := agentEventTypesFor(listEvents(f.h.ctx, t, f.h.uow, run.ID), outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentCompletionEvaluated) != 1 {
		t.Errorf("events = %v, want exactly one AGENT_COMPLETION_EVALUATED", types)
	}
}

// The deadline passes while a check waits on the service: the timeout transaction
// terminates the Agent Run TIMEOUT and records INCOMPLETE in the same transaction.
func TestClipCompletion_DeadlineTermination_Incomplete(t *testing.T) {
	f := newClipFixture(t, nil, agentHarnessOptions{})
	run, outcome := f.start(t, "wf-clip-timeout", clipMaxTurns, nil, clipTools, timelineCheckCall("S12", "S13"), deliveredFinal(clipReportSha))
	agentRunID := factAgentRunID(t, f.h, outcome)
	f.waitingAction(t, agentRunID, 1)

	f.h.clock.Advance(clipTimeoutMs*time.Millisecond + time.Second)
	if err := f.h.svc.TimeoutAgentRun(f.h.ctx, agentRunID); err != nil {
		t.Fatalf("timeout agent run: %v", err)
	}
	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, outcome.NodeRunID)
	if agentRun.Termination == nil || *agentRun.Termination != domain.TerminationTimeout {
		t.Fatalf("agent termination = %s, want TIMEOUT", agentTermination(agentRun))
	}
	completion := completionOf(t, f.h, agentRunID)
	if completion == nil || completion.Outcome != domain.CompletionIncomplete || !containsString(completionMissing(completion), runtime.MissingFinalResponse) {
		t.Fatalf("verdict = %+v, want INCOMPLETE naming the undeclared completion", completion)
	}
	if nodeRun := agentNodeRun(f.h.ctx, t, f.h.uow, run.ID); nodeRun.Status != domain.NodeRunFailed || nodeRun.Error == nil || nodeRun.Error.Code != "TIMEOUT" {
		t.Errorf("agent node run = %s %+v, want FAILED with the timeout's own error", nodeRun.Status, nodeRun.Error)
	}
}

// The verdict and the termination commit together or not at all: with the verdict's Event
// ID pre-occupied, the FINAL completion transaction fails after the verdict row was
// inserted, and nothing of it survives; the retried completion commits the whole outcome
// once.
func TestClipCompletion_EventInsertFails_RollsBackVerdictAndTermination(t *testing.T) {
	stop := &agentStopNotifier{}
	f := newClipFixture(t, nil, agentHarnessOptions{Notifier: stop})
	reason := "the material holds no contested claim"
	agentScriptFinal(f.h, mockmodel.Scenario{FinalOutput: cannotSatisfyFinal(&reason).FinalOutput})
	def := f.h.saveDefinition(clipDefinition("wf-clip-rollback", clipMaxTurns, nil, clipTools...))
	run := f.h.createRun(def.WorkflowID, def.Version, `{}`)
	outcome, action := agentReadyFinalAction(f.h, stop, run.ID)
	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, outcome.NodeRunID)

	// The completion transaction appends AGENT_ACTION_STARTED, AGENT_ACTION_COMPLETED and
	// AGENT_COMPLETED before the verdict's Event, so the collision is placed on the
	// fourth Event ID it asks for.
	const collidingEventID = "ev_clip_completion_collision"
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
	if completion == nil || completion.Outcome != domain.CompletionCannotSatisfy {
		t.Fatalf("verdict after the retried completion = %+v, want CANNOT_SATISFY", completion)
	}
	if nodeRun := agentNodeRun(f.h.ctx, t, f.h.uow, run.ID); nodeRun.Status != domain.NodeRunSucceeded {
		t.Errorf("agent node run after the retried completion = %s %+v, want SUCCEEDED", nodeRun.Status, nodeRun.Error)
	}
	types = agentEventTypesFor(listEvents(f.h.ctx, t, f.h.uow, run.ID), outcome.NodeRunID)
	if agentCountEventType(types, domain.EventAgentCompletionEvaluated) != 1 || agentCountEventType(types, domain.EventAgentCompleted) != 1 {
		t.Errorf("events after the retry = %v, want exactly one AGENT_COMPLETED and one AGENT_COMPLETION_EVALUATED", types)
	}
}
