package sandboxrunner

import (
	"context"
	"testing"
)

// TestMockVerificationBackend_UpgradeWithoutAdaptation_FailsTestsLikeTheRealBuild proves the
// mock backend reads the patch it is handed, not a mock mode: a patch that moves go.mod and
// the import to the new major version while the code still uses the removed API is reported
// as a failed test step, which is what compiling it really does.
func TestMockVerificationBackend_UpgradeWithoutAdaptation_FailsTestsLikeTheRealBuild(t *testing.T) {
	for _, name := range []string{"upgrade-without-adaptation.patch", "fix-gomod-only.patch"} {
		t.Run(name, func(t *testing.T) {
			outcome := NewMockVerificationBackend().Verify(context.Background(), VerificationJob{Patch: fixturePatch(t, name)})
			if !outcome.Verdict || outcome.TestsPassed || outcome.Tests.Failed == 0 || !outcome.ScanClean || !outcome.DependenciesClean {
				t.Fatalf("outcome = %+v, want a verdict with the tests failed and the other checks clean", outcome)
			}
		})
	}
}

func TestMockVerificationBackend_CompleteFix_Passes(t *testing.T) {
	outcome := NewMockVerificationBackend().Verify(context.Background(), VerificationJob{Patch: fixturePatch(t, "fix-complete.patch")})
	if !outcome.Verdict || !outcome.TestsPassed || !outcome.ScanClean || !outcome.DependenciesClean {
		t.Fatalf("outcome = %+v, want every check passed", outcome)
	}
}

func TestMockVerificationBackend_MockModeOverridesPatchJudgement(t *testing.T) {
	outcome := NewMockVerificationBackend().Verify(context.Background(), VerificationJob{Patch: fixturePatch(t, "upgrade-without-adaptation.patch"), Mock: mockPass})
	if !outcome.TestsPassed {
		t.Fatalf("outcome = %+v, want the named mock mode to decide", outcome)
	}
}
