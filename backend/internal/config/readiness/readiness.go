// Package readiness runs the fixed Backend startup gate and answers the
// liveness and readiness probes. It contains no knowledge of PostgreSQL, storage or the
// Registry: the process wires concrete checks in, so the ordering rule stays independent
// of the components it gates.
package readiness

import (
	"context"
	"fmt"
	"sync"
)

// Step names the startup gates in their fixed order. The order is part of the
// contract: the Reconciler may not start before storage is verified, and the process may
// not accept requests before the first scan is known to be running.
type Step string

const (
	StepDatabase      Step = "database_connect_and_migrate" // 1
	StepConfiguration Step = "configuration_and_secrets"    // 2
	StepRegistry      Step = "registry_registration"        // 3
	StepAssetStorage  Step = "asset_storage_read_write"     // 4
	StepReconciler    Step = "reconciler_start"             // 5
	StepFirstScan     Step = "first_reconciliation_scan"    // 6
	StepAcceptRequest Step = "accept_requests"              // 7
)

// order is the single source of truth for the gate sequence.
var order = []Step{
	StepDatabase,
	StepConfiguration,
	StepRegistry,
	StepAssetStorage,
	StepReconciler,
	StepFirstScan,
	StepAcceptRequest,
}

// Order returns the fixed startup gate order.
func Order() []Step {
	out := make([]Step, len(order))
	copy(out, order)
	return out
}

// CheckFunc performs one startup gate. A nil CheckFunc means the owning component is not
// part of this build yet; the step is recorded as unwired and the sequence continues.
type CheckFunc func(ctx context.Context) error

// Checks holds one entry per step. Steps 3 to 6 are pluggable so that the tracks that own
// the Registry, storage and the Reconciler can fill them in without changing this order.
// Step 7 has no work of its own: reaching it is what makes the process ready.
type Checks struct {
	Database      CheckFunc
	Configuration CheckFunc
	Registry      CheckFunc
	AssetStorage  CheckFunc
	Reconciler    CheckFunc
	FirstScan     CheckFunc
}

func (c Checks) byStep(step Step) CheckFunc {
	switch step {
	case StepDatabase:
		return c.Database
	case StepConfiguration:
		return c.Configuration
	case StepRegistry:
		return c.Registry
	case StepAssetStorage:
		return c.AssetStorage
	case StepReconciler:
		return c.Reconciler
	case StepFirstScan:
		return c.FirstScan
	default:
		return nil
	}
}

// StepError reports the gate that stopped startup. The failing step is named so that an
// operator can act on it without reading the startup log backwards.
type StepError struct {
	Step  Step
	Index int
	Err   error
}

func (e *StepError) Error() string {
	return fmt.Sprintf("readiness: step %d/%d %s failed: %v", e.Index, len(order), e.Step, e.Err)
}

func (e *StepError) Unwrap() error { return e.Err }

// Observer receives the outcome of each step. It exists so the process can log progress
// without this package choosing a logger.
type Observer interface {
	StepPassed(step Step, wired bool)
	StepFailed(step Step, err error)
}

// Probe holds the process readiness state behind the liveness and readiness endpoints.
// Liveness reports whether the process can keep working; it never fails because a Run
// failed or because work is backing up.
type Probe struct {
	mu           sync.RWMutex
	ready        bool
	shuttingDown bool
	completed    []Step
}

// NewProbe returns a Probe that is live but not ready.
func NewProbe() *Probe { return &Probe{} }

// Live reports process liveness. It is true until shutdown begins.
func (p *Probe) Live() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return !p.shuttingDown
}

// Ready reports whether the Backend may accept new requests. It is true only after the
// whole startup gate, up to and including step 7, has passed.
func (p *Probe) Ready() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.ready && !p.shuttingDown
}

// CompletedSteps returns the steps that passed, in order.
func (p *Probe) CompletedSteps() []Step {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]Step, len(p.completed))
	copy(out, p.completed)
	return out
}

// BeginShutdown stops new requests from being accepted. Liveness also turns false so an
// orchestrator does not restart a process that is already draining.
func (p *Probe) BeginShutdown() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shuttingDown = true
	p.ready = false
}

// Run executes the startup gate in the fixed order and stops at the first failure. The
// Probe stays not ready unless every step passes, so a partially started Backend never
// accepts a Run.
func (p *Probe) Run(ctx context.Context, checks Checks, observer Observer) error {
	p.mu.Lock()
	p.ready = false
	p.completed = nil
	p.mu.Unlock()

	for i, step := range order {
		if err := ctx.Err(); err != nil {
			return &StepError{Step: step, Index: i + 1, Err: err}
		}

		check := checks.byStep(step)
		if check != nil {
			if err := check(ctx); err != nil {
				if observer != nil {
					observer.StepFailed(step, err)
				}
				return &StepError{Step: step, Index: i + 1, Err: err}
			}
		}

		p.mu.Lock()
		p.completed = append(p.completed, step)
		p.mu.Unlock()

		if observer != nil {
			observer.StepPassed(step, check != nil)
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.shuttingDown {
		return nil
	}
	p.ready = true
	return nil
}
