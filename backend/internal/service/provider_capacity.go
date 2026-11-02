package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// Provider capacity admission. An asynchronous Provider runs at most the configured
// number of tasks at once, counted as NodeRuns that hold a persisted slot. Admission
// happens inside the Advance claim transaction, between the scheduler's choice and the
// READY -> RUNNING claim, under the Run aggregate lock first and the Provider row lock
// second; it issues SQL and in-memory Registry lookups only, never a Provider call. A
// refused NodeRun stays READY, is marked as queued once, and is woken again when a slot
// of its Provider is released or when the Reconciler rediscovers it.

// CapacityHooks are optional observation points inside Provider slot admission. They exist
// so failure tests can hold one admission at an exact point while another races it,
// without sleeping.
type CapacityHooks struct {
	// AfterProviderLock runs inside the claim transaction right after the Provider row
	// lock was taken, before the held count is read. A second admission of the same
	// Provider is blocked on that row lock until this transaction ends.
	AfterProviderLock func(nodeRunID string)
}

// nodeWaitingCapacityPayload is NODE_WAITING_CAPACITY: the Provider the NodeRun waits
// for, the limit it hit, and its queue position at the moment of the first refusal. The
// position is a snapshot; the live position is a read-time projection.
type nodeWaitingCapacityPayload struct {
	ProviderID string `json:"providerId"`
	Limit      int    `json:"limit"`
	Position   int    `json:"position"`
}

// capacityGovernance names the Provider a candidate would occupy a slot of and the limit
// that applies. governed is false for every candidate capacity does not cover: a node
// type that is not registered or not ASYNC, one that declares no dispatch model, a Model
// ID that resolves to no asynchronous Provider, or a Provider that has no limit.
type capacityGovernance struct {
	providerID string
	limit      int
	governed   bool
}

// governedProvider decides whether capacity governs a candidate, from registered
// metadata and the frozen node config only. A Model ID that fails to resolve is left to
// Execute, whose existing registration checks fail the NodeRun explicitly.
func (s *ExecutionService) governedProvider(def domain.Definition, nr domain.NodeRun) (capacityGovernance, error) {
	if len(s.deps.ProviderCapacity) == 0 || s.deps.Models == nil {
		return capacityGovernance{}, nil
	}
	reg, ok := s.deps.Nodes.Get(nr.NodeType)
	if !ok || reg.Metadata.ExecutionKind != domain.NodeExecutionAsync || reg.Metadata.DispatchModelPath == "" {
		return capacityGovernance{}, nil
	}
	node, ok := nodeByID(def, nr.NodeID)
	if !ok {
		return capacityGovernance{}, fmt.Errorf("execution: definition %s v%d no longer declares node %q", def.WorkflowID, def.Version, nr.NodeID)
	}
	config, err := decodeConfig(node.Config)
	if err != nil {
		return capacityGovernance{}, err
	}
	modelID, ok := configString(config, reg.Metadata.DispatchModelPath)
	if !ok {
		return capacityGovernance{}, nil
	}
	providerID, err := s.deps.Models.ProviderID(modelID)
	if err != nil {
		return capacityGovernance{}, nil
	}
	limit, ok := s.deps.ProviderCapacity[providerID]
	if !ok {
		return capacityGovernance{}, nil
	}
	return capacityGovernance{providerID: providerID, limit: limit, governed: true}, nil
}

// configString reads the string at a dotted property path of a decoded node config.
func configString(config map[string]any, path string) (string, bool) {
	current := any(config)
	for _, segment := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return "", false
		}
		current, ok = object[segment]
		if !ok {
			return "", false
		}
	}
	value, ok := current.(string)
	return value, ok && value != ""
}

// runQueueKey is the position a Run competes with for one Provider: the earliest
// (ready_at, id) among the candidate and this Run's READY NodeRuns already queued for the
// same Provider. The scheduler may pick a node that became READY later than one the Run
// already queued; comparing with the Run's earliest queued key keeps the Run from being
// blocked by its own earlier arrival, which would stall every later arrival behind it.
func runQueueKey(nodeRuns []domain.NodeRun, candidate domain.NodeRun, providerID string) store.QueueKey {
	key := store.QueueKey{ReadyAt: candidate.ReadyAt, NodeRunID: candidate.ID}
	for _, nr := range nodeRuns {
		if nr.ID == candidate.ID || nr.Status != domain.NodeRunReady || nr.CapacityDeferredAt == nil {
			continue
		}
		if nr.CapacityProviderID == nil || *nr.CapacityProviderID != providerID {
			continue
		}
		if queueKeyLess(store.QueueKey{ReadyAt: nr.ReadyAt, NodeRunID: nr.ID}, key) {
			key = store.QueueKey{ReadyAt: nr.ReadyAt, NodeRunID: nr.ID}
		}
	}
	return key
}

func queueKeyLess(a, b store.QueueKey) bool {
	if !a.ReadyAt.Equal(b.ReadyAt) {
		return a.ReadyAt.Before(b.ReadyAt)
	}
	return a.NodeRunID < b.NodeRunID
}

// capacityAdmission is the outcome of admitCandidate. admitted means the caller may claim
// the candidate; deferred means this call recorded the first refusal and appended
// NODE_WAITING_CAPACITY, so the transaction has Events to notify about.
type capacityAdmission struct {
	admitted bool
	deferred bool
}

// admitCandidate decides whether a READY candidate may occupy a Provider slot now. The
// caller holds the Run aggregate lock; this takes the Provider row lock after it, counts
// the held slots under that lock, and checks that no earlier-queued NodeRun of another
// live Run is still waiting (first come, first served). Admission inserts the occupancy
// row in the same transaction the caller then claims and starts the Attempt in. A
// refusal marks the candidate queued on its first occurrence only; a repeated refusal
// writes nothing.
func (s *ExecutionService) admitCandidate(ctx context.Context, tx store.Tx, lock *store.RunLock, run domain.Run, def domain.Definition, nodeRuns []domain.NodeRun, nr domain.NodeRun, now time.Time) (capacityAdmission, error) {
	governance, err := s.governedProvider(def, nr)
	if err != nil {
		return capacityAdmission{}, err
	}
	if !governance.governed {
		return capacityAdmission{admitted: true}, nil
	}

	slots := tx.ProviderSlots()
	if err := slots.LockProvider(ctx, governance.providerID); err != nil {
		return capacityAdmission{}, fmt.Errorf("execution: admit node run %s to provider %s: %w", nr.ID, governance.providerID, err)
	}
	if hook := s.deps.CapacityHooks.AfterProviderLock; hook != nil {
		hook(nr.ID)
	}
	held, err := slots.CountHeld(ctx, governance.providerID)
	if err != nil {
		return capacityAdmission{}, err
	}
	key := runQueueKey(nodeRuns, nr, governance.providerID)
	earlier, err := slots.CountEarlierDeferred(ctx, governance.providerID, run.ID, key)
	if err != nil {
		return capacityAdmission{}, err
	}

	if held < governance.limit && earlier == 0 {
		// A READY NodeRun never holds a slot: the row is inserted only together with the
		// READY -> RUNNING claim and a NodeRun never returns to READY. A conflict here is
		// therefore a broken invariant and fails the transaction rather than being absorbed.
		if err := slots.Acquire(ctx, nr.ID, governance.providerID, now); err != nil {
			return capacityAdmission{}, err
		}
		return capacityAdmission{admitted: true}, nil
	}

	first, err := tx.NodeRuns().MarkCapacityDeferred(ctx, nr.ID, governance.providerID, now)
	if err != nil {
		return capacityAdmission{}, err
	}
	if !first {
		return capacityAdmission{}, nil
	}
	if err := s.appendEvent(ctx, tx, lock, run.ID, &nr.ID, domain.EventNodeWaitingCapacity, now, nodeWaitingCapacityPayload{
		ProviderID: governance.providerID,
		Limit:      governance.limit,
		Position:   earlier + 1,
	}); err != nil {
		return capacityAdmission{}, err
	}
	// The Run's status does not change while a NodeRun waits for a slot; the aggregate
	// write only persists the seq watermark the Event allocated.
	if err := tx.Runs().UpdateAggregate(ctx, lock, lock.Status(), now); err != nil {
		return capacityAdmission{}, err
	}
	return capacityAdmission{deferred: true}, nil
}
