package api

import (
	"encoding/json"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// TestLatestStateDTO_BoundDecision pins the pure projection of the latest State Version:
// a value whose canonical encoding fits the bound is returned compacted, one above it is
// withheld as TOO_LARGE with a null value, and a missing or invalid saved value is shown
// as the empty object the State chain starts from.
func TestLatestStateDTO_BoundDecision(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		bound    int
		want     string
		withheld string
	}{
		{name: "fits", value: `{"plan": ["a", "b"]}`, bound: 64, want: `{"plan":["a","b"]}`},
		{name: "exactly at bound", value: `{"a":1}`, bound: 7, want: `{"a":1}`},
		{name: "one over bound", value: `{"a":12}`, bound: 7, want: `null`, withheld: withheldTooLarge},
		{name: "whitespace does not count", value: `{ "a" : 1 }`, bound: 7, want: `{"a":1}`},
		{name: "empty saved value is version zero object", value: ``, bound: 7, want: `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dto := toLatestStateDTO(domain.AgentStateVersion{Version: 3, Value: json.RawMessage(tc.value)}, tc.bound)
			body, err := json.Marshal(dto)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got struct {
				Version  int             `json:"version"`
				Value    json.RawMessage `json:"value"`
				Withheld string          `json:"withheld"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("decode %s: %v", body, err)
			}
			if got.Version != 3 {
				t.Errorf("version = %d, want 3", got.Version)
			}
			if string(got.Value) != tc.want {
				t.Errorf("value = %s, want %s", got.Value, tc.want)
			}
			if got.Withheld != tc.withheld {
				t.Errorf("withheld = %q, want %q", got.Withheld, tc.withheld)
			}
		})
	}
}
