package runtime

import (
	"encoding/json"
	"strconv"
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
	if refs[0].NodeID != "reference" || refs[0].InputKey != "reference" || refs[0].Path != "input/reference" {
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
		Path:     "input/reference",
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

// photoRef renders one photo-set AssetRef whose digest is the given character repeated.
func photoRef(assetID string, digit string) string {
	return `{"assetId":"` + assetID + `","mediaType":"image/jpeg","sizeBytes":100,"sha256":"` + strings.Repeat(digit, 64) + `"}`
}

// mixedAssetDefinition is one Media Brief keyed on `photos`, next to an Image Input keyed
// on `reference` and a Text Input that must never be mistaken for either.
func mixedAssetDefinition() domain.Definition {
	def := assetRefDefinition()
	def.Nodes = append(def.Nodes, domain.Node{ID: "brief", Type: nodeTypeMediaBrief, Config: json.RawMessage(`{"inputKey":"photos"}`)})
	return def
}

// TestRunInputAssetRefs_MediaBriefPhotoSet_EveryPhotoCollected: each photo of the set is an
// Asset the Run claims to reference, so each must reach the existence check. Each one
// carries its own index, so a rejection points at the photo that failed rather than at
// the whole set.
func TestRunInputAssetRefs_MediaBriefPhotoSet_EveryPhotoCollected(t *testing.T) {
	def := domain.Definition{Nodes: []domain.Node{
		{ID: "brief", Type: nodeTypeMediaBrief, Config: json.RawMessage(`{"inputKey":"photos"}`)},
	}}
	input := json.RawMessage(`{"photos":[` + photoRef("photo_a", "a") + `,` + photoRef("photo_b", "b") + `,` + photoRef("photo_c", "c") + `]}`)

	refs := RunInputAssetRefs(def, input)

	if len(refs) != 3 {
		t.Fatalf("collected refs = %d, want 3 (one per photo): %+v", len(refs), refs)
	}
	for index, want := range []string{"photo_a", "photo_b", "photo_c"} {
		got := refs[index]
		if got.NodeID != "brief" || got.InputKey != "photos" || got.Ref.AssetID != want {
			t.Errorf("ref %d = %+v, want node %q, key %q, asset %q", index, got, "brief", "photos", want)
		}
		if wantPath := "input/photos/" + strconv.Itoa(index); got.Path != wantPath {
			t.Errorf("ref %d path = %q, want %q", index, got.Path, wantPath)
		}
	}
	if refs[1].Ref.SHA256 != strings.Repeat("b", 64) || refs[1].Ref.MediaType != "image/jpeg" || refs[1].Ref.SizeBytes != 100 {
		t.Errorf("collected AssetRef = %+v, want the input's own values", refs[1].Ref)
	}
}

// TestRunInputAssetRefs_MixedDefinition_CollectsImageInputAndPhotoSet: a Definition with
// both an Image Input and a Media Brief has every one of their AssetRefs checked, each
// attributed to its own node.
func TestRunInputAssetRefs_MixedDefinition_CollectsImageInputAndPhotoSet(t *testing.T) {
	input := json.RawMessage(`{"caption":"x","reference":` + photoRef("ref_1", "1") + `,"photos":[` + photoRef("photo_a", "a") + `,` + photoRef("photo_b", "b") + `]}`)

	refs := RunInputAssetRefs(mixedAssetDefinition(), input)

	got := make(map[string]RunInputAssetRef, len(refs))
	for _, ref := range refs {
		got[ref.Ref.AssetID] = ref
	}
	if len(refs) != 3 || len(got) != 3 {
		t.Fatalf("collected refs = %+v, want the Image Input's and both photos", refs)
	}
	for assetID, want := range map[string][2]string{
		"ref_1":   {"reference", "input/reference"},
		"photo_a": {"brief", "input/photos/0"},
		"photo_b": {"brief", "input/photos/1"},
	} {
		if ref := got[assetID]; ref.NodeID != want[0] || ref.Path != want[1] {
			t.Errorf("ref for %s = %+v, want node %q at %q", assetID, ref, want[0], want[1])
		}
	}
}

// TestRunInputAssetRefs_MediaBriefMalformedSet_NothingCollected: the schema guarantees a
// non-empty array of AssetRefs before this runs, but the collector stays a total read: an
// empty set, a non-array value, or an item without an assetId claims no Asset and is not
// a reason to panic or invent a reference.
func TestRunInputAssetRefs_MediaBriefMalformedSet_NothingCollected(t *testing.T) {
	def := domain.Definition{Nodes: []domain.Node{
		{ID: "brief", Type: nodeTypeMediaBrief, Config: json.RawMessage(`{"inputKey":"photos"}`)},
	}}
	for name, input := range map[string]string{
		"empty set":    `{"photos":[]}`,
		"null":         `{"photos":null}`,
		"absent":       `{}`,
		"not an array": `{"photos":` + photoRef("photo_a", "a") + `}`,
		"string items": `{"photos":["photo_a"]}`,
		"no asset id":  `{"photos":[{"mediaType":"image/jpeg"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if refs := RunInputAssetRefs(def, json.RawMessage(input)); len(refs) != 0 {
				t.Errorf("collected refs = %+v, want none", refs)
			}
		})
	}
}

// TestRunInputAssetRef_AssetRejection_PointsAtThePhoto: a rejected photo names the Media
// Brief node and the photo's own position, in the same error shape an Image Input uses.
func TestRunInputAssetRef_AssetRejection_PointsAtThePhoto(t *testing.T) {
	input := json.RawMessage(`{"photos":[` + photoRef("photo_a", "a") + `,` + photoRef("photo_b", "b") + `]}`)
	refs := RunInputAssetRefs(mixedAssetDefinition(), input)
	if len(refs) != 2 {
		t.Fatalf("collected refs = %+v, want 2", refs)
	}

	verr := refs[1].AssetRejection("does not name an uploaded asset")

	if verr.Code != CodeValidationFailed || verr.NodeID != "brief" || verr.Path != "input/photos/1" {
		t.Errorf("rejection = %+v, want %s at node %q, path %q", verr, CodeValidationFailed, "brief", "input/photos/1")
	}
	if !strings.Contains(verr.Message, "photo_b") || strings.Contains(verr.Message, "photo_a") {
		t.Errorf("message = %q, want it to name the failing photo's assetId only", verr.Message)
	}
}
