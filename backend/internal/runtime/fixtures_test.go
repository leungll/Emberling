package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// loadFixture reads a Definition fixture from backend/test/fixtures/definitions. Fixtures
// carry no `version`/`runInputSchema`/`validation` (those are Compile's output), so the
// returned Definition is exactly what a client would submit to validate/save.
func loadFixture(t *testing.T, name string) domain.Definition {
	t.Helper()
	path := filepath.Join("..", "..", "test", "fixtures", "definitions", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	var def domain.Definition
	if err := json.Unmarshal(raw, &def); err != nil {
		t.Fatalf("unmarshal fixture %s: %v", path, err)
	}
	return def
}

// cloneDefinition deep-copies def via JSON round-trip, so a test can mutate the clone
// (drop an edge, corrupt a node) without affecting other tests that load the same
// fixture.
func cloneDefinition(t *testing.T, def domain.Definition) domain.Definition {
	t.Helper()
	raw, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("marshal definition: %v", err)
	}
	var clone domain.Definition
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatalf("unmarshal definition: %v", err)
	}
	return clone
}

func containsCode(errs []ValidationError, code string) bool {
	for _, e := range errs {
		if e.Code == code {
			return true
		}
	}
	return false
}

func removeEdge(def domain.Definition, edgeID string) domain.Definition {
	var edges []domain.Edge
	for _, e := range def.Edges {
		if e.ID != edgeID {
			edges = append(edges, e)
		}
	}
	def.Edges = edges
	return def
}

func removeNode(def domain.Definition, nodeID string) domain.Definition {
	var nodes []domain.Node
	for _, n := range def.Nodes {
		if n.ID != nodeID {
			nodes = append(nodes, n)
		}
	}
	def.Nodes = nodes
	return def
}
