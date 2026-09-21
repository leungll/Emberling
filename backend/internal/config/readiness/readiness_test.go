package readiness_test

import (
	"context"
	"errors"
	"testing"

	"github.com/leungll/Emberling/backend/internal/config/readiness"
)

type recorder struct {
	passed []readiness.Step
	failed readiness.Step
}

func (r *recorder) StepPassed(step readiness.Step, _ bool)  { r.passed = append(r.passed, step) }
func (r *recorder) StepFailed(step readiness.Step, _ error) { r.failed = step }

func TestReadiness_StepFails_StopsAndStaysNotReady(t *testing.T) {
	ctx := context.Background()
	probe := readiness.NewProbe()
	obs := &recorder{}

	storageErr := errors.New("asset storage is not writable")
	var registryRan, reconcilerRan bool

	err := probe.Run(ctx, readiness.Checks{
		Database:      func(context.Context) error { return nil },
		Configuration: func(context.Context) error { return nil },
		Registry:      func(context.Context) error { registryRan = true; return nil },
		AssetStorage:  func(context.Context) error { return storageErr },
		Reconciler:    func(context.Context) error { reconcilerRan = true; return nil },
		FirstScan:     func(context.Context) error { return nil },
	}, obs)

	var stepErr *readiness.StepError
	if !errors.As(err, &stepErr) {
		t.Fatalf("Run with a failing step: want *readiness.StepError, got %v", err)
	}
	if stepErr.Step != readiness.StepAssetStorage || stepErr.Index != 4 {
		t.Errorf("failing step: want %s at index 4, got %s at index %d",
			readiness.StepAssetStorage, stepErr.Step, stepErr.Index)
	}
	if !errors.Is(err, storageErr) {
		t.Error("StepError must wrap the underlying failure")
	}
	if !registryRan {
		t.Error("steps before the failure must run")
	}
	if reconcilerRan {
		t.Error("the Reconciler must not start after an earlier gate failed")
	}
	if probe.Ready() {
		t.Error("Ready: want false after a failed startup gate")
	}
	if !probe.Live() {
		t.Error("Live: want true, a failed startup gate is not a liveness failure")
	}
	if got := probe.CompletedSteps(); len(got) != 3 {
		t.Errorf("completed steps: want the 3 gates before the failure, got %v", got)
	}
	if obs.failed != readiness.StepAssetStorage {
		t.Errorf("observer failure: want %s, got %s", readiness.StepAssetStorage, obs.failed)
	}
}

func TestReadiness_AllStepsPass_ReadyOnlyAfterFinalStep(t *testing.T) {
	ctx := context.Background()
	probe := readiness.NewProbe()
	obs := &recorder{}

	// Wired step 6 asserts the process is still not ready while the first scan runs.
	firstScanSawReady := false
	err := probe.Run(ctx, readiness.Checks{
		Database:      func(context.Context) error { return nil },
		Configuration: func(context.Context) error { return nil },
		FirstScan: func(context.Context) error {
			firstScanSawReady = probe.Ready()
			return nil
		},
	}, obs)
	if err != nil {
		t.Fatalf("Run with passing steps: %v", err)
	}
	if firstScanSawReady {
		t.Error("Ready: want false while the first reconciliation scan is still running")
	}
	if !probe.Ready() {
		t.Error("Ready: want true after the final startup gate")
	}
	if got, want := len(obs.passed), len(readiness.Order()); got != want {
		t.Errorf("observed steps: want %d, got %d", want, got)
	}
}

func TestReadiness_UnwiredSteps_AreRecordedNotSkippedFromOrder(t *testing.T) {
	probe := readiness.NewProbe()
	obs := &recorder{}

	if err := probe.Run(context.Background(), readiness.Checks{
		Database:      func(context.Context) error { return nil },
		Configuration: func(context.Context) error { return nil },
	}, obs); err != nil {
		t.Fatalf("Run with only steps 1 and 2 wired: %v", err)
	}

	want := readiness.Order()
	got := probe.CompletedSteps()
	if len(got) != len(want) {
		t.Fatalf("completed steps: want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("step %d: want %s, got %s", i, want[i], got[i])
		}
	}
}

func TestReadiness_Order_MatchesOperationsSequence(t *testing.T) {
	want := []readiness.Step{
		readiness.StepDatabase,
		readiness.StepConfiguration,
		readiness.StepRegistry,
		readiness.StepAssetStorage,
		readiness.StepReconciler,
		readiness.StepFirstScan,
		readiness.StepAcceptRequest,
	}
	got := readiness.Order()
	if len(got) != len(want) {
		t.Fatalf("Order: want %d steps, got %v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("step %d: want %s, got %s", i+1, want[i], got[i])
		}
	}

	// Order must return a copy: a caller cannot rewrite the startup contract.
	got[0] = readiness.StepAcceptRequest
	if readiness.Order()[0] != readiness.StepDatabase {
		t.Fatal("Order must return a defensive copy")
	}
}

func TestReadiness_ShutdownAfterReady_StopsAcceptingRequests(t *testing.T) {
	probe := readiness.NewProbe()
	if err := probe.Run(context.Background(), readiness.Checks{
		Database:      func(context.Context) error { return nil },
		Configuration: func(context.Context) error { return nil },
	}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !probe.Ready() {
		t.Fatal("Ready: want true before shutdown")
	}

	probe.BeginShutdown()

	if probe.Ready() {
		t.Error("Ready: want false once shutdown begins")
	}
	if probe.Live() {
		t.Error("Live: want false once shutdown begins")
	}
}
