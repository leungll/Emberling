package sandboxrunner

import (
	"context"
	"errors"
)

// Fixed counts the mock Backend reports.
const (
	mockTotalTests  = 12
	mockFailedTests = 2
)

// mockErrorCode is the failure code of the mock "error" mode.
const mockErrorCode = "SANDBOX_ERROR"

// MockBackend answers every test from the request's mock mode without running anything:
// "pass" (the default) passes all tests, "fail" fails some, "error" produces no verdict,
// and "duplicate" and "lost" pass but change how the callback is delivered.
type MockBackend struct{}

// NewMockBackend returns the deterministic mock Backend.
func NewMockBackend() MockBackend { return MockBackend{} }

// ValidateMock accepts an empty mode and every named mock mode.
func (MockBackend) ValidateMock(mock string) error {
	switch mock {
	case "", mockPass, mockFail, mockError, mockDuplicate, mockLost:
		return nil
	}
	return errors.New(`mock must be absent or one of "pass", "fail", "error", "duplicate" and "lost"`)
}

// Run returns the outcome the job's mock mode names.
func (MockBackend) Run(_ context.Context, job Job) Outcome {
	switch job.Mock {
	case mockFail:
		return Outcome{Verdict: true, Passed: false, Total: mockTotalTests, Failed: mockFailedTests}
	case mockError:
		return Outcome{FailureCode: mockErrorCode, FailureMessage: "mock sandbox could not run the tests"}
	default:
		return Outcome{Verdict: true, Passed: true, Total: mockTotalTests}
	}
}
