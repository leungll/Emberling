// Package imageinput implements the built-in Image Input Node (02 §3.2, 08 §2.1). It
// reads one Run input field by its frozen inputKey -- an AssetRef Run creation already
// validated against the persisted Asset Metadata -- and wraps it as the `source: ASSET`
// ImageRef its `image` port carries (08 §2.2).
//
// It touches neither Asset storage nor the database: the binary is never loaded, and the
// Asset's existence, media type, size and digest were established once, at Run creation,
// by the CreateRun use case. This node owns no NodeRun state, retry or Event of its own
// (07 §1.1).
package imageinput

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const nodeType = "image_input"

// imagePort is the node's single handle, fixed by 08 §2.1.
const imagePort = "image"

// configSchema is the sole contract for Image Input config (08 §1.3): inputKey and
// required are mandatory; acceptedMediaTypes and maxSizeBytes are optional constraints
// that flow into the generated runInputSchema's AssetRef property.
const configSchema = `{
  "type": "object",
  "properties": {
    "inputKey": {"type": "string", "minLength": 1},
    "label": {"type": "string"},
    "required": {"type": "boolean"},
    "acceptedMediaTypes": {"type": "array", "items": {"type": "string", "minLength": 1}},
    "maxSizeBytes": {"type": "integer", "minimum": 1}
  },
  "required": ["inputKey", "required"],
  "additionalProperties": false
}`

// Registration returns the Image Input NodeRegistration. It has no external side effect
// and is always safe to repeat.
func Registration() registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          nodeType,
			DisplayName:   "Image Input",
			Category:      domain.NodeCategoryInput,
			ExecutionKind: domain.NodeExecutionSync,
			Outputs:       []domain.PortMetadata{{Name: imagePort, DataType: domain.PortTypeImage, Required: true}},
			ConfigSchema:  json.RawMessage(configSchema),
			UISchema: domain.NodeUISchema{Fields: []domain.UIField{
				{Path: "inputKey", Order: 10, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault},
				{Path: "label", Order: 20, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault},
				{Path: "required", Order: 30, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault},
				{Path: "acceptedMediaTypes", Order: 40, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault},
				{Path: "maxSizeBytes", Order: 50, Group: domain.UIGroupBasic, Widget: domain.UIWidgetDefault},
			}},
			SideEffect: domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		Binding: registry.ExecutorBinding{Executor: Executor{}},
	}
}

// Executor implements registry.NodeExecutor for the Image Input Node.
type Executor struct{}

// ValidateSemantics has nothing to check beyond ConfigSchema: every Image Input
// constraint (accepted media types, maximum size) is a value rule JSON Schema already
// expresses, and both are re-applied by the frozen runInputSchema at Run creation.
func (Executor) ValidateSemantics(_ context.Context, _ map[string]any) error { return nil }

// Execute reads Run.input[inputKey] and publishes it on the `image` port as a canonical
// ImageRef. Per 08 §1.3, MVP Input Nodes inject no default: an absent optional key
// produces an explicit JSON null port value rather than a substituted image.
func (Executor) Execute(_ context.Context, input registry.NodeInput, config map[string]any) (registry.NodeResult, error) {
	inputKey, _ := config["inputKey"].(string)
	required, _ := config["required"].(bool)

	var runInput map[string]json.RawMessage
	if len(input.RunInput) > 0 {
		if err := json.Unmarshal(input.RunInput, &runInput); err != nil {
			return registry.NodeResult{}, fmt.Errorf("image_input: run input is not a JSON object: %w", err)
		}
	}

	value, present := runInput[inputKey]
	if !present {
		if required {
			return registry.NodeResult{}, fmt.Errorf("image_input: required run input key %q is missing", inputKey)
		}
		return registry.CompletedResult(map[string]json.RawMessage{imagePort: json.RawMessage("null")}), nil
	}

	ref, err := parseAssetRef(value)
	if err != nil {
		// err never quotes the value: it reaches Trace, and the Run input is business
		// content rather than a bounded summary.
		return registry.NodeResult{}, fmt.Errorf("image_input: run input key %q is not an AssetRef: %w", inputKey, err)
	}
	image := domain.ImageRef{Source: domain.ImageSourceAsset, Asset: &ref}
	if err := image.Validate(); err != nil {
		return registry.NodeResult{}, fmt.Errorf("image_input: run input key %q: %w", inputKey, err)
	}
	encoded, err := json.Marshal(image)
	if err != nil {
		return registry.NodeResult{}, fmt.Errorf("image_input: encode %q reference: %w", imagePort, err)
	}
	return registry.CompletedResult(map[string]json.RawMessage{imagePort: encoded}), nil
}

// parseAssetRef decodes one Run input AssetRef strictly. An unknown member is rejected
// rather than dropped: the frozen runInputSchema forbids one, so a value carrying an
// extra field did not come through the contract this node relies on.
func parseAssetRef(raw json.RawMessage) (domain.AssetRef, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var ref domain.AssetRef
	if err := decoder.Decode(&ref); err != nil {
		return domain.AssetRef{}, fmt.Errorf("value is not an AssetRef object")
	}
	if decoder.More() {
		return domain.AssetRef{}, fmt.Errorf("value carries trailing content after the AssetRef object")
	}
	return ref, nil
}
