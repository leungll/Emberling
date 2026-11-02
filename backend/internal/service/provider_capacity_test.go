package service

import (
	"context"
	"errors"
	"time"

	"github.com/leungll/Emberling/backend/internal/store"
)

var errNoSlotsNotImplemented = errors.New("no-slots fake: not implemented")

// noSlotsRepo is the store.ProviderSlotRepository double of the in-memory service
// fakes: no Provider is limited, no NodeRun holds a slot and nothing is queued. A release
// finds no row, which is the ordinary case for every ungoverned node; admission paths are
// never reached because those fakes configure no capacity.
type noSlotsRepo struct{}

func (noSlotsRepo) SeedLocks(context.Context, []string) error  { return errNoSlotsNotImplemented }
func (noSlotsRepo) LockProvider(context.Context, string) error { return errNoSlotsNotImplemented }
func (noSlotsRepo) CountHeld(context.Context, string) (int, error) {
	return 0, errNoSlotsNotImplemented
}
func (noSlotsRepo) Acquire(context.Context, string, string, time.Time) error {
	return errNoSlotsNotImplemented
}
func (noSlotsRepo) Release(context.Context, string) (string, bool, error) { return "", false, nil }
func (noSlotsRepo) CountEarlierDeferred(context.Context, string, string, store.QueueKey) (int, error) {
	return 0, errNoSlotsNotImplemented
}
func (noSlotsRepo) NextDeferredRun(context.Context, string) (string, bool, error) {
	return "", false, nil
}
