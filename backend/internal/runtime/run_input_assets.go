package runtime

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// RunInputAssetRef is one AssetRef a Run input supplies, together with the node that
// will consume it. The node id is carried so a rejection can name the node a Studio user
// must fix, rather than only the input key.
//
// Path is the reference's location in the Run input: `input/<inputKey>` for an Image
// Input's single AssetRef, and `input/<inputKey>/<index>` for one photo of a Media Brief's
// photo set, so a rejection points at the one photo that failed.
type RunInputAssetRef struct {
	NodeID   string
	InputKey string
	Path     string
	Ref      domain.AssetRef
}

// RunInputAssetRefs collects every AssetRef a Run's input supplies to def: the single
// AssetRef of each Image Input and every AssetRef in each Media Brief's photo set. It is a
// pure read of an already schema-valid input: the frozen runInputSchema has fixed the
// AssetRef shape and the photo set's bounds by the time this runs, so a key that is
// absent, explicitly null, or not of the expected shape is skipped rather than
// re-reported here, as is a photo set that is empty or an item that is not an AssetRef.
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
		if node.Type != NodeTypeImageInput && node.Type != nodeTypeMediaBrief {
			continue
		}
		var cfg struct {
			InputKey string `json:"inputKey"`
		}
		if err := json.Unmarshal(node.Config, &cfg); err != nil || cfg.InputKey == "" {
			continue
		}
		raw, present := runInput[cfg.InputKey]
		if !present || len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		path := "input/" + cfg.InputKey
		if node.Type == NodeTypeImageInput {
			if ref, ok := decodeAssetRef(raw); ok {
				refs = append(refs, RunInputAssetRef{NodeID: node.ID, InputKey: cfg.InputKey, Path: path, Ref: ref})
			}
			continue
		}
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			continue
		}
		for index, item := range items {
			if ref, ok := decodeAssetRef(item); ok {
				refs = append(refs, RunInputAssetRef{
					NodeID: node.ID, InputKey: cfg.InputKey, Path: path + "/" + strconv.Itoa(index), Ref: ref,
				})
			}
		}
	}
	return refs
}

// decodeAssetRef reads one AssetRef value, reporting false for anything that does not
// name an Asset.
func decodeAssetRef(raw json.RawMessage) (domain.AssetRef, bool) {
	var ref domain.AssetRef
	if err := json.Unmarshal(raw, &ref); err != nil || ref.AssetID == "" {
		return domain.AssetRef{}, false
	}
	return ref, true
}

// MatchesAsset reports whether this reference describes the Asset PostgreSQL committed.
// An `asset_id` points at immutable content, so a media type, size or digest that
// disagrees with the Metadata is a
// reference to content the Asset never held, not a detail to be corrected silently.
func (r RunInputAssetRef) MatchesAsset(asset domain.Asset) bool {
	return r.Ref == asset.Ref()
}

// AssetRejection builds the Run input validation error for a reference that names no
// committed Asset, or one whose Metadata disagrees.
//
// The message names the consuming node and the asset id and nothing else. The offending
// member and its two values are deliberately left out: the caller already holds both the
// AssetRef it sent and the Metadata GET /assets/{assetId} returns, and a Run creation
// error is not a place to echo back stored facts.
func (r RunInputAssetRef) AssetRejection(reason string) ValidationError {
	return ValidationError{
		Code:    CodeValidationFailed,
		NodeID:  r.NodeID,
		Path:    r.Path,
		Message: "assetId \"" + r.Ref.AssetID + "\" " + reason,
	}
}
