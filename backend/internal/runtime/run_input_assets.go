package runtime

import (
	"bytes"
	"encoding/json"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// RunInputAssetRef is one Image Input's Run input AssetRef together with the node that
// will consume it. The node id is carried so a rejection can name the Image Input a
// Studio user must fix, rather than only the input key.
type RunInputAssetRef struct {
	NodeID   string
	InputKey string
	Ref      domain.AssetRef
}

// RunInputAssetRefs collects every AssetRef a Run's input supplies to an Image Input of
// def. It is a pure read of an already schema-valid input: the frozen runInputSchema has
// fixed the AssetRef shape by the time this runs (docs/08-interface-spec.md §1.3), so a
// key that is absent, explicitly null, or not an AssetRef object is skipped rather than
// re-reported here.
//
// It answers only "which Assets does this Run claim to reference". Whether each Asset
// exists and matches its committed Metadata is a persisted fact, so the lookup belongs to
// the Execution Service's transaction, not to this package.
func RunInputAssetRefs(def domain.Definition, input json.RawMessage) []RunInputAssetRef {
	var runInput map[string]json.RawMessage
	if len(input) > 0 {
		if err := json.Unmarshal(input, &runInput); err != nil {
			return nil
		}
	}
	if len(runInput) == 0 {
		return nil
	}

	var refs []RunInputAssetRef
	for _, node := range def.Nodes {
		if node.Type != NodeTypeImageInput {
			continue
		}
		var cfg imageInputConfig
		if err := json.Unmarshal(node.Config, &cfg); err != nil || cfg.InputKey == "" {
			continue
		}
		raw, present := runInput[cfg.InputKey]
		if !present || len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		var ref domain.AssetRef
		if err := json.Unmarshal(raw, &ref); err != nil || ref.AssetID == "" {
			continue
		}
		refs = append(refs, RunInputAssetRef{NodeID: node.ID, InputKey: cfg.InputKey, Ref: ref})
	}
	return refs
}

// MatchesAsset reports whether this reference describes the Asset PostgreSQL committed.
// An `asset_id` points at immutable content (docs/05-data-model.md §1.2 "Asset Metadata 与
// AssetRef"), so a media type, size or digest that disagrees with the Metadata is a
// reference to content the Asset never held, not a detail to be corrected silently.
func (r RunInputAssetRef) MatchesAsset(asset domain.Asset) bool {
	return r.Ref == asset.Ref()
}

// AssetRejection builds the Run input validation error for a reference that names no
// committed Asset, or one whose Metadata disagrees.
//
// The message names the Image Input node and the asset id and nothing else. The offending
// member and its two values are deliberately left out: the caller already holds both the
// AssetRef it sent and the Metadata GET /assets/{assetId} returns, and a Run creation
// error is not a place to echo back stored facts.
func (r RunInputAssetRef) AssetRejection(reason string) ValidationError {
	return ValidationError{
		Code:    CodeValidationFailed,
		NodeID:  r.NodeID,
		Path:    "input/" + r.InputKey,
		Message: "assetId \"" + r.Ref.AssetID + "\" " + reason,
	}
}
