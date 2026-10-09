package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func boolPtr(v bool) *bool { return &v }

// declaringToolMetadata returns a Tool that both produces and requires a fact, with every
// pointer syntactically valid. Each rejection case mutates one fact of it.
func declaringToolMetadata() ToolMetadata {
	return ToolMetadata{
		Name:          "review_asset",
		Description:   "Review a generated asset",
		InputSchema:   json.RawMessage(`{"type":"object"}`),
		OutputSchema:  json.RawMessage(`{"type":"object"}`),
		SideEffect:    SideEffectPolicy{Kind: SideEffectNone, Idempotency: IdempotencySafe},
		ExecutionKind: ToolExecutionSync,
		Produces: &FactProduction{
			FactType:       "asset_reviewed",
			SubjectPointer: FactPointer{Source: FactPointerResult, Pointer: "/assetId"},
			BindArguments: map[string]FactPointer{
				"photoAssetId": {Source: FactPointerArguments, Pointer: "/photoAssetId"},
			},
			VerdictPointer: &FactPointer{Source: FactPointerResult, Pointer: "/approved"},
			BasisFactType:  "image_generated",
		},
		Requires: []FactRequirement{{
			FactType:        "image_generated",
			SubjectArgument: "/assetRef",
			MatchBindings:   []string{"photoAssetId"},
			RequireVerdict:  boolPtr(true),
		}},
	}
}

func TestToolMetadata_Validate_FactDeclarationsWellFormed_Accepted(t *testing.T) {
	if err := declaringToolMetadata().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestToolMetadata_Validate_InvalidFactDeclaration_RejectedNamingToolAndRule(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*ToolMetadata)
		wantMsg string
	}{
		{"empty produced fact type", func(m *ToolMetadata) { m.Produces.FactType = "" }, "produces.factType is empty"},
		{"empty subject pointer", func(m *ToolMetadata) { m.Produces.SubjectPointer.Pointer = "" }, "produces.subjectPointer: pointer is empty"},
		{"subject pointer without leading slash", func(m *ToolMetadata) { m.Produces.SubjectPointer.Pointer = "assetId" }, `produces.subjectPointer: pointer "assetId" is not a valid JSON Pointer`},
		{"subject pointer with bad escape", func(m *ToolMetadata) { m.Produces.SubjectPointer.Pointer = "/asset~2Id" }, `pointer "/asset~2Id" is not a valid JSON Pointer`},
		{"subject pointer with trailing tilde", func(m *ToolMetadata) { m.Produces.SubjectPointer.Pointer = "/asset~" }, `pointer "/asset~" is not a valid JSON Pointer`},
		{"unknown pointer source", func(m *ToolMetadata) { m.Produces.SubjectPointer.Source = "HEADERS" }, `produces.subjectPointer: source "HEADERS"`},
		{"bad binding pointer", func(m *ToolMetadata) {
			m.Produces.BindArguments["photoAssetId"] = FactPointer{Source: FactPointerArguments, Pointer: "photoAssetId"}
		}, `produces.bindArguments["photoAssetId"]: pointer "photoAssetId" is not a valid JSON Pointer`},
		{"empty binding name", func(m *ToolMetadata) {
			m.Produces.BindArguments[""] = FactPointer{Source: FactPointerArguments, Pointer: "/x"}
		}, "produces.bindArguments: binding name is empty"},
		{"bad verdict pointer", func(m *ToolMetadata) { m.Produces.VerdictPointer.Pointer = "" }, "produces.verdictPointer: pointer is empty"},
		{"basis on own fact type", func(m *ToolMetadata) { m.Produces.BasisFactType = "asset_reviewed" }, `produces.basisFactType "asset_reviewed": a Tool must not base its fact on the fact type it produces`},
		{"requires own fact type", func(m *ToolMetadata) { m.Requires[0].FactType = "asset_reviewed" }, `requires[0].factType "asset_reviewed": a Tool must not require the fact type it produces`},
		{"empty required fact type", func(m *ToolMetadata) { m.Requires[0].FactType = "" }, "requires[0].factType is empty"},
		{"empty subject argument", func(m *ToolMetadata) { m.Requires[0].SubjectArgument = "" }, "requires[0].subjectArgument: pointer is empty"},
		{"bad subject argument", func(m *ToolMetadata) { m.Requires[0].SubjectArgument = "assetRef" }, `requires[0].subjectArgument: pointer "assetRef" is not a valid JSON Pointer`},
		{"empty match binding", func(m *ToolMetadata) { m.Requires[0].MatchBindings = []string{""} }, "requires[0].matchBindings: binding name is empty"},
		{"duplicate match binding", func(m *ToolMetadata) {
			m.Requires[0].MatchBindings = []string{"photoAssetId", "photoAssetId"}
		}, `requires[0].matchBindings: duplicate binding "photoAssetId"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metadata := declaringToolMetadata()
			tc.mutate(&metadata)

			err := metadata.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want rejection mentioning %q", tc.wantMsg)
			}
			if !strings.Contains(err.Error(), `invalid tool metadata "review_asset"`) {
				t.Fatalf("Validate() = %q, want it to name the Tool", err)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("Validate() = %q, want it to mention %q", err, tc.wantMsg)
			}
		})
	}
}

func TestNodeMetadata_Validate_InvalidFactInputs_Rejected(t *testing.T) {
	cases := []struct {
		name       string
		factInputs []string
		wantMsg    string
	}{
		{"empty fact type", []string{""}, "factInputs: fact type is empty"},
		{"duplicate fact type", []string{"asset_reviewed", "asset_reviewed"}, `factInputs: duplicate fact type "asset_reviewed"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metadata := validNodeMetadata()
			metadata.FactInputs = tc.factInputs

			err := metadata.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want rejection mentioning %q", tc.wantMsg)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("Validate() = %q, want it to mention %q", err, tc.wantMsg)
			}
		})
	}

	metadata := validNodeMetadata()
	metadata.FactInputs = []string{"image_generated", "asset_reviewed"}
	if err := metadata.Validate(); err != nil {
		t.Fatalf("Validate() with distinct fact inputs = %v, want nil", err)
	}
}

func TestJSONPointerTokens_EscapedTokens_Unescaped(t *testing.T) {
	got := JSONPointerTokens("/a~1b/c~0d/0/")
	want := []string{"a/b", "c~d", "0", ""}
	if len(got) != len(want) {
		t.Fatalf("JSONPointerTokens() = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("JSONPointerTokens() = %q, want %q", got, want)
		}
	}
}

func TestToolMetadata_JSON_FactDeclarationsUseWireNames(t *testing.T) {
	const want = `{"name":"review_asset","description":"Review a generated asset","inputSchema":{"type":"object"},"outputSchema":{"type":"object"},"sideEffect":{"kind":"NONE","idempotency":"SAFE"},"executionKind":"SYNC",` +
		`"produces":{"factType":"asset_reviewed","subjectPointer":{"source":"RESULT","pointer":"/assetId"},"bindArguments":{"photoAssetId":{"source":"ARGUMENTS","pointer":"/photoAssetId"}},"verdictPointer":{"source":"RESULT","pointer":"/approved"},"basisFactType":"image_generated"},` +
		`"requires":[{"factType":"image_generated","subjectArgument":"/assetRef","matchBindings":["photoAssetId"],"requireVerdict":true}],` +
		`"countsTowardGenerationLimit":false}`

	encoded, err := json.Marshal(declaringToolMetadata())
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	if string(encoded) != want {
		t.Fatalf("Marshal() =\n%s\nwant\n%s", encoded, want)
	}

	var decoded ToolMetadata
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	reencoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	if string(reencoded) != want {
		t.Fatalf("round trip =\n%s\nwant\n%s", reencoded, want)
	}
}
