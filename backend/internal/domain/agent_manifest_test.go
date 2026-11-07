package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestManifestItemKind_MaterialRange_IsValid: the material range kind is a declared kind.
func TestManifestItemKind_MaterialRange_IsValid(t *testing.T) {
	if !ManifestMaterialRange.IsValid() {
		t.Fatalf("%s is not a valid manifest kind", ManifestMaterialRange)
	}
	if ManifestItemKind("MATERIAL").IsValid() {
		t.Fatal("an undeclared kind is valid")
	}
}

// TestManifestItem_Range_SerialisedOnlyWhenPresent: the range is written beside the
// digest for a material range item and omitted for every other item, so stored items of
// the older kinds keep their shape.
func TestManifestItem_Range_SerialisedOnlyWhenPresent(t *testing.T) {
	withRange := ManifestItem{Kind: ManifestMaterialRange, Label: "art_1", Ref: "art_1", Version: "abc",
		SHA256: "d", Length: 3, Included: true, Range: &ManifestRange{StartLine: 2, LineCount: 1, TotalLines: 3}}
	encoded, err := json.Marshal(withRange)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"range":{"startLine":2,"lineCount":1,"totalLines":3}`) {
		t.Fatalf("encoded = %s, want the range", encoded)
	}
	var decoded ManifestItem
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Range == nil || *decoded.Range != *withRange.Range {
		t.Fatalf("decoded range = %+v, want %+v", decoded.Range, withRange.Range)
	}

	encoded, _ = json.Marshal(ManifestItem{Kind: ManifestToolResult, Included: true})
	if strings.Contains(string(encoded), "range") {
		t.Fatalf("encoded = %s, want no range member on a tool result", encoded)
	}
}
