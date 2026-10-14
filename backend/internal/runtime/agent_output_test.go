package runtime

import (
	"encoding/json"
	"testing"
)

func TestFinalOutputText_ObjectForm_IsCanonical(t *testing.T) {
	// PostgreSQL returns a stored JSONB object with its own key order and spacing; the
	// canonical text must not depend on either.
	output := json.RawMessage(`{"zeta": [3, 1, 2], "alpha": {"b": 1.50, "a": "<b> & </b>"},
		"Big": 123456789012345678901234567890, "small": 0.000001, "neg": -0.0, "e": "é☃"}`)
	got, err := FinalOutputText(output)
	if err != nil {
		t.Fatalf("FinalOutputText() error = %v", err)
	}
	const want = `{"Big":123456789012345678901234567890,"alpha":{"a":"<b> & </b>","b":1.50},"e":"é☃","neg":-0.0,"small":0.000001,"zeta":[3,1,2]}`
	if got != want {
		t.Errorf("FinalOutputText() =\n  %s\nwant\n  %s", got, want)
	}

	reordered := json.RawMessage(`{"small":0.000001,"neg":-0.0,"zeta":[3,1,2],"e":"é☃","Big":123456789012345678901234567890,"alpha":{"a":"<b> & </b>","b":1.50}}`)
	again, err := FinalOutputText(reordered)
	if err != nil {
		t.Fatalf("FinalOutputText(reordered) error = %v", err)
	}
	if again != got {
		t.Errorf("FinalOutputText() differs across key orders:\n  %s\n  %s", got, again)
	}
}

func TestFinalOutputText_TextForm_IsTheStringItself(t *testing.T) {
	got, err := FinalOutputText(json.RawMessage(`"line one\n<done> & \"quoted\""`))
	if err != nil {
		t.Fatalf("FinalOutputText() error = %v", err)
	}
	if want := "line one\n<done> & \"quoted\""; got != want {
		t.Errorf("FinalOutputText() = %q, want the decoded string %q", got, want)
	}

	// A double-encoded answer is text whose content happens to be JSON: it is kept as
	// written and never unwrapped.
	got, err = FinalOutputText(json.RawMessage(`"{\"b\":1,\"a\":2}"`))
	if err != nil {
		t.Fatalf("FinalOutputText() error = %v", err)
	}
	if want := `{"b":1,"a":2}`; got != want {
		t.Errorf("FinalOutputText() = %q, want the string content unchanged %q", got, want)
	}
}

func TestFinalOutputText_InvalidJSON_Fails(t *testing.T) {
	for _, output := range []string{``, `{"a":`, `{"a":1} {"b":2}`} {
		if _, err := FinalOutputText(json.RawMessage(output)); err == nil {
			t.Errorf("FinalOutputText(%q) error = nil, want an error", output)
		}
	}
}
