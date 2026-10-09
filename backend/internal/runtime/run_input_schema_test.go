package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// TestRunInputSchema_MatchesDocumentedAlgorithm_ByteIdentical exercises Compile end to
// end against document_processing.json (a single Input Node) and asserts the generated
// runInputSchema is byte-identical to the hand-computed canonical JSON. Equal Definition
// content under one validatorVersion must produce equal bytes.
func TestRunInputSchema_MatchesDocumentedAlgorithm_ByteIdentical(t *testing.T) {
	compiler, _ := newTestCompiler()
	def := loadFixture(t, "document_processing.json")

	got, err := compiler.Compile(context.Background(), def)
	if err != nil {
		t.Fatalf("Compile() unexpected error: %v", err)
	}

	want := `{"type":"object","additionalProperties":false,"properties":{"document":{"type":"string","minLength":1,"maxLength":20000}},"required":["document"]}`
	if string(got.RunInputSchema) != want {
		t.Errorf("RunInputSchema =\n%s\nwant\n%s", got.RunInputSchema, want)
	}

	// Re-compiling the same Definition must produce byte-identical output.
	got2, err := compiler.Compile(context.Background(), def)
	if err != nil {
		t.Fatalf("second Compile() unexpected error: %v", err)
	}
	if string(got.RunInputSchema) != string(got2.RunInputSchema) {
		t.Errorf("RunInputSchema not stable across repeated Compile: %s vs %s", got.RunInputSchema, got2.RunInputSchema)
	}
}

// TestRunInputSchema_AIGCMedia_AssetRefKeyOrderAndDedup asserts the image_input branch:
// the AssetRef schema's non-alphabetical property key order (assetId, mediaType,
// sizeBytes, sha256) and acceptedMediaTypes deduped-then-sorted into `enum`.
// aigc_media.json's node_reference deliberately repeats "image/png" in its
// acceptedMediaTypes list to exercise the dedup.
func TestRunInputSchema_AIGCMedia_AssetRefKeyOrderAndDedup(t *testing.T) {
	compiler, _ := newTestCompiler()
	def := loadFixture(t, "aigc_media.json")

	got, err := compiler.Compile(context.Background(), def)
	if err != nil {
		t.Fatalf("Compile() unexpected error: %v", err)
	}

	// Top-level keys ordered ascending by inputKey: "brief" < "reference".
	want := `{"type":"object","additionalProperties":false,"properties":{` +
		`"brief":{"type":"string"},` +
		`"reference":{"type":"object","additionalProperties":false,"properties":{` +
		`"assetId":{"type":"string","minLength":1},` +
		`"mediaType":{"type":"string","minLength":1,"enum":["image/jpeg","image/png"]},` +
		`"sizeBytes":{"type":"integer","minimum":0,"maximum":5242880},` +
		`"sha256":{"type":"string","minLength":1}` +
		`},"required":["assetId","mediaType","sizeBytes","sha256"]}` +
		`},"required":["brief"]}`
	if string(got.RunInputSchema) != want {
		t.Errorf("RunInputSchema =\n%s\nwant\n%s", got.RunInputSchema, want)
	}
}

// TestRunInputSchema_DuplicateInputKey_Fails exploits fakeCatalog's test-only "note"
// input port on text_output, wiring two distinct text_input nodes with the same inputKey
// to two different Output Node ports, so the Definition passes every earlier stage
// cleanly and fails only at the runInputSchema stage.
func TestRunInputSchema_DuplicateInputKey_Fails(t *testing.T) {
	compiler, _ := newTestCompiler()
	def := loadFixture(t, "document_processing.json")
	def = cloneDefinition(t, def)
	def.Nodes = append(def.Nodes, domain.Node{
		ID: "node_input2", Type: NodeTypeTextInput,
		Config: json.RawMessage(`{"inputKey":"document","required":true}`),
	})
	def.Edges = append(def.Edges, domain.Edge{
		ID: "edge_input2_output", Source: "node_input2", SourceHandle: "text",
		Target: "node_output", TargetHandle: "note",
	})

	_, err := compiler.Compile(context.Background(), def)
	if err == nil {
		t.Fatal("Compile() expected error, got nil")
	}
	ce, ok := AsCompileError(err)
	if !ok {
		t.Fatalf("expected *CompileError, got %T: %v", err, err)
	}
	if ce.Stage != StageRunInputSchema {
		t.Errorf("Stage = %q, want %q", ce.Stage, StageRunInputSchema)
	}
	if !containsCode(ce.Errors, CodeDuplicateInputKey) {
		t.Errorf("Errors = %+v, want to contain %s", ce.Errors, CodeDuplicateInputKey)
	}
}

// TestRunInputSchema_EmptyInputKey_Fails uses the same two-Input-Node wiring as the
// duplicate-key test, but gives the second Input Node an empty inputKey instead of a
// colliding one, isolating INPUT_KEY_REQUIRED from DUPLICATE_INPUT_KEY.
func TestRunInputSchema_EmptyInputKey_Fails(t *testing.T) {
	compiler, _ := newTestCompiler()
	def := loadFixture(t, "document_processing.json")
	def = cloneDefinition(t, def)
	def.Nodes = append(def.Nodes, domain.Node{
		ID: "node_input2", Type: NodeTypeTextInput,
		Config: json.RawMessage(`{"inputKey":"","required":false}`),
	})
	def.Edges = append(def.Edges, domain.Edge{
		ID: "edge_input2_output", Source: "node_input2", SourceHandle: "text",
		Target: "node_output", TargetHandle: "note",
	})

	_, err := compiler.Compile(context.Background(), def)
	if err == nil {
		t.Fatal("Compile() expected error, got nil")
	}
	ce, ok := AsCompileError(err)
	if !ok {
		t.Fatalf("expected *CompileError, got %T: %v", err, err)
	}
	if ce.Stage != StageRunInputSchema {
		t.Errorf("Stage = %q, want %q", ce.Stage, StageRunInputSchema)
	}
	if !containsCode(ce.Errors, CodeInputKeyRequired) {
		t.Errorf("Errors = %+v, want to contain %s", ce.Errors, CodeInputKeyRequired)
	}
}

// TestRunInputSchema_NoDefaultsInjected asserts buildRunInputSchema copies only the
// constraints an Input Node's config actually sets (inputKey, required, minLength,
// maxLength / acceptedMediaTypes, maxSizeBytes) and injects no default values of its own:
// node_prompt's optional "reference" Input Node in aigc_media.json sets neither
// maxSizeBytes-independent bound beyond what's configured, so its generated property
// carries exactly what's configured, nothing more. This is checked on
// document_processing.json's node_input, which sets minLength/maxLength, versus a
// variant with those two fields absent, to confirm no default minLength/maxLength appear
// when the config omits them.
func TestRunInputSchema_NoDefaultsInjected(t *testing.T) {
	compiler, _ := newTestCompiler()
	def := loadFixture(t, "document_processing.json")
	def = cloneDefinition(t, def)
	for i, n := range def.Nodes {
		if n.ID == "node_input" {
			def.Nodes[i].Config = json.RawMessage(`{"inputKey":"document","required":true}`)
		}
	}

	got, err := compiler.Compile(context.Background(), def)
	if err != nil {
		t.Fatalf("Compile() unexpected error: %v", err)
	}

	want := `{"type":"object","additionalProperties":false,"properties":{"document":{"type":"string"}},"required":["document"]}`
	if string(got.RunInputSchema) != want {
		t.Errorf("RunInputSchema =\n%s\nwant\n%s (no minLength/maxLength should be injected)", got.RunInputSchema, want)
	}
}

func TestRunInputSchema_AllOptional_OmitsTopLevelRequired(t *testing.T) {
	compiler, _ := newTestCompiler()
	def := loadFixture(t, "document_processing.json")
	def = cloneDefinition(t, def)
	for i, node := range def.Nodes {
		if node.ID == "node_input" {
			def.Nodes[i].Config = json.RawMessage(`{"inputKey":"document","required":false}`)
		}
	}

	got, err := compiler.Compile(context.Background(), def)
	if err != nil {
		t.Fatalf("Compile() unexpected error: %v", err)
	}

	want := `{"type":"object","additionalProperties":false,"properties":{"document":{"type":"string"}}}`
	if string(got.RunInputSchema) != want {
		t.Errorf("RunInputSchema =\n%s\nwant\n%s (top-level required must be omitted, not empty)", got.RunInputSchema, want)
	}
}

func mediaBriefDefinition(inputKey string, extra ...domain.Node) domain.Definition {
	nodes := []domain.Node{{
		ID: "node_brief", Type: nodeTypeMediaBrief,
		Config: json.RawMessage(`{"inputKey":"` + inputKey + `"}`),
	}}
	return domain.Definition{Nodes: append(nodes, extra...)}
}

// TestRunInputSchema_MediaBrief_RequiredBoundedImageAssetRefArray pins the photo set
// property byte for byte: an always-required array of the shared AssetRef schema with the
// sorted image mediaType enum and no size maximum, bounded to one through twelve items.
func TestRunInputSchema_MediaBrief_RequiredBoundedImageAssetRefArray(t *testing.T) {
	got, errs := buildRunInputSchema(mediaBriefDefinition("photos", domain.Node{
		ID: "node_text", Type: NodeTypeTextInput,
		Config: json.RawMessage(`{"inputKey":"brief","required":false}`),
	}))
	if len(errs) > 0 {
		t.Fatalf("buildRunInputSchema() errors = %+v, want none", errs)
	}

	want := `{"type":"object","additionalProperties":false,"properties":{` +
		`"brief":{"type":"string"},` +
		`"photos":{"type":"array","items":{"type":"object","additionalProperties":false,"properties":{` +
		`"assetId":{"type":"string","minLength":1},` +
		`"mediaType":{"type":"string","minLength":1,"enum":["image/jpeg","image/png","image/webp"]},` +
		`"sizeBytes":{"type":"integer","minimum":0},` +
		`"sha256":{"type":"string","minLength":1}},` +
		`"required":["assetId","mediaType","sizeBytes","sha256"]},"minItems":1,"maxItems":12}},` +
		`"required":["photos"]}`
	if string(got) != want {
		t.Errorf("RunInputSchema =\n%s\nwant\n%s", got, want)
	}
}

func TestRunInputSchema_MediaBrief_EnforcesPhotoSetBoundsAtRunCreation(t *testing.T) {
	schema, errs := buildRunInputSchema(mediaBriefDefinition("photos"))
	if len(errs) > 0 {
		t.Fatalf("buildRunInputSchema() errors = %+v, want none", errs)
	}
	photo := func(id, mediaType string) string {
		return `{"assetId":"` + id + `","mediaType":"` + mediaType + `","sizeBytes":10,"sha256":"abc"}`
	}
	thirteen := ""
	for i := 0; i < 13; i++ {
		if i > 0 {
			thirteen += ","
		}
		thirteen += photo("asset_"+string(rune('a'+i)), "image/png")
	}

	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "one photo", input: `{"photos":[` + photo("asset_a", "image/png") + `]}`},
		{name: "absent", input: `{}`, wantErr: true},
		{name: "empty", input: `{"photos":[]}`, wantErr: true},
		{name: "thirteen photos", input: `{"photos":[` + thirteen + `]}`, wantErr: true},
		{name: "non-image media type", input: `{"photos":[` + photo("asset_a", "application/pdf") + `]}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateRunInput(schema, json.RawMessage(tc.input))
			if tc.wantErr && len(errs) == 0 {
				t.Fatal("ValidateRunInput() errors = none, want a rejection")
			}
			if !tc.wantErr && len(errs) > 0 {
				t.Fatalf("ValidateRunInput() errors = %+v, want none", errs)
			}
		})
	}
}

func TestRunInputSchema_MediaBrief_EmptyInputKey_Fails(t *testing.T) {
	_, errs := buildRunInputSchema(mediaBriefDefinition(""))
	if !containsCode(errs, CodeInputKeyRequired) {
		t.Errorf("errors = %+v, want to contain %s", errs, CodeInputKeyRequired)
	}
}

func TestRunInputSchema_MediaBrief_KeySharedWithInputNode_FailsOnBoth(t *testing.T) {
	_, errs := buildRunInputSchema(mediaBriefDefinition("photos", domain.Node{
		ID: "node_image", Type: NodeTypeImageInput,
		Config: json.RawMessage(`{"inputKey":"photos","required":true}`),
	}))
	flagged := map[string]bool{}
	for _, e := range errs {
		if e.Code == CodeDuplicateInputKey {
			flagged[e.NodeID] = true
		}
	}
	if !flagged["node_brief"] || !flagged["node_image"] {
		t.Errorf("DUPLICATE_INPUT_KEY flagged nodes = %v, want node_brief and node_image (errors=%+v)", flagged, errs)
	}
}
