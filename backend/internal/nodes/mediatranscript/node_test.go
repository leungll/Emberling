package mediatranscript_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/nodes/mediatranscript"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const (
	sourceID  = "src_456"
	sourceSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	material  = "S1\t0.000\t2.500\tHello there.\nS2\t2.500\t5.000\tWelcome back.\n"
)

var callback = registry.CallbackContext{URL: "https://emberling.test/api/callbacks", Token: "one-time-token"}

// taskJSON is the task object a Task Input node publishes, with members this node does
// not read, which must be republished unchanged.
var taskJSON = json.RawMessage(`{"sourceId":"` + sourceID + `","sourceSha256":"` + sourceSHA + `","durationMs":421000,"requirement":"a 30 s clip about the welcome","goal":"DELIVER_CLIP"}`)

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// fakeTasks records what was submitted and answers status queries from a script.
type fakeTasks struct {
	submitErr error
	submits   []submit
	status    registry.PollStatus
	body      string
	getErr    error
}

type submit struct {
	taskID, taskType string
	input            json.RawMessage
	callback         registry.CallbackContext
}

func (f *fakeTasks) Submit(_ context.Context, taskID, taskType string, input json.RawMessage, cb registry.CallbackContext) error {
	f.submits = append(f.submits, submit{taskID, taskType, input, cb})
	return f.submitErr
}

func (f *fakeTasks) Get(context.Context, string) (registry.PollStatus, json.RawMessage, error) {
	return f.status, json.RawMessage(f.body), f.getErr
}

// fakeArtifacts stores text in memory, content-addressed like the real store.
type fakeArtifacts struct {
	err     error
	written []string
}

func (f *fakeArtifacts) WriteText(_ context.Context, r io.Reader) (domain.ArtifactRef, error) {
	if f.err != nil {
		return domain.ArtifactRef{}, f.err
	}
	content, err := io.ReadAll(r)
	if err != nil {
		return domain.ArtifactRef{}, err
	}
	f.written = append(f.written, string(content))
	digest := sha(string(content))
	id, err := domain.ArtifactIDForSHA256(digest)
	if err != nil {
		return domain.ArtifactRef{}, err
	}
	return domain.ArtifactRef{ArtifactID: id, MediaType: domain.ArtifactMediaTypeTextPlain, SizeBytes: int64(len(content)), SHA256: digest}, nil
}

func newExecutor(t *testing.T, tasks *fakeTasks, artifacts *fakeArtifacts) registry.PollableAsyncNodeExecutor {
	t.Helper()
	reg, err := mediatranscript.Registration(mediatranscript.Ports{Tasks: tasks, Artifacts: artifacts})
	if err != nil {
		t.Fatalf("Registration: %v", err)
	}
	executor, ok := reg.Binding.(registry.ExecutorBinding).Executor.(registry.PollableAsyncNodeExecutor)
	if !ok {
		t.Fatal("the registered executor is not pollable")
	}
	return executor
}

func asyncState(ports map[string]json.RawMessage) registry.NodeAsyncState {
	return registry.NodeAsyncState{
		RunID: "run_1", NodeRunID: "nr_1", AttemptNo: 1,
		ExternalTask: registry.ExternalTask{ProviderID: mediatranscript.DefaultProviderID, ExternalTaskID: mediatranscript.TaskID("nr_1", 1)},
		Config:       map[string]any{}, Ports: ports,
	}
}

func succeededPayload(overrides map[string]any) []byte {
	result := map[string]any{
		"sourceId": sourceID, "sourceSha256": sourceSHA, "transcriptSha256": sha(material),
		"language": "en", "sentenceCount": 2, "lineCount": 2, "durationMs": 421000, "model": "small.en", "material": material,
	}
	for k, v := range overrides {
		result[k] = v
	}
	raw, _ := json.Marshal(map[string]any{"status": "SUCCEEDED", "result": result})
	return raw
}

func TestRegistration_Metadata_AsyncExternalSafePolledWithoutModel(t *testing.T) {
	reg, err := mediatranscript.Registration(mediatranscript.Ports{Tasks: &fakeTasks{}, Artifacts: &fakeArtifacts{}})
	if err != nil {
		t.Fatalf("Registration: %v", err)
	}
	r := registry.NewNodeRegistry()
	if err := r.Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	md := reg.Metadata
	if md.Type != mediatranscript.NodeType || md.ExecutionKind != domain.NodeExecutionAsync {
		t.Fatalf("metadata type/kind = %s/%s, want media_transcript ASYNC", md.Type, md.ExecutionKind)
	}
	if md.SideEffect != (domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencySafe}) {
		t.Fatalf("side effect = %+v, want EXTERNAL/SAFE", md.SideEffect)
	}
	if md.Poll == nil || md.Poll.IntervalMs != 5000 || md.Poll.MaxPolls != 120 {
		t.Fatalf("poll policy = %+v, want 5 s interval and 120 polls", md.Poll)
	}
	if md.DispatchModelPath != "" {
		t.Fatalf("DispatchModelPath = %q, want none", md.DispatchModelPath)
	}
	if len(md.Inputs) != 1 || md.Inputs[0].Name != "task" || len(md.Outputs) != 3 ||
		md.Outputs[0].Name != "transcript" || md.Outputs[1].Name != "task" || md.Outputs[2].Name != "assignment" {
		t.Fatalf("ports = in %+v out %+v, want task -> transcript, task, assignment", md.Inputs, md.Outputs)
	}
}

func TestRegistration_MissingPort_Fails(t *testing.T) {
	if _, err := mediatranscript.Registration(mediatranscript.Ports{Artifacts: &fakeArtifacts{}}); err == nil {
		t.Fatal("Registration without a task client succeeded")
	}
	if _, err := mediatranscript.Registration(mediatranscript.Ports{Tasks: &fakeTasks{}}); err == nil {
		t.Fatal("Registration without an artifact writer succeeded")
	}
}

func TestTaskID_StablePerAttemptDistinctAcrossAttempts(t *testing.T) {
	first := mediatranscript.TaskID("nr_a", 1)
	if first != mediatranscript.TaskID("nr_a", 1) {
		t.Fatalf("the same Attempt produced two ids")
	}
	if !regexp.MustCompile(`^extn_[0-9a-f]{32}$`).MatchString(first) {
		t.Fatalf("id = %q, want the extn_ prefix and 32 hex characters", first)
	}
	if first == mediatranscript.TaskID("nr_a", 2) || first == mediatranscript.TaskID("nr_b", 1) {
		t.Fatal("another Attempt or NodeRun shares the id")
	}
}

func TestExecute_ValidTask_SubmitsTranscribeAndReturnsDispatched(t *testing.T) {
	tasks := &fakeTasks{}
	executor := newExecutor(t, tasks, &fakeArtifacts{})
	result, err := executor.Execute(context.Background(), registry.NodeInput{
		RunID: "run_1", NodeRunID: "nr_1", AttemptNo: 2, Ports: map[string]json.RawMessage{"task": taskJSON}, Callback: &callback,
	}, map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	wantID := mediatranscript.TaskID("nr_1", 2)
	if result.Kind != registry.NodeResultDispatched || result.ExternalTask == nil ||
		result.ExternalTask.ExternalTaskID != wantID || result.ExternalTask.ProviderID != mediatranscript.DefaultProviderID {
		t.Fatalf("result = %+v, want DISPATCHED task %s at %s", result, wantID, mediatranscript.DefaultProviderID)
	}
	if len(tasks.submits) != 1 {
		t.Fatalf("submits = %d, want 1", len(tasks.submits))
	}
	got := tasks.submits[0]
	if got.taskID != wantID || got.taskType != mediatranscript.TaskType || string(got.input) != `{"sourceId":"`+sourceID+`"}` || got.callback != callback {
		t.Fatalf("submit = %+v, want TRANSCRIBE of %s with the callback context", got, sourceID)
	}
}

func TestExecute_InvalidInput_FailsBeforeSubmit(t *testing.T) {
	for name, input := range map[string]registry.NodeInput{
		"no callback":     {NodeRunID: "nr_1", AttemptNo: 1, Ports: map[string]json.RawMessage{"task": taskJSON}},
		"no task port":    {NodeRunID: "nr_1", AttemptNo: 1, Ports: map[string]json.RawMessage{}, Callback: &callback},
		"null task":       {NodeRunID: "nr_1", AttemptNo: 1, Ports: map[string]json.RawMessage{"task": json.RawMessage(`null`)}, Callback: &callback},
		"no source id":    {NodeRunID: "nr_1", AttemptNo: 1, Ports: map[string]json.RawMessage{"task": json.RawMessage(`{"sourceSha256":"` + sourceSHA + `","durationMs":1}`)}, Callback: &callback},
		"bad source hash": {NodeRunID: "nr_1", AttemptNo: 1, Ports: map[string]json.RawMessage{"task": json.RawMessage(`{"sourceId":"s","sourceSha256":"ABC","durationMs":1}`)}, Callback: &callback},
		"no duration":     {NodeRunID: "nr_1", AttemptNo: 1, Ports: map[string]json.RawMessage{"task": json.RawMessage(`{"sourceId":"s","sourceSha256":"` + sourceSHA + `"}`)}, Callback: &callback},
		"no attempt":      {NodeRunID: "nr_1", Ports: map[string]json.RawMessage{"task": taskJSON}, Callback: &callback},
	} {
		t.Run(name, func(t *testing.T) {
			tasks := &fakeTasks{}
			if _, err := newExecutor(t, tasks, &fakeArtifacts{}).Execute(context.Background(), input, map[string]any{}); err == nil {
				t.Fatal("Execute succeeded")
			}
			if len(tasks.submits) != 0 {
				t.Fatalf("a task was submitted despite the invalid input")
			}
		})
	}
}

func TestExecute_SubmitFails_WrapsTheClientError(t *testing.T) {
	cause := errors.New("service rejected the task")
	tasks := &fakeTasks{submitErr: cause}
	_, err := newExecutor(t, tasks, &fakeArtifacts{}).Execute(context.Background(), registry.NodeInput{
		NodeRunID: "nr_1", AttemptNo: 1, Ports: map[string]json.RawMessage{"task": taskJSON}, Callback: &callback,
	}, map[string]any{})
	if !errors.Is(err, cause) {
		t.Fatalf("Execute error = %v, want it to wrap the client error", err)
	}
	if strings.Contains(err.Error(), callback.Token) {
		t.Fatalf("the error quotes the callback token: %v", err)
	}
}

func TestOnCallback_Succeeded_StoresMaterialAndPublishesReferenceOnly(t *testing.T) {
	artifacts := &fakeArtifacts{}
	executor := newExecutor(t, &fakeTasks{}, artifacts)
	output, err := executor.OnCallback(context.Background(), asyncState(map[string]json.RawMessage{"task": taskJSON}), succeededPayload(nil))
	if err != nil {
		t.Fatalf("OnCallback: %v", err)
	}
	if len(artifacts.written) != 1 || artifacts.written[0] != material {
		t.Fatalf("stored material = %q, want the transcript", artifacts.written)
	}
	wantID, _ := domain.ArtifactIDForSHA256(sha(material))
	wantTranscript := `{"artifactId":"` + wantID + `","sha256":"` + sha(material) + `","lineCount":2,"sentenceCount":2,"sizeBytes":` + strconv.Itoa(len(material)) + `,"language":"en","durationMs":421000,"sourceId":"` + sourceID + `","sourceSha256":"` + sourceSHA + `"}`
	if got := string(output.Ports["transcript"]); got != wantTranscript {
		t.Fatalf("transcript port = %s\nwant %s", got, wantTranscript)
	}
	if got := string(output.Ports["task"]); got != string(taskJSON) {
		t.Fatalf("task port = %s, want the task object unchanged", got)
	}
	// The assignment is the task object with the transcript reference added under
	// "transcript", in key order, so one Agent input port carries the requirement, the
	// goal and the material reference together.
	wantAssignment := `{"durationMs":421000,"goal":"DELIVER_CLIP","requirement":"a 30 s clip about the welcome","sourceId":"` + sourceID + `","sourceSha256":"` + sourceSHA + `","transcript":` + wantTranscript + `}`
	if got := string(output.Ports["assignment"]); got != wantAssignment {
		t.Fatalf("assignment port = %s\nwant %s", got, wantAssignment)
	}
	if len(output.Ports) != 3 {
		t.Fatalf("ports = %v, want transcript, task and assignment only", output.Ports)
	}
	if len(output.Artifacts) != 1 || output.Artifacts[0].ArtifactID != wantID || output.Artifacts[0].MediaType != domain.ArtifactMediaTypeTextPlain {
		t.Fatalf("artifacts = %+v, want the stored text artifact", output.Artifacts)
	}
	encoded, _ := json.Marshal(output.Ports)
	if strings.Contains(string(encoded), "Hello there") {
		t.Fatalf("the output carries transcript text: %s", encoded)
	}
}

// TestOnCallback_ResultNamesTheServedSource_PublishesSourceURLAndMediaType: a result that
// says where the source is served carries that into the transcript port, after the fixed
// members; a result without it (an older service) publishes the port unchanged.
func TestOnCallback_ResultNamesTheServedSource_PublishesSourceURLAndMediaType(t *testing.T) {
	executor := newExecutor(t, &fakeTasks{}, &fakeArtifacts{})
	state := asyncState(map[string]json.RawMessage{"task": taskJSON})
	payload := succeededPayload(map[string]any{"url": "https://media.example/v1/sources/" + sourceID + "/content", "mediaType": "video/webm"})
	output, err := executor.OnCallback(context.Background(), state, payload)
	if err != nil {
		t.Fatalf("OnCallback: %v", err)
	}
	var transcript mediatranscript.Transcript
	if err := json.Unmarshal(output.Ports["transcript"], &transcript); err != nil {
		t.Fatalf("decode transcript port: %v", err)
	}
	if transcript.SourceURL != "https://media.example/v1/sources/"+sourceID+"/content" || transcript.SourceMediaType != "video/webm" {
		t.Fatalf("transcript = %+v, want the served source URL and media type from the result", transcript)
	}
	if !strings.HasSuffix(string(output.Ports["transcript"]), `,"sourceSha256":"`+sourceSHA+`","sourceUrl":"https://media.example/v1/sources/`+sourceID+`/content","sourceMediaType":"video/webm"}`) {
		t.Fatalf("transcript port = %s, want the source URL and media type after the fixed members", output.Ports["transcript"])
	}

	output, err = executor.OnCallback(context.Background(), state, succeededPayload(nil))
	if err != nil {
		t.Fatalf("OnCallback (older result): %v", err)
	}
	if strings.Contains(string(output.Ports["transcript"]), "sourceUrl") || strings.Contains(string(output.Ports["transcript"]), "sourceMediaType") {
		t.Fatalf("transcript port = %s, want no source URL members when the result carries none", output.Ports["transcript"])
	}
}

func TestOnCallback_Failed_ReportsProviderFailure(t *testing.T) {
	executor := newExecutor(t, &fakeTasks{}, &fakeArtifacts{})
	for name, tc := range map[string]struct{ payload, code, message string }{
		"named":   {`{"status":"FAILED","error":{"code":"SOURCE_NOT_FOUND","message":"no such source"}}`, "SOURCE_NOT_FOUND", "no such source"},
		"unnamed": {`{"status":"FAILED"}`, "TRANSCRIPT_TASK_FAILED", "the media service reported the transcript task failed"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := executor.OnCallback(context.Background(), asyncState(map[string]json.RawMessage{"task": taskJSON}), []byte(tc.payload))
			var failure *registry.ProviderFailure
			if !errors.As(err, &failure) || failure.Err.Code != tc.code || failure.Err.Message != tc.message {
				t.Fatalf("OnCallback error = %v, want ProviderFailure %s: %s", err, tc.code, tc.message)
			}
		})
	}
}

func TestOnCallback_ResultOfAnotherSource_FailsWithSourceMismatch(t *testing.T) {
	otherSHA := strings.Repeat("ab", 32)
	for name, overrides := range map[string]map[string]any{
		"source id":     {"sourceId": "src_other"},
		"source hash":   {"sourceSha256": otherSHA},
		"duration far":  {"durationMs": 422500},
		"duration near": {"durationMs": 419000},
	} {
		t.Run(name, func(t *testing.T) {
			artifacts := &fakeArtifacts{}
			_, err := newExecutor(t, &fakeTasks{}, artifacts).OnCallback(context.Background(), asyncState(map[string]json.RawMessage{"task": taskJSON}), succeededPayload(overrides))
			var failure *registry.ProviderFailure
			if !errors.As(err, &failure) || failure.Err.Code != mediatranscript.CodeSourceMismatch {
				t.Fatalf("OnCallback error = %v, want ProviderFailure %s", err, mediatranscript.CodeSourceMismatch)
			}
			if strings.Contains(failure.Err.Message, otherSHA) || strings.Contains(failure.Err.Message, "Hello") {
				t.Fatalf("failure message quotes the result: %s", failure.Err.Message)
			}
			if len(artifacts.written) != 0 {
				t.Fatal("material of a mismatching result was stored")
			}
		})
	}
	// Within the tolerance the result is accepted.
	if _, err := newExecutor(t, &fakeTasks{}, &fakeArtifacts{}).OnCallback(context.Background(), asyncState(map[string]json.RawMessage{"task": taskJSON}), succeededPayload(map[string]any{"durationMs": 420100})); err != nil {
		t.Fatalf("a duration within the tolerance was refused: %v", err)
	}
}

func TestOnCallback_MaterialDoesNotMatchDigest_FailsWithMaterialMismatch(t *testing.T) {
	for name, overrides := range map[string]map[string]any{
		"digest":   {"transcriptSha256": strings.Repeat("cd", 32)},
		"empty":    {"material": "", "transcriptSha256": sha("")},
		"no lines": {"lineCount": 0},
	} {
		t.Run(name, func(t *testing.T) {
			artifacts := &fakeArtifacts{}
			_, err := newExecutor(t, &fakeTasks{}, artifacts).OnCallback(context.Background(), asyncState(map[string]json.RawMessage{"task": taskJSON}), succeededPayload(overrides))
			var failure *registry.ProviderFailure
			if !errors.As(err, &failure) || failure.Err.Code != mediatranscript.CodeMaterialMismatch {
				t.Fatalf("OnCallback error = %v, want ProviderFailure %s", err, mediatranscript.CodeMaterialMismatch)
			}
			if len(artifacts.written) != 0 {
				t.Fatal("untrusted material was stored")
			}
		})
	}
}

func TestOnCallback_UninterpretablePayload_PlainErrorStoresNothing(t *testing.T) {
	for name, payload := range map[string]string{
		"not json":       `not json`,
		"other status":   `{"status":"QUEUED"}`,
		"no result":      `{"status":"SUCCEEDED"}`,
		"result not obj": `{"status":"SUCCEEDED","result":"text"}`,
	} {
		t.Run(name, func(t *testing.T) {
			artifacts := &fakeArtifacts{}
			_, err := newExecutor(t, &fakeTasks{}, artifacts).OnCallback(context.Background(), asyncState(map[string]json.RawMessage{"task": taskJSON}), []byte(payload))
			var failure *registry.ProviderFailure
			if err == nil || errors.As(err, &failure) {
				t.Fatalf("OnCallback error = %v, want a plain error", err)
			}
			if len(artifacts.written) != 0 {
				t.Fatal("material was stored for an uninterpretable payload")
			}
		})
	}
}

func TestOnCallback_StorageFails_PlainError(t *testing.T) {
	_, err := newExecutor(t, &fakeTasks{}, &fakeArtifacts{err: errors.New("disk full")}).OnCallback(context.Background(), asyncState(map[string]json.RawMessage{"task": taskJSON}), succeededPayload(nil))
	var failure *registry.ProviderFailure
	if err == nil || errors.As(err, &failure) {
		t.Fatalf("OnCallback error = %v, want a plain error that leaves the NodeRun waiting", err)
	}
	if strings.Contains(err.Error(), "Hello") {
		t.Fatalf("the error quotes the material: %v", err)
	}
}

func TestPoll_Answers_NormalisedLikeTheCallback(t *testing.T) {
	state := asyncState(map[string]json.RawMessage{"task": taskJSON})
	succeeded := string(succeededPayload(nil))
	for name, tc := range map[string]struct {
		tasks      fakeTasks
		wantStatus registry.PollStatus
		wantErr    bool
		wantCode   string
	}{
		"running":           {tasks: fakeTasks{status: registry.PollRunning}, wantStatus: registry.PollRunning},
		"unknown":           {tasks: fakeTasks{status: registry.PollStatusUnknown}, wantStatus: registry.PollStatusUnknown},
		"unknown task":      {tasks: fakeTasks{status: registry.PollUnknownTask}, wantStatus: registry.PollStatusUnknown},
		"succeeded":         {tasks: fakeTasks{status: registry.PollSucceeded, body: succeeded}, wantStatus: registry.PollSucceeded},
		"failed":            {tasks: fakeTasks{status: registry.PollFailed, body: `{"status":"FAILED","error":{"code":"X","message":"y"}}`}, wantStatus: registry.PollFailed, wantCode: "X"},
		"status contradict": {tasks: fakeTasks{status: registry.PollFailed, body: succeeded}, wantStatus: registry.PollStatusUnknown},
		"mismatch":          {tasks: fakeTasks{status: registry.PollSucceeded, body: string(succeededPayload(map[string]any{"sourceId": "other"}))}, wantStatus: registry.PollFailed, wantCode: mediatranscript.CodeSourceMismatch},
		"no answer":         {tasks: fakeTasks{getErr: errors.New("no response")}, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			tasks := tc.tasks
			artifacts := &fakeArtifacts{}
			result, err := newExecutor(t, &tasks, artifacts).Poll(context.Background(), state)
			if tc.wantErr {
				if err == nil {
					t.Fatal("Poll succeeded, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Poll: %v", err)
			}
			if result.Status != tc.wantStatus {
				t.Fatalf("Poll status = %s, want %s", result.Status, tc.wantStatus)
			}
			if tc.wantCode != "" && (result.Error == nil || result.Error.Code != tc.wantCode) {
				t.Fatalf("Poll error = %+v, want code %s", result.Error, tc.wantCode)
			}
			if tc.wantStatus == registry.PollSucceeded {
				callbackOutput, err := newExecutor(t, &tasks, &fakeArtifacts{}).OnCallback(context.Background(), state, []byte(succeeded))
				if err != nil {
					t.Fatalf("OnCallback: %v", err)
				}
				if result.Output == nil || string(result.Output.Ports["transcript"]) != string(callbackOutput.Ports["transcript"]) {
					t.Fatalf("Poll output = %+v, want the callback's output", result.Output)
				}
			}
		})
	}
}
