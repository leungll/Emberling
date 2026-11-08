package registry

import (
	"encoding/json"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// validateToolDisplayPointers checks that every display declaration a Tool makes lands
// inside the schema of the document it reads: media link pointers inside the Output
// Schema, display fields inside the Input Schema for ARGUMENTS and the Output Schema for
// RESULT. A pointer that can never select a value would make a run card silently show
// nothing, so it fails registration like a fact pointer does. Pointer syntax and the
// shape rules are reported by domain.ToolMetadata.Validate and skipped here.
func validateToolDisplayPointers(metadata domain.ToolMetadata) []error {
	var errs []error
	check := func(field string, schema json.RawMessage, schemaName, pointer string) {
		if domain.ValidateJSONPointer(pointer) != nil {
			return
		}
		if err := schemaPointerResolves(schema, pointer); err != nil {
			errs = append(errs, fmt.Errorf("tool %q: %s: pointer %q is outside the %s: %w", metadata.Name, field, pointer, schemaName, err))
		}
	}
	for i, link := range metadata.MediaLinks {
		field := fmt.Sprintf("mediaLinks[%d]", i)
		check(field+".refPointer", metadata.OutputSchema, "outputSchema", link.RefPointer)
		check(field+".urlPointer", metadata.OutputSchema, "outputSchema", link.URLPointer)
		if link.MediaTypePointer != "" {
			check(field+".mediaTypePointer", metadata.OutputSchema, "outputSchema", link.MediaTypePointer)
		}
	}
	for i, display := range metadata.Display {
		field := fmt.Sprintf("display[%d]", i)
		switch display.Source {
		case domain.FactPointerArguments:
			check(field, metadata.InputSchema, "inputSchema", display.Pointer)
		case domain.FactPointerResult:
			check(field, metadata.OutputSchema, "outputSchema", display.Pointer)
		}
	}
	return errs
}
