package runtime

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRunSubmissionDigest_KeyOrderAndWhitespace_SameDigest(t *testing.T) {
	a, err := RunSubmissionDigest(1, json.RawMessage(`{"brief":"ember","options":{"a":1,"b":[1.50,2]}}`))
	if err != nil {
		t.Fatalf("digest a: %v", err)
	}
	b, err := RunSubmissionDigest(1, json.RawMessage("{ \"options\": {\"b\": [1.50, 2], \"a\": 1},\n \"brief\": \"ember\" }"))
	if err != nil {
		t.Fatalf("digest b: %v", err)
	}
	if a != b {
		t.Fatalf("reordered keys and whitespace changed the digest: %s vs %s", a, b)
	}
	if !strings.HasPrefix(a, "sha256:") || len(a) != len("sha256:")+64 {
		t.Fatalf("digest format: want sha256:<64 hex>, got %q", a)
	}
}

func TestRunSubmissionDigest_DifferentVersionOrInput_DifferentDigest(t *testing.T) {
	base, err := RunSubmissionDigest(1, json.RawMessage(`{"brief":"ember"}`))
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	for name, tc := range map[string]struct {
		version int
		input   string
	}{
		"other version": {2, `{"brief":"ember"}`},
		"other input":   {1, `{"brief":"embers"}`},
		"number text":   {1, `{"brief":"ember","n":1.0}`},
	} {
		got, err := RunSubmissionDigest(tc.version, json.RawMessage(tc.input))
		if err != nil {
			t.Fatalf("%s: digest: %v", name, err)
		}
		if got == base {
			t.Fatalf("%s: want a different digest from the base submission", name)
		}
	}
}

func TestRunSubmissionDigest_MalformedInput_Error(t *testing.T) {
	if _, err := RunSubmissionDigest(1, json.RawMessage(`{"brief":`)); err == nil {
		t.Fatal("malformed input: want an error, got nil")
	}
}
