package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

func TestNodeMetadataResponse_EmptyCollections_AreArrays(t *testing.T) {
	metadata := domain.NodeMetadata{Type: "output"}
	encoded, err := json.Marshal(nodeMetadataForResponse(metadata))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"inputs":[]`, `"outputs":[]`, `"fields":[]`} {
		if !strings.Contains(string(encoded), field) {
			t.Errorf("response %s does not contain %s", encoded, field)
		}
	}
	if metadata.Inputs != nil || metadata.Outputs != nil || metadata.UISchema.Fields != nil {
		t.Fatal("response projection mutated registered metadata")
	}
}

func TestModelMetadataResponse_NilCapabilities_IsArray(t *testing.T) {
	metadata := domain.ModelMetadata{ID: "model", ConfigSchema: json.RawMessage(`{"type":"object"}`)}
	encoded, err := json.Marshal(modelMetadataForResponse(metadata))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"capabilities":[]`) {
		t.Errorf("response %s does not contain %s", encoded, `"capabilities":[]`)
	}
	if metadata.Capabilities != nil {
		t.Fatal("response projection mutated registered metadata")
	}
}

func TestNodeMetadataResponse_NilFactInputs_IsArray(t *testing.T) {
	encoded, err := json.Marshal(nodeMetadataForResponse(domain.NodeMetadata{Type: "output"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"factInputs":[]`) {
		t.Errorf("response %s does not contain %s", encoded, `"factInputs":[]`)
	}
}

func TestToolMetadataResponse_UndeclaredFacts_RequiresIsArrayAndProducesOmitted(t *testing.T) {
	metadata := domain.ToolMetadata{Name: "lookup"}
	encoded, err := json.Marshal(toolMetadataForResponse(metadata))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"requires":[]`, `"countsTowardGenerationLimit":false`, `"records":[]`,
		`"usesWorkspace":false`, `"reconcilable":false`, `"returnUnmetRequirement":false`, `"requiresReviewModel":false`} {
		if !strings.Contains(string(encoded), field) {
			t.Errorf("response %s does not contain %s", encoded, field)
		}
	}
	if strings.Contains(string(encoded), `"produces"`) {
		t.Errorf("response %s contains produces, want it omitted when undeclared", encoded)
	}
	if strings.Contains(string(encoded), `"authorization"`) {
		t.Errorf("response %s contains authorization, want it omitted when undeclared (LOW risk)", encoded)
	}
	if strings.Contains(string(encoded), `"reviewModelCapability"`) {
		t.Errorf("response %s contains reviewModelCapability, want it omitted when the tool uses no review model", encoded)
	}
	if metadata.Requires != nil || metadata.Records != nil {
		t.Fatal("response projection mutated registered metadata")
	}
}

func TestToolMetadataResponse_DeclaredAuthorizationAndRecords_Serialised(t *testing.T) {
	metadata := domain.ToolMetadata{
		Name:          "write_file",
		Authorization: &domain.ToolAuthorization{Risk: domain.ToolRiskLow, Reversible: true},
		Records:       []domain.ToolRecordKind{domain.ToolRecordWorkspacePatch},
		UsesWorkspace: true,
	}
	encoded, err := json.Marshal(toolMetadataForResponse(metadata))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"authorization":{"risk":"LOW","reversible":true}`, `"records":["WORKSPACE_PATCH"]`, `"usesWorkspace":true`} {
		if !strings.Contains(string(encoded), field) {
			t.Errorf("response %s does not contain %s", encoded, field)
		}
	}
}

func TestToolMetadataResponse_NilCollectionsInDeclarations_AreEmptyCollections(t *testing.T) {
	metadata := domain.ToolMetadata{
		Name: "review_asset",
		Produces: &domain.FactProduction{
			FactType:       "asset_reviewed",
			SubjectPointer: domain.FactPointer{Source: domain.FactPointerResult, Pointer: "/assetId"},
		},
		Requires: []domain.FactRequirement{{FactType: "image_generated", SubjectArgument: "/assetRef"}},
	}
	encoded, err := json.Marshal(toolMetadataForResponse(metadata))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"bindArguments":{}`, `"matchBindings":[]`} {
		if !strings.Contains(string(encoded), field) {
			t.Errorf("response %s does not contain %s", encoded, field)
		}
	}
	if metadata.Produces.BindArguments != nil || metadata.Requires[0].MatchBindings != nil {
		t.Fatal("response projection mutated registered metadata")
	}
}
