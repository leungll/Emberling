package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// validateToolFactPointers checks that every fact pointer a Tool declares lands inside the
// schema of the document it reads: ARGUMENTS pointers and requirement subjects inside the
// Input Schema, RESULT pointers inside the Output Schema. A pointer that can never select
// a value would make the declaration silently unsatisfiable, so it fails registration.
// Pointer syntax is reported by domain.ToolMetadata.Validate and skipped here.
func validateToolFactPointers(metadata domain.ToolMetadata) []error {
	var errs []error
	check := func(field string, pointer domain.FactPointer) {
		if domain.ValidateJSONPointer(pointer.Pointer) != nil {
			return
		}
		var schema json.RawMessage
		var schemaName string
		switch pointer.Source {
		case domain.FactPointerArguments:
			schema, schemaName = metadata.InputSchema, "inputSchema"
		case domain.FactPointerResult:
			schema, schemaName = metadata.OutputSchema, "outputSchema"
		default:
			return
		}
		if err := schemaPointerResolves(schema, pointer.Pointer); err != nil {
			errs = append(errs, fmt.Errorf("tool %q: %s: pointer %q is outside the %s: %w", metadata.Name, field, pointer.Pointer, schemaName, err))
		}
	}

	if produces := metadata.Produces; produces != nil {
		check("produces.subjectPointer", produces.SubjectPointer)
		for _, name := range sortedFactBindingNames(produces.BindArguments) {
			check(fmt.Sprintf("produces.bindArguments[%q]", name), produces.BindArguments[name])
		}
		if produces.VerdictPointer != nil {
			check("produces.verdictPointer", *produces.VerdictPointer)
		}
	}
	for i, requirement := range metadata.Requires {
		check(fmt.Sprintf("requires[%d].subjectArgument", i), domain.FactPointer{Source: domain.FactPointerArguments, Pointer: requirement.SubjectArgument})
	}
	return errs
}

// schemaPointerResolves walks a JSON Pointer through the `properties`, `items` and
// `additionalProperties` of a JSON Schema document. Where the schema stops constraining
// structure (no `properties` or `items`, or `additionalProperties: true`) any remaining
// path is accepted, because the schema allows values the pointer may select.
func schemaPointerResolves(raw json.RawMessage, pointer string) error {
	var current any
	if err := json.Unmarshal(raw, &current); err != nil {
		return errors.New("schema is not valid JSON")
	}
	for _, token := range domain.JSONPointerTokens(pointer) {
		node, ok := current.(map[string]any)
		if !ok {
			// A boolean schema: true accepts every value, false accepts none.
			if allowed, isBool := current.(bool); isBool && allowed {
				return nil
			}
			return fmt.Errorf("token %q is not allowed by the schema", token)
		}

		properties, hasProperties := node["properties"].(map[string]any)
		items, hasItems := node["items"]
		additional, hasAdditional := node["additionalProperties"]

		if hasProperties {
			if next, found := properties[token]; found {
				current = next
				continue
			}
		}
		if hasItems && isArrayIndexToken(token) {
			if _, tuple := items.([]any); tuple {
				// A positional items list is not walked; the array element is accepted.
				return nil
			}
			current = items
			continue
		}
		if hasAdditional {
			if allowed, isBool := additional.(bool); isBool {
				if allowed {
					return nil
				}
				return fmt.Errorf("token %q is not a declared property", token)
			}
			current = additional
			continue
		}
		if hasProperties || hasItems {
			return fmt.Errorf("token %q is not a declared property or array index", token)
		}
		// The schema declares no structure at this point, so it is open-ended.
		return nil
	}
	return nil
}

func isArrayIndexToken(token string) bool {
	if token == "-" {
		return true
	}
	if token == "" || (len(token) > 1 && token[0] == '0') {
		return false
	}
	_, err := strconv.ParseUint(token, 10, 64)
	return err == nil
}

func sortedFactBindingNames(bindings map[string]domain.FactPointer) []string {
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ValidateFactDeclarations checks the fact declarations that span registrations, once
// every Tool and Node Type is registered: producers may register after their consumers.
// Every fact type a Tool requires or bases its fact on, and every fact type a Node Type
// reads, must be produced by a registered Tool, and every binding a requirement matches
// must be bound by some producer of that fact type. A failure keeps the Backend from
// becoming ready; the Registry never substitutes another producer.
func ValidateFactDeclarations(tools []domain.ToolMetadata, nodes []domain.NodeMetadata) error {
	producers := make(map[string][]domain.FactProduction)
	for _, tool := range tools {
		if tool.Produces != nil && tool.Produces.FactType != "" {
			producers[tool.Produces.FactType] = append(producers[tool.Produces.FactType], *tool.Produces)
		}
	}

	var errs []error
	for _, tool := range tools {
		if tool.Produces != nil && tool.Produces.BasisFactType != "" {
			if _, ok := producers[tool.Produces.BasisFactType]; !ok {
				errs = append(errs, fmt.Errorf("tool %q: produces.basisFactType %q has no registered producer Tool", tool.Name, tool.Produces.BasisFactType))
			}
		}
		for i, requirement := range tool.Requires {
			candidates, ok := producers[requirement.FactType]
			if !ok {
				errs = append(errs, fmt.Errorf("tool %q: requires[%d].factType %q has no registered producer Tool", tool.Name, i, requirement.FactType))
				continue
			}
			for _, name := range requirement.MatchBindings {
				if !anyProducerBinds(candidates, name) {
					errs = append(errs, fmt.Errorf("tool %q: requires[%d].matchBindings %q is not bound by any producer of fact type %q", tool.Name, i, name, requirement.FactType))
				}
			}
		}
	}
	for _, node := range nodes {
		for _, factType := range node.FactInputs {
			if _, ok := producers[factType]; !ok {
				errs = append(errs, fmt.Errorf("node type %q: factInputs %q has no registered producer Tool", node.Type, factType))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("validate fact declarations: %w", errors.Join(errs...))
	}
	return nil
}

func anyProducerBinds(producers []domain.FactProduction, name string) bool {
	for _, producer := range producers {
		if _, ok := producer.BindArguments[name]; ok {
			return true
		}
	}
	return false
}
