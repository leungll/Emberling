//go:build integration

// Software delivery fault stories: a Backend restart while the Agent waits for the
// sandbox runner's callback, and a deployment whose response is lost when the Backend
// stops while production holds it.
package contract

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/reconciler"
	"github.com/leungll/Emberling/backend/internal/tools/deploy"
	"github.com/leungll/Emberling/backend/internal/tools/sandboxtest"
)

// shiftedClock is the system clock moved by a fixed offset, so a restarted Backend and
// its Reconciler can stand past a deadline the stopped Backend froze without real time
// passing.
type shiftedClock struct{ offset time.Duration }

func (c shiftedClock) Now() time.Time { return time.Now().UTC().Add(c.offset) }

// TestE2E_SoftwareDelivery_RestartWhileWaitingForTestCallback_CallbackResumesOriginalActionOnce
// holds the sandbox runner's callback at its barrier while the Agent waits for it, stops
// the Backend and starts a second one over the same database. The runner then delivers
// its result twice: the first delivery resumes the original sandbox_test Attempt, the
// second is answered as a duplicate, the test is never dispatched again, and the Agent
// continues to one deployment.
func TestE2E_SoftwareDelivery_RestartWhileWaitingForTestCallback_CallbackResumesOriginalActionOnce(t *testing.T) {
	f := newDeliveryFixture(t)
	control(t, f.runner.http.URL, "pause", `{"kinds":["callback"]}`)
	first := f.startBackend(t, testEnvOptions{})

	runID := createDeliveryRun(t, first, "software-delivery-duplicate-callback")
	waiting := first.waitForNodeRunStatus(t, runID, "node_agent", "WAITING_CALLBACK", e2eWait)
	agentNodeRunID, _ := waiting["id"].(string)
	waitRunnerHeld(t, f, "callback")

	before := loadDeliveryTrace(t, first, runID, agentNodeRunID)
	tests := before.callsOf(sandboxtest.ToolName)
	if len(tests) != 1 || tests[0].attempt.Status != "DISPATCHED" || tests[0].actionStatus != "WAITING_CALLBACK" ||
		tests[0].attempt.CallbackBinding == nil {
		t.Fatalf("sandbox_test call while waiting = %+v, want one bound DISPATCHED Attempt and its Action WAITING_CALLBACK", tests)
	}
	testAttemptID := tests[0].attempt.ID
	testID := tests[0].attempt.CallbackBinding.ExternalTaskID

	// --- restart: same database, new Backend; the runner's callback is still held.
	pool := first.pool
	first.stop()
	second := f.startBackend(t, testEnvOptions{Pool: pool})
	control(t, f.runner.http.URL, "release", "")

	accepted := f.nextDelivery(t)
	if accepted.StatusCode != http.StatusOK || accepted.ExternalTaskID != testID {
		t.Fatalf("first callback after the restart = status %d test %q, want 200 for %q; body=%s",
			accepted.StatusCode, accepted.ExternalTaskID, testID, accepted.Body)
	}
	if outcome := decodeBody[callbackResponseDTO](t, accepted.Body); !outcome.Accepted || outcome.Pending || outcome.Duplicate {
		t.Errorf("first callback outcome = %+v, want accepted", outcome)
	}
	duplicate := f.nextDelivery(t)
	if duplicate.StatusCode != http.StatusOK || duplicate.ExternalTaskID != testID {
		t.Fatalf("second callback = status %d test %q, want 200 for %q; body=%s",
			duplicate.StatusCode, duplicate.ExternalTaskID, testID, duplicate.Body)
	}
	if outcome := decodeBody[callbackResponseDTO](t, duplicate.Body); !outcome.Accepted || !outcome.Duplicate {
		t.Errorf("second callback outcome = %+v, want an accepted duplicate", outcome)
	}

	snapshot := second.waitForTerminal(t, runID, e2eWait)
	delivered := assertDeliveredOnce(t, second, f, runID, snapshot)
	if delivered.testAttemptID != testAttemptID || delivered.testID != testID {
		t.Errorf("completed sandbox_test Attempt = %s test %s, want the original %s test %s",
			delivered.testAttemptID, delivered.testID, testAttemptID, testID)
	}
	if arrived := recordLines(serviceRecord(t, f.runner.http.URL), "arrived", "test"); len(arrived) != 1 {
		t.Errorf("runner test arrivals = %v, want 1: the restart must not dispatch the test again", arrived)
	}
	events := second.listEvents(t, runID)
	for _, typ := range []string{"AGENT_ACTION_WAITING", "RUN_PAUSED", "RUN_RESUMED", "RUN_COMPLETED"} {
		if n := len(eventsOfType(events, typ)); n != 1 {
			t.Errorf("%s events = %d, want exactly 1", typ, n)
		}
	}
}

// TestE2E_SoftwareDelivery_DeployHeldPastDeadline_TimesOutWithoutSecondDeployment holds the
// deployment request at production's barrier and stops the Backend, so the request is
// abandoned and its outcome never reaches Emberling. A second Backend whose clock stands
// past the Agent deadline lets the Reconciler end the Agent with TIMEOUT: the deploy
// Attempt and its Action fail, the Run fails, and neither recovery nor a second
// Reconciler pass calls production again.
func TestE2E_SoftwareDelivery_DeployHeldPastDeadline_TimesOutWithoutSecondDeployment(t *testing.T) {
	f := newDeliveryFixture(t)
	control(t, f.production.URL, "pause", `{"kinds":["deploy"]}`)
	first := f.startBackend(t, testEnvOptions{})

	runID := createDeliveryRun(t, first, "software-delivery")
	var operationID string
	select {
	case operationID = <-f.productionHeld:
	case <-time.After(e2eWait):
		t.Fatalf("production held no deployment request within %s", e2eWait)
	}

	// --- restart while production holds the request: its response is lost.
	pool := first.pool
	first.stop()
	waitRecordLine(t, f.production.URL, "abandoned", "deploy", func(line map[string]any) bool {
		return line["operationId"] == operationID
	})

	// The fixture's Agent deadline is two minutes; an hour later it has expired.
	past := shiftedClock{offset: time.Hour}
	second := f.startBackend(t, testEnvOptions{Pool: pool, Clock: past})
	rec := reconciler.New(reconciler.Config{UoW: second.uow, Executor: second.execution, Clock: past, BatchLimit: 100})
	report, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("reconciler RunOnce: %v", err)
	}
	if len(report.Errors) != 0 || report.ExpiredAgentRunsFound != 1 || report.AgentRunsTimedOut != 1 {
		t.Fatalf("reconciler report = %+v, want the expired Agent Run found and timed out once", report)
	}

	snapshot := second.waitForTerminal(t, runID, e2eWait)
	if got := runStatusOf(snapshot); got != "FAILED" {
		t.Fatalf("Run status = %q, want FAILED; snapshot=%v", got, snapshot)
	}
	if got := nodeRunStatusOf(snapshot, "node_result"); got == "SUCCEEDED" {
		t.Errorf("result NodeRun status = %q, want it never reached", got)
	}
	trace := loadDeliveryTrace(t, second, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	if trace.AgentRun.Termination == nil || *trace.AgentRun.Termination != string(domain.TerminationTimeout) {
		t.Errorf("Agent termination = %v, want TIMEOUT", trace.AgentRun.Termination)
	}
	deploys := trace.callsOf(deploy.ToolName)
	if len(deploys) != 1 {
		t.Fatalf("deploy Attempts = %+v, want exactly 1: an uncertain deployment is never retried", deploys)
	}
	if call := deploys[0]; call.attempt.Status != "FAILED" || call.attempt.AttemptNo != 1 ||
		call.actionStatus != "FAILED" || call.actionError != "TIMEOUT" {
		t.Errorf("deploy call = %+v, want its first Attempt and its Action FAILED with TIMEOUT", call)
	}
	if want := deploy.OperationID(deploys[0].actionID, 1); operationID != want {
		t.Errorf("held operationId = %q, want %q derived from the deploy Action", operationID, want)
	}

	// A second pass finds nothing left to time out, and nothing calls production again.
	again, err := rec.RunOnce(context.Background())
	if err != nil || len(again.Errors) != 0 || again.AgentRunsTimedOut != 0 {
		t.Errorf("second reconciler pass = %+v err %v, want nothing to do", again, err)
	}
	productionLines := serviceRecord(t, f.production.URL)
	if arrived := recordLines(productionLines, "arrived", "deploy"); len(arrived) != 1 || arrived[0]["operationId"] != operationID {
		t.Errorf("production deploy arrivals = %v, want only the held %s", arrived, operationID)
	}
	if deployed := recordLines(productionLines, "deployed", "deploy"); len(deployed) != 0 {
		t.Errorf("production deployed lines = %v, want none: the held request was abandoned", deployed)
	}

	events := second.listEvents(t, runID)
	assertContiguousSeq(t, events)
	if n := len(eventsOfType(events, "RUN_FAILED")); n != 1 {
		t.Errorf("RUN_FAILED events = %d, want exactly 1", n)
	}
}

// deliveryUncertainReport is the FINAL document the deploy-lost script answers with when
// the deployment's outcome is unknown.
type deliveryUncertainReport struct {
	Status      string `json:"status"`
	BaseCommit  string `json:"baseCommit"`
	PatchDigest string `json:"patchDigest"`
	Message     string `json:"message"`
}

// TestE2E_SoftwareDelivery_DeployHeldAndBackendRestarted_AgentToldUncertainReportsWithoutSecondDeployment
// holds the deployment request at production's barrier and stops the Backend, so the
// request is abandoned and its outcome never reaches Emberling. A second Backend under a
// new process identity, still within the Agent deadline, finds the STARTED deploy Attempt
// of the stopped process on its Reconciler scan and hands it back to the Agent: the
// Attempt and its Action fail with RESULT_UNCERTAIN and failureSource RUNNER_LOST, the
// Agent's next Turn answers with a report instead of deploying again, the Run completes
// with that report, and production is never called a second time.
func TestE2E_SoftwareDelivery_DeployHeldAndBackendRestarted_AgentToldUncertainReportsWithoutSecondDeployment(t *testing.T) {
	f := newDeliveryFixture(t)
	control(t, f.production.URL, "pause", `{"kinds":["deploy"]}`)
	first := f.startBackend(t, testEnvOptions{RunnerID: "runner-before-restart"})

	runID := createDeliveryRun(t, first, "software-delivery-deploy-lost")
	var operationID string
	select {
	case operationID = <-f.productionHeld:
	case <-time.After(e2eWait):
		t.Fatalf("production held no deployment request within %s", e2eWait)
	}

	// --- restart while production holds the request: its response is lost.
	pool := first.pool
	first.stop()
	waitRecordLine(t, f.production.URL, "abandoned", "deploy", func(line map[string]any) bool {
		return line["operationId"] == operationID
	})

	const restartedRunner = "runner-after-restart"
	second := f.startBackend(t, testEnvOptions{Pool: pool, RunnerID: restartedRunner})
	rec := reconciler.New(reconciler.Config{UoW: second.uow, Executor: second.execution, BatchLimit: 100, RunnerID: restartedRunner})
	report, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("reconciler RunOnce: %v", err)
	}
	if len(report.Errors) != 0 || report.OrphanToolAttemptsFound != 1 || report.OrphanToolAttemptsHandled != 1 || report.AgentRunsTimedOut != 0 {
		t.Fatalf("reconciler report = %+v, want the orphaned deploy Attempt found and handled once, nothing timed out", report)
	}

	snapshot := second.waitForTerminal(t, runID, e2eWait)
	if got := runStatusOf(snapshot); got != "COMPLETED" {
		t.Fatalf("Run status = %q, want COMPLETED with the Agent's report; snapshot=%v", got, snapshot)
	}
	trace := loadDeliveryTrace(t, second, runID, nodeRunIDOf(t, snapshot, "node_agent"))
	if trace.AgentRun.Termination == nil || *trace.AgentRun.Termination != string(domain.TerminationFinalResponse) {
		t.Errorf("Agent termination = %v, want FINAL_RESPONSE", trace.AgentRun.Termination)
	}
	deploys := trace.callsOf(deploy.ToolName)
	if len(deploys) != 1 {
		t.Fatalf("deploy Attempts = %+v, want exactly 1: an uncertain deployment is never retried", deploys)
	}
	call := deploys[0]
	if call.attempt.Status != "FAILED" || call.attempt.AttemptNo != 1 || call.actionStatus != "FAILED" ||
		call.actionError != "RESULT_UNCERTAIN" || call.failureSource != string(domain.FailureRunnerLost) {
		t.Errorf("deploy call = %+v, want its first Attempt and its Action FAILED with RESULT_UNCERTAIN and RUNNER_LOST", call)
	}
	if want := deploy.OperationID(call.actionID, 1); operationID != want {
		t.Errorf("held operationId = %q, want %q derived from the deploy Action", operationID, want)
	}
	var nextDecision string
	for _, turn := range trace.Turns {
		if turn.TurnNo == call.turnNo+1 && turn.Decision != nil {
			nextDecision = turn.Decision.Kind
		}
	}
	if nextDecision != string(domain.DecisionFinal) {
		t.Errorf("Decision of the Turn after the uncertain deployment = %q, want FINAL", nextDecision)
	}

	var patchDigest string
	for _, fact := range trace.Facts.Items {
		if fact.FactType == "patch_tested" && fact.Verdict != nil && *fact.Verdict {
			patchDigest = fact.Subject
		}
	}
	text, _ := nodeRunOutputOf(t, snapshot, "node_result")["text"].(string)
	var result deliveryUncertainReport
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("result text %q is not the Agent's report: %v", text, err)
	}
	if patchDigest == "" || result.Status != "DEPLOYMENT_UNCERTAIN" || result.BaseCommit != deliveryBaseCommit ||
		result.PatchDigest != patchDigest || result.Message == "" {
		t.Errorf("result = %+v, want DEPLOYMENT_UNCERTAIN for the tested patch %q", result, patchDigest)
	}

	// A second pass finds nothing left to hand back, and nothing calls production again.
	again, err := rec.RunOnce(context.Background())
	if err != nil || len(again.Errors) != 0 || again.OrphanToolAttemptsFound != 0 || again.AgentRunsTimedOut != 0 {
		t.Errorf("second reconciler pass = %+v err %v, want nothing to do", again, err)
	}
	productionLines := serviceRecord(t, f.production.URL)
	if arrived := recordLines(productionLines, "arrived", "deploy"); len(arrived) != 1 || arrived[0]["operationId"] != operationID {
		t.Errorf("production deploy arrivals = %v, want only the held %s", arrived, operationID)
	}
	if deployed := recordLines(productionLines, "deployed", "deploy"); len(deployed) != 0 {
		t.Errorf("production deployed lines = %v, want none: the held request was abandoned", deployed)
	}

	events := second.listEvents(t, runID)
	assertContiguousSeq(t, events)
	failed := eventsOfType(events, "AGENT_ACTION_FAILED")
	if len(failed) != 1 {
		t.Fatalf("AGENT_ACTION_FAILED events = %d, want exactly 1", len(failed))
	}
	payload, _ := failed[0]["payload"].(map[string]any)
	if payload["failureSource"] != string(domain.FailureRunnerLost) {
		t.Errorf("AGENT_ACTION_FAILED payload = %v, want failureSource RUNNER_LOST", payload)
	}
	if n := len(eventsOfType(events, "RUN_COMPLETED")); n != 1 {
		t.Errorf("RUN_COMPLETED events = %d, want exactly 1", n)
	}
	if n := len(eventsOfType(events, "RUN_FAILED")); n != 0 {
		t.Errorf("RUN_FAILED events = %d, want none", n)
	}
}
