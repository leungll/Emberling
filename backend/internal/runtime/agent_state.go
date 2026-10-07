package runtime

import (
	"bytes"
	"encoding/json"
)

// ApplyStatePatch applies a Decision's optional state patch to the current Agent State
// with JSON Merge Patch (RFC 7386) semantics, as the Agent State contract requires: the
// State root must be a JSON object, object fields merge recursively, an array replaces the
// previous value whole, and a field whose patch value is null is removed from the result.
//
// It is a pure decision: the caller validates the returned result against the Agent Run's
// frozen State Schema and decides, inside its own transaction, whether to commit a new
// State Version. changed is JSON-semantic: object key order is not a change, so a patch
// that restates the current values produces changed == false and no new State Version.
// An absent or empty patch leaves the State unchanged.
//
// result is always canonical (object keys sorted), so a caller can compare or persist it
// byte-for-byte. When changed is false the caller keeps the existing State Version and
// must not persist result.
//
// Every failure is an *InvalidActionError: a patch that cannot be applied fails the Action
// as INVALID_ACTION and creates no State Version.
func ApplyStatePatch(current, patch json.RawMessage) (json.RawMessage, bool, error) {
	// Version 0 of the State chain is the empty object, so an empty current value is that
	// object rather than a missing State.
	currentValue, err := decodeJSONObject(current, "state", `{}`)
	if err != nil {
		return nil, false, err
	}
	currentEncoded, err := encodeCanonical(currentValue)
	if err != nil {
		return nil, false, &InvalidActionError{Subject: "state patch", Message: "current state cannot be re-encoded", Err: err}
	}

	if len(bytes.TrimSpace(patch)) == 0 {
		return currentEncoded, false, nil
	}

	patchValue, err := decodeJSONObject(patch, "state patch", "")
	if err != nil {
		return nil, false, err
	}

	merged := mergeStatePatch(currentValue, patchValue)
	mergedEncoded, err := encodeCanonical(merged)
	if err != nil {
		return nil, false, &InvalidActionError{Subject: "state patch", Message: "patched state cannot be encoded", Err: err}
	}
	return mergedEncoded, !bytes.Equal(currentEncoded, mergedEncoded), nil
}

// ValidateAgentState is the execution-time check of the *whole* patched State against the
// Agent Run's frozen State Schema (the Runtime applies the patch to the current State
// Version, then validates the complete result against the State Schema rather than
// validating the patch alone). It completes the trio of execution-time checks, alongside
// ValidateToolCall and ValidateFinalOutput; the caller passes the result ApplyStatePatch
// returned, never the patch alone.
//
// An absent State Schema constrains nothing beyond the root-object rule ApplyStatePatch
// already enforces. A violation is an *InvalidActionError: the Action fails as
// INVALID_ACTION and no State Version is created.
func ValidateAgentState(state, stateSchema json.RawMessage) error {
	if len(bytes.TrimSpace(stateSchema)) == 0 {
		return nil
	}
	value, err := decodeJSONObject(state, "state", `{}`)
	if err != nil {
		return err
	}
	encoded, err := encodeCanonical(value)
	if err != nil {
		return &InvalidActionError{Subject: "state patch", Message: "patched state cannot be encoded", Err: err}
	}
	if err := validateAgainstSchema(stateSchema, encoded); err != nil {
		return &InvalidActionError{Subject: "state patch", Message: "patched state does not satisfy the frozen State Schema", Err: err}
	}
	return nil
}

// mergeStatePatch is RFC 7386's MergePatch, restricted to the values encoding/json
// produces. target and patch are never mutated: every object the patch touches is copied,
// so the caller can still compare the pre-patch value afterwards.
func mergeStatePatch(target, patch any) any {
	patchObject, ok := patch.(map[string]any)
	if !ok {
		// Arrays and scalars replace the previous value whole.
		return patch
	}
	targetObject, ok := target.(map[string]any)
	if !ok {
		targetObject = map[string]any{}
	}

	result := make(map[string]any, len(targetObject)+len(patchObject))
	for key, value := range targetObject {
		result[key] = value
	}
	for key, value := range patchObject {
		if value == nil {
			delete(result, key)
			continue
		}
		result[key] = mergeStatePatch(result[key], value)
	}
	return result
}

// decodeJSONObject decodes raw as a JSON object. An empty raw decodes to whenEmpty (itself
// a JSON document) when one is given, and is rejected otherwise. Numbers are kept as
// json.Number so a patch cannot silently round a value the model or an earlier Tool wrote.
func decodeJSONObject(raw json.RawMessage, subject, whenEmpty string) (map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		if whenEmpty == "" {
			return nil, &InvalidActionError{Subject: subject, Message: "value is empty"}
		}
		trimmed = []byte(whenEmpty)
	}

	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, &InvalidActionError{Subject: subject, Message: "value is not valid JSON", Err: err}
	}
	if decoder.More() {
		return nil, &InvalidActionError{Subject: subject, Message: "value carries trailing JSON content"}
	}

	object, ok := value.(map[string]any)
	if !ok {
		return nil, &InvalidActionError{Subject: subject, Message: "root value is not a JSON object"}
	}
	return object, nil
}
