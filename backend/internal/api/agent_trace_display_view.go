package api

import (
	"encoding/json"
	"strings"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// ---- Agent Trace: declared Tool-call display fields ----
//
// A Tool's registration declares the few members of a call's arguments and result a person
// wants to see on a run card. This projection copies exactly those members out of the
// persisted documents, under the declared titles, for any Tool, so Studio needs no table
// of Tool names. The whole arguments and result never enter the Trace: an undeclared
// member is not shown, a result member is read only from a SUCCEEDED Attempt (the one
// status whose result is a committed Tool result), and every value is bounded and
// credential-checked before it is served.

// shownFieldDTO is one declared member of a Tool Attempt. Value is the member's JSON as
// persisted, compacted, or null when Withheld says why it was left out: its encoding
// exceeded the field bound (TOO_LARGE, never cut) or a string in it is a URL carrying
// userinfo, a query or a fragment (UNSAFE_URL).
type shownFieldDTO struct {
	Title    string                   `json:"title"`
	Source   domain.FactPointerSource `json:"source"`
	Pointer  string                   `json:"pointer"`
	Value    json.RawMessage          `json:"value"`
	Withheld string                   `json:"withheld,omitempty"`
}

// projectShownFields renders the declared display fields of one Tool Attempt. A field
// whose pointer selects nothing, or selects null, is not shown; its absence is not a
// value. The order is the declaration's.
func projectShownFields(attempt domain.ToolAttempt, fields []domain.ToolDisplayField, maxFieldBytes int) []shownFieldDTO {
	if len(fields) == 0 {
		return nil
	}
	documents := map[domain.FactPointerSource]any{}
	if arguments, ok := decodeDocument(attempt.Input); ok {
		documents[domain.FactPointerArguments] = arguments
	}
	if attempt.Status == domain.ToolAttemptSucceeded {
		if result, ok := decodeDocument(attempt.Result); ok {
			documents[domain.FactPointerResult] = result
		}
	}

	var shown []shownFieldDTO
	for _, field := range fields {
		document, ok := documents[field.Source]
		if !ok {
			continue
		}
		value, found := domain.LookupJSONPointer(document, field.Pointer)
		if !found || value == nil {
			continue
		}
		dto := shownFieldDTO{Title: field.Title, Source: field.Source, Pointer: field.Pointer, Value: json.RawMessage(`null`)}
		encoded, err := json.Marshal(value)
		switch {
		case err != nil:
			continue
		case len(encoded) > maxFieldBytes:
			dto.Withheld = withheldTooLarge
		case carriesUnsafeURL(value):
			dto.Withheld = withheldUnsafeURL
		default:
			dto.Value = encoded
		}
		shown = append(shown, dto)
	}
	return shown
}

// decodeDocument decodes a persisted JSON document, reporting false for an absent column
// or the literal null.
func decodeDocument(raw json.RawMessage) (any, bool) {
	if !hasJSONValue(raw) {
		return nil, false
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, false
	}
	return document, true
}

// carriesUnsafeURL reports whether any string in value, at any depth, reads as an http or
// https URL that fails the credential-free rule. Only URL-shaped strings are judged: a
// digest, a path or a sentence is never a URL and is shown as it is.
func carriesUnsafeURL(value any) bool {
	switch v := value.(type) {
	case string:
		lower := strings.ToLower(v)
		if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			return false
		}
		return !credentialFreeURL(v)
	case []any:
		for _, item := range v {
			if carriesUnsafeURL(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range v {
			if carriesUnsafeURL(item) {
				return true
			}
		}
	}
	return false
}
