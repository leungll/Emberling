// Package mediaresult implements the Media Result Node. It reads an Agent's FINAL output,
// which declares the template settings and the example chosen for each photo, together
// with the photo set the Media Brief published, and checks the delivery against this
// Run's committed execution facts rather than against the model's own account.
//
// A delivery that fails a check is not an execution failure: the node still succeeds and
// reports accepted=false with bounded reasons. Only input it cannot read at all -- a
// malformed FINAL or brief -- fails the node.
//
// The node is pure: the Execution Service attaches the facts it declares to its input, so
// it touches neither the database nor a Provider and owns no state, retry or Event.
package mediaresult

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
)

const nodeType = "media_result"

// Port names. `text` takes the Agent's FINAL output and `brief` the Media Brief document,
// so the node knows the photo set without trusting the FINAL's copy of it.
const (
	textPort    = "text"
	briefPort   = "brief"
	imagePort   = "image"
	captionPort = "caption"
)

// Fact types the delivery checks read.
const (
	factImageGenerated = "image_generated"
	factAssetReviewed  = "asset_reviewed"
)

// generatedMediaType is the media type of a generated image: the generation Provider
// serves every generated asset as PNG, and the generation fact binds no media type.
const generatedMediaType = "image/png"

// configSchema is empty: every check is fixed, so there is nothing to configure.
const configSchema = `{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`

// Registration returns the Media Result NodeRegistration. It has no external side effect
// and is always safe to repeat.
func Registration() registry.NodeRegistration {
	return registry.NodeRegistration{
		Metadata: domain.NodeMetadata{
			Type:          nodeType,
			DisplayName:   "Media Result",
			Category:      domain.NodeCategoryOutput,
			ExecutionKind: domain.NodeExecutionSync,
			Inputs: []domain.PortMetadata{
				{Name: textPort, DataType: domain.PortTypeText, Required: true},
				{Name: briefPort, DataType: domain.PortTypeText, Required: true},
			},
			Outputs: []domain.PortMetadata{
				{Name: imagePort, DataType: domain.PortTypeImage, Required: true},
				{Name: captionPort, DataType: domain.PortTypeText, Required: true},
			},
			ConfigSchema: json.RawMessage(configSchema),
			SideEffect:   domain.SideEffectPolicy{Kind: domain.SideEffectNone, Idempotency: domain.IdempotencySafe},
			FactInputs:   []string{factImageGenerated, factAssetReviewed},
		},
		Binding: registry.ExecutorBinding{Executor: Executor{}},
	}
}

// Executor implements registry.NodeExecutor for the Media Result Node.
type Executor struct{}

// ValidateSemantics has nothing to check beyond ConfigSchema: the node has no config.
func (Executor) ValidateSemantics(_ context.Context, _ map[string]any) error { return nil }

// Execute checks the declared delivery against the facts on its input and publishes the
// cover image and the caption summary. Errors name the port and the shape problem only;
// they never quote the FINAL or the brief, because both are business content in Trace.
func (Executor) Execute(_ context.Context, input registry.NodeInput, _ map[string]any) (registry.NodeResult, error) {
	photos, err := readBrief(input)
	if err != nil {
		return registry.NodeResult{}, err
	}
	final, err := readFinal(input)
	if err != nil {
		return registry.NodeResult{}, err
	}

	summary, coverURL := checkDelivery(photos, final, input.Facts[factImageGenerated], input.Facts[factAssetReviewed])

	image, err := coverImage(photos[0], coverURL)
	if err != nil {
		return registry.NodeResult{}, err
	}
	imageValue, err := json.Marshal(image)
	if err != nil {
		return registry.NodeResult{}, fmt.Errorf("media_result: encode %q port value: %w", imagePort, err)
	}
	caption, err := encodeCanonical(summary)
	if err != nil {
		return registry.NodeResult{}, fmt.Errorf("media_result: encode %q summary: %w", captionPort, err)
	}
	// The caption port carries text, so the summary travels as one JSON string value.
	captionValue, err := json.Marshal(string(caption))
	if err != nil {
		return registry.NodeResult{}, fmt.Errorf("media_result: encode %q port value: %w", captionPort, err)
	}
	return registry.CompletedResult(map[string]json.RawMessage{imagePort: imageValue, captionPort: captionValue}), nil
}

// coverImage is the `image` port value. The cover is the first photo of the set, and its
// example is shown through the imageUrl its generation fact binds, so the delivered image
// is one this Run provably generated rather than one the model names. When no fact
// supplies a usable URL, the port carries the cover photo's own AssetRef instead.
func coverImage(cover photo, generatedURL string) (domain.ImageRef, error) {
	if generatedURL != "" {
		external := domain.ImageRef{Source: domain.ImageSourceExternal, URI: generatedURL, MediaType: generatedMediaType}
		if external.Validate() == nil {
			return external, nil
		}
	}
	image := domain.ImageRef{Source: domain.ImageSourceAsset, Asset: &domain.AssetRef{
		AssetID: cover.PhotoAssetID, MediaType: cover.MediaType, SizeBytes: cover.SizeBytes, SHA256: cover.SHA256,
	}}
	if err := image.Validate(); err != nil {
		return domain.ImageRef{}, fmt.Errorf("media_result: input port %q: cover photo is not a valid image reference: %w", briefPort, err)
	}
	return image, nil
}

// photo is one entry of the Media Brief photo set.
type photo struct {
	MediaType    string `json:"mediaType"`
	PhotoAssetID string `json:"photoAssetId"`
	SHA256       string `json:"sha256"`
	SizeBytes    int64  `json:"sizeBytes"`
}

// example is one declared example: the generated asset the Agent chose for a photo.
type example struct {
	PhotoAssetID string `json:"photoAssetId"`
	AssetRef     string `json:"assetRef"`
}

// finalOutput is the part of the Agent's FINAL the checks read. Members it does not
// name, such as a free-text summary, are ignored.
type finalOutput struct {
	Examples       []example `json:"examples"`
	SettingsDigest string    `json:"settingsDigest"`
}

// readBrief decodes the Media Brief document from the `brief` port: a JSON string whose
// content is an object with a non-empty photo set of distinct photos.
func readBrief(input registry.NodeInput) ([]photo, error) {
	content, err := portText(input, briefPort)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Photos []photo `json:"photos"`
	}
	if err := json.Unmarshal([]byte(content), &doc); err != nil {
		return nil, fmt.Errorf("media_result: input port %q is not a Media Brief JSON document", briefPort)
	}
	if len(doc.Photos) == 0 {
		return nil, fmt.Errorf("media_result: input port %q carries no photos", briefPort)
	}
	seen := make(map[string]bool, len(doc.Photos))
	for index, p := range doc.Photos {
		if p.PhotoAssetID == "" {
			return nil, fmt.Errorf("media_result: input port %q: photo %d has no photoAssetId", briefPort, index)
		}
		if seen[p.PhotoAssetID] {
			return nil, fmt.Errorf("media_result: input port %q: photo %d repeats a photo of the set", briefPort, index)
		}
		seen[p.PhotoAssetID] = true
	}
	return doc.Photos, nil
}

// readFinal decodes the Agent's FINAL from the `text` port: a JSON string whose content is
// a JSON object. Unknown members are tolerated; a member of the wrong type is not.
func readFinal(input registry.NodeInput) (finalOutput, error) {
	content, err := portText(input, textPort)
	if err != nil {
		return finalOutput{}, err
	}
	trimmed := bytes.TrimSpace([]byte(content))
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return finalOutput{}, fmt.Errorf("media_result: input port %q is not a JSON object", textPort)
	}
	var final finalOutput
	if err := json.Unmarshal(trimmed, &final); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return finalOutput{}, fmt.Errorf("media_result: input port %q: member %q has the wrong type", textPort, typeErr.Field)
		}
		return finalOutput{}, fmt.Errorf("media_result: input port %q is not valid JSON", textPort)
	}
	return final, nil
}

// portText reads a required text port as a JSON string.
func portText(input registry.NodeInput, port string) (string, error) {
	raw, present := input.Port(port)
	if !present || string(raw) == "null" {
		return "", fmt.Errorf("media_result: required input port %q has no value", port)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("media_result: input port %q is not a JSON string", port)
	}
	return value, nil
}

// encodeCanonical renders v without HTML escaping or a trailing newline, so equal
// summaries always produce byte-identical text.
func encodeCanonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
