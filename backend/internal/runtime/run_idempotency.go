package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// RunSubmissionDigest returns "sha256:" followed by the lowercase hex SHA-256 of the
// canonical JSON of {definitionVersion, input}: the content a Run submission key is bound
// to. Object keys are sorted at every depth and numbers keep their literal text, so key
// order and whitespace never change the digest, while a different Definition version or
// any other input value does. An absent input digests as JSON null.
func RunSubmissionDigest(definitionVersion int, input json.RawMessage) (string, error) {
	var decoded any
	if len(bytes.TrimSpace(input)) > 0 {
		doc, err := decodeFactDocument(input)
		if err != nil {
			return "", fmt.Errorf("run submission digest: input is not a single valid JSON document: %w", err)
		}
		decoded = doc
	}
	encoded, err := encodeCanonical(canonicalObject{
		{Key: "definitionVersion", Value: definitionVersion},
		{Key: "input", Value: decoded},
	})
	if err != nil {
		return "", fmt.Errorf("run submission digest: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
