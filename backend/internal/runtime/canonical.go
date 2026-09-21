package runtime

import (
	"bytes"
	"encoding/json"
	"sort"
)

// canonicalField is one ordered key/value pair of a canonicalObject. An explicit ordered
// list (rather than a map) lets the generated runInputSchema match
// docs/08-interface-spec.md §1.3 byte-for-byte, including the AssetRef schema's
// non-alphabetical key order (assetId, mediaType, sizeBytes, sha256).
type canonicalField struct {
	Key   string
	Value any
}

// canonicalObject is a JSON object with a caller-fixed key order.
type canonicalObject []canonicalField

// encodeCanonical renders v as canonical, deterministic JSON: no HTML-escaping, no
// trailing newline, and stable key ordering throughout. v may be:
//   - canonicalObject: rendered with the given explicit key order.
//   - []canonicalObject: a JSON array of objects, element order preserved.
//   - map[string]any: rendered with alphabetically sorted keys (generic fallback; the
//     algorithm in §1.3 never needs this for the shapes runtime currently produces, but it
//     keeps encodeCanonical defensively total over arbitrary JSON-able input).
//   - any other value: delegated to encoding/json (string, number, bool, nil, *int,
//     *int64, []string, json.RawMessage, ...).
func encodeCanonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch val := v.(type) {
	case canonicalObject:
		return writeCanonicalObject(buf, val)
	case []canonicalObject:
		buf.WriteByte('[')
		for i, item := range val {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonicalObject(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		obj := make(canonicalObject, 0, len(keys))
		for _, k := range keys {
			obj = append(obj, canonicalField{Key: k, Value: val[k]})
		}
		return writeCanonicalObject(buf, obj)
	default:
		return writeLeaf(buf, v)
	}
}

func writeCanonicalObject(buf *bytes.Buffer, obj canonicalObject) error {
	buf.WriteByte('{')
	for i, f := range obj {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyJSON, err := encodeCanonical(f.Key)
		if err != nil {
			return err
		}
		buf.Write(keyJSON)
		buf.WriteByte(':')
		if err := writeCanonical(buf, f.Value); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

// writeLeaf encodes any value encoding/json already renders deterministically for a
// single value (strings, numbers, bools, nil, typed pointers, slices of primitives).
// SetEscapeHTML(false) keeps '<', '>' and '&' un-escaped, matching a plain
// json.Marshal-free canonical form; Encode always appends a trailing newline, which is
// trimmed so concatenation stays exact.
func writeLeaf(buf *bytes.Buffer, v any) error {
	var leaf bytes.Buffer
	enc := json.NewEncoder(&leaf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	buf.Write(bytes.TrimRight(leaf.Bytes(), "\n"))
	return nil
}
