// Package externaltool turns one declaration into a complete asynchronous Tool that
// dispatches a task to an external task service, interprets the service's callback and
// answers status polls. A new Tool of this kind supplies its Metadata and two mappings,
// from the Agent's arguments to the task input and from the task result to the Tool
// result; dispatch, callback decoding and polling are written once here.
//
// The task protocol is reached through the narrow TaskClient port. An Adapter implements
// it over HTTP and the process entry point injects it, so this package imports no Adapter
// and no transport. The service reports a finished task twice: once as a callback whose
// payload is {"status": "SUCCEEDED", "result": {...}} or {"status": "FAILED", "error":
// {"code": ..., "message": ...}}, and once more as the answer to a status query, which
// carries the same members. Both are interpreted by one function, so a callback and a
// poll of the same task produce identical results and failures.
//
// A check that does not pass is a successful Tool call whose result says so; only a task
// the service reports FAILED is a Provider failure. Every Tool declared here has an
// EXTERNAL side effect that is SAFE to repeat, so a task the service lost is handed back to
// the Agent as interrupted work instead of being resent by the Runtime.
//
// The one-time plaintext callback token travels to the service through Submit and nowhere
// else: this package logs nothing, and its errors carry only the Tool Name, the external
// task id, a short operation label and whatever the TaskClient reports about its own
// request, never the token, the callback URL, the task input or a response body.
package externaltool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// DefaultProviderID is the stable Provider identifier a Spec that names none records in
// its Callback Bindings. It identifies the task service in Trace; it is not a credential.
const DefaultProviderID = "external-task-service"

// Spec declares one external Tool: its registered Metadata, the task type the service
// runs for it, and the two mappings. Everything else - the task id, dispatch, callback
// decoding and polling - is supplied by Registration.
type Spec struct {
	// Metadata is the Tool's registration contract. ExecutionKind and SideEffect may be
	// left zero; Registration sets them to ASYNC and EXTERNAL/SAFE, which every external
	// Tool has, and rejects any other value. Poll must be declared: the Runtime's status
	// query is how a task the service lost is detected.
	Metadata domain.ToolMetadata
	// ProviderID is the stable Provider identifier recorded in the Callback Binding of
	// every dispatch; empty means DefaultProviderID.
	ProviderID string
	// TaskType names the task the service runs, sent verbatim with every dispatch.
	TaskType string
	// MapInput builds the task input from the committed Action. The arguments already
	// passed the Tool's InputSchema. An error means nothing is dispatched.
	MapInput func(action registry.ToolAction) (json.RawMessage, error)
	// MapResult builds the Tool result from the result object of a SUCCEEDED task. The
	// Runtime validates the returned Output against the Tool's OutputSchema afterwards;
	// a mapping must not swallow a result it cannot represent, so that violation fails the
	// Attempt with its Trace. An error here means the payload could not be interpreted:
	// nothing is written and the Action keeps waiting for a later delivery.
	MapResult func(ctx context.Context, result json.RawMessage) (registry.ToolResult, error)
}

// TaskClient is the task protocol a Tool dispatches through. An Adapter implements it over
// the service's HTTP API and the process entry point injects it; an asynchronous node that
// needs the same service declares its own interface with this method set and receives the
// same implementation.
//
// Submit sends exactly one task and never retries. A task the service already holds under
// the same id is a success. Get sends exactly one status query: RUNNING for a task the
// service still holds (queued or running), SUCCEEDED or FAILED with the terminal payload
// the callback also carries, UNKNOWN_TASK when the service answers definitely that it does
// not know the task, and UNKNOWN when the answer could not be interpreted. An error means
// no answer arrived at all.
type TaskClient interface {
	Submit(ctx context.Context, taskID, taskType string, input json.RawMessage, callback registry.CallbackContext) error
	Get(ctx context.Context, taskID string) (registry.PollStatus, json.RawMessage, error)
}

// externalSideEffect is the one side-effect policy an external Tool has: the task service
// does work outside Emberling, and running the same task again is safe, so a lost task may
// be handed back to the Agent to repeat.
var externalSideEffect = domain.SideEffectPolicy{Kind: domain.SideEffectExternal, Idempotency: domain.IdempotencySafe}

// Registration returns the complete ToolRegistration for spec, dispatching through client.
// It fails when the declaration is incomplete or contradicts what every external Tool is:
// a missing task type, mapping or poll policy, or a Metadata that declares another
// execution kind or side-effect policy. A failed Registration registers nothing.
func Registration(spec Spec, client TaskClient) (registry.ToolRegistration, error) {
	if err := validateSpec(spec); err != nil {
		return registry.ToolRegistration{}, fmt.Errorf("externaltool: tool %q: %w", spec.Metadata.Name, err)
	}
	if client == nil {
		return registry.ToolRegistration{}, fmt.Errorf("externaltool: tool %q: task client is nil", spec.Metadata.Name)
	}
	metadata := spec.Metadata
	metadata.ExecutionKind = domain.ToolExecutionAsync
	metadata.SideEffect = externalSideEffect
	providerID := spec.ProviderID
	if providerID == "" {
		providerID = DefaultProviderID
	}
	return registry.ToolRegistration{
		Metadata: metadata,
		Executor: &Executor{
			toolName:   metadata.Name,
			providerID: providerID,
			taskType:   spec.TaskType,
			mapInput:   spec.MapInput,
			mapResult:  spec.MapResult,
			client:     client,
		},
	}, nil
}

func validateSpec(spec Spec) error {
	var errs []error
	if spec.Metadata.Name == "" {
		errs = append(errs, errors.New("metadata names no tool"))
	}
	if spec.TaskType == "" {
		errs = append(errs, errors.New("task type is empty"))
	}
	if spec.MapInput == nil {
		errs = append(errs, errors.New("MapInput is nil"))
	}
	if spec.MapResult == nil {
		errs = append(errs, errors.New("MapResult is nil"))
	}
	if spec.Metadata.Poll == nil {
		errs = append(errs, errors.New("metadata declares no poll policy; an external tool is polled so a lost task is detected"))
	}
	if kind := spec.Metadata.ExecutionKind; kind != "" && kind != domain.ToolExecutionAsync {
		errs = append(errs, fmt.Errorf("metadata declares executionKind %q; an external tool is always ASYNC", kind))
	}
	if side := spec.Metadata.SideEffect; side != (domain.SideEffectPolicy{}) && side != externalSideEffect {
		errs = append(errs, fmt.Errorf("metadata declares side effect %s/%s; an external tool is always %s/%s",
			side.Kind, side.Idempotency, externalSideEffect.Kind, externalSideEffect.Idempotency))
	}
	return errors.Join(errs...)
}
