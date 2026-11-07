package mockmodel

import (
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/registry"
)

// TestProvider_Generate_IncompleteFinal_AdaptsAndDeclaresDoneWithoutVerifyingAgain plays
// the incomplete-final script: after the failed verification of the unadapted upgrade the
// Agent adapts the code and answers FINAL straight away, naming the adapted patch and the
// earlier verification's digest, without verifying the adapted patch or recording a change
// request. The Runtime's completion check, not the script, finds what is missing.
func TestProvider_Generate_IncompleteFinal_AdaptsAndDeclaresDoneWithoutVerifyingAgain(t *testing.T) {
	const script = "vuln-fix-incomplete-final"
	results := mainVerifyResults()

	adapt, err := generateVulnTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() after the failed verification error = %v", err)
	}
	if adapt.Decision.Kind != registry.DecisionToolCall || *adapt.Decision.ToolName != "write_file" ||
		!strings.Contains(string(adapt.Decision.Arguments), `"path":"internal/auth/token.go"`) ||
		!strings.Contains(string(adapt.Decision.Arguments), "RegisteredClaims") {
		t.Fatalf("Decision after the failed verification = %+v, want the adapting write_file of internal/auth/token.go", adapt.Decision)
	}

	final, err := generateVulnTurn(t, script, append(results, writeTokenResult)...)
	if err != nil {
		t.Fatalf("Generate() after the adapting write error = %v", err)
	}
	want := `{"advisory":"GO-2025-3553","patchDigest":"d2d2","resultDigest":"sha256:r-d1b1","status":"PATCH_VERIFIED","verifiedDigest":"d1b1"}`
	if got := finalText(t, final); got != want {
		t.Fatalf("FINAL text =\n%s\nwant\n%s", got, want)
	}
}

// TestProvider_Generate_IncompleteFinal_PassingVerificationStillAdaptsThenFinal proves the
// script does not depend on the verification's verdict: whatever the first verification
// says, the next Decisions are the adapting write and the FINAL, so the story holds against
// either runner.
func TestProvider_Generate_IncompleteFinal_PassingVerificationStillAdaptsThenFinal(t *testing.T) {
	const script = "vuln-fix-incomplete-final"
	results := []string{readResult, readResult, readResult, writeGoModResult, writeImportResult, verifyResult("d1b1", true)}

	adapt, err := generateVulnTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() after the verification error = %v", err)
	}
	if adapt.Decision.Kind != registry.DecisionToolCall || *adapt.Decision.ToolName != "write_file" {
		t.Fatalf("Decision = %+v, want the adapting write_file", adapt.Decision)
	}
	final, err := generateVulnTurn(t, script, append(results, writeTokenResult)...)
	if err != nil {
		t.Fatalf("Generate() after the adapting write error = %v", err)
	}
	if final.Decision.Kind != registry.DecisionFinal {
		t.Fatalf("Decision = %+v, want FINAL", final.Decision)
	}
}
