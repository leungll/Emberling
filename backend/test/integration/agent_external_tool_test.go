//go:build integration

// A Tool declared only through an externaltool.Spec, end to end against PostgreSQL and an
// in-process task service speaking the task protocol: dispatch under the precomputed
// external task id, the service's callback committing the result once, a duplicate
// callback writing nothing, a callback that arrives before the Binding commits being
// staged and replayed, a poll racing a callback with exactly one winner, a service that
// lost the task handing the call back to the Agent, a result the OutputSchema rejects
// failing the Attempt with Trace, and a FAILED task becoming a Provider failure.
//
// They share the agentHarness of agent_loop_test.go and the fact fixtures of
// agent_action_facts_test.go.
package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/adapters/externaltask"
	"github.com/leungll/Emberling/backend/internal/adapters/mockmodel"
	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/service"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/externaltool"
)

const (
	echoToolName     = "echo_task"
	echoTaskType     = "ECHO"
	echoPollInterval = 30 * time.Second
	echoArguments    = `{"text":"hello"}`
)

// echoSpec is the non-video Tool that proves the protocol: its arguments are the task
// input and the task result is its output.
func echoSpec() externaltool.Spec {
	return externaltool.Spec{
		Metadata: domain.ToolMetadata{
			Name:        echoToolName,
			Description: "Echo the text through the task service",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"echo":{"type":"string"},"passed":{"type":"boolean"}},
				"required":["echo","passed"],"additionalProperties":false}`),
			Poll:     &domain.PollPolicy{IntervalMs: echoPollInterval.Milliseconds(), MaxPolls: 5},
			Requires: []domain.FactRequirement{},
		},
		TaskType: echoTaskType,
		MapInput: func(action registry.ToolAction) (json.RawMessage, error) { return action.Arguments, nil },
		MapResult: func(_ context.Context, result json.RawMessage) (registry.ToolResult, error) {
			return registry.ToolResult{Output: result}, nil
		},
	}
}

// fakeTask is one task the fake service holds.
type fakeTask struct {
	taskType      string
	input         json.RawMessage
	callbackURL   string
	callbackToken string
	status        string
	payload       string
}

// fakeTaskService is an in-process task service: POST /v1/tasks records a task (202, or
// 200 for an id it already holds) and GET /v1/tasks/{id} answers from the recorded state.
// Tests finish a task by setting its terminal payload and delivering the callback
// themselves through the same intake the HTTP callback endpoint calls. onSubmit, when set,
// runs inside the submit handler before the service answers, which is the only moment at
// which the Tool's dispatch is in flight and its Binding is not yet committed.
type fakeTaskService struct {
	mu       sync.Mutex
	tasks    map[string]*fakeTask
	submits  int
	gets     int
	onSubmit func(taskID string, task fakeTask)
}

func newFakeTaskService() *fakeTaskService {
	return &fakeTaskService{tasks: map[string]*fakeTask{}}
}

func (s *fakeTaskService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/tasks":
		var body struct {
			TaskID        string          `json:"taskId"`
			Type          string          `json:"type"`
			Input         json.RawMessage `json:"input"`
			CallbackURL   string          `json:"callbackUrl"`
			CallbackToken string          `json:"callbackToken"`
		}
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if json.Unmarshal(raw, &body) != nil || body.TaskID == "" || body.Type == "" || body.CallbackToken == "" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.submits++
		existing, held := s.tasks[body.TaskID]
		if !held {
			existing = &fakeTask{taskType: body.Type, input: body.Input, callbackURL: body.CallbackURL, callbackToken: body.CallbackToken, status: externaltask.StatusQueued}
			s.tasks[body.TaskID] = existing
		}
		task := *existing
		hook := s.onSubmit
		s.mu.Unlock()
		if hook != nil {
			hook(body.TaskID, task)
		}
		if held {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusAccepted)
		}
		_, _ = io.WriteString(w, `{"taskId":"`+body.TaskID+`","status":"`+task.status+`"}`)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/tasks/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/tasks/")
		s.mu.Lock()
		s.gets++
		task, held := s.tasks[id]
		s.mu.Unlock()
		if !held {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"unknown task"}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		if task.payload == "" {
			_, _ = io.WriteString(w, `{"taskId":"`+id+`","status":"`+task.status+`"}`)
			return
		}
		// A terminal answer carries the same status, result and error members as the
		// callback payload, under the task id.
		_, _ = io.WriteString(w, `{"taskId":"`+id+`",`+strings.TrimPrefix(task.payload, "{"))
	default:
		http.NotFound(w, r)
	}
}

// task returns the recorded task, failing when the service never received it.
func (s *fakeTaskService) task(t *testing.T, taskID string) fakeTask {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	task, held := s.tasks[taskID]
	if !held {
		t.Fatalf("the task service never received task %s", taskID)
	}
	return *task
}

// finish records the terminal payload a later status query answers with.
func (s *fakeTaskService) finish(taskID, payload string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task := s.tasks[taskID]
	task.payload = payload
	var head struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal([]byte(payload), &head)
	task.status = head.Status
}

// forget drops the task, as a service that restarted without durable task state would.
func (s *fakeTaskService) forget(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tasks, taskID)
}

func (s *fakeTaskService) counts() (submits, gets int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.submits, s.gets
}

func echoSucceededPayload(text string, passed bool) string {
	payload, _ := json.Marshal(map[string]any{"status": "SUCCEEDED", "result": map[string]any{"echo": text, "passed": passed}})
	return string(payload)
}

// echoFixture is one Backend with echo_task registered over the real externaltask client
// against the fake service.
type echoFixture struct {
	h       *agentHarness
	service *fakeTaskService
}

func newEchoFixture(t *testing.T, opts agentHarnessOptions) *echoFixture {
	t.Helper()
	fake := newFakeTaskService()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	reg, err := externaltool.Registration(echoSpec(), externaltask.New(server.URL, server.Client()))
	if err != nil {
		t.Fatalf("Registration: %v", err)
	}
	if opts.Clock == nil {
		opts.Clock = newExecClock(time.Now().UTC().Truncate(time.Millisecond))
	}
	opts.Tools = append(opts.Tools, reg)
	return &echoFixture{h: newAgentHarness(t, opts), service: fake}
}

// start scripts one echo_task call (the model answers FINAL afterwards), creates the Run
// and executes the first Turn, returning its Action and the external task id the Tool
// computed for the Action's first Attempt.
func (f *echoFixture) start(t *testing.T, workflowID string) (domain.Run, service.AdvanceOutcome, domain.AgentAction, string) {
	t.Helper()
	factScriptToolCalls(f.h, mockmodel.Scenario{ToolName: echoToolName, ToolArguments: json.RawMessage(echoArguments)})
	def := f.h.saveDefinition(factDefinition(workflowID, echoToolName))
	run := f.h.createRun(def.WorkflowID, def.Version, `{"question":"`+agentQuestion+`"}`)
	outcome := f.h.claimAgentNode(run.ID)
	f.h.execute(outcome)
	action, _ := agentActionOfTurn(f.h.ctx, t, f.h.uow, outcome.AgentTurnID)
	return run, outcome, action, externaltool.TaskID(action.ID, 1)
}

// dispatch is start with the Action left waiting on the task service's answer.
func (f *echoFixture) dispatch(t *testing.T, workflowID string) (domain.Run, service.AdvanceOutcome, domain.AgentAction, string) {
	t.Helper()
	run, outcome, action, taskID := f.start(t, workflowID)
	if action.Status != domain.AgentActionWaitingCallback {
		t.Fatalf("precondition: action status = %s, want WAITING_CALLBACK", action.Status)
	}
	return run, outcome, action, taskID
}

// deliver hands the fake service's callback for taskID to the same intake the HTTP
// callback endpoint calls, with the token the Tool sent the service.
func (f *echoFixture) deliver(ctx context.Context, t *testing.T, taskID, payload string) (service.CallbackOutcome, error) {
	t.Helper()
	task := f.service.task(t, taskID)
	return f.h.svc.HandleCallback(ctx, service.HandleCallback{Token: task.callbackToken, ExternalTaskID: taskID, Payload: json.RawMessage(payload)})
}

func TestExternalTool_RoundTrip_DispatchCallbackCommitsResultOnce(t *testing.T) {
	f := newEchoFixture(t, agentHarnessOptions{})
	run, outcome, action, taskID := f.dispatch(t, "wf-external-echo")

	if !strings.HasPrefix(taskID, externaltool.TaskIDPrefix) {
		t.Fatalf("task id %q lacks the %s prefix", taskID, externaltool.TaskIDPrefix)
	}
	task := f.service.task(t, taskID)
	if task.taskType != echoTaskType || string(task.input) != echoArguments || task.callbackURL == "" || task.callbackToken == "" {
		t.Fatalf("dispatched task = %+v, want type %s with the arguments as input and a callback", task, echoTaskType)
	}
	attempt := agentOnlyToolAttempt(t, f.h, action.ID)
	if attempt.Status != domain.ToolAttemptDispatched {
		t.Fatalf("attempt status = %s, want DISPATCHED", attempt.Status)
	}
	bindings := agentToolBindings(t, f.h, attempt.ID)
	if len(bindings) != 1 || bindings[0].ExternalTaskID != taskID || bindings[0].ProviderID != externaltool.DefaultProviderID {
		t.Fatalf("bindings = %+v, want one for %s under %s", bindings, taskID, externaltool.DefaultProviderID)
	}

	delivered, err := f.deliver(f.h.ctx, t, taskID, echoSucceededPayload("hello", false))
	if err != nil || !delivered.Accepted || delivered.Duplicate {
		t.Fatalf("callback = %+v, %v, want accepted", delivered, err)
	}
	if action = getAgentAction(f.h.ctx, t, f.h.uow, action.ID); action.Status != domain.AgentActionSucceeded {
		t.Fatalf("action status = %s, want SUCCEEDED: a check that does not pass is still a successful call", action.Status)
	}
	attempt = getToolAttempt(t, f.h, attempt.ID)
	if attempt.Status != domain.ToolAttemptSucceeded {
		t.Errorf("attempt status = %s, want SUCCEEDED", attempt.Status)
	}
	assertSameJSON(t, "attempt result", json.RawMessage(`{"echo":"hello","passed":false}`), attempt.Result)
	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, outcome.NodeRunID)
	if !agentTurnNoExists(f.h.ctx, t, f.h.uow, agentRun.ID, 2) {
		t.Errorf("the result did not continue the Agent Loop to turn 2")
	}
	events := listEvents(f.h.ctx, t, f.h.uow, run.ID)
	assertContiguousSeq(t, events)
	if n := agentCallbackCompletions(t, events); n != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want exactly 1", n)
	}
	assertAgentTokenAbsent(t, f.h, run.ID, task.callbackToken)
	if submits, _ := f.service.counts(); submits != 1 {
		t.Errorf("task service received %d submits, want exactly one", submits)
	}
}

func TestExternalTool_DuplicateCallback_DoesNotAdvanceTwice(t *testing.T) {
	f := newEchoFixture(t, agentHarnessOptions{})
	run, outcome, action, taskID := f.dispatch(t, "wf-external-echo-duplicate")
	payload := echoSucceededPayload("hello", true)

	first, err := f.deliver(f.h.ctx, t, taskID, payload)
	if err != nil || !first.Accepted || first.Duplicate {
		t.Fatalf("callback = %+v, %v, want accepted", first, err)
	}
	before := agentSnapshot(t, f.h, run, outcome)
	seqBefore := agentRunRow(f.h.ctx, t, f.h.uow, run.ID).LastSeq
	second, err := f.deliver(f.h.ctx, t, taskID, payload)
	if err != nil || !second.Accepted || !second.Duplicate {
		t.Fatalf("second callback = %+v, %v, want an accepted duplicate", second, err)
	}
	if after := agentSnapshot(t, f.h, run, outcome); after != before {
		t.Fatalf("state after the duplicate = %+v, want unchanged %+v", after, before)
	}
	if seqAfter := agentRunRow(f.h.ctx, t, f.h.uow, run.ID).LastSeq; seqAfter != seqBefore {
		t.Errorf("last seq moved from %d to %d on a duplicate", seqBefore, seqAfter)
	}
	if attempts := agentToolAttempts(f.h.ctx, t, f.h.uow, action.ID); len(attempts) != 1 || attempts[0].Status != domain.ToolAttemptSucceeded {
		t.Errorf("attempts = %+v, want one SUCCEEDED Attempt", attempts)
	}
	if n := agentCallbackCompletions(t, listEvents(f.h.ctx, t, f.h.uow, run.ID)); n != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want exactly 1", n)
	}
}

// The service finishes the task and delivers its callback while the Tool's dispatch is
// still in flight, before the Callback Binding commits. The intake stores the delivery as
// a Pending Callback and the dispatch transaction's replay consumes it once.
func TestExternalTool_CallbackBeforeBindingCommit_StagedAndReplayed(t *testing.T) {
	f := newEchoFixture(t, agentHarnessOptions{})
	var early service.CallbackOutcome
	var earlyErr error
	f.service.onSubmit = func(taskID string, task fakeTask) {
		f.service.finish(taskID, echoSucceededPayload("hello", true))
		early, earlyErr = f.h.svc.HandleCallback(context.Background(), service.HandleCallback{
			Token: task.callbackToken, ExternalTaskID: taskID, Payload: json.RawMessage(echoSucceededPayload("hello", true)),
		})
	}
	run, outcome, action, taskID := f.start(t, "wf-external-echo-early")

	if earlyErr != nil || !early.Pending || early.Accepted {
		t.Fatalf("early callback = %+v, %v, want stored as pending", early, earlyErr)
	}
	if status := asyncPendingStatus(f.h.ctx, t, f.h.uow, taskID); status != "consumed" {
		t.Fatalf("pending callback row = %s, want consumed by the dispatch's replay", status)
	}
	if action.Status != domain.AgentActionSucceeded {
		t.Errorf("action status = %s, want SUCCEEDED from the replayed callback", action.Status)
	}
	attempt := agentOnlyToolAttempt(t, f.h, action.ID)
	if attempt.Status != domain.ToolAttemptSucceeded {
		t.Errorf("attempt status = %s, want SUCCEEDED", attempt.Status)
	}
	assertSameJSON(t, "attempt result", json.RawMessage(`{"echo":"hello","passed":true}`), attempt.Result)
	events := listEvents(f.h.ctx, t, f.h.uow, run.ID)
	assertContiguousSeq(t, events)
	if n := agentCallbackCompletions(t, events); n != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource CALLBACK = %d, want exactly 1", n)
	}

	// The service re-delivers live: the Attempt is resolved, so nothing more is written.
	seqBefore := agentRunRow(f.h.ctx, t, f.h.uow, run.ID).LastSeq
	got, err := f.deliver(f.h.ctx, t, taskID, echoSucceededPayload("hello", true))
	if err != nil || !got.Duplicate {
		t.Fatalf("live re-delivery = %+v, %v, want duplicate", got, err)
	}
	if seqAfter := agentRunRow(f.h.ctx, t, f.h.uow, run.ID).LastSeq; seqAfter != seqBefore {
		t.Errorf("last seq moved from %d to %d on a duplicate", seqBefore, seqAfter)
	}
	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, outcome.NodeRunID)
	if !agentTurnNoExists(f.h.ctx, t, f.h.uow, agentRun.ID, 2) {
		t.Errorf("the replayed callback did not continue the Agent Loop to turn 2")
	}
}

// The service answers the status poll SUCCEEDED while its callback for the same task is
// being delivered. Both reach the resume transaction together; the conditional update
// from DISPATCHED lets exactly one of them write the result.
func TestExternalTool_PollRacesCallback_ExactlyOneWinnerOneResult(t *testing.T) {
	arrived := make(chan struct{})
	release := make(chan struct{})
	claimed := make(chan struct{})
	var barrier *pollArmedBarrierUoW
	f := newEchoFixture(t, agentHarnessOptions{
		WrapUoW: func(inner store.UnitOfWork) store.UnitOfWork {
			barrier = &pollArmedBarrierUoW{UnitOfWork: inner, arrived: arrived, release: release}
			return barrier
		},
		PollHooks: service.PollHooks{AfterClaim: func(context.Context, string) error {
			// The claim committed: from here on every state-changing transaction waits at
			// the barrier, and the callback may start.
			barrier.armed.Store(true)
			close(claimed)
			return nil
		}},
	})
	run, outcome, action, taskID := f.dispatch(t, "wf-external-echo-race")
	attempt := agentOnlyToolAttempt(t, f.h, action.ID)
	payload := echoSucceededPayload("hello", true)
	f.service.finish(taskID, payload)
	f.h.clock.Advance(echoPollInterval)

	var wg sync.WaitGroup
	var pollOutcome service.PollToolAttemptOutcome
	var callbackOutcome service.CallbackOutcome
	var pollErr, callbackErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		pollOutcome, pollErr = f.h.svc.PollToolAttempt(f.h.ctx, service.PollToolAttempt{AttemptID: attempt.ID, ActionID: attempt.ActionID})
	}()
	go func() {
		defer wg.Done()
		<-claimed
		callbackOutcome, callbackErr = f.deliver(f.h.ctx, t, taskID, payload)
	}()
	<-arrived
	<-arrived
	barrier.armed.Store(false)
	close(release)
	wg.Wait()

	if pollErr != nil || callbackErr != nil {
		t.Fatalf("poll err = %v, callback err = %v, want neither", pollErr, callbackErr)
	}
	pollWon := pollOutcome.Kind == service.PollAttemptResumed
	callbackWon := callbackOutcome.Accepted && !callbackOutcome.Duplicate
	if pollWon == callbackWon {
		t.Fatalf("poll outcome = %+v, callback outcome = %+v, want exactly one winner", pollOutcome, callbackOutcome)
	}
	attempt = getToolAttempt(t, f.h, attempt.ID)
	if attempt.Status != domain.ToolAttemptSucceeded {
		t.Errorf("attempt status = %s, want SUCCEEDED", attempt.Status)
	}
	assertSameJSON(t, "attempt result", json.RawMessage(`{"echo":"hello","passed":true}`), attempt.Result)
	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, outcome.NodeRunID)
	if !agentTurnNoExists(f.h.ctx, t, f.h.uow, agentRun.ID, 2) || agentTurnNoExists(f.h.ctx, t, f.h.uow, agentRun.ID, 3) {
		t.Errorf("turns after the race: want exactly turn 2 created")
	}
	events := listEvents(f.h.ctx, t, f.h.uow, run.ID)
	assertContiguousSeq(t, events)
	if got := agentCountEventType(execEventTypes(events), domain.EventAgentActionCompleted); got != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED events = %d, want exactly 1: the loser appends nothing", got)
	}
	if _, gets := f.service.counts(); gets != 1 {
		t.Errorf("status queries = %d, want exactly one", gets)
	}
}

// A service that lost the task answers the status poll 404. The Tool is SAFE to repeat,
// so the Runtime hands the call back to the Agent as EXECUTION_INTERRUPTED with one new
// Turn and never re-dispatches it.
func TestExternalTool_PollTaskNotFound_ReturnedAsExecutionInterrupted(t *testing.T) {
	f := newEchoFixture(t, agentHarnessOptions{})
	run, outcome, action, taskID := f.dispatch(t, "wf-external-echo-lost")
	agentRunID := factAgentRunID(t, f.h, outcome)
	attempt := agentOnlyToolAttempt(t, f.h, action.ID)
	f.service.forget(taskID)

	f.h.clock.Advance(echoPollInterval)
	got := pollToolAttempt(t, f.h, attempt)
	if got.Kind != service.PollAttemptReturned || !got.Failed || got.FailureSource != domain.FailureProviderPoll {
		t.Fatalf("poll = %+v, want the lost call handed back to the Agent", got)
	}
	attempt = getToolAttempt(t, f.h, attempt.ID)
	if attempt.Status != domain.ToolAttemptFailed || attempt.Error == nil || attempt.Error.Code != "EXECUTION_INTERRUPTED" {
		t.Errorf("attempt = status %s error %+v, want FAILED with EXECUTION_INTERRUPTED", attempt.Status, attempt.Error)
	}
	if attempt.Uncertain == nil || *attempt.Uncertain {
		t.Errorf("attempt uncertain = %v, want false: the task is SAFE to repeat", attempt.Uncertain)
	}
	if action = getAgentAction(f.h.ctx, t, f.h.uow, action.ID); action.Status != domain.AgentActionFailed || action.Error == nil || action.Error.Code != "EXECUTION_INTERRUPTED" {
		t.Errorf("action = status %s error %+v, want FAILED with EXECUTION_INTERRUPTED", action.Status, action.Error)
	}
	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, outcome.NodeRunID)
	if agentRun.Termination != nil {
		t.Fatalf("agent run terminated as %s, want it to continue", *agentRun.Termination)
	}
	if !agentTurnNoExists(f.h.ctx, t, f.h.uow, agentRunID, 2) || agentTurnNoExists(f.h.ctx, t, f.h.uow, agentRunID, 3) {
		t.Errorf("want exactly one Turn to follow the handback")
	}
	message := wsToolMessage(t, f.h, agentRunID, 1, action.ID)
	if message["status"] != "EXECUTION_INTERRUPTED" {
		t.Errorf("tool message = %v, want EXECUTION_INTERRUPTED handed to the Agent", message)
	}
	if submits, gets := f.service.counts(); submits != 1 || gets != 1 {
		t.Errorf("task service saw %d submits and %d status queries, want one each: the Runtime never re-dispatches", submits, gets)
	}
	if persisted := agentRunRow(f.h.ctx, t, f.h.uow, run.ID); persisted.Status != domain.RunRunning {
		t.Errorf("run status = %s, want RUNNING", persisted.Status)
	}
}

// A result the registered OutputSchema rejects is not repaired by the mapping: the
// Attempt and the Action fail with TOOL_RESULT_INVALID, with the callback as the failure
// source, and the failure is on the Trace.
func TestExternalTool_ResultViolatesOutputSchema_FailsAttemptWithTrace(t *testing.T) {
	f := newEchoFixture(t, agentHarnessOptions{})
	run, outcome, action, taskID := f.dispatch(t, "wf-external-echo-invalid")
	eventsBefore := len(listEvents(f.h.ctx, t, f.h.uow, run.ID))

	delivered, err := f.deliver(f.h.ctx, t, taskID, `{"status":"SUCCEEDED","result":{"echo":42}}`)
	if err != nil || !delivered.Accepted {
		t.Fatalf("callback = %+v, %v, want accepted", delivered, err)
	}
	action = assertAgentActionFailed(t, f.h, run.ID, outcome, domain.TerminationToolError, domain.FailureCallback)
	if action.Error == nil || action.Error.Code != "TOOL_RESULT_INVALID" {
		t.Errorf("action error = %+v, want TOOL_RESULT_INVALID", action.Error)
	}
	attempt := agentOnlyToolAttempt(t, f.h, action.ID)
	if attempt.Status != domain.ToolAttemptFailed || attempt.Error == nil || attempt.Error.Code != "TOOL_RESULT_INVALID" {
		t.Errorf("attempt = status %s error %+v, want FAILED with TOOL_RESULT_INVALID", attempt.Status, attempt.Error)
	}
	events := listEvents(f.h.ctx, t, f.h.uow, run.ID)
	assertContiguousSeq(t, events)
	tail := execEventTypes(events[eventsBefore:])
	if agentCountEventType(tail, domain.EventAgentActionFailed) != 1 {
		t.Errorf("events after the callback = %v, want exactly one AGENT_ACTION_FAILED", tail)
	}
	failed := agentLastEventPayload(t, events, domain.EventAgentActionFailed)
	if failed["toolAttemptId"] != attempt.ID || failed["failureSource"] != string(domain.FailureCallback) {
		t.Errorf("AGENT_ACTION_FAILED payload = %v, want the attempt id and failureSource CALLBACK", failed)
	}
}

// A task the service reports FAILED is a Provider failure carrying the service's code.
func TestExternalTool_FailedTask_FailsActionWithServiceCode(t *testing.T) {
	f := newEchoFixture(t, agentHarnessOptions{})
	run, outcome, action, taskID := f.dispatch(t, "wf-external-echo-failed")

	delivered, err := f.deliver(f.h.ctx, t, taskID, `{"status":"FAILED","error":{"code":"SOURCE_MISSING","message":"the source video is gone"}}`)
	if err != nil || !delivered.Accepted {
		t.Fatalf("callback = %+v, %v, want accepted", delivered, err)
	}
	action = assertAgentActionFailed(t, f.h, run.ID, outcome, domain.TerminationToolError, domain.FailureCallback)
	if action.Error == nil || action.Error.Code != "SOURCE_MISSING" {
		t.Errorf("action error = %+v, want the service's SOURCE_MISSING", action.Error)
	}
	if attempt := agentOnlyToolAttempt(t, f.h, action.ID); attempt.Status != domain.ToolAttemptFailed {
		t.Errorf("attempt status = %s, want FAILED", attempt.Status)
	}
}

// The service's callback never arrives; the due status poll finds the task SUCCEEDED and
// commits the same result the callback would have, through the same resume path.
func TestExternalTool_CallbackLost_PollCommitsResultOnce(t *testing.T) {
	f := newEchoFixture(t, agentHarnessOptions{})
	run, outcome, action, taskID := f.dispatch(t, "wf-external-echo-poll")
	attempt := agentOnlyToolAttempt(t, f.h, action.ID)
	f.service.finish(taskID, echoSucceededPayload("hello", true))

	f.h.clock.Advance(echoPollInterval)
	got := pollToolAttempt(t, f.h, attempt)
	if got.Kind != service.PollAttemptResumed || got.Status != registry.PollSucceeded || got.Failed {
		t.Fatalf("poll = %+v, want RESUMED with SUCCEEDED", got)
	}
	attempt = getToolAttempt(t, f.h, attempt.ID)
	if attempt.Status != domain.ToolAttemptSucceeded {
		t.Errorf("attempt status = %s, want SUCCEEDED", attempt.Status)
	}
	assertSameJSON(t, "attempt result", json.RawMessage(`{"echo":"hello","passed":true}`), attempt.Result)
	if n := agentPollCompletions(t, listEvents(f.h.ctx, t, f.h.uow, run.ID), domain.CompletionProviderPoll); n != 1 {
		t.Errorf("AGENT_ACTION_COMPLETED with completionSource PROVIDER_POLL = %d, want exactly 1", n)
	}
	agentRun, _ := agentRunOfNodeRun(f.h.ctx, t, f.h.uow, outcome.NodeRunID)
	if !agentTurnNoExists(f.h.ctx, t, f.h.uow, agentRun.ID, 2) {
		t.Errorf("the polled result did not continue the Agent Loop to turn 2")
	}
	// The callback arrives late: the Attempt is resolved, so it is a duplicate.
	late, err := f.deliver(f.h.ctx, t, taskID, echoSucceededPayload("hello", true))
	if err != nil || !late.Duplicate {
		t.Errorf("late callback = %+v, %v, want duplicate", late, err)
	}
}
