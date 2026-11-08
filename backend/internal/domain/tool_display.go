package domain

import (
	"fmt"
	"strings"
)

// Display declarations tell Studio what of a Tool call a person wants to see, without
// Studio knowing any Tool by name and without the Trace carrying a call's whole arguments
// or result. A Tool declares the few members worth showing and where the media it produced
// is addressed; the Agent Trace projection reads those declarations from the Registry and
// copies only the named members, bounded and credential-checked, out of the persisted
// documents. The Runtime never reads these declarations and a model request never
// carries them.

// ToolMediaLink says where a successful result of the Tool carries one media reference
// and the URL that serves it. RefPointer and URLPointer are JSON Pointers into the result;
// the reference is the value a final output or a fact names the media by (an artifact id,
// a digest, or the URL itself), so Studio can match it exactly instead of building a URL
// from it. Exactly one of MediaTypePointer (a result member holding the media type) or
// MediaType (a literal, for a Tool whose result carries none) is set.
type ToolMediaLink struct {
	RefPointer       string `json:"refPointer"`
	URLPointer       string `json:"urlPointer"`
	MediaTypePointer string `json:"mediaTypePointer,omitempty"`
	MediaType        string `json:"mediaType,omitempty"`
}

// ToolDisplayField names one member of a Tool call's arguments or result that Studio
// shows on a run card, under Title. It follows the fact pointer pattern: Source says which
// document the pointer reads. A result member is only ever shown from a SUCCEEDED Attempt.
type ToolDisplayField struct {
	Source  FactPointerSource `json:"source"`
	Pointer string            `json:"pointer"`
	Title   string            `json:"title"`
}

// validateToolMediaLinks checks the shape of a Tool's media link declarations. Whether
// each pointer lands inside the Output Schema is checked by the Registry.
func validateToolMediaLinks(links []ToolMediaLink) []error {
	var errs []error
	for i, link := range links {
		field := fmt.Sprintf("mediaLinks[%d]", i)
		if err := ValidateJSONPointer(link.RefPointer); err != nil {
			errs = append(errs, fmt.Errorf("%s.refPointer: %w", field, err))
		}
		if err := ValidateJSONPointer(link.URLPointer); err != nil {
			errs = append(errs, fmt.Errorf("%s.urlPointer: %w", field, err))
		}
		switch {
		case link.MediaTypePointer == "" && link.MediaType == "":
			errs = append(errs, fmt.Errorf("%s: exactly one of mediaTypePointer or mediaType must be set, got neither", field))
		case link.MediaTypePointer != "" && link.MediaType != "":
			errs = append(errs, fmt.Errorf("%s: exactly one of mediaTypePointer or mediaType must be set, got both", field))
		case link.MediaTypePointer != "":
			if err := ValidateJSONPointer(link.MediaTypePointer); err != nil {
				errs = append(errs, fmt.Errorf("%s.mediaTypePointer: %w", field, err))
			}
		default:
			if !isMediaTypeLiteral(link.MediaType) {
				errs = append(errs, fmt.Errorf("%s.mediaType must be a type/subtype media type", field))
			}
		}
	}
	return errs
}

// isMediaTypeLiteral accepts a plain "type/subtype" media type: one slash, both halves
// non-empty, printable ASCII without spaces or parameters.
func isMediaTypeLiteral(value string) bool {
	kind, subtype, ok := strings.Cut(value, "/")
	if !ok || kind == "" || subtype == "" || strings.ContainsAny(subtype, "/;, ") {
		return false
	}
	for _, r := range value {
		if r <= ' ' || r > '~' {
			return false
		}
	}
	return true
}

// validateToolDisplay checks the shape of a Tool's display field declarations: a valid
// source and pointer, a required single-line title within the display title bound, and no
// member declared twice. Whether each pointer lands inside the schema of its source is
// checked by the Registry.
func validateToolDisplay(fields []ToolDisplayField) []error {
	var errs []error
	seen := make(map[FactPointer]struct{}, len(fields))
	for i, field := range fields {
		label := fmt.Sprintf("display[%d]", i)
		pointer := FactPointer{Source: field.Source, Pointer: field.Pointer}
		errs = append(errs, validateFactPointer(label, pointer)...)
		if field.Title == "" {
			errs = append(errs, fmt.Errorf("%s.title is empty", label))
		} else if err := ValidateDisplayLine(field.Title, MaxDisplayTitleLength); err != nil {
			errs = append(errs, fmt.Errorf("%s.title: %w", label, err))
		}
		if _, duplicate := seen[pointer]; duplicate {
			errs = append(errs, fmt.Errorf("%s: duplicate declaration of %s %s", label, field.Source, field.Pointer))
			continue
		}
		seen[pointer] = struct{}{}
	}
	return errs
}

// LookupJSONPointer evaluates a syntactically valid, non-empty JSON Pointer against a
// decoded JSON document (maps, slices and scalars as encoding/json produces them). found
// is false when any token names a missing member, an out-of-range or non-canonical array
// index, or descends into a scalar. A found null is reported as (nil, true).
func LookupJSONPointer(document any, pointer string) (any, bool) {
	current := document
	for _, token := range JSONPointerTokens(pointer) {
		switch node := current.(type) {
		case map[string]any:
			next, ok := node[token]
			if !ok {
				return nil, false
			}
			current = next
		case []any:
			index, ok := arrayIndex(token)
			if !ok || index >= len(node) {
				return nil, false
			}
			current = node[index]
		default:
			return nil, false
		}
	}
	return current, true
}

// arrayIndex parses a canonical, non-negative decimal array index: no sign, no leading
// zero except "0" itself.
func arrayIndex(token string) (int, bool) {
	if token == "" || (len(token) > 1 && token[0] == '0') {
		return 0, false
	}
	index := 0
	for _, r := range token {
		if r < '0' || r > '9' {
			return 0, false
		}
		index = index*10 + int(r-'0')
		if index > 1<<30 {
			return 0, false
		}
	}
	return index, true
}
