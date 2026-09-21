package runtime

import (
	"errors"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// ValidationError is one structural, semantic, config or instance violation found while
// compiling a Definition or validating Run input. Code is a stable, machine-readable
// identifier (docs/08-interface-spec.md §6); Path locates the violation inside the
// Definition (e.g. "nodes[2].config") or inside the instance being validated.
type ValidationError struct {
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	NodeID  string `json:"nodeId,omitempty"`
	Message string `json:"message"`
}

func (e ValidationError) Error() string {
	if e.NodeID != "" {
		return fmt.Sprintf("%s: %s (node=%s, path=%s)", e.Code, e.Message, e.NodeID, e.Path)
	}
	return fmt.Sprintf("%s: %s (path=%s)", e.Code, e.Message, e.Path)
}

// Structural/semantic error codes (HTTP 422, docs/08-interface-spec.md §6: "Definition
// 语义无效"). These describe an invalid graph shape, not an invalid config value.
const (
	CodeDAGHasCycle             = "DAG_HAS_CYCLE"
	CodeIncompatibleEdge        = "INCOMPATIBLE_EDGE"
	CodeUnknownNodeType         = "UNKNOWN_NODE_TYPE"
	CodeUnknownPort             = "UNKNOWN_PORT"
	CodeMissingRequiredInput    = "MISSING_REQUIRED_INPUT"
	CodeAmbiguousInput          = "AMBIGUOUS_INPUT"
	CodeOutputNodeCount         = "OUTPUT_NODE_COUNT"
	CodeOutputNodeNotUniqueSink = "OUTPUT_NODE_NOT_UNIQUE_SINK"
	CodeNodeUnreachableToOutput = "NODE_UNREACHABLE_TO_OUTPUT"
	CodeInputKeyRequired        = "INPUT_KEY_REQUIRED"
	CodeDuplicateInputKey       = "DUPLICATE_INPUT_KEY"
)

// Config/schema/instance error codes (HTTP 400, docs/08-interface-spec.md §6: "请求内容
// 无效").
const (
	CodeValidationFailed         = "VALIDATION_FAILED"
	CodeSemanticValidationFailed = "SEMANTIC_VALIDATION_FAILED"
	// CodeModelNotFound, CodeToolNotFound and CodeModelConfigInvalid are produced by
	// service.DefinitionService's post-compile Agent Registry resolution, not by the
	// Compiler itself: a `agent` node's modelId and allowedTools name identifiers only the
	// Model and Tool Registries can resolve, and runtime holds no Registry access
	// (CLAUDE.md "Package boundaries"). They stay in this same HTTP 400 family because
	// they are still "请求内容无效" (08 §6), not a graph-shape violation.
	CodeModelNotFound      = "MODEL_NOT_FOUND"
	CodeToolNotFound       = "TOOL_NOT_FOUND"
	CodeModelConfigInvalid = "MODEL_CONFIG_INVALID"
)

// CompileStage names one stage of the Compiler's validation chain
// (docs/06-execution-model.md §1.2). Compile stops after the first stage that reports
// any error, so a caller only ever sees one stage's worth of ValidationErrors.
type CompileStage string

const (
	StageStructure      CompileStage = "STRUCTURE"
	StageConfigSchema   CompileStage = "CONFIG_SCHEMA"
	StageSemantics      CompileStage = "SEMANTICS"
	StageGraph          CompileStage = "GRAPH"
	StagePlan           CompileStage = "PLAN"
	StageRunInputSchema CompileStage = "RUN_INPUT_SCHEMA"
)

// CompileError is returned by Compile when one stage produces one or more
// ValidationErrors. Errors are deterministically ordered (node index, edge index, code)
// so repeated compilation of the same invalid Definition reports errors identically.
type CompileError struct {
	Stage  CompileStage
	Errors []ValidationError
}

func (e *CompileError) Error() string {
	return fmt.Sprintf("compile failed at stage %s: %d error(s)", e.Stage, len(e.Errors))
}

// AsCompileError extracts a *CompileError from err (following the errors.As convention)
// so callers can inspect Stage and Errors without depending on how Compile constructs it.
func AsCompileError(err error) (*CompileError, bool) {
	var ce *CompileError
	if errors.As(err, &ce) {
		return ce, true
	}
	return nil, false
}

// InvalidActionError is a deterministic Agent failure: a model Decision whose basic shape
// is wrong, a Tool outside the Agent Run's frozen allowlist, arguments or a Final output
// that fail their frozen Schema, or a state patch that cannot be applied
// (docs/06-execution-model.md §1.7 termination table). It is a typed error so the calling
// use case can terminate the Agent Run with domain.TerminationInvalidAction instead of
// treating a model mistake as an infrastructure failure or retrying it.
//
// Subject and Message are bounded, stable text. Neither carries a Provider payload, a
// credential or the full instance that failed.
type InvalidActionError struct {
	// Subject names what was rejected: "decision", "state patch", "tool call" or
	// "final output".
	Subject string
	Message string
	// Err is the underlying JSON or Schema violation, when one exists.
	Err error
}

func (e *InvalidActionError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %s: %v", domain.TerminationInvalidAction, e.Subject, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s: %s", domain.TerminationInvalidAction, e.Subject, e.Message)
}

func (e *InvalidActionError) Unwrap() error { return e.Err }

// AsInvalidActionError extracts an *InvalidActionError from err (errors.As convention), so
// a caller decides INVALID_ACTION from the error type rather than from message text.
func AsInvalidActionError(err error) (*InvalidActionError, bool) {
	var iae *InvalidActionError
	if errors.As(err, &iae) {
		return iae, true
	}
	return nil, false
}
