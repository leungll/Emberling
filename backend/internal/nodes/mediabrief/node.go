// Package mediabrief implements the built-in Media Brief Node. It reads one Run input
// field by its frozen inputKey -- a photo set of AssetRefs Run creation validated against
// the generated runInputSchema -- and joins it with the text on its `brief` input port into
// one canonical JSON document on its `text` output port, ready to become an Agent's task.
//
// The node is pure: it touches neither Asset storage nor the database, loads no binary and
// calls no Provider. It owns no NodeRun state, retry or Event of its own.
package mediabrief

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const nodeType = "media_brief"

// Port names. The brief arrives as text and the composed document leaves as text, so the
// node connects a text source to an Agent's task port without a JSON port type.
const (
	briefPort = "brief"
	textPort  = "text"
)

// Photo set bounds. They match the array bounds the generated runInputSchema imposes on
// this node's Run input property; the node re-checks them because it must not trust that
// a value reached it through that schema.
const (
	minPhotos = 1
	maxPhotos = 12
)

// configSchema is the sole contract for Media Brief config. The photo set is always
// required, so unlike the Input Nodes there is no `required` switch.
const configSchema = `{
  "type": "object",
  "properties": {
    "inputKey": {"type": "string", "minLength": 1}
  },
  "required": ["inputKey"],
  "additionalProperties": false
}`

// Registration returns the Media Brief NodeRegistration. It has no external side effect
// and is always safe to repeat.
func Registration() registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          nodeType,
			DisplayName:   "Media Brief",
			Category:      domain.NodeCategoryInput,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs:        []domain.PortMetadata{{Name: briefPort, DataType: domain.PortTypeText, Required: true}},
			Outputs:       []domain.PortMetadata{{Name: textPort, DataType: domain.PortTypeText, Required: true}},
			ConfigSchema:  json.RawMessage(configSchema),
			UISchema: domain.NodeUISchema{Fields: []domain.UIField{
				{Path: "inputKey", Order: 10, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault},
			}},
			SideEffect: domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		Binding: registry.ExecutorBinding{Executor: Executor{}},
	}
}

// Executor implements registry.NodeExecutor for the Media Brief Node.
type Executor struct{}

// ValidateSemantics has nothing to check beyond ConfigSchema: the photo set's shape and
// bounds are value rules the generated runInputSchema applies at Run creation.
func (Executor) ValidateSemantics(_ context.Context, _ map[string]any) error { return nil }

// photo is one entry of the composed document. Field order is alphabetical so the
// encoded document is canonical: keys sorted, no insignificant whitespace.
type photo struct {
	MediaType    string `json:"mediaType"`
	PhotoAssetID string `json:"photoAssetId"`
	SHA256       string `json:"sha256"`
	SizeBytes    int64  `json:"sizeBytes"`
}

// document is the `text` port's JSON, again with alphabetical field order.
type document struct {
	Brief  string  `json:"brief"`
	Photos []photo `json:"photos"`
}

// Execute reads Run.input[inputKey] as a photo set and publishes it with the brief text.
// Every error names the inputKey and the offending position only; it never quotes a Run
// input value or the brief, because it reaches Trace and those are business content.
func (Executor) Execute(_ context.Context, input registry.NodeInput, config map[string]any) (registry.NodeResult, error) {
	inputKey, _ := config["inputKey"].(string)

	var runInput map[string]json.RawMessage
	if len(input.RunInput) > 0 {
		if err := json.Unmarshal(input.RunInput, &runInput); err != nil {
			return registry.NodeResult{}, fmt.Errorf("media_brief: run input is not a JSON object: %w", err)
		}
	}
	value, present := runInput[inputKey]
	if !present || string(value) == "null" {
		return registry.NodeResult{}, fmt.Errorf("media_brief: required run input key %q is missing", inputKey)
	}
	photos, err := parsePhotoSet(value)
	if err != nil {
		return registry.NodeResult{}, fmt.Errorf("media_brief: run input key %q: %w", inputKey, err)
	}

	brief, err := briefText(input)
	if err != nil {
		return registry.NodeResult{}, err
	}

	encoded, err := encodeDocument(document{Brief: brief, Photos: photos})
	if err != nil {
		return registry.NodeResult{}, fmt.Errorf("media_brief: encode %q document: %w", textPort, err)
	}
	// The port carries text, so the document travels as one JSON string value.
	portValue, err := json.Marshal(string(encoded))
	if err != nil {
		return registry.NodeResult{}, fmt.Errorf("media_brief: encode %q port value: %w", textPort, err)
	}
	return registry.CompletedResult(map[string]json.RawMessage{textPort: portValue}), nil
}

// parsePhotoSet decodes the photo set strictly and checks its membership: between minPhotos
// and maxPhotos AssetRefs, each a supported image with an identity, and no Asset twice. An
// unknown AssetRef member is rejected rather than dropped, since the generated schema
// forbids one.
func parsePhotoSet(raw json.RawMessage) ([]photo, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, errors.New("value is not an array of AssetRefs")
	}
	if len(items) < minPhotos {
		return nil, fmt.Errorf("photo set is empty; at least %d photo is required", minPhotos)
	}
	if len(items) > maxPhotos {
		return nil, fmt.Errorf("photo set has %d photos; at most %d are allowed", len(items), maxPhotos)
	}

	photos := make([]photo, 0, len(items))
	seen := make(map[string]bool, len(items))
	for index, item := range items {
		ref, err := parseAssetRef(item)
		if err != nil {
			return nil, fmt.Errorf("photo %d: %w", index, err)
		}
		switch {
		case ref.AssetID == "":
			return nil, fmt.Errorf("photo %d: assetId is empty", index)
		case !domain.IsSupportedAssetMediaType(ref.MediaType):
			return nil, fmt.Errorf("photo %d: media type is not a supported image type", index)
		case ref.SizeBytes < 0:
			return nil, fmt.Errorf("photo %d: sizeBytes is negative", index)
		case ref.SHA256 == "":
			return nil, fmt.Errorf("photo %d: sha256 is empty", index)
		case seen[ref.AssetID]:
			return nil, fmt.Errorf("photo %d: the same Asset appears more than once in the photo set", index)
		}
		seen[ref.AssetID] = true
		photos = append(photos, photo{
			MediaType:    ref.MediaType,
			PhotoAssetID: ref.AssetID,
			SHA256:       ref.SHA256,
			SizeBytes:    ref.SizeBytes,
		})
	}
	return photos, nil
}

// parseAssetRef decodes one AssetRef object, rejecting unknown members and trailing content.
func parseAssetRef(raw json.RawMessage) (domain.AssetRef, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var ref domain.AssetRef
	if err := decoder.Decode(&ref); err != nil {
		return domain.AssetRef{}, errors.New("value is not an AssetRef object")
	}
	if decoder.More() {
		return domain.AssetRef{}, errors.New("value carries trailing content after the AssetRef object")
	}
	return ref, nil
}

// briefText reads the `brief` port as a JSON string. The port is required, so an absent
// or null value is an error rather than an empty brief.
func briefText(input registry.NodeInput) (string, error) {
	raw, present := input.Port(briefPort)
	if !present || string(raw) == "null" {
		return "", fmt.Errorf("media_brief: required input port %q has no value", briefPort)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("media_brief: input port %q is not a JSON string", briefPort)
	}
	return value, nil
}

// encodeDocument renders doc without HTML escaping or a trailing newline, so equal photo
// sets and briefs always produce byte-identical text.
func encodeDocument(doc document) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(doc); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
