package sandboxrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

// Fixed values the mock verification backend reports.
const (
	mockVerificationTests   = 9
	mockVerificationFailed  = 1
	mockVerificationCommit  = "baseline"
	mockVulnerableAdvisory  = "GO-2025-3553"
	mockVulnerableDependent = "github.com/golang-jwt/jwt@v3.2.2+incompatible (GO-2025-3553, indirect)"
)

// MockVerificationBackend answers every verification from the request's mock mode without
// running anything: "pass" (the default) passes all three checks; "tests-fail" fails the
// test suite; "scan-vulnerable" passes the tests but the scan reports an advisory;
// "dependencies-vulnerable" passes the tests and the scan but an indirect dependency stays
// at an affected version; "error" produces no verdict; "log-overflow" passes with a log
// over the result bound; "duplicate" and "lost" pass but change how the callback is
// delivered.
type MockVerificationBackend struct{}

// NewMockVerificationBackend returns the deterministic mock verification backend.
func NewMockVerificationBackend() MockVerificationBackend { return MockVerificationBackend{} }

// ValidateMock accepts an empty mode and every named verification mock mode.
func (MockVerificationBackend) ValidateMock(mock string) error {
	switch mock {
	case "", mockPass, mockTestsFail, mockScanVulnerable, mockDependenciesVulnerable, mockError, mockDuplicate, mockLost, mockLogOverflow:
		return nil
	}
	return errors.New(`mock must be absent or one of "pass", "tests-fail", "scan-vulnerable", "dependencies-vulnerable", "error", "log-overflow", "duplicate" and "lost"`)
}

// Verify returns the outcome the job's mock mode names.
func (MockVerificationBackend) Verify(_ context.Context, job VerificationJob) VerificationOutcome {
	clean := VerificationOutcome{
		Verdict: true, TestsPassed: true, ScanClean: true, DependenciesClean: true, ScanRan: true,
		Tests: testCounts{Total: mockVerificationTests}, Scanner: scannerSnapshot, SnapshotDigest: "sha256:mock",
		Log: []byte(fmt.Sprintf("mock verification of patch %s on %s\n", job.PatchDigest, job.BaseCommit)),
	}
	switch job.Mock {
	case mockTestsFail:
		clean.TestsPassed = false
		clean.Tests.Failed = mockVerificationFailed
	case mockScanVulnerable:
		clean.ScanClean = false
		clean.ScanFindings = []string{mockVulnerableAdvisory}
	case mockDependenciesVulnerable:
		clean.DependenciesClean = false
		clean.VulnerableDependencies = []string{mockVulnerableDependent}
	case mockError:
		return VerificationOutcome{FailureCode: mockErrorCode, FailureMessage: "mock sandbox could not run the verification"}
	case mockLogOverflow:
		clean.Log = bytes.Repeat([]byte("mock verification output line\n"), MaxVerificationLogBytes/16)
	}
	return clean
}
