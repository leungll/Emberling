package runtime

import (
	"encoding/json"
	"testing"
)

func TestStatePatch_NullField_DeletesKey(t *testing.T) {
	result, changed, err := ApplyStatePatch(json.RawMessage(`{"a":1,"b":2}`), json.RawMessage(`{"b":null}`))
	if err != nil {
		t.Fatalf("ApplyStatePatch() error = %v, want nil", err)
	}
	if got, want := string(result), `{"a":1}`; got != want {
		t.Fatalf("ApplyStatePatch() result = %s, want %s", got, want)
	}
	if !changed {
		t.Fatal("ApplyStatePatch() changed = false, want true when a key is deleted")
	}
}

func TestStatePatch_NestedObject_MergesRecursively(t *testing.T) {
	current := json.RawMessage(`{"turns":1,"user":{"name":"ember","tier":"gold"}}`)
	result, changed, err := ApplyStatePatch(current, json.RawMessage(`{"user":{"tier":"platinum"}}`))
	if err != nil {
		t.Fatalf("ApplyStatePatch() error = %v, want nil", err)
	}
	want := `{"turns":1,"user":{"name":"ember","tier":"platinum"}}`
	if got := string(result); got != want {
		t.Fatalf("ApplyStatePatch() result = %s, want %s", got, want)
	}
	if !changed {
		t.Fatal("ApplyStatePatch() changed = false, want true")
	}
}

func TestStatePatch_ArrayValue_ReplacesWhole(t *testing.T) {
	result, changed, err := ApplyStatePatch(json.RawMessage(`{"items":[1,2,3]}`), json.RawMessage(`{"items":[9]}`))
	if err != nil {
		t.Fatalf("ApplyStatePatch() error = %v, want nil", err)
	}
	if got, want := string(result), `{"items":[9]}`; got != want {
		t.Fatalf("ApplyStatePatch() result = %s, want %s (arrays replace whole, RFC 7386)", got, want)
	}
	if !changed {
		t.Fatal("ApplyStatePatch() changed = false, want true")
	}
}

func TestStatePatch_ResultUnchanged_ReportsNoChange(t *testing.T) {
	current := json.RawMessage(`{"a":1,"b":{"x":1,"y":2}}`)
	// The patch restates every value the State already holds and writes the nested
	// object's keys in the opposite order: object key order is not a change.
	result, changed, err := ApplyStatePatch(current, json.RawMessage(`{"b":{"y":2,"x":1},"a":1}`))
	if err != nil {
		t.Fatalf("ApplyStatePatch() error = %v, want nil", err)
	}
	if changed {
		t.Fatalf("ApplyStatePatch() changed = true, want false; result = %s", result)
	}
	if got, want := string(result), `{"a":1,"b":{"x":1,"y":2}}`; got != want {
		t.Fatalf("ApplyStatePatch() result = %s, want %s", got, want)
	}
}

func TestStatePatch_RootNotObject_Fails(t *testing.T) {
	cases := []struct {
		name    string
		current json.RawMessage
		patch   json.RawMessage
	}{
		{"patch is an array", json.RawMessage(`{}`), json.RawMessage(`[1,2]`)},
		{"patch is a string", json.RawMessage(`{}`), json.RawMessage(`"replaced"`)},
		{"patch is null", json.RawMessage(`{}`), json.RawMessage(`null`)},
		{"patch is not JSON", json.RawMessage(`{}`), json.RawMessage(`{`)},
		{"current is an array", json.RawMessage(`[]`), json.RawMessage(`{"a":1}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ApplyStatePatch(tc.current, tc.patch)
			if err == nil {
				t.Fatal("ApplyStatePatch() error = nil, want error for a non-object root")
			}
			if _, ok := AsInvalidActionError(err); !ok {
				t.Fatalf("ApplyStatePatch() error = %v, want an *InvalidActionError the service maps to INVALID_ACTION", err)
			}
		})
	}
}

func TestStatePatch_EmptyPatch_NoChange(t *testing.T) {
	current := json.RawMessage(`{"a":1}`)
	for _, patch := range []json.RawMessage{nil, json.RawMessage(``), json.RawMessage(`{}`)} {
		result, changed, err := ApplyStatePatch(current, patch)
		if err != nil {
			t.Fatalf("ApplyStatePatch(%q) error = %v, want nil", patch, err)
		}
		if changed {
			t.Fatalf("ApplyStatePatch(%q) changed = true, want false", patch)
		}
		if got, want := string(result), `{"a":1}`; got != want {
			t.Fatalf("ApplyStatePatch(%q) result = %s, want %s", patch, got, want)
		}
	}
}

func TestStatePatch_NullForAbsentKey_NoChange(t *testing.T) {
	// RFC 7386: removing a member that is not there is a no-op, so the Action commits no
	// new State Version.
	result, changed, err := ApplyStatePatch(json.RawMessage(`{"a":1}`), json.RawMessage(`{"b":null}`))
	if err != nil {
		t.Fatalf("ApplyStatePatch() error = %v, want nil", err)
	}
	if changed {
		t.Fatalf("ApplyStatePatch() changed = true, want false; result = %s", result)
	}
	if got, want := string(result), `{"a":1}`; got != want {
		t.Fatalf("ApplyStatePatch() result = %s, want %s", got, want)
	}
}

func TestStatePatch_ObjectOverNonObject_ReplacesAndMergesNested(t *testing.T) {
	// RFC 7386: when the target member is not an object, the patch object is merged into
	// an empty object instead. The nested null still deletes.
	current := json.RawMessage(`{"user":"ember","seen":{"a":1,"b":2}}`)
	patch := json.RawMessage(`{"user":{"name":"ember"},"seen":{"b":null}}`)
	result, changed, err := ApplyStatePatch(current, patch)
	if err != nil {
		t.Fatalf("ApplyStatePatch() error = %v, want nil", err)
	}
	want := `{"seen":{"a":1},"user":{"name":"ember"}}`
	if got := string(result); got != want {
		t.Fatalf("ApplyStatePatch() result = %s, want %s", got, want)
	}
	if !changed {
		t.Fatal("ApplyStatePatch() changed = false, want true")
	}
}

func TestStatePatch_NumbersKeepTheirLiteralForm(t *testing.T) {
	// A patch must not silently round or reformat a value it does not touch: a float64
	// round-trip would rewrite these two literals and produce a spurious State Version.
	current := json.RawMessage(`{"big":12345678901234567890,"exact":0.1000000000000000055511151231257827}`)
	result, changed, err := ApplyStatePatch(current, json.RawMessage(`{"note":"kept"}`))
	if err != nil {
		t.Fatalf("ApplyStatePatch() error = %v, want nil", err)
	}
	if !changed {
		t.Fatal("ApplyStatePatch() changed = false, want true")
	}
	want := `{"big":12345678901234567890,"exact":0.1000000000000000055511151231257827,"note":"kept"}`
	if got := string(result); got != want {
		t.Fatalf("ApplyStatePatch() result = %s, want %s", got, want)
	}
}

func TestValidateAgentState_SchemaViolation_Fails(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"turns":{"type":"integer"}},"required":["turns"],"additionalProperties":false}`)

	if err := ValidateAgentState(json.RawMessage(`{"turns":2}`), schema); err != nil {
		t.Fatalf("ValidateAgentState() error = %v, want nil for a State the frozen Schema accepts", err)
	}

	// The whole patched State is validated, not the patch: a patch that only sets `extra`
	// still fails because the merged result violates the frozen Schema.
	patched, _, err := ApplyStatePatch(json.RawMessage(`{"turns":2}`), json.RawMessage(`{"extra":true}`))
	if err != nil {
		t.Fatalf("ApplyStatePatch() error = %v, want nil", err)
	}
	err = ValidateAgentState(patched, schema)
	if err == nil {
		t.Fatal("ValidateAgentState() error = nil, want error for a patched State the frozen State Schema rejects")
	}
	if _, ok := AsInvalidActionError(err); !ok {
		t.Fatalf("ValidateAgentState() error = %v, want an *InvalidActionError", err)
	}
}

func TestValidateAgentState_NoSchema_AcceptsAnyObject(t *testing.T) {
	for _, schema := range []json.RawMessage{nil, json.RawMessage(``)} {
		if err := ValidateAgentState(json.RawMessage(`{"anything":[1,2]}`), schema); err != nil {
			t.Fatalf("ValidateAgentState(no schema) error = %v, want nil", err)
		}
	}
	// An empty State is Version 0's empty object, not a missing State.
	if err := ValidateAgentState(nil, json.RawMessage(`{"type":"object"}`)); err != nil {
		t.Fatalf("ValidateAgentState(empty state) error = %v, want nil", err)
	}
}
