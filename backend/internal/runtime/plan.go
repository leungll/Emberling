package runtime

import (
	"errors"
	"sort"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// buildPlan is the Compiler's fifth stage: it produces the stable topological Order the
// Scheduler follows plus the Upstream/Downstream dependency indexes.
//
// Upstream/Downstream hold distinct predecessor/successor node ids, deduped by node pair
// rather than by edge count: two distinct edges between the same pair of nodes (e.g.
// targeting two different input ports) contribute one dependency, not two, so Kahn's
// indegree correctly represents "how many distinct upstream nodes remain", not "how many
// edges remain".
//
// buildPlan assumes def is already known to be acyclic; validateGraph must run first.
// It returns an error only if that invariant was violated by a caller bypassing Compile.
func buildPlan(def domain.Definition) (order []string, upstream, downstream map[string][]string, err error) {
	upstreamSet := make(map[string]map[string]bool, len(def.Nodes))
	downstreamSet := make(map[string]map[string]bool, len(def.Nodes))
	for _, node := range def.Nodes {
		upstreamSet[node.ID] = make(map[string]bool)
		downstreamSet[node.ID] = make(map[string]bool)
	}
	for _, edge := range def.Edges {
		upstreamSet[edge.Target][edge.Source] = true
		downstreamSet[edge.Source][edge.Target] = true
	}

	upstream = make(map[string][]string, len(def.Nodes))
	downstream = make(map[string][]string, len(def.Nodes))
	indegree := make(map[string]int, len(def.Nodes))
	for _, node := range def.Nodes {
		ups := setToSortedSlice(upstreamSet[node.ID])
		downs := setToSortedSlice(downstreamSet[node.ID])
		upstream[node.ID] = ups
		downstream[node.ID] = downs
		indegree[node.ID] = len(ups)
	}

	// Kahn's algorithm with ascending-node-id tie-breaking for a deterministic order.
	var ready []string
	for _, node := range def.Nodes {
		if indegree[node.ID] == 0 {
			ready = append(ready, node.ID)
		}
	}
	sort.Strings(ready)

	order = make([]string, 0, len(def.Nodes))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		var newlyReady []string
		for _, next := range downstream[id] {
			indegree[next]--
			if indegree[next] == 0 {
				newlyReady = append(newlyReady, next)
			}
		}
		if len(newlyReady) > 0 {
			ready = append(ready, newlyReady...)
			sort.Strings(ready)
		}
	}

	if len(order) != len(def.Nodes) {
		return nil, nil, nil, errors.New("runtime: buildPlan called on a cyclic definition")
	}
	return order, upstream, downstream, nil
}

func setToSortedSlice(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
