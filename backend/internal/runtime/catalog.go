package runtime

import (
	"context"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// NodeCatalog resolves a registered Node Type to the metadata and semantic validation the
// Compiler needs. It is the test/compile-time seam between runtime (pure decisions) and
// registry (owns actual Node registration storage); runtime never accesses Registry
// storage, a Provider SDK or any other impure resource directly.
type NodeCatalog interface {
	// NodeMetadata returns the registration for nodeType, or false if no such Node Type
	// is currently registered.
	NodeMetadata(nodeType string) (domain.NodeMetadata, bool)

	// ValidateSemantics runs Node-Type-specific business rules on a node's decoded
	// config, beyond what ConfigSchema alone can express (every node config is first
	// validated against ConfigSchema, then ValidateSemantics runs). It returns nil
	// when the Node Type has nothing beyond ConfigSchema to check.
	ValidateSemantics(ctx context.Context, nodeType string, config map[string]any) error
}
