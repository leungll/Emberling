package mockprovider

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDelaySpec_UnmarshalJSON_AbsentFieldDefaultsToImmediate(t *testing.T) {
	var req taskRequest
	if err := json.Unmarshal([]byte(`{"callbackUrl":"http://x","callbackToken":"t"}`), &req); err != nil {
		t.Fatalf("Unmarshal() error = %v, want nil", err)
	}
	if req.DelayMs.Mode != delayImmediate {
		t.Fatalf("DelayMs.Mode = %v, want delayImmediate when the field is absent", req.DelayMs.Mode)
	}
}

func TestDelaySpec_UnmarshalJSON_ZeroIsImmediate(t *testing.T) {
	var d delaySpec
	if err := json.Unmarshal([]byte(`0`), &d); err != nil {
		t.Fatalf("Unmarshal() error = %v, want nil", err)
	}
	if d.Mode != delayImmediate {
		t.Fatalf("Mode = %v, want delayImmediate", d.Mode)
	}
}

func TestDelaySpec_UnmarshalJSON_PositiveNumberIsDelayAfterWithDuration(t *testing.T) {
	var d delaySpec
	if err := json.Unmarshal([]byte(`250`), &d); err != nil {
		t.Fatalf("Unmarshal() error = %v, want nil", err)
	}
	if d.Mode != delayAfter {
		t.Fatalf("Mode = %v, want delayAfter", d.Mode)
	}
	if d.Duration != 250*time.Millisecond {
		t.Fatalf("Duration = %v, want 250ms", d.Duration)
	}
}

func TestDelaySpec_UnmarshalJSON_NegativeNumberIsRejected(t *testing.T) {
	var d delaySpec
	if err := json.Unmarshal([]byte(`-1`), &d); err == nil {
		t.Fatal("Unmarshal() error = nil, want error for a negative delayMs")
	}
}

func TestDelaySpec_UnmarshalJSON_SentinelStringsAreRecognised(t *testing.T) {
	cases := map[string]delayMode{
		`"lost"`:           delayLost,
		`"duplicate"`:      delayDuplicate,
		`"beforeResponse"`: delayBeforeResponse,
	}
	for raw, want := range cases {
		var d delaySpec
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			t.Fatalf("Unmarshal(%s) error = %v, want nil", raw, err)
		}
		if d.Mode != want {
			t.Fatalf("Unmarshal(%s) Mode = %v, want %v", raw, d.Mode, want)
		}
	}
}

func TestDelaySpec_UnmarshalJSON_UnknownStringIsRejected(t *testing.T) {
	var d delaySpec
	if err := json.Unmarshal([]byte(`"immediately-please"`), &d); err == nil {
		t.Fatal("Unmarshal() error = nil, want error for an unrecognised sentinel string")
	}
}

func TestDelaySpec_UnmarshalJSON_WrongJSONTypeIsRejected(t *testing.T) {
	var d delaySpec
	if err := json.Unmarshal([]byte(`{"not":"a scalar"}`), &d); err == nil {
		t.Fatal("Unmarshal() error = nil, want error for a non-number, non-string value")
	}
}

func TestOutcome_UnmarshalJSON_AbsentFieldDefaultsToSucceeded(t *testing.T) {
	var req taskRequest
	if err := json.Unmarshal([]byte(`{"callbackUrl":"http://x","callbackToken":"t"}`), &req); err != nil {
		t.Fatalf("Unmarshal() error = %v, want nil", err)
	}
	if req.Outcome != outcomeSucceeded {
		t.Fatalf("Outcome = %v, want outcomeSucceeded when the field is absent", req.Outcome)
	}
}

func TestOutcome_UnmarshalJSON_SentinelStringsAreRecognised(t *testing.T) {
	cases := map[string]outcome{
		`"succeeded"`:   outcomeSucceeded,
		`"failed"`:      outcomeFailed,
		`"wrong-token"`: outcomeWrongToken,
	}
	for raw, want := range cases {
		var got outcome
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("Unmarshal(%s) error = %v, want nil", raw, err)
		}
		if got != want {
			t.Fatalf("Unmarshal(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestOutcome_UnmarshalJSON_UnknownValueIsRejected(t *testing.T) {
	var got outcome
	if err := json.Unmarshal([]byte(`"exploded"`), &got); err == nil {
		t.Fatal("Unmarshal() error = nil, want error for an unrecognised outcome")
	}
	if err := json.Unmarshal([]byte(`7`), &got); err == nil {
		t.Fatal("Unmarshal() error = nil, want error for a non-string outcome")
	}
}

func TestOutcome_MarshalJSON_RoundTripsThroughTaskRequest(t *testing.T) {
	encoded, err := json.Marshal(taskRequest{CallbackURL: "http://x", CallbackToken: "t", Outcome: outcomeWrongToken})
	if err != nil {
		t.Fatalf("Marshal() error = %v, want nil", err)
	}
	var req taskRequest
	if err := json.Unmarshal(encoded, &req); err != nil {
		t.Fatalf("Unmarshal(%s) error = %v, want nil", encoded, err)
	}
	if req.Outcome != outcomeWrongToken {
		t.Fatalf("round-tripped Outcome = %v, want outcomeWrongToken", req.Outcome)
	}
}
