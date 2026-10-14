package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The strict output subset an object-form outputSchema must stay inside. It is the
// intersection of what Provider strict structured output enforces and what the Runtime's
// own validation enforces, so the Provider's constraint and the Runtime's check read the
// same Schema the same way. It is checked per Definition, independent of the selected
// model, and is part of the Adapter contract: widening or narrowing it is a contract
// change, not a local tweak.
const (
	// maxOutputSchemaDepth bounds nesting: the root Schema is level 1 and every
	// `properties` member or `items` Schema is one level below its parent.
	maxOutputSchemaDepth = 5
	// maxOutputSchemaProperties bounds the properties of every object in the Schema taken
	// together.
	maxOutputSchemaProperties = 100
	// maxOutputSchemaBytes bounds the Schema's canonical JSON encoding.
	maxOutputSchemaBytes = 16 * 1024
)

// strictOutputKeywords are the only keywords an object-form Schema may use.
var strictOutputKeywords = []string{
	"additionalProperties", "description", "enum", "items", "properties", "required", "title", "type",
}

// strictOutputTypes are the base types a Schema may declare, alone or in a union with
// "null".
var strictOutputTypes = []string{"array", "boolean", "integer", "number", "object", "string"}

// classifyAgentOutputSchema decides the form a present outputSchema gives the Agent's
// FINAL output, or rejects it. A Schema whose root type is exactly "object" is the object
// form and must stay inside the strict output subset; any other Schema is the text form
// only when it accepts the string "". A rejection names the violated rule and its JSON
// Pointer inside the Schema and never repeats the Schema's own content.
func classifyAgentOutputSchema(rawSchema json.RawMessage) (AgentOutputForm, error) {
	document, err := decodeOutputSchema(rawSchema)
	if err != nil {
		return "", outputSchemaError("outputSchema is not valid JSON")
	}

	if root, ok := document.(map[string]any); ok && root["type"] == "object" {
		if violation := checkStrictOutputSchema(root); violation != nil {
			return "", outputSchemaError(fmt.Sprintf("object-form outputSchema at JSON Pointer %q: %s",
				violation.pointer, violation.rule))
		}
		if _, err := compileAgentSchema(rawSchema); err != nil {
			// The subset admits nothing the compiler rejects; its text is not carried,
			// because it can quote the Schema.
			return "", outputSchemaError("object-form outputSchema is not a usable JSON Schema")
		}
		return AgentOutputObject, nil
	}

	probe, _ := json.Marshal("")
	err = validateAgainstSchema(rawSchema, probe)
	if err == nil {
		return AgentOutputText, nil
	}
	var rejected *jsonschema.ValidationError
	if !errors.As(err, &rejected) {
		return "", outputSchemaError("outputSchema is not a usable JSON Schema: " + err.Error())
	}
	return "", outputSchemaError(`outputSchema must either accept the string "" (text form) ` +
		`or declare the root type "object" inside the strict output subset (object form)`)
}

func outputSchemaError(message string) *AgentConfigError {
	return &AgentConfigError{Field: "outputSchema", Message: message}
}

// decodeOutputSchema decodes a Schema keeping every number literal, so the canonical size
// is measured on the values the author wrote.
func decodeOutputSchema(rawSchema json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(rawSchema))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, errors.New("trailing JSON content")
	}
	return document, nil
}

// outputSchemaViolation is the first strict-subset rule an object-form Schema breaks.
type outputSchemaViolation struct {
	pointer string
	rule    string
}

// checkStrictOutputSchema reports the first violation of the strict output subset, in a
// deterministic order: keywords and properties are visited sorted by name.
func checkStrictOutputSchema(root map[string]any) *outputSchemaViolation {
	encoded, err := encodeCanonical(root)
	if err != nil {
		return &outputSchemaViolation{pointer: "", rule: "the Schema cannot be encoded as JSON"}
	}
	if len(encoded) > maxOutputSchemaBytes {
		return &outputSchemaViolation{pointer: "", rule: fmt.Sprintf(
			"the Schema is %d bytes as canonical JSON; the strict output subset allows at most 16 KiB", len(encoded))}
	}
	walker := strictOutputWalker{}
	return walker.schema(root, "", 1)
}

type strictOutputWalker struct {
	properties int
}

func (w *strictOutputWalker) schema(node any, pointer string, depth int) *outputSchemaViolation {
	schema, ok := node.(map[string]any)
	if !ok {
		return &outputSchemaViolation{pointer, "a Schema in the strict output subset must be a JSON object"}
	}
	if depth > maxOutputSchemaDepth {
		return &outputSchemaViolation{pointer, fmt.Sprintf(
			"the Schema nests deeper than %d levels", maxOutputSchemaDepth)}
	}

	keywords := make([]string, 0, len(schema))
	for keyword := range schema {
		keywords = append(keywords, keyword)
	}
	sort.Strings(keywords)
	for _, keyword := range keywords {
		if !slices.Contains(strictOutputKeywords, keyword) {
			return &outputSchemaViolation{pointerTo(pointer, keyword), "the keyword is outside the strict output subset"}
		}
	}

	rawType, present := schema["type"]
	if !present {
		return &outputSchemaViolation{pointer, "every Schema must declare its type"}
	}
	baseType, violation := strictOutputType(rawType, pointerTo(pointer, "type"))
	if violation != nil {
		return violation
	}

	for _, keyword := range []string{"description", "title"} {
		if value, present := schema[keyword]; present {
			if _, isString := value.(string); !isString {
				return &outputSchemaViolation{pointerTo(pointer, keyword), keyword + " must be a string"}
			}
		}
	}
	if values, present := schema["enum"]; present {
		if violation := strictOutputEnum(values, pointerTo(pointer, "enum")); violation != nil {
			return violation
		}
	}

	if baseType != "object" {
		for _, keyword := range []string{"additionalProperties", "properties", "required"} {
			if _, present := schema[keyword]; present {
				return &outputSchemaViolation{pointerTo(pointer, keyword), keyword + ` applies only to a Schema of type "object"`}
			}
		}
	}
	if baseType != "array" {
		if _, present := schema["items"]; present {
			return &outputSchemaViolation{pointerTo(pointer, "items"), `items applies only to a Schema of type "array"`}
		}
	}

	switch baseType {
	case "object":
		return w.object(schema, pointer, depth)
	case "array":
		items, present := schema["items"]
		if !present {
			return &outputSchemaViolation{pointer, `a Schema of type "array" must declare items`}
		}
		if _, single := items.(map[string]any); !single {
			return &outputSchemaViolation{pointerTo(pointer, "items"), "items must be a single Schema"}
		}
		return w.schema(items, pointerTo(pointer, "items"), depth+1)
	}
	return nil
}

func (w *strictOutputWalker) object(schema map[string]any, pointer string, depth int) *outputSchemaViolation {
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		return &outputSchemaViolation{pointerTo(pointer, "properties"), `a Schema of type "object" must declare at least one property`}
	}
	if additional, present := schema["additionalProperties"]; !present {
		return &outputSchemaViolation{pointer, "every object must declare additionalProperties: false"}
	} else if additional != false {
		return &outputSchemaViolation{pointerTo(pointer, "additionalProperties"), "every object must declare additionalProperties: false"}
	}

	requiredPointer := pointerTo(pointer, "required")
	const requiredRule = "required must list every property exactly once; an optional value is a type union with \"null\""
	rawRequired, ok := schema["required"].([]any)
	if !ok || len(rawRequired) != len(properties) {
		return &outputSchemaViolation{requiredPointer, requiredRule}
	}
	listed := make(map[string]bool, len(rawRequired))
	for _, entry := range rawRequired {
		name, isString := entry.(string)
		if !isString || listed[name] {
			return &outputSchemaViolation{requiredPointer, requiredRule}
		}
		if _, declared := properties[name]; !declared {
			return &outputSchemaViolation{requiredPointer, requiredRule}
		}
		listed[name] = true
	}

	w.properties += len(properties)
	if w.properties > maxOutputSchemaProperties {
		return &outputSchemaViolation{pointerTo(pointer, "properties"), fmt.Sprintf(
			"the Schema declares more than %d properties in total", maxOutputSchemaProperties)}
	}

	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if violation := w.schema(properties[name], pointerTo(pointerTo(pointer, "properties"), name), depth+1); violation != nil {
			return violation
		}
	}
	return nil
}

// strictOutputType reads a `type` value: one base type, or a union of one base type with
// "null", which is how the subset spells an optional value.
func strictOutputType(rawType any, pointer string) (string, *outputSchemaViolation) {
	const rule = `type must be one of array, boolean, integer, number, object or string, ` +
		`or a union of one of them with "null"`
	switch typed := rawType.(type) {
	case string:
		if slices.Contains(strictOutputTypes, typed) {
			return typed, nil
		}
	case []any:
		if len(typed) != 2 {
			break
		}
		first, firstOK := typed[0].(string)
		second, secondOK := typed[1].(string)
		if !firstOK || !secondOK {
			break
		}
		if first == "null" {
			first, second = second, first
		}
		if second == "null" && slices.Contains(strictOutputTypes, first) {
			return first, nil
		}
	}
	return "", &outputSchemaViolation{pointer, rule}
}

// strictOutputEnum accepts a non-empty list of scalar values.
func strictOutputEnum(values any, pointer string) *outputSchemaViolation {
	list, ok := values.([]any)
	if !ok || len(list) == 0 {
		return &outputSchemaViolation{pointer, "enum must be a non-empty array"}
	}
	for i, value := range list {
		switch value.(type) {
		case string, json.Number, bool, nil:
		default:
			return &outputSchemaViolation{fmt.Sprintf("%s/%d", pointer, i), "enum values must be scalar"}
		}
	}
	return nil
}

// pointerTo appends one reference token to a JSON Pointer, escaping it as RFC 6901 does.
func pointerTo(pointer, token string) string {
	token = strings.ReplaceAll(token, "~", "~0")
	token = strings.ReplaceAll(token, "/", "~1")
	return pointer + "/" + token
}
