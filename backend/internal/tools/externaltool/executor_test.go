package externaltool_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/tools/externaltool"
)

const (
	echoToolName  = "echo_task"
	echoTaskType  = "ECHO"
	echoActionID  = "act_0123456789abcdef"
	echoToken     = "one-time-callback-token"
	echoCallback  = "http://emberling.internal/v1/callbacks"
	echoInputJSON = `{"text":"hello"}`
)

const echoInputSchema = `{
  "type": "object",
  "properties": {"text": {"type": "string"}},
  "required": ["text"],
  "additionalProperties": false
}`

const echoOutputSchema = `{
  "type": "object",
  "properties": {"echo": {"type": "string"}, "passed": {"type": "boolean"}},
  "required": ["echo", "passed"],
  "additionalProperties": false
}`

// echoSpec is the smallest external Tool: it forwards its arguments as the task input and
// the task result as its output.
func echoSpec() externaltool.Spec {
	return externaltool.Spec{
		Metadata: domain.ToolMetadata{
			Name:         echoToolName,
			Description:  "Echo the text through the external task service",
			InputSchema:  json.RawMessage(echoInputSchema),
			OutputSchema: json.RawMessage(echoOutputSchema),
			Poll:         &domain.PollPolicy{IntervalMs: 1000, MaxPolls: 5},
			Requires:     []domain.FactRequirement{},
		},
		TaskType: echoTaskType,
		MapInput: func(action registry.ToolAction) (json.RawMessage, error) { return action.Arguments, nil },
		MapResult: func(_ context.Context, result json.RawMessage) (registry.ToolResult, error) {
			return registry.ToolResult{Output: result}, nil
		},
	}
}

// submission is one Submit call as the fake client recorded it.
type submission struct {
	taskID   string
	taskType string
	input    json.RawMessage
	callback registry.CallbackContext
}

// fakeTaskClient is an in-memory task service: Submit records the task and answers the
// scripted error, Get answers the scripted status.
type fakeTaskClient struct {
	mu          sync.Mutex
	submissions []submission
	submitErr   error
	status      registry.PollStatus
	payload     json.RawMessage
	getErr      error
	gets        []string
}

func (f *fakeTaskClient) Submit(_ context.Context, taskID, taskType string, input json.RawMessage, callback registry.CallbackContext) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submissions = append(f.submissions, submission{taskID: taskID, taskType: taskType, input: input, callback: callback})
	return f.submitErr
}

func (f *fakeTaskClient) Get(_ context.Context, taskID string) (registry.PollStatus, json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets = append(f.gets, taskID)
	return f.status, f.payload, f.getErr
}

func echoAction() registry.ToolAction {
	return registry.ToolAction{
		RunID: "run_1", AgentRunID: "agr_1", TurnID: "trn_1", ActionID: echoActionID, ToolName: echoToolName, AttemptNo: 1,
		Arguments: json.RawMessage(echoInputJSON),
		Callback:  &registry.CallbackContext{URL: echoCallback, Token: echoToken},
	}
}

func echoState(taskID string) registry.ToolAsyncState {
	return registry.ToolAsyncState{
		AgentRunID: "agr_1", ActionID: echoActionID, ToolName: echoToolName, AttemptNo: 1,
		ExternalTask: registry.ExternalTask{ProviderID: externaltool.DefaultProviderID, ExternalTaskID: taskID},
	}
}

func mustRegistration(t *testing.T, spec externaltool.Spec, client externaltool.TaskClient) registry.ToolRegistration {
	t.Helper()
	reg, err := externaltool.Registration(spec, client)
	if err != nil {
		t.Fatalf("Registration: %v", err)
	}
	return reg
}

func pollable(t *testing.T, reg registry.ToolRegistration) registry.PollableAsyncToolExecutor {
	t.Helper()
	executor, ok := reg.Executor.(registry.PollableAsyncToolExecutor)
	if !ok {
		t.Fatalf("executor %T does not implement PollableAsyncToolExecutor", reg.Executor)
	}
	return executor
}

func TestExecute_Dispatch_SubmitsTaskUnderPrecomputedIDAndReportsIt(t *testing.T) {
	client := &fakeTaskClient{}
	executor := pollable(t, mustRegistration(t, echoSpec(), client))

	got, err := executor.Execute(context.Background(), echoAction())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	wantID := externaltool.TaskID(echoActionID, 1)
	if got.Kind != registry.ToolResultDispatched || got.ExternalTask == nil ||
		got.ExternalTask.ExternalTaskID != wantID || got.ExternalTask.ProviderID != externaltool.DefaultProviderID {
		t.Fatalf("result = %+v, want DISPATCHED %s under %s", got, wantID, externaltool.DefaultProviderID)
	}
	if len(client.submissions) != 1 {
		t.Fatalf("submissions = %d, want exactly one", len(client.submissions))
	}
	sent := client.submissions[0]
	if sent.taskID != wantID || sent.taskType != echoTaskType || string(sent.input) != echoInputJSON {
		t.Errorf("submitted task = %+v, want id %s type %s input %s", sent, wantID, echoTaskType, echoInputJSON)
	}
	if sent.callback.URL != echoCallback || sent.callback.Token != echoToken {
		t.Errorf("submitted callback = %+v, want the Runtime's callback context forwarded unchanged", sent.callback)
	}
}

func TestExecute_SubmitFails_ErrorNamesTaskNotSecrets(t *testing.T) {
	client := &fakeTaskClient{submitErr: errors.New("service rejected the task (status 503)")}
	executor := pollable(t, mustRegistration(t, echoSpec(), client))

	_, err := executor.Execute(context.Background(), echoAction())
	if err == nil {
		t.Fatal("Execute error = nil, want the submit failure")
	}
	if !errors.Is(err, client.submitErr) {
		t.Errorf("error %v does not wrap the client's error", err)
	}
	for _, leak := range []string{echoToken, echoCallback, "hello"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error %q leaks %q", err, leak)
		}
	}
	if !strings.Contains(err.Error(), externaltool.TaskID(echoActionID, 1)) || !strings.Contains(err.Error(), echoToolName) {
		t.Errorf("error %q should name the tool and the task id", err)
	}
}

func TestExecute_MissingCallbackOrMapInputError_SubmitsNothing(t *testing.T) {
	spec := echoSpec()
	spec.MapInput = func(registry.ToolAction) (json.RawMessage, error) { return nil, errors.New("arguments name no text") }
	for name, tc := range map[string]struct {
		spec   externaltool.Spec
		action registry.ToolAction
	}{
		"no callback": {spec: echoSpec(), action: func() registry.ToolAction { a := echoAction(); a.Callback = nil; return a }()},
		"empty token": {spec: echoSpec(), action: func() registry.ToolAction { a := echoAction(); a.Callback.Token = " "; return a }()},
		"no action":   {spec: echoSpec(), action: func() registry.ToolAction { a := echoAction(); a.ActionID = ""; return a }()},
		"map input":   {spec: spec, action: echoAction()},
	} {
		t.Run(name, func(t *testing.T) {
			client := &fakeTaskClient{}
			executor := pollable(t, mustRegistration(t, tc.spec, client))
			if _, err := executor.Execute(context.Background(), tc.action); err == nil {
				t.Fatal("Execute error = nil, want a dispatch error")
			}
			if len(client.submissions) != 0 {
				t.Errorf("submissions = %d, want none", len(client.submissions))
			}
		})
	}
}

func TestOnCallback_Succeeded_MapsResultMembers(t *testing.T) {
	executor := pollable(t, mustRegistration(t, echoSpec(), &fakeTaskClient{}))
	taskID := externaltool.TaskID(echoActionID, 1)

	got, err := executor.OnCallback(context.Background(), echoState(taskID), []byte(`{"status":"SUCCEEDED","result":{"echo":"hello","passed":false}}`))
	if err != nil {
		t.Fatalf("OnCallback: %v", err)
	}
	if string(got.Output) != `{"echo":"hello","passed":false}` {
		t.Errorf("output = %s, want the result object", got.Output)
	}
}

// A check that did not pass is still a SUCCEEDED task: the verdict lives in the result,
// and only a task the service reports FAILED is a Provider failure.
func TestOnCallback_Failed_IsBoundedProviderFailure(t *testing.T) {
	executor := pollable(t, mustRegistration(t, echoSpec(), &fakeTaskClient{}))
	taskID := externaltool.TaskID(echoActionID, 1)
	long := strings.Repeat("é", 600)

	_, err := executor.OnCallback(context.Background(), echoState(taskID),
		[]byte(`{"status":"FAILED","error":{"code":"SOURCE_MISSING","message":"`+long+`"}}`))
	var failure *registry.ProviderFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want a ProviderFailure", err)
	}
	if failure.Err.Code != "SOURCE_MISSING" {
		t.Errorf("code = %q, want the service's code", failure.Err.Code)
	}
	if len(failure.Err.Message) > 512 || !strings.HasPrefix(long, failure.Err.Message) {
		t.Errorf("message of %d bytes is not a bounded prefix of the service's message", len(failure.Err.Message))
	}

	_, err = executor.OnCallback(context.Background(), echoState(taskID), []byte(`{"status":"FAILED"}`))
	if !errors.As(err, &failure) || failure.Err.Code != "EXTERNAL_TASK_FAILED" {
		t.Errorf("error without a code = %v, want the default failure code", err)
	}
}

func TestOnCallback_Uninterpretable_PlainErrorNotProviderFailure(t *testing.T) {
	spec := echoSpec()
	spec.MapResult = func(context.Context, json.RawMessage) (registry.ToolResult, error) {
		return registry.ToolResult{}, errors.New("result names no echo")
	}
	executor := pollable(t, mustRegistration(t, spec, &fakeTaskClient{}))
	taskID := externaltool.TaskID(echoActionID, 1)
	for name, payload := range map[string]string{
		"not json":       `not json`,
		"no status":      `{"result":{"echo":"x"}}`,
		"other status":   `{"status":"RUNNING"}`,
		"null result":    `{"status":"SUCCEEDED","result":null}`,
		"mapping failed": `{"status":"SUCCEEDED","result":{"echo":"x","passed":true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := executor.OnCallback(context.Background(), echoState(taskID), []byte(payload))
			var failure *registry.ProviderFailure
			if err == nil || errors.As(err, &failure) {
				t.Fatalf("error = %v, want a plain interpretation error", err)
			}
		})
	}
}

// A result the OutputSchema rejects is passed through unchanged: the Runtime's schema
// validation fails the Attempt, so the mapping must not hide or repair it here.
func TestOnCallback_ResultOutsideOutputSchema_PassedThroughForRuntimeValidation(t *testing.T) {
	executor := pollable(t, mustRegistration(t, echoSpec(), &fakeTaskClient{}))
	taskID := externaltool.TaskID(echoActionID, 1)
	got, err := executor.OnCallback(context.Background(), echoState(taskID), []byte(`{"status":"SUCCEEDED","result":{"echo":42}}`))
	if err != nil {
		t.Fatalf("OnCallback: %v", err)
	}
	if string(got.Output) != `{"echo":42}` {
		t.Errorf("output = %s, want the service's result verbatim", got.Output)
	}
}

func TestPoll_MapsClientAnswers(t *testing.T) {
	taskID := externaltool.TaskID(echoActionID, 1)
	for name, tc := range map[string]struct {
		client     *fakeTaskClient
		wantStatus registry.PollStatus
		wantOutput string
		wantCode   string
		wantErr    bool
	}{
		"running":      {client: &fakeTaskClient{status: registry.PollRunning}, wantStatus: registry.PollRunning},
		"unknown task": {client: &fakeTaskClient{status: registry.PollUnknownTask}, wantStatus: registry.PollUnknownTask},
		"unknown":      {client: &fakeTaskClient{status: registry.PollStatusUnknown}, wantStatus: registry.PollStatusUnknown},
		"succeeded": {
			client:     &fakeTaskClient{status: registry.PollSucceeded, payload: json.RawMessage(`{"taskId":"x","status":"SUCCEEDED","result":{"echo":"hello","passed":true}}`)},
			wantStatus: registry.PollSucceeded, wantOutput: `{"echo":"hello","passed":true}`,
		},
		"failed": {
			client:     &fakeTaskClient{status: registry.PollFailed, payload: json.RawMessage(`{"status":"FAILED","error":{"code":"RENDER_FAILED","message":"ffmpeg exited 1"}}`)},
			wantStatus: registry.PollFailed, wantCode: "RENDER_FAILED",
		},
		"terminal without payload": {
			client:     &fakeTaskClient{status: registry.PollSucceeded, payload: json.RawMessage(`{"status":"RUNNING"}`)},
			wantStatus: registry.PollStatusUnknown,
		},
		"no answer": {client: &fakeTaskClient{getErr: errors.New("no response")}, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			executor := pollable(t, mustRegistration(t, echoSpec(), tc.client))
			got, err := executor.Poll(context.Background(), echoState(taskID))
			if tc.wantErr {
				if err == nil || !errors.Is(err, tc.client.getErr) {
					t.Fatalf("Poll error = %v, want the client's error wrapped", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Poll: %v", err)
			}
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %s, want %s", got.Status, tc.wantStatus)
			}
			if tc.wantOutput != "" && (got.ToolResult == nil || string(got.ToolResult.Output) != tc.wantOutput) {
				t.Errorf("tool result = %+v, want output %s", got.ToolResult, tc.wantOutput)
			}
			if tc.wantCode != "" && (got.Error == nil || got.Error.Code != tc.wantCode) {
				t.Errorf("error = %+v, want code %s", got.Error, tc.wantCode)
			}
			if len(tc.client.gets) != 1 || tc.client.gets[0] != taskID {
				t.Errorf("status queries = %v, want exactly one for %s", tc.client.gets, taskID)
			}
		})
	}
}

// The round trip a non-video Tool declared only through a Spec makes: dispatch, the
// service's callback, and the result the Runtime commits.
func TestEchoSpec_RoundTrip_DispatchCallbackResult(t *testing.T) {
	client := &fakeTaskClient{}
	reg := mustRegistration(t, echoSpec(), client)
	tools := registry.NewToolRegistry()
	if err := tools.Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	executor := pollable(t, reg)

	dispatched, err := executor.Execute(context.Background(), echoAction())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// The service echoes the text it was sent; the callback carries the finished result.
	var input struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(client.submissions[0].input, &input); err != nil || input.Text != "hello" {
		t.Fatalf("submitted input = %s, want the arguments", client.submissions[0].input)
	}
	payload, _ := json.Marshal(map[string]any{"status": "SUCCEEDED", "result": map[string]any{"echo": input.Text, "passed": true}})
	got, err := executor.OnCallback(context.Background(), registry.ToolAsyncState{ExternalTask: *dispatched.ExternalTask}, payload)
	if err != nil {
		t.Fatalf("OnCallback: %v", err)
	}
	if string(got.Output) != `{"echo":"hello","passed":true}` {
		t.Errorf("output = %s", got.Output)
	}
}
