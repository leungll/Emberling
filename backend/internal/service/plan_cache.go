package service

import (
	"sync"

	"github.com/leungll/Emberling/backend/internal/runtime"
)

// planCacheCapacity bounds the number of compiled Definition plans held in memory at
// once. A cached plan is immutable and never invalidated: a Definition version never
// changes once saved, so nothing ever needs to evict a *correct* entry, only bound
// memory. 256 is an MVP default (same category as runtime/retry.go's backoff constants
// and adapters/mockmodel's request-recording bound), not a documented requirement.
const planCacheCapacity = 256

type planCacheKey struct {
	workflowID string
	version    int
}

// planCache is a bounded, mutex-guarded cache of compiled Definition plans keyed by
// Workflow ID and version. CreateRun and Advance both need the same
// *runtime.CompiledDefinition repeatedly across a Run's lifetime, and Compile is not
// free (JSON Schema compilation plus graph analysis), so it is worth remembering.
//
// Eviction is FIFO rather than LRU: correctness never depends on which entry is evicted
// (a miss just recompiles), so the simpler bookkeeping is preferred over an LRU's extra
// moving parts (CLAUDE.md: "optimize for correctness and maintainability before
// abstraction").
type planCache struct {
	mu       sync.Mutex
	capacity int
	order    []planCacheKey
	entries  map[planCacheKey]*runtime.CompiledDefinition
}

func newPlanCache(capacity int) *planCache {
	if capacity <= 0 {
		capacity = planCacheCapacity
	}
	return &planCache{
		capacity: capacity,
		entries:  make(map[planCacheKey]*runtime.CompiledDefinition),
	}
}

func (c *planCache) get(workflowID string, version int) (*runtime.CompiledDefinition, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	plan, ok := c.entries[planCacheKey{workflowID, version}]
	return plan, ok
}

func (c *planCache) put(workflowID string, version int, plan *runtime.CompiledDefinition) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := planCacheKey{workflowID, version}
	if _, exists := c.entries[key]; exists {
		c.entries[key] = plan
		return
	}
	if len(c.order) >= c.capacity {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
	c.order = append(c.order, key)
	c.entries[key] = plan
}
