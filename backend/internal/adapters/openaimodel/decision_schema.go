package openaimodel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

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
// optional property. The envelope is therefore derived rather than hand-written, once, at
// construction: the kind constants of every oneOf branch become one enum, a string-typed
// member stays a string, every other member except output (an arbitrary JSON value or an
// object) travels as a JSON-encoded string, and every member is required but nullable.
// The output member is not part of the fixed envelope: each request derives it from its
// own final output Schema (see outputMember), so the Provider generates the FINAL value
// itself rather than an encoding of it. Mutual exclusion and the per-kind required members
// are not expressed structurally; the Runtime checks them before it commits a Decision, so
// a TOOL_CALL without a tool stays an invalid action rather than becoming a model error.
// Because the structure cannot carry those rules, every member's description states them,
// derived from each branch's required list: which kind requires the member, for which it
// is optional, and for which it must be null.
type strictDecision struct {
	// properties are the envelope members other than output, as sent to the Provider.
	properties map[string]any
	required   []string
	// validator checks an answer against the envelope with output left unconstrained:
	// the shape of output is the Runtime's to judge against the frozen Schema.
	validator *jsonschema.Schema
	// encoded lists the members that carry a JSON-encoded value.
	encoded map[string]bool
	// outputDescription states when the output member is required. It is attached to the
	// text-form output member only: an object-form output member is the request's own
	// final output Schema, sent unchanged apart from its nullability.
	outputDescription string
}

type decisionBranch struct {
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
}

// branchRule is what one oneOf branch says about the envelope members: its kind constant,
// the members it requires in declaration order, and the members it declares at all.
type branchRule struct {
	kind     string
	required []string
	declared map[string]bool
}

type decisionDocument struct {
	OneOf []decisionBranch `json:"oneOf"`
}

type memberSchema struct {
	Const *string `json:"const"`
	Type  any     `json:"type"`
}

// newStrictDecision derives the strict envelope from the registered Decision Schema.
func newStrictDecision(decisionSchema json.RawMessage) (strictDecision, error) {
	var document decisionDocument
	if err := json.Unmarshal(decisionSchema, &document); err != nil {
		return strictDecision{}, fmt.Errorf("decision schema is not a JSON object: %w", err)
	}
	if len(document.OneOf) == 0 {
		return strictDecision{}, errors.New("decision schema declares no oneOf branch")
	}

	var kinds []string
	var rules []branchRule
	stringMembers := map[string]bool{}
	encoded := map[string]bool{}
	for i, branch := range document.OneOf {
		var kind memberSchema
		raw, ok := branch.Properties[memberKind]
		if !ok || json.Unmarshal(raw, &kind) != nil || kind.Const == nil {
			return strictDecision{}, fmt.Errorf("decision schema branch %d has no constant %q", i, memberKind)
		}
		if slices.Contains(kinds, *kind.Const) {
			return strictDecision{}, fmt.Errorf("decision schema declares kind %q in more than one branch", *kind.Const)
		}
		kinds = append(kinds, *kind.Const)
		rule := branchRule{kind: *kind.Const, declared: map[string]bool{}}
		for name := range branch.Properties {
			rule.declared[name] = true
		}
		for _, name := range branch.Required {
			if name == memberKind {
				continue
			}
			if !rule.declared[name] {
				return strictDecision{}, fmt.Errorf("decision schema branch %q requires undeclared member %q", rule.kind, name)
			}
			rule.required = append(rule.required, name)
		}
		rules = append(rules, rule)
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
	// output carries the FINAL value in whatever form the request's Schema gives it, so
	// it must be an unconstrained member of the registered envelope.
	if !encoded[memberOutput] {
		return strictDecision{}, fmt.Errorf("decision schema must declare %q as an unconstrained member", memberOutput)
	}
	delete(encoded, memberOutput)

	names := make([]string, 0, len(stringMembers)+len(encoded)+1)
	for name := range stringMembers {
		names = append(names, name)
	}
	for name := range encoded {
		names = append(names, name)
	}
	names = append(names, memberOutput)
	slices.Sort(names)

	properties := map[string]any{
		memberKind: map[string]any{"type": "string", "enum": kinds, "description": kindDescription(rules)},
	}
	for _, name := range names {
		if name == memberOutput {
			continue
		}
		properties[name] = map[string]any{
			"type":        []string{"string", "null"},
			"description": memberMeaning(name, encoded[name]) + " " + kindRequirement(name, rules),
		}
	}
	strict := strictDecision{
		properties:        properties,
		required:          append([]string{memberKind}, names...),
		encoded:           encoded,
		outputDescription: memberMeaning(memberOutput, false) + " " + kindRequirement(memberOutput, rules),
	}
	// Locally, output accepts any JSON value, null included.
	answerSchema, err := strict.encode(map[string]any{})
	if err != nil {
		return strictDecision{}, err
	}
	validator, err := registry.CompileSchema(answerSchema)
	if err != nil {
		return strictDecision{}, fmt.Errorf("compile strict decision schema: %w", err)
	}
	strict.validator = validator
	return strict, nil
}

// memberMeaning is what a member carries. The meaning of each member is fixed by the
// Adapter contract (parse maps every member onto one Decision field); whether the value
// travels JSON-encoded follows the registered Schema.
func memberMeaning(name string, encoded bool) string {
	switch name {
	case memberTool:
		return "Name of the tool to call."
	case memberArguments:
		if encoded {
			return `The called tool's arguments as a JSON-encoded object that satisfies its input schema, for example "{\"key\":\"value\"}".`
		}
		return "The called tool's arguments as an object that satisfies its input schema."
	case memberStatePatch:
		if encoded {
			return "A JSON-encoded JSON Merge Patch object applied to the current state."
		}
		return "A JSON Merge Patch object applied to the current state."
	case memberOutput:
		return "The final answer, written as plain text."
	}
	return ""
}

// kindRequirement states, from the registered branches, for which kinds a member is
// required, for which it is optional, and for which it must be null.
func kindRequirement(name string, rules []branchRule) string {
	var required, optional, absent []string
	for _, rule := range rules {
		switch {
		case slices.Contains(rule.required, name):
			required = append(required, rule.kind)
		case rule.declared[name]:
			optional = append(optional, rule.kind)
		default:
			absent = append(absent, rule.kind)
		}
	}
	var sentences []string
	if len(required) > 0 {
		sentences = append(sentences, "Required (non-null) when kind is "+orList(required)+".")
	}
	if len(optional) > 0 {
		sentences = append(sentences, "Optional when kind is "+orList(optional)+"; null when not used.")
	}
	if len(absent) > 0 {
		sentences = append(sentences, "Must be null when kind is "+orList(absent)+".")
	}
	return strings.Join(sentences, " ")
}

// kindDescription summarises which members each registered kind requires.
func kindDescription(rules []branchRule) string {
	sentences := make([]string, 0, len(rules)+1)
	for _, rule := range rules {
		if len(rule.required) == 0 {
			sentences = append(sentences, rule.kind+" requires no other member.")
			continue
		}
		sentences = append(sentences, rule.kind+" requires non-null "+andList(rule.required)+".")
	}
	sentences = append(sentences, "Set every member the chosen kind does not require to null, unless that member's description marks it optional for that kind.")
	return strings.Join(sentences, " ")
}

func orList(items []string) string  { return joinList(items, "or") }
func andList(items []string) string { return joinList(items, "and") }

func joinList(items []string, conjunction string) string {
	if len(items) <= 1 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " " + conjunction + " " + items[len(items)-1]
}

func knownMember(name string) bool {
	switch name {
	case memberTool, memberArguments, memberOutput, memberStatePatch:
		return true
	}
	return false
}

// schemaFor is the strict Decision Schema sent with one request: the fixed envelope plus
// the output member derived from that request's final output Schema.
func (s strictDecision) schemaFor(finalOutputSchema json.RawMessage) (json.RawMessage, error) {
	return s.encode(outputMember(finalOutputSchema, s.outputDescription))
}

func (s strictDecision) encode(output any) (json.RawMessage, error) {
	properties := maps.Clone(s.properties)
	properties[memberOutput] = output
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
		"required":             s.required,
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("encode strict decision schema: %w", err)
	}
	return encoded, nil
}

// outputMember derives the strict output member from one request's final output Schema.
//
// An object-form Agent's Schema, whose root type is exactly "object", is sent as itself,
// made nullable by widening the root type to ["object", "null"], because a TOOL_CALL answer
// leaves output null. The Runtime admits a root type of "object" only for a Schema inside
// the strict output subset, and decides the object form by that same root type, so the
// Provider is constrained by exactly the Schema the Runtime later validates against.
//
// Every other request, a text-form Agent with or without a Schema and Text Generation's
// string Schema alike, gets a nullable string whose value is the answer text itself, not a
// JSON encoding of it, described by description.
func outputMember(finalOutputSchema json.RawMessage, description string) any {
	plain := map[string]any{"type": []string{"string", "null"}, "description": description}
	if !present(finalOutputSchema) {
		return plain
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(finalOutputSchema, &root); err != nil {
		return plain
	}
	if !bytes.Equal(bytes.TrimSpace(root["type"]), []byte(`"object"`)) {
		return plain
	}
	root["type"] = json.RawMessage(`["object","null"]`)
	return root
}

// parse turns the model's message content into a normalised Decision. Content that is not
// JSON, does not satisfy the strict envelope, or carries a JSON-encoded member that is not
// JSON cannot be normalised; the caller reports that as a model error.
//
// output is taken as the JSON value the model wrote: a string becomes a JSON string, an
// object stays that object, and null means no output. It is never unwrapped, re-encoded or
// checked against the final output Schema here: a value of the wrong shape is still a
// normalised Decision, which the Runtime rejects as an invalid action.
func (s strictDecision) parse(content string) (registry.ModelDecision, error) {
	if !json.Valid([]byte(content)) {
		return registry.ModelDecision{}, errors.New("message content is not JSON")
	}
	if err := registry.ValidateValue(s.validator, json.RawMessage(content)); err != nil {
		return registry.ModelDecision{}, errors.New("message content does not match the strict Decision schema")
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &members); err != nil {
		return registry.ModelDecision{}, errors.New("message content is not a Decision object")
	}

	decision := registry.ModelDecision{}
	for name, value := range members {
		if !present(value) {
			continue
		}
		if name == memberOutput {
			decision.Output = json.RawMessage(compact(value))
			continue
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return registry.ModelDecision{}, fmt.Errorf("decision member %q is not a string", name)
		}
		var raw json.RawMessage
		if s.encoded[name] {
			if !json.Valid([]byte(text)) {
				return registry.ModelDecision{}, fmt.Errorf("decision member %q is not a JSON-encoded value", name)
			}
			raw = json.RawMessage(text)
		}
		switch name {
		case memberKind:
			decision.Kind = registry.DecisionKind(text)
		case memberTool:
			decision.ToolName = &text
		case memberArguments:
			decision.Arguments = raw
		case memberStatePatch:
			decision.StatePatch = raw
		}
	}
	return decision, nil
}
