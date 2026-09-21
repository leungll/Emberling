// Package registry resolves the stable identifiers a Definition or an Agent Action
// stores - Node Type, Model ID and Tool Name - to the registered metadata and the
// implementation the Runtime calls after COMMIT (07 §0).
//
// A registration describes a capability; it never owns NodeRun state, Attempts, retry,
// timeout or Events. This package therefore depends on domain and a JSON Schema compiler
// only: it holds no database handle, no HTTP client and no Provider credential.
package registry

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// NodeRegistration is one registered Node Type (07 §1.1). Metadata lets Studio and the
// Compiler understand the node; Binding lets the Runtime execute it.
type NodeRegistration struct {
	Metadata domain.NodeMetadata
	Binding  NodeBinding
}

// NodeBindingKind names the execution path a Binding selects. The Runtime reads it from
// the Binding, never from a Go type assertion on an execution result.
type NodeBindingKind string

const (
	// BindingExecutor routes to a plain NodeExecutor.
	BindingExecutor NodeBindingKind = "EXECUTOR"
	// BindingManagedAgent routes to the built-in Agent Runtime.
	BindingManagedAgent NodeBindingKind = "MANAGED_AGENT"
)

// NodeBinding tells the Runtime which execution path a registration uses.
type NodeBinding interface {
	Kind() NodeBindingKind
}

// ExecutorBinding carries the Executor of a SYNC or ASYNC node.
type ExecutorBinding struct {
	Executor NodeExecutor
}

func (ExecutorBinding) Kind() NodeBindingKind { return BindingExecutor }

// ManagedAgentBinding routes a node to the built-in Agent Runtime. RuntimeKey is fixed to
// the built-in Runtime in the MVP; it exists so the Binding names its target instead of
// leaving the Runtime to infer one.
type ManagedAgentBinding struct {
	RuntimeKey string
}

func (ManagedAgentBinding) Kind() NodeBindingKind { return BindingManagedAgent }

// NodeExecutor performs one registered operation. It does not retry, time out, write
// Runtime state or emit Events.
type NodeExecutor interface {
	// ValidateSemantics runs after ConfigSchema validation has already passed. It only
	// checks cross-field constraints and capability compatibility that JSON Schema
	// cannot express; it must not maintain a second set of type, required-ness or
	// default rules (07 §1.1).
	ValidateSemantics(ctx context.Context, config map[string]any) error
	// Execute returns a final result, or dispatch information for an ASYNC node.
	Execute(ctx context.Context, input NodeInput, config map[string]any) (NodeResult, error)
}

// AsyncNodeExecutor converts a Provider callback payload into the Node output. The
// Runtime restores the original Attempt before calling it; the Executor keeps no NodeRun
// state of its own (07 §1.3).
type AsyncNodeExecutor interface {
	NodeExecutor
	// OnCallback returns a ProviderFailure when the Provider reports that the external
	// task itself failed, and a plain error when the payload could not be interpreted:
	// the first fails the Attempt, the second leaves the NodeRun WAITING_CALLBACK
	// (06 §1.6).
	OnCallback(ctx context.Context, state NodeAsyncState, payload []byte) (NodeOutput, error)
}

// PollableAsyncNodeExecutor is optional. Poll and callback enter the same idempotent
// resume use case; Poll never bypasses the Callback Binding.
type PollableAsyncNodeExecutor interface {
	AsyncNodeExecutor
	Poll(ctx context.Context, state NodeAsyncState) (PollResult, error)
}

// CallbackContext is the Attempt-scoped callback credential the Runtime generates before
// dispatch (08 §4). Token is the one-time plaintext handed to the Provider: only its hash
// is persisted, and it must never reach an Event, Trace, log or error message.
type CallbackContext struct {
	URL   string
	Token string
}

// NodeInput is the execution input of one Node Attempt. Ports carries upstream values
// keyed by input port name; RunInput carries the Run input object an Input Node reads by
// its frozen inputKey (08 §1.1). Both hold complete logical values, not Trace
// projections.
type NodeInput struct {
	RunID     string
	NodeRunID string
	AttemptNo int

	Ports    map[string]json.RawMessage
	RunInput json.RawMessage

	// Callback is set only for an ASYNC dispatch.
	Callback *CallbackContext

	// IdempotencyKey is set by the Execution Service only for a node whose registered
	// SideEffectPolicy is EXTERNAL + KEYED, and is empty for every other node. The
	// service derives it from the NodeRun ID, so it is stable across every Attempt of
	// that NodeRun: an Adapter must send exactly this value as the Provider's
	// idempotency key and must never generate one of its own (07 §1.2).
	IdempotencyKey string
}

// Port returns one input port value and whether the port was supplied at all. An absent
// port and a port carrying JSON null are different facts, so a caller can tell "no value"
// from "an explicit null".
func (in NodeInput) Port(name string) (json.RawMessage, bool) {
	raw, ok := in.Ports[name]
	return raw, ok
}

// NodeResultKind distinguishes a finished call from a dispatch. The Runtime still selects
// its execution path from the registered ExecutionKind, not from this value.
type NodeResultKind string

const (
	NodeResultCompleted  NodeResultKind = "COMPLETED"
	NodeResultDispatched NodeResultKind = "DISPATCHED"
)

// NodeResult is the outcome of one Execute call. A SYNC node returns COMPLETED with
// Output; an ASYNC node returns DISPATCHED with ExternalTask.
type NodeResult struct {
	Kind         NodeResultKind
	Output       *NodeOutput
	ExternalTask *ExternalTask
	// TokenUsage is set only by a model-calling node, and only when the Provider
	// reported usage.
	TokenUsage *domain.TokenUsage
}

// NodeOutput is the complete logical result of a node, keyed by output port name. An
// Output Node has no output ports and still returns its logical result here; the service
// copies that object into Run.output (05 §1.3).
type NodeOutput struct {
	Ports map[string]json.RawMessage
}

// CompletedResult is the common SYNC success shape.
func CompletedResult(ports map[string]json.RawMessage) NodeResult {
	return NodeResult{Kind: NodeResultCompleted, Output: &NodeOutput{Ports: ports}}
}

// ExternalTask is the identity of one accepted external task (07 §1.3). ProviderID is a
// stable Provider identifier, never a credential, endpoint or internal configuration.
type ExternalTask struct {
	ProviderID     string
	ExternalTaskID string
}

// NodeAsyncState is what the Runtime restores from committed facts before asking an
// Executor to interpret a callback or poll an external task. It carries identity, the
// frozen node config and the external task; it never carries the plaintext callback
// token.
type NodeAsyncState struct {
	RunID        string
	NodeRunID    string
	AttemptNo    int
	ExternalTask ExternalTask
	Config       map[string]any
}

// PollStatus is the normalised result of one optional Provider status query.
type PollStatus string

const (
	PollRunning   PollStatus = "RUNNING"
	PollSucceeded PollStatus = "SUCCEEDED"
	PollFailed    PollStatus = "FAILED"
)

// PollResult is shared by the optional pollable Node and Tool Executors: Output is set
// for a Node, ToolResult for a Tool, and Error for a failure the Provider reported. A
// Poll never advances state by itself; the Runtime still decides the single winner.
//
// Error carries the same fact as ProviderFailure.Err: a Poll reports a Provider-reported
// failure through Status FAILED plus Error, while OnCallback has only a return error and
// therefore reports it as a ProviderFailure.
type PollResult struct {
	Status     PollStatus
	Output     *NodeOutput
	ToolResult *ToolResult
	Error      *domain.ExecutionError
}

// ProviderFailure is returned by OnCallback - and carried by PollResult.Error in the Poll
// path - when the Provider reports that the external task itself failed. It is distinct
// from a payload parse error: a parse error leaves the NodeRun WAITING_CALLBACK, while a
// ProviderFailure fails the Attempt with FailureSource CALLBACK or PROVIDER_POLL
// (06 §1.6, §2.2). Callers identify it with errors.As, so an Executor may wrap it.
type ProviderFailure struct {
	Err domain.ExecutionError
}

func (f *ProviderFailure) Error() string {
	return fmt.Sprintf("provider reported task failure: %s: %s", f.Err.Code, f.Err.Message)
}
