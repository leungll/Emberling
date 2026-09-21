package domain

import (
	"errors"
	"fmt"
)

// Sentinel errors shared across layers. Transport mapping lives in the api layer; the
// store layer translates driver-specific failures into these.
var (
	// ErrNotFound is returned when a requested fact does not exist.
	ErrNotFound = errors.New("emberling: not found")

	// ErrConflict is returned when a uniqueness or identity constraint rejects a write.
	ErrConflict = errors.New("emberling: conflict")

	// ErrVersionConflict is returned when a Definition version already exists. Stored
	// versions are immutable, so the caller must create a new version instead.
	ErrVersionConflict = fmt.Errorf("emberling: definition version conflict: %w", ErrConflict)

	// ErrStaleClaim is returned when a conditional update affected zero rows. The caller
	// lost the race: another claimant, a callback, a timeout or the Reconciler already
	// advanced this fact. It is an expected outcome, not a failure to report upward.
	ErrStaleClaim = errors.New("emberling: stale claim")
)

// InvalidStateTransitionError reports a transition the state machine does not allow. It
// is distinct from ErrStaleClaim: the transition itself is illegal, not merely lost.
type InvalidStateTransitionError struct {
	Entity string
	ID     string
	From   string
	To     string
}

func (e *InvalidStateTransitionError) Error() string {
	return fmt.Sprintf("emberling: invalid %s transition %s -> %s (id=%s)", e.Entity, e.From, e.To, e.ID)
}

// Unwrap lets callers match this against ErrConflict when mapping to HTTP 409.
func (e *InvalidStateTransitionError) Unwrap() error { return ErrConflict }
