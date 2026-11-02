package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/store"
)

// validateProviderCapacity checks that every Provider id the capacity configuration
// limits is the asynchronous Provider of at least one registered model. A limit on an
// unknown id would never govern anything, which is a misconfiguration rather than a
// no-op, so the Backend does not become ready with it.
func validateProviderCapacity(models *registry.ModelRegistry, limits map[string]int) error {
	if len(limits) == 0 {
		return nil
	}
	known := make(map[string]struct{})
	for _, model := range models.ListMetadata() {
		if model.ProviderID != "" {
			known[model.ProviderID] = struct{}{}
		}
	}
	var unknown []string
	for id := range limits {
		if _, ok := known[id]; !ok {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("provider capacity: %v name no registered model's provider", unknown)
}

// seedProviderSlotLocks inserts the row admission locks for every limited Provider. The
// insert keeps existing rows, so repeated starts and concurrent processes converge.
func seedProviderSlotLocks(ctx context.Context, uow store.UnitOfWork, limits map[string]int) error {
	if len(limits) == 0 {
		return nil
	}
	ids := make([]string, 0, len(limits))
	for id := range limits {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.ProviderSlots().SeedLocks(ctx, ids)
	}); err != nil {
		return fmt.Errorf("provider capacity: seed provider slot locks: %w", err)
	}
	return nil
}
