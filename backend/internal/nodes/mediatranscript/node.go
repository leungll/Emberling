// Package mediatranscript implements the asynchronous Media Transcript Node. It is the
// fixed first step of a video-editing Run: it reads the task object a Task Input node
// published, dispatches one TRANSCRIBE task for the source video to the media task
// service and, once the service reports the task finished, writes the transcript text to
// Execution Artifact storage and publishes a reference to it. The Agent downstream works
// from that reference; the transcript text itself never enters a node output, an Event,
// Trace or a log.
//
// The service is reached through the narrow TaskClient port, which has the same method
// set as the port the external Tools dispatch through, so the process entry point injects
// one implementation for both. Dispatch, callback decoding and polling follow the same
// task protocol: a finished task is reported once as a callback payload and again as the
// answer to a status query, both {"status": "SUCCEEDED", "result": {...}} or
// {"status": "FAILED", "error": {"code": ..., "message": ...}}, and one function
// interprets both.
//
// The node owns no NodeRun state, retry, timeout, callback routing or Event. Its side
// effect is EXTERNAL and SAFE to repeat: transcribing the same source twice yields the
// same material, so repeating the task would be harmless. A lost task is not detected by
// this node: a status query the service answers with "unknown task" is reported as an
// answer that resolves nothing, and the Attempt's own deadline ends the wait. The Runtime
// does not open a new Attempt for a failure that comes after dispatch, so a lost task ends
// the NodeRun; re-dispatching it is a known gap, not something this node promises.
package mediatranscript

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// NodeType is the stable Node Type a Definition names.
const NodeType = "media_transcript"

// TaskType is the task the media service runs for this node.
const TaskType = "TRANSCRIBE"

// DefaultProviderID is the stable Provider identifier recorded in every Callback Binding
// when the registration names none. It identifies the media service in Trace and is not a
// credential.
const DefaultProviderID = "media-service"

// Port names. The input port takes the task object a Task Input node published; the
// transcript output carries the material reference and its structure, and the task
// output republishes the task object byte for byte so the Agent can read the requirement
// and the goal beside the material.
const (
	TaskPort       = "task"
	TranscriptPort = "transcript"
)

// pollIntervalMs and maxPolls bound the Runtime's status queries for one Attempt: one
// query every five seconds, at most 120 of them, so a task the service lost stops being
// polled after ten minutes. Polling never extends the Attempt deadline, which is what
// ends a task that neither a poll nor a callback resolved; a Definition sets that
// deadline through the node's execution policy.
const (
	pollIntervalMs = 5000
	maxPolls       = 120
)

// configSchema is empty: the task object carries everything the node needs.
const configSchema = `{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`

// TaskClient is the task protocol the node dispatches through. Submit sends exactly one
// task and never retries; a task the service already holds under the same id is a
// success. Get sends exactly one status query and answers RUNNING for a task the service
// still holds, SUCCEEDED or FAILED with the terminal body the callback also carries,
// UNKNOWN_TASK when the service definitely does not know the task and UNKNOWN when the
// answer could not be interpreted. An error means no answer arrived at all.
type TaskClient interface {
	Submit(ctx context.Context, taskID, taskType string, input json.RawMessage, callback registry.CallbackContext) error
	Get(ctx context.Context, taskID string) (registry.PollStatus, json.RawMessage, error)
}

// ArtifactWriter stores one text Execution Artifact and returns its content-addressed
// reference. The node writes the transcript material through it before declaring the
// artifact with its output; the Runtime commits the metadata with the output.
type ArtifactWriter interface {
	WriteText(ctx context.Context, r io.Reader) (domain.ArtifactRef, error)
}

// Ports are the two dependencies the node is registered with, plus the Provider ID its
// external task identities carry; an empty ProviderID means DefaultProviderID.
type Ports struct {
	Tasks      TaskClient
	Artifacts  ArtifactWriter
	ProviderID string
}

// Registration returns the Media Transcript NodeRegistration over ports. A missing task
// client or artifact writer is a wiring error of the process, reported here so nothing
// is registered that could dispatch a task it cannot finish.
func Registration(ports Ports) (registry.NodeRegistration, error) {
	if ports.Tasks == nil {
		return registry.NodeRegistration{}, errors.New("media_transcript: task client is nil")
	}
	if ports.Artifacts == nil {
		return registry.NodeRegistration{}, errors.New("media_transcript: artifact writer is nil")
	}
	providerID := ports.ProviderID
	if providerID == "" {
		providerID = DefaultProviderID
	}
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          NodeType,
			DisplayName:   "Media Transcript",
			Category:      domain.NodeCategoryPromptAndModel,
			ExecutionKind: domain.NodeExecutionAsync,
			Inputs:        []domain.PortMetadata{{Name: TaskPort, DataType: domain.PortTypeJSON, Required: true}},
			Outputs: []domain.PortMetadata{
				{Name: TranscriptPort, DataType: domain.PortTypeJSON, Required: true},
				{Name: TaskPort, DataType: domain.PortTypeJSON, Required: true},
			},
			ConfigSchema: json.RawMessage(configSchema),
			// Transcription is deterministic for one source, so repeating the task would be
			// safe and needs no Provider idempotency key.
			SideEffect: domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencySafe},
			Poll:       &domain.PollPolicy{IntervalMs: pollIntervalMs, MaxPolls: maxPolls},
			// No DispatchModelPath: the node calls no model and is not governed by
			// Provider capacity.
		},
		Binding: registry.ExecutorBinding{Executor: &Executor{
			tasks: ports.Tasks, artifacts: ports.Artifacts, providerID: providerID,
		}},
	}, nil
}

// Executor implements registry.PollableAsyncNodeExecutor for the Media Transcript Node.
type Executor struct {
	tasks      TaskClient
	artifacts  ArtifactWriter
	providerID string
}

// ValidateSemantics has nothing to check: the config is empty.
func (*Executor) ValidateSemantics(context.Context, map[string]any) error { return nil }

// taskObject is the part of the Run's task object this node reads. The whole object is
// republished unchanged; these members identify the source the transcript must belong
// to. DurationMs is a pointer so an absent member is told apart from zero.
type taskObject struct {
	SourceID     string `json:"sourceId"`
	SourceSHA256 string `json:"sourceSha256"`
	DurationMs   *int64 `json:"durationMs"`
}

// readTask decodes the task port value and checks the members the dispatch and the
// result verification rely on. Errors name the rule, never the value.
func readTask(ports map[string]json.RawMessage) (taskObject, json.RawMessage, error) {
	raw, present := ports[TaskPort]
	if !present || len(strings.TrimSpace(string(raw))) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return taskObject{}, nil, fmt.Errorf("media_transcript: required input port %q is missing", TaskPort)
	}
	var task taskObject
	if err := json.Unmarshal(raw, &task); err != nil {
		return taskObject{}, nil, fmt.Errorf("media_transcript: input port %q is not a task object", TaskPort)
	}
	switch {
	case strings.TrimSpace(task.SourceID) == "":
		return taskObject{}, nil, errors.New("media_transcript: the task names no sourceId")
	case !isSHA256Hex(task.SourceSHA256):
		return taskObject{}, nil, errors.New("media_transcript: the task's sourceSha256 is not 64 lowercase hex characters")
	case task.DurationMs == nil || *task.DurationMs < 0:
		return taskObject{}, nil, errors.New("media_transcript: the task's durationMs is missing or negative")
	}
	return task, raw, nil
}

// transcribeInput is the TRANSCRIBE task input: the source the service transcribes.
type transcribeInput struct {
	SourceID string `json:"sourceId"`
}

// Execute dispatches one TRANSCRIBE task and returns its external identity. The id is
// computed from the NodeRun and Attempt before anything is sent, so it is known even when
// the service's answer is lost and a retry Attempt never reuses it. Being an ASYNC node
// it returns DISPATCHED or an explicit error, never a completed result.
func (e *Executor) Execute(ctx context.Context, input registry.NodeInput, _ map[string]any) (registry.NodeResult, error) {
	if input.Callback == nil || strings.TrimSpace(input.Callback.URL) == "" || strings.TrimSpace(input.Callback.Token) == "" {
		// Nothing has been sent, so no task can exist.
		return registry.NodeResult{}, errors.New("media_transcript: asynchronous dispatch requires a callback context")
	}
	if input.NodeRunID == "" || input.AttemptNo <= 0 {
		return registry.NodeResult{}, errors.New("media_transcript: the call names no node run id and attempt number")
	}
	task, _, err := readTask(input.Ports)
	if err != nil {
		return registry.NodeResult{}, err
	}
	taskInput, err := json.Marshal(transcribeInput{SourceID: task.SourceID})
	if err != nil {
		return registry.NodeResult{}, fmt.Errorf("media_transcript: encode task input: %w", err)
	}
	taskID := TaskID(input.NodeRunID, input.AttemptNo)
	if err := e.tasks.Submit(ctx, taskID, TaskType, taskInput, *input.Callback); err != nil {
		// Wrapped, not replaced: the client's error may carry the uncertainty
		// classification the Execution Service reads.
		return registry.NodeResult{}, fmt.Errorf("media_transcript: dispatch task %s to provider %q: %w", taskID, e.providerID, err)
	}
	return registry.NodeResult{
		Kind:         registry.NodeResultDispatched,
		ExternalTask: &registry.ExternalTask{ProviderID: e.providerID, ExternalTaskID: taskID},
	}, nil
}

// OnCallback converts one callback payload into the node's output. A task the service
// reports FAILED, and a result that does not belong to the dispatched source, become a
// *registry.ProviderFailure, which fails the Attempt; a payload this node cannot
// interpret, or material it could not store, stays a plain error, which leaves the
// NodeRun waiting for a later delivery or the Attempt deadline.
func (e *Executor) OnCallback(ctx context.Context, state registry.NodeAsyncState, payload []byte) (registry.NodeOutput, error) {
	var body terminalPayload
	if err := json.Unmarshal(payload, &body); err != nil {
		return registry.NodeOutput{}, fmt.Errorf("media_transcript: callback payload for task %s is not a JSON object", state.ExternalTask.ExternalTaskID)
	}
	return e.interpret(ctx, state, body)
}

// Poll asks the service for the task's status once and normalises the answer. A finished
// task carries the same payload its callback would and is interpreted by the same rules.
// An answer the client could not classify, a terminal body whose own status contradicts
// the polled status and a service that no longer knows the task all resolve nothing: the
// node cannot hand a lost task back to anyone, so the Attempt deadline decides and the
// NodeRun fails with it. A query that got no answer is an error,
// which the Runtime reports and the already scheduled next poll follows.
func (e *Executor) Poll(ctx context.Context, state registry.NodeAsyncState) (registry.PollResult, error) {
	taskID := state.ExternalTask.ExternalTaskID
	status, payload, err := e.tasks.Get(ctx, taskID)
	if err != nil {
		return registry.PollResult{}, fmt.Errorf("media_transcript: query task %s: %w", taskID, err)
	}
	switch status {
	case registry.PollSucceeded, registry.PollFailed:
		var body terminalPayload
		if err := json.Unmarshal(payload, &body); err != nil || body.Status != string(status) {
			return registry.PollResult{Status: registry.PollStatusUnknown}, nil
		}
		output, err := e.interpret(ctx, state, body)
		var failure *registry.ProviderFailure
		switch {
		case errors.As(err, &failure):
			reported := failure.Err
			return registry.PollResult{Status: registry.PollFailed, Error: &reported}, nil
		case err != nil:
			return registry.PollResult{}, err
		}
		return registry.PollResult{Status: registry.PollSucceeded, Output: &output}, nil
	case registry.PollRunning:
		return registry.PollResult{Status: registry.PollRunning}, nil
	default:
		// UNKNOWN, UNKNOWN_TASK or a status this node does not know: nothing can be
		// concluded here.
		return registry.PollResult{Status: registry.PollStatusUnknown}, nil
	}
}
