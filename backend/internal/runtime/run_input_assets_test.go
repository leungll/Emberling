package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// assetRefDefinition is one Image Input keyed on `reference`, plus a Text Input that must
// never be mistaken for one.
func assetRefDefinition() domain.Definition {
	return domain.Definition{
		Nodes: []domain.Node{
			{ID: "caption", Type: NodeTypeTextInput, Config: json.RawMessage(`{"inputKey":"caption","required":true}`)},
			{ID: "reference", Type: NodeTypeImageInput, Config: json.RawMessage(`{"inputKey":"reference","required":false}`)},
		},
	}
}

// TestRunInputAssetRefs_ImageInputKey_CollectedWithItsNode: the collected reference must
// carry the Image Input's node id, because that -- not the input key -- is what a rejection
// points a Studio user at.
func TestRunInputAssetRefs_ImageInputKey_CollectedWithItsNode(t *testing.T) {
	input := json.RawMessage(`{"caption":"a small ember creature","reference":{"assetId":"asset_1","mediaType":"image/png","sizeBytes":320,"sha256":"` + strings.Repeat("a", 64) + `"}}`)

	refs := RunInputAssetRefs(assetRefDefinition(), input)

	if len(refs) != 1 {
		t.Fatalf("collected refs = %d, want 1 (the text_input key is not an AssetRef)", len(refs))
	}
	if refs[0].NodeID != "reference" || refs[0].InputKey != "reference" {
		t.Errorf("collected ref = %+v, want node and key %q", refs[0], "reference")
	}
	if refs[0].Ref.AssetID != "asset_1" || refs[0].Ref.SizeBytes != 320 {
		t.Errorf("collected AssetRef = %+v, want the input's own values", refs[0].Ref)
	}
}

// TestRunInputAssetRefs_AbsentOrNullKey_NotCollected: an optional Image Input that was not
// supplied claims no Asset, so there is nothing to look up. Collecting it would turn an
// omitted optional input into a 400.
func TestRunInputAssetRefs_AbsentOrNullKey_NotCollected(t *testing.T) {
	for name, input := range map[string]string{
		"absent":     `{"caption":"x"}`,
		"null":       `{"caption":"x","reference":null}`,
		"empty":      `{}`,
		"no input":   ``,
		"not an obj": `{"caption":"x","reference":"asset_1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if refs := RunInputAssetRefs(assetRefDefinition(), json.RawMessage(input)); len(refs) != 0 {
				t.Errorf("collected refs = %+v, want none", refs)
			}
		})
	}
}

// TestRunInputAssetRef_MatchesAsset_RequiresEveryMetadataField: an asset_id points at
// immutable content, so a reference that agrees on the id but not on the content it
// describes is not a match.
func TestRunInputAssetRef_MatchesAsset_RequiresEveryMetadataField(t *testing.T) {
	asset := domain.Asset{AssetID: "asset_1", MediaType: "image/png", SizeBytes: 320, SHA256: strings.Repeat("a", 64)}
	ref := RunInputAssetRef{NodeID: "reference", InputKey: "reference", Ref: asset.Ref()}

	if !ref.MatchesAsset(asset) {
		t.Fatalf("the Metadata's own AssetRef does not match its Asset: %+v", ref.Ref)
	}
	for name, mutate := range map[string]func(*domain.AssetRef){
		"mediaType": func(r *domain.AssetRef) { r.MediaType = "image/jpeg" },
		"sizeBytes": func(r *domain.AssetRef) { r.SizeBytes = 321 },
		"sha256":    func(r *domain.AssetRef) { r.SHA256 = strings.Repeat("b", 64) },
		"assetId":   func(r *domain.AssetRef) { r.AssetID = "asset_2" },
	} {
		t.Run(name, func(t *testing.T) {
			mismatched := ref
			mutate(&mismatched.Ref)
			if mismatched.MatchesAsset(asset) {
				t.Errorf("a reference disagreeing on %s matched the committed Asset", name)
			}
		})
	}
}

// TestRunInputAssetRef_AssetRejection_NamesNodeAndAssetIDOnly pins what a rejection is
// allowed to say: the Image Input and the assetId the caller already holds, never the
// stored Metadata values it disagreed with.
func TestRunInputAssetRef_AssetRejection_NamesNodeAndAssetIDOnly(t *testing.T) {
	ref := RunInputAssetRef{
		NodeID:   "reference",
		InputKey: "reference",
		Ref:      domain.AssetRef{AssetID: "asset_1", MediaType: "image/png", SizeBytes: 320, SHA256: strings.Repeat("a", 64)},
	}

	verr := ref.AssetRejection("does not match the uploaded asset's metadata")

	if verr.Code != CodeValidationFailed {
		t.Errorf("code = %q, want %q", verr.Code, CodeValidationFailed)
	}
	if verr.NodeID != "reference" || verr.Path != "input/reference" {
		t.Errorf("rejection = %+v, want it to point at the Image Input node and its input key", verr)
	}
	if !strings.Contains(verr.Message, "asset_1") {
		t.Errorf("message = %q, want it to name the assetId", verr.Message)
	}
	for _, forbidden := range []string{"image/png", "320", strings.Repeat("a", 64)} {
		if strings.Contains(verr.Message, forbidden) {
			t.Errorf("message = %q, want it to echo back no stored Metadata value (%q)", verr.Message, forbidden)
		}
	}
}
