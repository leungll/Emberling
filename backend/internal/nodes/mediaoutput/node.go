// Package mediaoutput implements the built-in Media Output Node. It is the
// DAG's sole terminal node in the AIGC media scenario: its complete logical result becomes
// both its NodeRun output and Run.output, under the same persistence rule Text Output uses
// (Text Output and Media Output share one persistence rule and differ only in their output
// Schema).
//
// The interface contract has no Media Output row (its only worked example is
// image_generation); the `image` (image, required) and `caption` (text, required) input
// ports and the SYNC/NONE-SAFE declaration below follow internal/runtime/catalog_test.go's
// fake catalog fixture, the only place these ports were already specified.
package mediaoutput

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const nodeType = "media_output"

// imagePort and captionPort are the node's two input handles.
const (
	imagePort   = "image"
	captionPort = "caption"
)

// configSchema is empty: Media Output takes no configuration, same as Text Output.
const configSchema = `{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`

// Registration returns the Media Output NodeRegistration. It has no external side effect
// and is always safe to repeat.
func Registration() registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          nodeType,
			DisplayName:   "Media Output",
			Category:      domain.NodeCategoryOutput,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs: []domain.PortMetadata{
				{Name: imagePort, DataType: domain.PortTypeImage, Required: true},
				{Name: captionPort, DataType: domain.PortTypeText, Required: true},
			},
			ConfigSchema: json.RawMessage(configSchema),
			SideEffect:   domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
		},
		Binding: registry.ExecutorBinding{Executor: Executor{}},
	}
}

// Executor implements registry.NodeExecutor for the Media Output Node.
type Executor struct{}

// ValidateSemantics has nothing to check beyond ConfigSchema: Media Output has no config
// fields and no cross-field constraint.
func (Executor) ValidateSemantics(_ context.Context, _ map[string]any) error {
	return nil
}

// Execute combines the `image` and `caption` input ports into the node's complete logical
// result. The service layer copies this object into Run.output when this node is the
// Output Node.
//
// `image` must be a valid domain.ImageRef and `caption` a JSON string: the ImageRef rules
// give this node exactly those two inputs and forbids it from accepting an arbitrary JSON
// object. The reference is preserved as the normalised value it already is - re-encoded
// from the parsed ref, so the result cannot differ from what the upstream node published.
func (Executor) Execute(_ context.Context, input registry.NodeInput, _ map[string]any) (registry.NodeResult, error) {
	imageRaw, present := input.Port(imagePort)
	if !present {
		return registry.NodeResult{}, fmt.Errorf("media_output: required input port %q is missing", imagePort)
	}
	image, err := domain.ParseImageRef(imageRaw)
	if err != nil {
		return registry.NodeResult{}, fmt.Errorf("media_output: input port %q is not a valid image reference: %w", imagePort, err)
	}
	captionRaw, present := input.Port(captionPort)
	if !present {
		return registry.NodeResult{}, fmt.Errorf("media_output: required input port %q is missing", captionPort)
	}
	var caption string
	if err := json.Unmarshal(captionRaw, &caption); err != nil {
		return registry.NodeResult{}, fmt.Errorf("media_output: input port %q is not a JSON string: %w", captionPort, err)
	}
	encodedImage, err := json.Marshal(image)
	if err != nil {
		return registry.NodeResult{}, fmt.Errorf("media_output: encode input port %q: %w", imagePort, err)
	}
	return registry.CompletedResult(map[string]json.RawMessage{
		imagePort:   encodedImage,
		captionPort: captionRaw,
	}), nil
}
