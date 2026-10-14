package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// DecisionEnvelope is one normalised model Decision as the Runtime receives it. It mirrors
// the Adapter contract's decision envelope (registry.ModelDecision) without importing
// registry: runtime holds pure decisions and
// never depends on the Provider-facing packages (CLAUDE.md "Package boundaries"). The
// caller maps the Adapter's result onto this struct.
type DecisionEnvelope struct {
	Kind domain.DecisionKind
	// ToolName and Arguments are required for TOOL_CALL.
	ToolName  *string
	Arguments json.RawMessage
	// Output is required for FINAL.
	Output json.RawMessage
	// StatePatch is optional for both kinds.
	StatePatch json.RawMessage
}

// ParseModelDecision performs the only check the model result transaction may make before
// it commits an immutable Decision: the basic envelope shape. The kind must be valid,
// each kind's required fields must
// be present, the two kinds' fields are mutually exclusive, and an optional state patch
// must be a JSON object.
//
// It deliberately does not validate arguments, output or the patched State: those are
// checked against the Agent Run's frozen Schemas when the committed Action executes, by
// ValidateToolCall, ValidateFinalOutput and ApplyStatePatch. A failure here is an
// *InvalidActionError: the Agent terminates as INVALID_ACTION and neither a Decision nor
// an Action is created.
//
// The returned domain.AgentDecision carries the fields to commit. Identity, Turn, response
// summary and timestamp belong to the commit transaction and are left zero, so a shape
// error names no Decision id.
func ParseModelDecision(envelope DecisionEnvelope) (domain.AgentDecision, error) {
	decision := domain.AgentDecision{
		Kind:       envelope.Kind,
		ToolName:   envelope.ToolName,
		Arguments:  presentJSON(envelope.Arguments),
		Output:     presentJSON(envelope.Output),
		StatePatch: presentJSON(envelope.StatePatch),
	}

	// domain.AgentDecision.Validate is the same rule the agent_decisions CHECK constraint
	// enforces; reusing it keeps the pre-commit check and the persisted invariant single.
	if err := decision.Validate(); err != nil {
		return domain.AgentDecision{}, &InvalidActionError{Subject: "decision", Message: "model decision has an invalid envelope shape", Err: err}
	}

	switch decision.Kind {
	case domain.DecisionToolCall:
		// The Decision envelope Schema declares `arguments` with no type,
		// so only well-formedness is checked here; the
		// Tool InputSchema rejects a wrongly shaped value when the Action executes.
		if !json.Valid(decision.Arguments) {
			return domain.AgentDecision{}, &InvalidActionError{Subject: "decision", Message: "TOOL_CALL arguments are not valid JSON"}
		}
	case domain.DecisionFinal:
		if !json.Valid(decision.Output) {
			return domain.AgentDecision{}, &InvalidActionError{Subject: "decision", Message: "FINAL output is not valid JSON"}
		}
	}

	if len(decision.StatePatch) > 0 {
		if _, err := decodeJSONObject(decision.StatePatch, "decision", ""); err != nil {
			return domain.AgentDecision{}, err
		}
	}
	return decision, nil
}

// ValidateToolCall is the execution-time check of a committed TOOL_CALL Decision:
// the Tool must belong to the Agent Run's frozen
// allowlist and the arguments must satisfy that Tool's registered InputSchema. tool is the
// metadata the caller already resolved through the Tool Registry for decision.ToolName;
// runtime resolves no Tool itself and never infers Tool behaviour from an Executor's type.
//
// A violation is an *InvalidActionError: the Action fails as INVALID_ACTION, the Tool is
// not called and the model is not asked again.
func ValidateToolCall(decision domain.AgentDecision, allowlist []string, tool domain.ToolMetadata) error {
	if decision.Kind != domain.DecisionToolCall {
		return &InvalidActionError{Subject: "tool call", Message: fmt.Sprintf("decision kind %q is not TOOL_CALL", decision.Kind)}
	}
	if decision.ToolName == nil || *decision.ToolName == "" {
		return &InvalidActionError{Subject: "tool call", Message: "decision names no Tool"}
	}
	name := *decision.ToolName

	if !slices.Contains(allowlist, name) {
		return &InvalidActionError{Subject: "tool call", Message: fmt.Sprintf("tool %q is not in the Agent Run's frozen allowlist", name)}
	}
	if tool.Name != name {
		return &InvalidActionError{Subject: "tool call", Message: fmt.Sprintf("resolved tool %q does not match the decision's tool %q", tool.Name, name)}
	}
	if err := validateAgainstSchema(tool.InputSchema, decision.Arguments); err != nil {
		return &InvalidActionError{Subject: "tool call", Message: fmt.Sprintf("arguments do not satisfy the InputSchema of tool %q", name), Err: err}
	}
	return nil
}

// ValidateFinalOutput is the execution-time check of a committed FINAL Decision's output
// against the Agent Run's frozen OutputSchema, in the form that Schema declares. An absent
// OutputSchema is the text form without a Schema: the output must be a JSON string. A
// Schema whose root type is "object" is the object form: the output must be a JSON object
// satisfying it. Any other frozen Schema is the text form and the output must satisfy it.
// The form is read from the frozen Schema itself, because the text form can never declare
// the root type "object": its Schema has to accept the string "".
//
// A violation is an *InvalidActionError: the Final Action fails as INVALID_ACTION, without
// a new State Version and without a second model call. Its message names the form the
// Agent's output must take and the JSON type the output actually decoded to, and for a
// Schema violation the failing keyword locations inside the frozen OutputSchema. It never
// repeats the output itself: no value, member name or validator text taken from what the
// model wrote, because that is unbounded model content.
func ValidateFinalOutput(output json.RawMessage, outputSchema json.RawMessage) error {
	outputSchema = presentJSON(outputSchema)
	objectForm := frozenOutputForm(outputSchema) == AgentOutputObject
	expected := "this Agent's output must be a JSON string"
	switch {
	case objectForm:
		expected = "this Agent's output must be a JSON object satisfying its frozen OutputSchema"
	case len(outputSchema) > 0:
		expected = "this Agent's output must satisfy its frozen OutputSchema"
	}

	output = presentJSON(output)
	if len(output) == 0 {
		return finalOutputError("FINAL decision carries no output; " + expected)
	}
	decoded, ok := decodedJSONType(output)
	if !ok {
		return finalOutputError("FINAL output is not valid JSON; " + expected)
	}

	if len(outputSchema) == 0 {
		if decoded != jsonTypeString {
			return finalOutputError(fmt.Sprintf("decoded to %s; %s", decoded, expected))
		}
		return nil
	}
	if objectForm && decoded != jsonTypeObject {
		return finalOutputError(fmt.Sprintf("decoded to %s, but the frozen OutputSchema requires %s", decoded, jsonTypeObject))
	}

	err := validateAgainstSchema(outputSchema, output)
	if err == nil {
		return nil
	}
	var violation *jsonschema.ValidationError
	if !errors.As(err, &violation) {
		// The frozen Schema itself cannot be compiled. Its compiler text is not carried:
		// the Definition author fixes the Schema, not the model.
		return finalOutputError(fmt.Sprintf("decoded to %s, but the frozen OutputSchema is not usable", decoded))
	}
	return finalOutputError(fmt.Sprintf("decoded to %s, but %s", decoded, describeSchemaViolation(violation)))
}

// frozenOutputForm reads the output form of an outputSchema frozen on an Agent Run. The
// Schema already passed ParseAgentNodeConfig, so its root type alone decides the form.
func frozenOutputForm(outputSchema json.RawMessage) AgentOutputForm {
	var root struct {
		Type json.RawMessage `json:"type"`
	}
	if len(outputSchema) == 0 || json.Unmarshal(outputSchema, &root) != nil {
		return AgentOutputText
	}
	if bytes.Equal(bytes.TrimSpace(root.Type), []byte(`"object"`)) {
		return AgentOutputObject
	}
	return AgentOutputText
}

func finalOutputError(message string) *InvalidActionError {
	return &InvalidActionError{Subject: "final output", Message: message}
}

// The JSON types a FINAL output can decode to, phrased for an INVALID_ACTION message.
const (
	jsonTypeObject  = "a JSON object"
	jsonTypeArray   = "a JSON array"
	jsonTypeString  = "a JSON string"
	jsonTypeNumber  = "a JSON number"
	jsonTypeBoolean = "a JSON boolean"
	jsonTypeNull    = "JSON null"
)

// decodedJSONType reports which JSON type one complete value decodes to. Only the first
// significant byte is inspected once the value is known to be valid JSON.
func decodedJSONType(raw json.RawMessage) (string, bool) {
	if !json.Valid(raw) {
		return "", false
	}
	switch bytes.TrimSpace(raw)[0] {
	case '{':
		return jsonTypeObject, true
	case '[':
		return jsonTypeArray, true
	case '"':
		return jsonTypeString, true
	case 't', 'f':
		return jsonTypeBoolean, true
	case 'n':
		return jsonTypeNull, true
	default:
		return jsonTypeNumber, true
	}
}

// schemaTypeName phrases one JSON Schema `type` name like the decoded types above.
func schemaTypeName(schemaType string) (string, bool) {
	switch schemaType {
	case "object":
		return jsonTypeObject, true
	case "array":
		return jsonTypeArray, true
	case "string":
		return jsonTypeString, true
	case "number":
		return jsonTypeNumber, true
	case "integer":
		return "a JSON integer", true
	case "boolean":
		return jsonTypeBoolean, true
	case "null":
		return jsonTypeNull, true
	default:
		return "", false
	}
}

// maxReportedSchemaLocations bounds how many failing keyword locations one message lists.
const maxReportedSchemaLocations = 3

// describeSchemaViolation states why the frozen OutputSchema rejected the output. A wrong
// type at the root is named as the type the Schema requires. Any other violation is named
// by its keyword locations inside the Schema, which come from the Definition author and
// never from the model, unlike the validator's own text, which can quote the instance.
func describeSchemaViolation(violation *jsonschema.ValidationError) string {
	leaves := schemaViolationLeaves(violation, nil)
	for _, leaf := range leaves {
		if wrongType, ok := leaf.ErrorKind.(*kind.Type); ok && len(leaf.InstanceLocation) == 0 {
			wants := make([]string, 0, len(wrongType.Want))
			for _, want := range wrongType.Want {
				if name, known := schemaTypeName(want); known {
					wants = append(wants, name)
				}
			}
			if len(wants) > 0 {
				return "the frozen OutputSchema requires " + strings.Join(wants, " or ")
			}
		}
	}

	locations := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		location := leaf.SchemaURL
		if i := strings.IndexByte(location, '#'); i >= 0 {
			location = location[i+1:]
		}
		for _, keyword := range leaf.ErrorKind.KeywordPath() {
			location += "/" + keyword
		}
		if location == "" {
			location = "/"
		}
		if !slices.Contains(locations, location) {
			locations = append(locations, location)
		}
	}
	if len(locations) == 0 {
		return "the frozen OutputSchema rejects it"
	}
	if len(locations) > maxReportedSchemaLocations {
		return fmt.Sprintf("the frozen OutputSchema rejects it at %s and %d more",
			strings.Join(locations[:maxReportedSchemaLocations], ", "), len(locations)-maxReportedSchemaLocations)
	}
	return "the frozen OutputSchema rejects it at " + strings.Join(locations, ", ")
}

// schemaViolationLeaves collects the innermost violations, the ones naming a keyword.
func schemaViolationLeaves(violation *jsonschema.ValidationError, leaves []*jsonschema.ValidationError) []*jsonschema.ValidationError {
	if len(violation.Causes) == 0 {
		return append(leaves, violation)
	}
	for _, cause := range violation.Causes {
		leaves = schemaViolationLeaves(cause, leaves)
	}
	return leaves
}

// ValidateToolResult checks the result of one real Tool call against that Tool's
// registered OutputSchema (the OutputSchema is the Tool's
// sole result contract, and the Runtime -- not the Executor -- enforces it).
//
// It is deliberately not an *InvalidActionError: the model's Decision was valid and the
// Tool was allowed to run, so a result the Tool's own contract rejects is a Tool failure
// (TOOL_ERROR), not an invalid Action. A Tool that registers no OutputSchema constrains
// nothing beyond the result being valid JSON.
func ValidateToolResult(result json.RawMessage, outputSchema json.RawMessage) error {
	result = presentJSON(result)
	if len(result) == 0 {
		return fmt.Errorf("tool result: the Tool returned no result")
	}
	if !json.Valid(result) {
		return fmt.Errorf("tool result: the Tool returned invalid JSON")
	}
	if len(presentJSON(outputSchema)) == 0 {
		return nil
	}
	if err := validateAgainstSchema(outputSchema, result); err != nil {
		return fmt.Errorf("tool result: result does not satisfy the registered OutputSchema: %w", err)
	}
	return nil
}

// validateAgainstSchema compiles one frozen Schema and validates instance against it. It
// mirrors ValidateRunInput's compilation (a Definition's frozen runInputSchema) for the
// Schemas an Agent Action validates; like that one it caches nothing, because an Action
// validates at most one instance per call.
func validateAgainstSchema(rawSchema, instance json.RawMessage) error {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawSchema))
	if err != nil {
		return fmt.Errorf("frozen schema is not valid JSON: %w", err)
	}
	const schemaURL = "mem://agent-action-schema"
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(schemaURL, document); err != nil {
		return fmt.Errorf("frozen schema is not usable: %w", err)
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		return fmt.Errorf("frozen schema is not usable: %w", err)
	}

	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(instance))
	if err != nil {
		return fmt.Errorf("value is not valid JSON: %w", err)
	}
	return schema.Validate(value)
}

// presentJSON normalises an absent optional JSON field: whitespace-only bytes are the same
// as no field at all, so "absent" is one condition everywhere below.
func presentJSON(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return raw
}
