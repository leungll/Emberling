package mediaresult

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
)

// settingsDigest returns the digest of a settings object exactly as the Runtime computes
// it: "sha256:" followed by the lowercase hex SHA-256 of the object's canonical JSON, in
// which numbers keep their literal text, object keys are sorted at every depth, HTML
// characters are not escaped and no trailing newline follows. Node packages may depend
// only on the registry contract and domain types, so the canonical form is restated here;
// a test pins it to the Runtime's digest. ok is false unless settings is exactly one JSON
// object.
func settingsDigest(settings json.RawMessage) (string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(settings))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", false
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return "", false
	}
	object, isObject := value.(map[string]any)
	if !isObject {
		return "", false
	}
	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(object); err != nil {
		return "", false
	}
	sum := sha256.Sum256(bytes.TrimSuffix(canonical.Bytes(), []byte("\n")))
	return "sha256:" + hex.EncodeToString(sum[:]), true
}
