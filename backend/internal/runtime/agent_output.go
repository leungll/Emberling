package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// AgentOutputForm is the form an Agent's outputSchema gives its FINAL output. It is
// decided once, by ParseAgentNodeConfig, for Definition Validate, Save and Run creation
// alike, and it travels with the outputSchema frozen on the Agent Run.
type AgentOutputForm string

const (
	// AgentOutputText is an Agent without an outputSchema, or whose outputSchema accepts
	// the string "". Its FINAL output is text, and the `text` port carries that text.
	AgentOutputText AgentOutputForm = "TEXT"
	// AgentOutputObject is an Agent whose outputSchema has the root type "object" and
	// stays inside the strict output subset. Its FINAL output is a JSON object, and the
	// `text` port carries that object's canonical JSON text.
	AgentOutputObject AgentOutputForm = "OBJECT"
)

// FinalOutputText is the text an Agent's `text` port carries for one validated FINAL
// output, read back from the committed Decision.
//
// A JSON string is the answer itself and is returned decoded; a string whose content
// happens to be JSON is kept exactly as written and never unwrapped. Any other value -- the
// object of an object-form Agent, or a non-string accepted by an older text-form Schema
// such as {} -- becomes its canonical JSON text: object keys sorted by their bytes at every
// level, no insignificant whitespace, no HTML escaping, every number kept as the literal
// it was saved with and array order unchanged.
//
// The result depends only on the committed value, so immediate advancement and the
// Reconciler completing the same Final Action produce byte-identical text.
func FinalOutputText(output json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 {
		return "", errors.New("final output text: the output is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", fmt.Errorf("final output text: the output is not valid JSON: %w", err)
	}
	if decoder.More() {
		return "", errors.New("final output text: the output carries trailing JSON content")
	}
	if text, ok := value.(string); ok {
		return text, nil
	}
	encoded, err := encodeCanonical(value)
	if err != nil {
		return "", fmt.Errorf("final output text: encode canonical JSON: %w", err)
	}
	return string(encoded), nil
}
