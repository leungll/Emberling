// Package lookup implements the built-in `lookup` Tool of the MVP Tool Registry
// ("Read a deterministic record"). It is the synchronous
// Tool the Agent Loop is validated against: one registered operation, no I/O, no state of
// its own, and a result derived only from the arguments it is given.
//
// It owns no retry, timeout, callback routing, state transition or Event:
// a failed lookup is reported upward as an error and the
// Agent Runtime decides what happens next.
package lookup

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

// ToolName is this Tool's stable Registry name. It is what a Definition stores in an
// Agent's Tool allowlist.
const ToolName = "lookup"

// MissingKey is the one key for which this Tool fails deterministically. Tool failure
// tests need a failing call that is still a real, registered Tool call (the TOOL_ERROR
// path of the Agent termination table), and a scripted key gives them one without a
// second registration or an injected fault.
const MissingKey = "missing"

// inputSchema is the sole contract for this Tool's arguments; the Runtime validates a
// committed TOOL_CALL Decision's arguments against it before the Tool is called.
const inputSchema = `{
  "type": "object",
  "properties": {
    "key": {"type": "string", "minLength": 1}
  },
  "required": ["key"],
  "additionalProperties": false
}`

// outputSchema is the sole contract for this Tool's result.
const outputSchema = `{
  "type": "object",
  "properties": {
    "key": {"type": "string"},
    "record": {"type": "string"}
  },
  "required": ["key", "record"],
  "additionalProperties": false
}`

// Registration returns the `lookup` ToolRegistration. The Tool reads nothing outside the
// process, so it has no external side effect and is always safe to repeat.
func Registration() registry.ToolRegistration {
	return registry.ToolRegistration{
		Metadata: domain.ToolMetadata{
			Name:          ToolName,
			Description:   "Read a deterministic record",
			InputSchema:   json.RawMessage(inputSchema),
			OutputSchema:  json.RawMessage(outputSchema),
			SideEffect:    domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
			ExecutionKind: domain.ToolExecutionSync,
		},
		Executor: Executor{},
	}
}

// Executor implements registry.ToolExecutor for the `lookup` Tool.
type Executor struct{}

// arguments is the decoded, schema-validated input of one lookup call.
type arguments struct {
	Key string `json:"key"`
}

// record is the Tool's result. The field order here is the result's key order, so the same
// arguments always produce the same bytes.
type record struct {
	Key    string `json:"key"`
	Record string `json:"record"`
}

// Execute reads the record named by `key` and returns it. Being a SYNC Tool it always
// returns a completed result or an error; it never dispatches and never retries.
func (Executor) Execute(_ context.Context, action registry.ToolAction) (registry.ToolExecutionResult, error) {
	var args arguments
	if err := json.Unmarshal(action.Arguments, &args); err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: arguments are not a JSON object: %w", ToolName, err)
	}
	if args.Key == "" {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: key is empty", ToolName)
	}
	if args.Key == MissingKey {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: no record for key %q", ToolName, args.Key)
	}

	output, err := json.Marshal(record{Key: args.Key, Record: "record for " + args.Key})
	if err != nil {
		return registry.ToolExecutionResult{}, fmt.Errorf("%s: encode result for key %q: %w", ToolName, args.Key, err)
	}
	return registry.ToolExecutionResult{
		Kind:   registry.ToolResultCompleted,
		Result: &registry.ToolResult{Output: output},
	}, nil
}
