package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// validateSemantics is the Compiler's third stage: Node-Type-specific business rules
// beyond what ConfigSchema alone can express (docs/08-interface-spec.md §1.2: "再执行
// ValidateSemantics()"). Nodes whose type could not be resolved, or whose config is not
// valid JSON, were already reported by an earlier stage and Compile never reaches this
// stage in that case.
//
// `agent` nodes are special-cased to ParseAgentNodeConfig instead of
// catalog.ValidateSemantics: the ManagedAgentBinding a real agent Node Type resolves to
// has no Executor and registers a no-op ValidateSemantics (registry.NodeRegistry), so the
// fixed Agent config shape's own invariants (maxTurns/timeoutMs bounds, Schema
// compilability, stateSchema accepting {}, outputSchema accepting a string) are enforced
// here directly, inside pure runtime, rather than through the Registry seam.
func validateSemantics(ctx context.Context, def domain.Definition, catalog NodeCatalog) []ValidationError {
	var errs []ValidationError
	for _, node := range def.Nodes {
		if _, ok := catalog.NodeMetadata(node.Type); !ok {
			continue
		}

		if node.Type == NodeTypeAgent {
			if err := validateAgentNodeSemantics(node.ID, node.Config); err != nil {
				errs = append(errs, *err)
			}
			continue
		}

		config, err := decodeConfig(node.Config)
		if err != nil {
			continue
		}
		if err := catalog.ValidateSemantics(ctx, node.Type, config); err != nil {
			errs = append(errs, ValidationError{
				Code:    CodeSemanticValidationFailed,
				NodeID:  node.ID,
				Path:    "nodes[" + node.ID + "].config",
				Message: err.Error(),
			})
		}
	}
	return errs
}

// validateAgentNodeSemantics converts a ParseAgentNodeConfig failure into the Compiler's
// ValidationError shape, appending the offending field (when named) to Path so it points
// at the same location a ConfigSchema violation would.
func validateAgentNodeSemantics(nodeID string, config json.RawMessage) *ValidationError {
	_, err := ParseAgentNodeConfig(config)
	if err == nil {
		return nil
	}
	path := "nodes[" + nodeID + "].config"
	var ace *AgentConfigError
	if errors.As(err, &ace) && ace.Field != "" {
		path += "." + ace.Field
	}
	return &ValidationError{
		Code:    CodeSemanticValidationFailed,
		NodeID:  nodeID,
		Path:    path,
		Message: err.Error(),
	}
}

// decodeConfig decodes a node's raw config into a plain map for ValidateSemantics. An
// absent config decodes to an empty map rather than an error: ConfigSchema stage already
// accepts "{}" as the default when Config is empty.
func decodeConfig(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var config map[string]any
	if err := dec.Decode(&config); err != nil {
		return nil, err
	}
	if config == nil {
		config = map[string]any{}
	}
	return config, nil
}
