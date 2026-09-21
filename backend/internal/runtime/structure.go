package runtime

import "github.com/leungll/Emberling/backend/internal/domain"

// validateStructure is the Compiler's first stage: every node's type must be a
// registered Node Type, and every edge must reference a real output port on its source
// node and a real input port on its target node
// (docs/06-execution-model.md §1.2: "节点类型和配置校验").
//
// Port resolution is only attempted once every node type is known: an edge touching a
// node whose type could not be resolved would just produce a confusing, redundant
// UNKNOWN_PORT alongside the real UNKNOWN_NODE_TYPE, so this stage returns immediately
// after the node-type pass if it found any error.
func validateStructure(def domain.Definition, catalog NodeCatalog) []ValidationError {
	var errs []ValidationError

	metadata := make(map[string]domain.NodeMetadata, len(def.Nodes))
	for _, node := range def.Nodes {
		meta, ok := catalog.NodeMetadata(node.Type)
		if !ok {
			errs = append(errs, ValidationError{
				Code:    CodeUnknownNodeType,
				NodeID:  node.ID,
				Path:    "nodes[" + node.ID + "].type",
				Message: "node type \"" + node.Type + "\" is not registered",
			})
			continue
		}
		metadata[node.ID] = meta
	}
	if len(errs) > 0 {
		return errs
	}

	for _, edge := range def.Edges {
		if !hasPort(metadata[edge.Source].Outputs, edge.SourceHandle) {
			errs = append(errs, ValidationError{
				Code:    CodeUnknownPort,
				NodeID:  edge.Source,
				Path:    "edges[" + edge.ID + "].sourceHandle",
				Message: "node \"" + edge.Source + "\" has no output port \"" + edge.SourceHandle + "\"",
			})
		}
		if !hasPort(metadata[edge.Target].Inputs, edge.TargetHandle) {
			errs = append(errs, ValidationError{
				Code:    CodeUnknownPort,
				NodeID:  edge.Target,
				Path:    "edges[" + edge.ID + "].targetHandle",
				Message: "node \"" + edge.Target + "\" has no input port \"" + edge.TargetHandle + "\"",
			})
		}
	}
	return errs
}

func hasPort(ports []domain.PortMetadata, name string) bool {
	for _, p := range ports {
		if p.Name == name {
			return true
		}
	}
	return false
}

func findPort(ports []domain.PortMetadata, name string) (domain.PortMetadata, bool) {
	for _, p := range ports {
		if p.Name == name {
			return p, true
		}
	}
	return domain.PortMetadata{}, false
}
