package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// schemaResourceURL is the in-memory location every registered Schema is compiled under.
// Registrations are self-contained documents: the compiler never loads a remote $ref.
const schemaResourceURL = "emberling:///registration.json"

// CompileSchema compiles one registered JSON Schema. Registrations, node semantic checks
// and Adapters share this helper so that a Schema accepted at startup is validated by the
// same compiler later.
func CompileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("schema is empty")
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("schema is not valid JSON: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(schemaResourceURL, doc); err != nil {
		return nil, fmt.Errorf("schema is not a usable JSON Schema: %w", err)
	}
	compiled, err := compiler.Compile(schemaResourceURL)
	if err != nil {
		return nil, fmt.Errorf("schema is not a usable JSON Schema: %w", err)
	}
	return compiled, nil
}

// ValidateValue validates a decoded Go value against a compiled Schema. The value is
// normalised through the compiler's own JSON decoding so that a map built by
// encoding/json and a map built by a test literal validate identically.
func ValidateValue(schema *jsonschema.Schema, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("value is not encodable as JSON: %w", err)
	}
	decoded, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("value is not valid JSON: %w", err)
	}
	return schema.Validate(decoded)
}

// ApplyTopLevelDefaults copies each top-level property's declared JSON Schema "default"
// into value, for every property value does not already set. It exists so a resolved
// Model's modelConfig can be frozen with its full, normalised shape -- every declared
// default applied -- rather than the possibly-partial object a Definition author
// submitted (the model ConfigSchema contract). Only top-level `properties`
// defaults are applied; a nested object's own defaults are that object's concern, not
// this helper's.
//
// The returned map is never nil, so a caller that marshals it back to JSON gets "{}" for
// an empty schema and an empty value, never "null".
func ApplyTopLevelDefaults(schema json.RawMessage, value map[string]any) (map[string]any, error) {
	result := make(map[string]any, len(value))
	for k, v := range value {
		result[k] = v
	}

	if len(bytes.TrimSpace(schema)) == 0 {
		return result, nil
	}
	var document map[string]any
	if err := json.Unmarshal(schema, &document); err != nil {
		return nil, fmt.Errorf("schema is not a JSON object: %w", err)
	}
	properties, ok := document["properties"].(map[string]any)
	if !ok {
		return result, nil
	}
	for name, raw := range properties {
		property, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if _, present := result[name]; present {
			continue
		}
		if def, hasDefault := property["default"]; hasDefault {
			result[name] = def
		}
	}
	return result, nil
}

// schemaProperty resolves a dotted UIField path to the property subschema it names.
// Resolution walks the declared `properties` of the document, which is what Studio renders
// and what ConfigSchema validation enforces; a path that names no declared property is a
// registration error rather than a silently ignored field.
func schemaProperty(raw json.RawMessage, path string) (map[string]any, error) {
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("configSchema is not a JSON object")
	}
	if path == "" {
		return nil, fmt.Errorf("path is empty")
	}

	current := document
	segments := strings.Split(path, ".")
	for i, segment := range segments {
		properties, ok := current["properties"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("path %q does not resolve: %q declares no properties", path, strings.Join(segments[:i], "."))
		}
		next, ok := properties[segment].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("path %q does not resolve to a declared property", path)
		}
		current = next
	}
	return current, nil
}

// propertyHasType reports whether a property subschema declares the given JSON type.
// `type` may be a single name or a list of names.
func propertyHasType(property map[string]any, want string) bool {
	switch declared := property["type"].(type) {
	case string:
		return declared == want
	case []any:
		for _, item := range declared {
			if name, ok := item.(string); ok && name == want {
				return true
			}
		}
	}
	return false
}

// propertyHasStringItems reports whether a property subschema is an array whose items are
// strings, the only shape a TOOL_SELECTOR value (a list of Tool names) can take.
func propertyHasStringItems(property map[string]any) bool {
	if !propertyHasType(property, "array") {
		return false
	}
	items, ok := property["items"].(map[string]any)
	return ok && propertyHasType(items, "string")
}

// propertyHasEnum reports whether a property subschema declares a non-empty enum, which
// is what the SELECT widget renders.
func propertyHasEnum(property map[string]any) bool {
	values, ok := property["enum"].([]any)
	return ok && len(values) > 0
}
