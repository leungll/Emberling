package runtime

import (
	"sort"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// validateGraph is the Compiler's fourth stage: the DAG-shape rules that ConfigSchema and
// Semantics cannot express -- edge type compatibility, required-input cardinality, cycle
// freedom, exactly one Output Node, that Output Node being the unique DAG sink, and full
// reachability of every node to it. It returns the resolved Output Node id (best-effort,
// "" if none/ambiguous) and every violation found.
//
// A cycle makes topological order and forward reachability meaningless, so this stage
// returns immediately once it detects one, before running the output/reachability checks.
func validateGraph(def domain.Definition, catalog NodeCatalog) (string, []ValidationError) {
	var errs []ValidationError

	metadata := make(map[string]domain.NodeMetadata, len(def.Nodes))
	for _, node := range def.Nodes {
		meta, _ := catalog.NodeMetadata(node.Type) // Structure stage already guaranteed ok
		metadata[node.ID] = meta
	}

	// --- Edge type compatibility --------------------------------------------------
	for _, edge := range def.Edges {
		srcPort, srcOK := findPort(metadata[edge.Source].Outputs, edge.SourceHandle)
		dstPort, dstOK := findPort(metadata[edge.Target].Inputs, edge.TargetHandle)
		if !srcOK || !dstOK {
			// Structure stage already reported this edge as UNKNOWN_PORT; Compile never
			// reaches Graph in that case, but stay defensive rather than panic.
			continue
		}
		if !srcPort.DataType.IsCompatibleWith(dstPort.DataType) {
			errs = append(errs, ValidationError{
				Code:   CodeIncompatibleEdge,
				NodeID: edge.Target,
				Path:   "edges[" + edge.ID + "]",
				Message: "edge \"" + edge.ID + "\" carries " + string(srcPort.DataType) +
					" into a " + string(dstPort.DataType) + " port",
			})
		}
	}

	// --- Required-input cardinality -------------------------------------------------
	incomingByTarget := make(map[string]map[string]int, len(def.Nodes))
	for _, edge := range def.Edges {
		if incomingByTarget[edge.Target] == nil {
			incomingByTarget[edge.Target] = make(map[string]int)
		}
		incomingByTarget[edge.Target][edge.TargetHandle]++
	}
	for _, node := range def.Nodes {
		for _, port := range metadata[node.ID].Inputs {
			if !port.Required {
				continue
			}
			count := incomingByTarget[node.ID][port.Name]
			switch {
			case count == 0:
				errs = append(errs, ValidationError{
					Code:    CodeMissingRequiredInput,
					NodeID:  node.ID,
					Path:    "nodes[" + node.ID + "].inputs." + port.Name,
					Message: "required input port \"" + port.Name + "\" has no incoming edge",
				})
			case count > 1:
				errs = append(errs, ValidationError{
					Code:    CodeAmbiguousInput,
					NodeID:  node.ID,
					Path:    "nodes[" + node.ID + "].inputs." + port.Name,
					Message: "required input port \"" + port.Name + "\" has more than one incoming edge",
				})
			}
		}
	}

	// --- Cycle detection -------------------------------------------------------------
	if hasCycle(def) {
		errs = append(errs, ValidationError{
			Code:    CodeDAGHasCycle,
			Message: "definition contains a cycle",
		})
		return "", errs
	}

	// --- Exactly one Output Node -----------------------------------------------------
	var outputNodeIDs []string
	outgoing := make(map[string]int, len(def.Nodes))
	for _, edge := range def.Edges {
		outgoing[edge.Source]++
	}
	for _, node := range def.Nodes {
		if isOutputNodeType(node.Type) {
			outputNodeIDs = append(outputNodeIDs, node.ID)
		}
	}
	switch len(outputNodeIDs) {
	case 0:
		errs = append(errs, ValidationError{
			Code:    CodeOutputNodeCount,
			Message: "definition has no Output Node",
		})
	case 1:
		// ok
	default:
		errs = append(errs, ValidationError{
			Code:    CodeOutputNodeCount,
			Message: "definition has more than one Output Node",
		})
	}

	var outputNodeID string
	if len(outputNodeIDs) == 1 {
		outputNodeID = outputNodeIDs[0]
		if outgoing[outputNodeID] > 0 {
			errs = append(errs, ValidationError{
				Code:    CodeOutputNodeNotUniqueSink,
				NodeID:  outputNodeID,
				Message: "the Output Node must be the unique sink of the DAG but has outgoing edges",
			})
		}
	}

	// --- Full reachability to the Output Node -----------------------------------------
	if outputNodeID != "" {
		reachable := reachableTo(def, outputNodeID)
		var unreachable []string
		for _, node := range def.Nodes {
			if !reachable[node.ID] {
				unreachable = append(unreachable, node.ID)
			}
		}
		sort.Strings(unreachable)
		for _, nodeID := range unreachable {
			errs = append(errs, ValidationError{
				Code:    CodeNodeUnreachableToOutput,
				NodeID:  nodeID,
				Message: "node \"" + nodeID + "\" cannot reach the Output Node",
			})
		}
	}

	return outputNodeID, errs
}

// hasCycle reports whether def's edges form a cycle, via Kahn's algorithm: the graph is
// acyclic exactly when every node can be stripped away by repeatedly removing zero
// -indegree nodes. Parallel edges between the same ordered node pair count once for
// indegree purposes (distinct-predecessor completion, not edge count).
func hasCycle(def domain.Definition) bool {
	indegree := make(map[string]int, len(def.Nodes))
	adj := make(map[string][]string, len(def.Nodes))
	for _, node := range def.Nodes {
		indegree[node.ID] = 0
	}
	seenEdge := make(map[[2]string]bool, len(def.Edges))
	for _, edge := range def.Edges {
		key := [2]string{edge.Source, edge.Target}
		if seenEdge[key] {
			continue
		}
		seenEdge[key] = true
		adj[edge.Source] = append(adj[edge.Source], edge.Target)
		indegree[edge.Target]++
	}

	var queue []string
	for _, node := range def.Nodes {
		if indegree[node.ID] == 0 {
			queue = append(queue, node.ID)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, next := range adj[id] {
			indegree[next]--
			if indegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	return visited != len(def.Nodes)
}

// reachableTo returns the set of node ids with a directed path to target, including
// target itself, by walking predecessor edges backward from target. Parallel edges
// between the same ordered node pair are deduped before the walk.
func reachableTo(def domain.Definition, target string) map[string]bool {
	predecessors := make(map[string][]string, len(def.Nodes))
	seenEdge := make(map[[2]string]bool, len(def.Edges))
	for _, edge := range def.Edges {
		key := [2]string{edge.Source, edge.Target}
		if seenEdge[key] {
			continue
		}
		seenEdge[key] = true
		predecessors[edge.Target] = append(predecessors[edge.Target], edge.Source)
	}

	reachable := map[string]bool{target: true}
	queue := []string{target}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, pred := range predecessors[id] {
			if !reachable[pred] {
				reachable[pred] = true
				queue = append(queue, pred)
			}
		}
	}
	return reachable
}
