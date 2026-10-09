package openaimodel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/leungll/Emberling/backend/internal/registry"
)

// Wire names of the Decision envelope members. They are the property names of the
// registered Decision Schema; strictDecision rejects a Schema that introduces any other
// member, so a change to the envelope fails at construction instead of silently dropping
// a field.
const (
	memberKind       = "kind"
	memberTool       = "tool"
	memberArguments  = "arguments"
	memberOutput     = "output"
	memberStatePatch = "statePatch"
)

// strictDecision is the Provider-facing form of the registered Decision Schema together
// with the compiled validator for a Provider answer.
//
// Strict structured output accepts neither oneOf, nor an untyped `{}` value, nor an
// optional property. The form is therefore derived rather than hand-written: the kind
// constants of every oneOf branch become one enum, a string-typed member stays a string,
// every other member (an arbitrary JSON value or an object) travels as a JSON-encoded
// string, and every member is required but nullable. Mutual exclusion and the per-kind
// required members are not expressed here; the Runtime checks them before it commits a
// Decision, so a TOOL_CALL without a tool stays an invalid action rather than becoming a
// model error.
type strictDecision struct {
	schema    json.RawMessage
	validator *jsonschema.Schema
	// encoded lists the members that carry a JSON-encoded value.
	encoded map[string]bool
}

type decisionBranch struct {
	Properties map[string]json.RawMessage `json:"properties"`
}

type decisionDocument struct {
	OneOf []decisionBranch `json:"oneOf"`
}

type memberSchema struct {
	Const *string `json:"const"`
	Type  any     `json:"type"`
}

// newStrictDecision derives the strict form from the registered Decision Schema.
func newStrictDecision(decisionSchema json.RawMessage) (strictDecision, error) {
	var document decisionDocument
	if err := json.Unmarshal(decisionSchema, &document); err != nil {
		return strictDecision{}, fmt.Errorf("decision schema is not a JSON object: %w", err)
	}
	if len(document.OneOf) == 0 {
		return strictDecision{}, errors.New("decision schema declares no oneOf branch")
	}

	var kinds []string
	stringMembers := map[string]bool{}
	encoded := map[string]bool{}
	for i, branch := range document.OneOf {
		var kind memberSchema
		raw, ok := branch.Properties[memberKind]
		if !ok || json.Unmarshal(raw, &kind) != nil || kind.Const == nil {
			return strictDecision{}, fmt.Errorf("decision schema branch %d has no constant %q", i, memberKind)
		}
		if !slices.Contains(kinds, *kind.Const) {
			kinds = append(kinds, *kind.Const)
		}
		for name, rawMember := range branch.Properties {
			if name == memberKind {
				continue
			}
			if !knownMember(name) {
				return strictDecision{}, fmt.Errorf("decision schema member %q has no Decision field", name)
			}
			var member memberSchema
			if err := json.Unmarshal(rawMember, &member); err != nil {
				return strictDecision{}, fmt.Errorf("decision schema member %q: %w", name, err)
			}
			isString := member.Type == "string"
			if (isString && encoded[name]) || (!isString && stringMembers[name]) {
				return strictDecision{}, fmt.Errorf("decision schema member %q has different types across branches", name)
			}
			if isString {
				stringMembers[name] = true
			} else {
				encoded[name] = true
			}
		}
	}

	names := make([]string, 0, len(stringMembers)+len(encoded))
	for name := range stringMembers {
		names = append(names, name)
	}
	for name := range encoded {
		names = append(names, name)
	}
	slices.Sort(names)

	properties := map[string]any{
		memberKind: map[string]any{"type": "string", "enum": kinds},
	}
	for _, name := range names {
		property := map[string]any{"type": []string{"string", "null"}}
		if encoded[name] {
			property["description"] = "JSON-encoded value, or null when it does not apply"
		}
		properties[name] = property
	}
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
		"required":             append([]string{memberKind}, names...),
	}
	encodedSchema, err := json.Marshal(schema)
	if err != nil {
		return strictDecision{}, fmt.Errorf("encode strict decision schema: %w", err)
	}
	validator, err := registry.CompileSchema(encodedSchema)
	if err != nil {
		return strictDecision{}, fmt.Errorf("compile strict decision schema: %w", err)
	}
	return strictDecision{schema: encodedSchema, validator: validator, encoded: encoded}, nil
}

func knownMember(name string) bool {
	switch name {
	case memberTool, memberArguments, memberOutput, memberStatePatch:
		return true
	}
	return false
}

// parse turns the model's message content into a normalised Decision. Content that is not
// JSON, does not satisfy the strict form, or carries a JSON-encoded member that is not
// JSON cannot be normalised; the caller reports that as a model error.
func (s strictDecision) parse(content string) (registry.ModelDecision, error) {
	if !json.Valid([]byte(content)) {
		return registry.ModelDecision{}, errors.New("message content is not JSON")
	}
	if err := registry.ValidateValue(s.validator, json.RawMessage(content)); err != nil {
		return registry.ModelDecision{}, errors.New("message content does not match the strict Decision schema")
	}
	var members map[string]*string
	decoder := json.NewDecoder(bytes.NewReader([]byte(content)))
	if err := decoder.Decode(&members); err != nil {
		return registry.ModelDecision{}, errors.New("message content is not a Decision object")
	}

	decision := registry.ModelDecision{}
	if kind := members[memberKind]; kind != nil {
		decision.Kind = registry.DecisionKind(*kind)
	}
	for name, value := range members {
		if name == memberKind || value == nil {
			continue
		}
		var raw json.RawMessage
		if s.encoded[name] {
			if !json.Valid([]byte(*value)) {
				return registry.ModelDecision{}, fmt.Errorf("decision member %q is not a JSON-encoded value", name)
			}
			raw = json.RawMessage(*value)
		}
		switch name {
		case memberTool:
			tool := *value
			decision.ToolName = &tool
		case memberArguments:
			decision.Arguments = raw
		case memberOutput:
			decision.Output = raw
		case memberStatePatch:
			decision.StatePatch = raw
		}
	}
	return decision, nil
}
