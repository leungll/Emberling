package runtime

import (
	"encoding/json"
	"sort"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// textInputConfig is the subset of a text_input node's config that drives runInputSchema
// generation. Fields decode into typed Go values (not map[string]any / any) so numeric
// constants render as JSON integers, never floats, when copied into the generated schema.
type textInputConfig struct {
	InputKey  string `json:"inputKey"`
	Required  bool   `json:"required"`
	MinLength *int   `json:"minLength"`
	MaxLength *int   `json:"maxLength"`
}

// imageInputConfig is the image_input analogue of textInputConfig.
type imageInputConfig struct {
	InputKey           string   `json:"inputKey"`
	Required           bool     `json:"required"`
	AcceptedMediaTypes []string `json:"acceptedMediaTypes"`
	MaxSizeBytes       *int64   `json:"maxSizeBytes"`
}

// nodeTypeMediaBrief is the Media Brief node, which reads a required photo set from Run
// input. It is not an Input Node -- it also consumes a brief through an input port -- but
// it contributes one runInputSchema property and shares the inputKey namespace with the
// Input Nodes.
const nodeTypeMediaBrief = "media_brief"

// Media Brief photo set bounds, mirrored by the node itself.
const (
	mediaBriefMinPhotos = 1
	mediaBriefMaxPhotos = 12
)

// mediaBriefImageTypes is the photo set's mediaType enum: the supported image media types,
// sorted so the generated schema is canonical.
var mediaBriefImageTypes = []string{"image/jpeg", "image/png", "image/webp"}

// mediaBriefConfig is the subset of a media_brief node's config that drives
// runInputSchema generation.
type mediaBriefConfig struct {
	InputKey string `json:"inputKey"`
}

// mediaBriefSchema is the always-required photo set property: an array of AssetRefs
// restricted to image media types, with no per-Asset size bound.
func mediaBriefSchema() canonicalObject {
	return canonicalObject{
		{Key: "type", Value: "array"},
		{Key: "items", Value: assetRefSchema(mediaBriefImageTypes, nil)},
		{Key: "minItems", Value: mediaBriefMinPhotos},
		{Key: "maxItems", Value: mediaBriefMaxPhotos},
	}
}

// assetRefSchema is the fixed AssetRef object every Image Input generates before any
// acceptedMediaTypes/maxSizeBytes constraint is layered on. The property key order --
// assetId, mediaType, sizeBytes, sha256 -- matches that example exactly and is NOT
// alphabetical (sha256 < sizeBytes alphabetically), so it is built as an explicit
// canonicalObject rather than derived from a generic sorted-key map.
func assetRefSchema(mediaTypeEnum []string, maxSizeBytes *int64) canonicalObject {
	mediaTypeProps := canonicalObject{
		{Key: "type", Value: "string"},
		{Key: "minLength", Value: 1},
	}
	if len(mediaTypeEnum) > 0 {
		mediaTypeProps = append(mediaTypeProps, canonicalField{Key: "enum", Value: mediaTypeEnum})
	}

	sizeBytesProps := canonicalObject{
		{Key: "type", Value: "integer"},
		{Key: "minimum", Value: 0},
	}
	if maxSizeBytes != nil {
		sizeBytesProps = append(sizeBytesProps, canonicalField{Key: "maximum", Value: *maxSizeBytes})
	}

	return canonicalObject{
		{Key: "type", Value: "object"},
		{Key: "additionalProperties", Value: false},
		{Key: "properties", Value: canonicalObject{
			{Key: "assetId", Value: canonicalObject{
				{Key: "type", Value: "string"},
				{Key: "minLength", Value: 1},
			}},
			{Key: "mediaType", Value: mediaTypeProps},
			{Key: "sizeBytes", Value: sizeBytesProps},
			{Key: "sha256", Value: canonicalObject{
				{Key: "type", Value: "string"},
				{Key: "minLength", Value: 1},
			}},
		}},
		{Key: "required", Value: []string{"assetId", "mediaType", "sizeBytes", "sha256"}},
	}
}

// buildRunInputSchema is the Compiler's sixth stage: one top-level property per Input
// Node (and per Media Brief node), keyed by inputKey and ordered ascending by inputKey, with the top-level
// `required` array in the same order. The result is canonical JSON: byte-identical for
// equal Definition content and validatorVersion.
//
// Top-level key order is type, additionalProperties, properties, then required when at
// least one Input Node is required. When none is required, the required member is omitted
// rather than emitted as an empty array. Both choices are part of the canonical encoding.
func buildRunInputSchema(def domain.Definition) (json.RawMessage, []ValidationError) {
	type inputEntry struct {
		inputKey string
		required bool
		property canonicalObject
	}

	var errs []ValidationError
	var entries []inputEntry
	keyCount := make(map[string]int)

	for _, node := range def.Nodes {
		switch node.Type {
		case NodeTypeTextInput:
			var cfg textInputConfig
			if err := json.Unmarshal(node.Config, &cfg); err != nil {
				continue // already reported by the ConfigSchema stage
			}
			if cfg.InputKey == "" {
				errs = append(errs, ValidationError{
					Code:    CodeInputKeyRequired,
					NodeID:  node.ID,
					Path:    "nodes[" + node.ID + "].config.inputKey",
					Message: "inputKey must not be empty",
				})
				continue
			}
			keyCount[cfg.InputKey]++
			prop := canonicalObject{{Key: "type", Value: "string"}}
			if cfg.MinLength != nil {
				prop = append(prop, canonicalField{Key: "minLength", Value: *cfg.MinLength})
			}
			if cfg.MaxLength != nil {
				prop = append(prop, canonicalField{Key: "maxLength", Value: *cfg.MaxLength})
			}
			entries = append(entries, inputEntry{inputKey: cfg.InputKey, required: cfg.Required, property: prop})

		case NodeTypeImageInput:
			var cfg imageInputConfig
			if err := json.Unmarshal(node.Config, &cfg); err != nil {
				continue
			}
			if cfg.InputKey == "" {
				errs = append(errs, ValidationError{
					Code:    CodeInputKeyRequired,
					NodeID:  node.ID,
					Path:    "nodes[" + node.ID + "].config.inputKey",
					Message: "inputKey must not be empty",
				})
				continue
			}
			keyCount[cfg.InputKey]++
			mediaTypes := dedupeSorted(cfg.AcceptedMediaTypes)
			entries = append(entries, inputEntry{
				inputKey: cfg.InputKey, required: cfg.Required,
				property: assetRefSchema(mediaTypes, cfg.MaxSizeBytes),
			})

		case nodeTypeMediaBrief:
			var cfg mediaBriefConfig
			if err := json.Unmarshal(node.Config, &cfg); err != nil {
				continue
			}
			if cfg.InputKey == "" {
				errs = append(errs, ValidationError{
					Code:    CodeInputKeyRequired,
					NodeID:  node.ID,
					Path:    "nodes[" + node.ID + "].config.inputKey",
					Message: "inputKey must not be empty",
				})
				continue
			}
			keyCount[cfg.InputKey]++
			entries = append(entries, inputEntry{inputKey: cfg.InputKey, required: true, property: mediaBriefSchema()})
		}
	}

	for key, count := range keyCount {
		if count <= 1 {
			continue
		}
		for _, node := range def.Nodes {
			if !isInputNodeType(node.Type) && node.Type != nodeTypeMediaBrief {
				continue
			}
			var raw struct {
				InputKey string `json:"inputKey"`
			}
			if err := json.Unmarshal(node.Config, &raw); err != nil {
				continue
			}
			if raw.InputKey == key {
				errs = append(errs, ValidationError{
					Code:    CodeDuplicateInputKey,
					NodeID:  node.ID,
					Path:    "nodes[" + node.ID + "].config.inputKey",
					Message: "inputKey \"" + key + "\" is used by more than one Input Node",
				})
			}
		}
	}

	if len(errs) > 0 {
		return nil, errs
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].inputKey < entries[j].inputKey })

	properties := make(canonicalObject, 0, len(entries))
	var required []string
	for _, e := range entries {
		properties = append(properties, canonicalField{Key: e.inputKey, Value: e.property})
		if e.required {
			required = append(required, e.inputKey)
		}
	}

	top := canonicalObject{
		{Key: "type", Value: "object"},
		{Key: "additionalProperties", Value: false},
		{Key: "properties", Value: properties},
	}
	if len(required) > 0 {
		top = append(top, canonicalField{Key: "required", Value: required})
	}

	encoded, err := encodeCanonical(top)
	if err != nil {
		return nil, []ValidationError{{Code: CodeValidationFailed, Message: "failed to encode runInputSchema: " + err.Error()}}
	}
	return encoded, nil
}

func dedupeSorted(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}
