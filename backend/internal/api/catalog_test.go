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
