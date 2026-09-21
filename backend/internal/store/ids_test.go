package store_test

import (
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

func TestUUIDGenerator_NewID_IsPrefixedAndUnique(t *testing.T) {
	var gen domain.IDGenerator = store.UUIDGenerator{}

	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := gen.NewID(domain.IDPrefixNodeRun)
		if !strings.HasPrefix(id, domain.IDPrefixNodeRun+"_") {
			t.Fatalf("NewID: want the %q prefix, got %q", domain.IDPrefixNodeRun, id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("NewID returned a duplicate identifier: %q", id)
		}
		seen[id] = struct{}{}
	}
}
