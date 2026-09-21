package runtime

// MVP Node Type literals (docs/02-scope.md §3.2). These are the only types the fixed
// compiler stages reason about structurally (Input/Output detection, runInputSchema
// generation); every other aspect of a Node Type's behaviour comes from its registered
// domain.NodeMetadata, resolved through NodeCatalog.
const (
	NodeTypeTextInput       = "text_input"
	NodeTypeImageInput      = "image_input"
	NodeTypePromptTemplate  = "prompt_template"
	NodeTypeTextGeneration  = "text_generation"
	NodeTypeImageGeneration = "image_generation"
	NodeTypeAgent           = "agent"
	NodeTypeTextOutput      = "text_output"
	NodeTypeMediaOutput     = "media_output"
)

// isInputNodeType reports whether nodeType is one of the two Input Node types that
// contribute a property to the generated runInputSchema (docs/08-interface-spec.md §1.3).
func isInputNodeType(nodeType string) bool {
	return nodeType == NodeTypeTextInput || nodeType == NodeTypeImageInput
}

// isOutputNodeType reports whether nodeType is one of the two Output Node types. Each
// Definition must contain exactly one Output Node, and it must be the unique DAG sink
// (docs/02-scope.md §3.2, docs/11-decisions.md §5).
func isOutputNodeType(nodeType string) bool {
	return nodeType == NodeTypeTextOutput || nodeType == NodeTypeMediaOutput
}
