// Package registry resolves the stable identifiers a Definition or an Agent Action
// stores - Node Type, Model ID and Tool Name - to the registered metadata and the
// implementation the Runtime calls after COMMIT.
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

// NodeRegistration is one registered Node Type. Metadata lets Studio and the
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
	// default rules.
	ValidateSemantics(ctx context.Context, config map[string]any) error
	// Execute returns a final result, or dispatch information for an ASYNC node.
	Execute(ctx context.Context, input NodeInput, config map[string]any) (NodeResult, error)
}

// AsyncNodeExecutor converts a Provider callback payload into the Node output. The
// Runtime restores the original Attempt before calling it; the Executor keeps no NodeRun
// state of its own.
type AsyncNodeExecutor interface {
	NodeExecutor
	// OnCallback returns a ProviderFailure when the Provider reports that the external
	// task itself failed, and a plain error when the payload could not be interpreted:
	// the first fails the Attempt, the second leaves the NodeRun WAITING_CALLBACK.
	OnCallback(ctx context.Context, state NodeAsyncState, payload []byte) (NodeOutput, error)
}

// PollableAsyncNodeExecutor is optional. Poll and callback enter the same idempotent
// resume use case; Poll never bypasses the Callback Binding.
type PollableAsyncNodeExecutor interface {
	AsyncNodeExecutor
	Poll(ctx context.Context, state NodeAsyncState) (PollResult, error)
}

// CallbackContext is the Attempt-scoped callback credential the Runtime generates before
// dispatch. Token is the one-time plaintext handed to the Provider: only its hash
// is persisted, and it must never reach an Event, Trace, log or error message.
type CallbackContext struct {
	URL   string
	Token string
}

// NodeInput is the execution input of one Node Attempt. Ports carries upstream values
// keyed by input port name; RunInput carries the Run input object an Input Node reads by
// its frozen inputKey. Both hold complete logical values, not Trace
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
	// idempotency key and must never generate one of its own.
	IdempotencyKey string

	// Facts is set only for a Node Type whose registered Metadata declares FactInputs: it
	// holds this Run's committed execution facts of each declared type, keyed by fact
	// type, read in the transaction that started the Attempt. Every declared type has an
	// entry, empty when the Run has no such fact. A node checks these facts; it never
	// queries execution facts itself.
	Facts map[string]FactSet
}

// FactSet is the committed execution facts of one type in one Run, oldest first
// (creation time, then id). The read is bounded: Truncated reports that the Run holds
// more facts of the type than were delivered, so a node checking the whole set must not
// treat Facts as complete.
type FactSet struct {
	Facts     []domain.ExecutionFact
	Truncated bool
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
// copies that object into Run.output.
type NodeOutput struct {
	Ports map[string]json.RawMessage
	// Artifacts declares the Execution Artifacts whose content the node already wrote to
	// content storage, with the same meaning as ToolResult.Artifacts: the Runtime commits
	// their metadata and their link to the Node Attempt in the transaction that commits
	// the output, so an output that never commits leaves only unreferenced content
	// behind. A node that produces no artifact leaves it nil.
	Artifacts []domain.ArtifactRef
	// Cost is the charge the Provider reported for the external call that produced this
	// output, or nil when it reported none. The completion transaction records it on the
	// Attempt beside the result; it takes no part in any execution decision.
	Cost *domain.Cost
}

// CompletedResult is the common SYNC success shape.
func CompletedResult(ports map[string]json.RawMessage) NodeResult {
	return NodeResult{Kind: NodeResultCompleted, Output: &NodeOutput{Ports: ports}}
}

// ExternalTask is the identity of one accepted external task. ProviderID is a
// stable Provider identifier, never a credential, endpoint or internal configuration.
type ExternalTask struct {
	ProviderID     string
	ExternalTaskID string
	// Replayed reports that the Provider answered with a task it had already accepted
	// under the same idempotency key instead of creating a new one. It is set only by an
	// Adapter whose Provider tells the two answers apart, and it is recorded, never used
	// to route a callback.
	Replayed bool
}

// NodeAsyncState is what the Runtime restores from committed facts before asking an
// Executor to interpret a callback or poll an external task. It carries identity, the
// frozen node config, the input ports the Attempt was dispatched with and the external
// task; it never carries the plaintext callback token.
type NodeAsyncState struct {
	RunID        string
	NodeRunID    string
	AttemptNo    int
	ExternalTask ExternalTask
	Config       map[string]any
	// Ports are the input port values the Attempt was dispatched with, restored from the
	// Attempt's immutable input, so an Executor can check a Provider's result against
	// what it asked for without reading anything itself.
	Ports map[string]json.RawMessage
}

// PollStatus is the normalised result of one optional Provider status query.
type PollStatus string

const (
	PollRunning   PollStatus = "RUNNING"
	PollSucceeded PollStatus = "SUCCEEDED"
	PollFailed    PollStatus = "FAILED"
	// PollStatusUnknown reports a query that produced no interpretable task state: the
	// Provider answered a non-2xx status or sent a body that cannot be read, so whether it
	// still knows the task is itself unknown. It changes no Attempt state and is not a
	// failure; a later callback, poll or the Attempt deadline still decides the outcome.
	PollStatusUnknown PollStatus = "UNKNOWN"
	// PollUnknownTask reports a definite answer that the Provider does not know the task
	// it was asked about: the task was never recorded or the Provider lost it, as after a
	// restart. Only a Tool Poll returns it. The Runtime decides from the Tool's registered
	// side-effect policy whether the lost call is handed back to the Agent as safe to
	// repeat or kept waiting for a later answer; the Executor never re-dispatches.
	PollUnknownTask PollStatus = "UNKNOWN_TASK"
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
// ProviderFailure fails the Attempt with FailureSource CALLBACK or PROVIDER_POLL.
// Callers identify it with errors.As, so an Executor may wrap it.
type ProviderFailure struct {
	Err domain.ExecutionError
}

func (f *ProviderFailure) Error() string {
	return fmt.Sprintf("provider reported task failure: %s: %s", f.Err.Code, f.Err.Message)
}
