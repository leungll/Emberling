package mockmodel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/registry"
)

// Tool messages the vulnerability-fix scripts read. Only the members a script matches on
// or reads through a placeholder are present.
const (
	vulnTask           = `{"advisory":"GO-2025-3553","goal":"PREPARE_ONLY","service":"token-service"}`
	readResult         = `{"path":"RULES.md","patchSeq":0,"kind":"file","content":"rules","sizeBytes":5,"truncated":false}`
	writeGoModResult   = `{"path":"go.mod","bytesWritten":80,"patchDigest":"d1d1","touchedPaths":["go.mod"]}`
	writeTokenResult   = `{"path":"internal/auth/token.go","bytesWritten":3041,"patchDigest":"d2d2","touchedPaths":["go.mod","internal/auth/token.go"]}`
	scopeDeniedMessage = `{"status":"REJECTED","code":"SCOPE_DENIED","message":"tests/auth_test.go is outside the allowed directories."}`
	stalePatchMessage  = `{"status":"REJECTED","code":"STALE_PATCH","message":"patchDigest is not the workspace's current patch."}`
	goalDeniedMessage  = `{"status":"REJECTED","code":"GOAL_DENIED","message":"the goal does not include deployment."}`
)

func verifyResult(digest string, passed bool) string {
	return `{"passed":` + boolText(passed) + `,"testsPassed":` + boolText(passed) + `,"scanClean":true,"dependenciesClean":true,` +
		`"baseCommit":"2706ca6fb4ddbe969e9acfa5464a347ff90c7774","patchDigest":"` + digest + `","resultDigest":"sha256:r-` + digest + `",` +
		`"summary":{"tests":{"total":9,"failed":1},"scanner":"govulncheck","scanFindings":[],"vulnerableDependencies":[],"snapshotDigest":"sha256:mock"},` +
		`"logArtifactId":"artifact_log","logTruncated":false}`
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// scriptedVulnRequest builds a request for a vulnerability-fix script with the task the
// task_input node publishes and the given tool results, oldest first.
func scriptedVulnRequest(t *testing.T, script string, toolResults ...string) registry.ModelRequest {
	t.Helper()
	request := scriptedRequest(t, script, toolResults...)
	request.Messages[0] = userMessage(t, vulnTask)
	return request
}

func generateVulnTurn(t *testing.T, script string, toolResults ...string) (registry.ModelResponse, error) {
	t.Helper()
	return NewProvider().Generate(context.Background(), scriptedVulnRequest(t, script, toolResults...))
}

func finalText(t *testing.T, response registry.ModelResponse) string {
	t.Helper()
	var text string
	if response.Decision.Kind != registry.DecisionFinal || json.Unmarshal(response.Decision.Output, &text) != nil {
		t.Fatalf("Decision = %+v, want FINAL with a JSON string output", response.Decision)
	}
	return text
}

// mainVerifyResults are the first five results of the main verify script: three reads,
// the go.mod write and a verification that failed.
func mainVerifyResults() []string {
	return []string{readResult, readResult, readResult, writeGoModResult, verifyResult("d1d1", false)}
}

// TestProvider_Generate_ResultCondition_FailedVerificationInsertsAdaptationOnce plays the
// main verify script: the fifth result reports passed=false, the condition inserts the
// adaptation write, the next scripted Turn verifies the digest of that inserted write, and
// the FINAL names the passing verification.
func TestProvider_Generate_ResultCondition_FailedVerificationInsertsAdaptationOnce(t *testing.T) {
	const script = "vuln-fix-main-verify"
	results := mainVerifyResults()

	inserted, err := generateVulnTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() after the failed verification error = %v", err)
	}
	if inserted.Decision.Kind != registry.DecisionToolCall || *inserted.Decision.ToolName != "write_file" ||
		!strings.Contains(string(inserted.Decision.Arguments), `"path":"internal/auth/token.go"`) {
		t.Fatalf("inserted Decision = %+v, want write_file of internal/auth/token.go", inserted.Decision)
	}

	// The inserted write's result is not a scripted result: the next Turn is the script's
	// own sixth Turn, which verifies the latest patch digest.
	results = append(results, writeTokenResult)
	verify, err := generateVulnTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() after the inserted write error = %v", err)
	}
	assertToolCall(t, verify, "verify_patch", `{"mock":"pass","patchDigest":"d2d2"}`)

	results = append(results, verifyResult("d2d2", true))
	final, err := generateVulnTurn(t, script, results...)
	if err != nil {
		t.Fatalf("final Generate() error = %v", err)
	}
	want := `{"advisory":"GO-2025-3553","passed":true,"patchDigest":"d2d2","resultDigest":"sha256:r-d2d2","status":"PATCH_VERIFIED"}`
	if got := finalText(t, final); got != want {
		t.Fatalf("FINAL text =\n%s\nwant\n%s", got, want)
	}
}

// TestProvider_Generate_ResultCondition_SecondFailureDoesNotInsertAgain bounds the inserted
// step: a second verification that also fails is an ordinary result, so the script goes on
// to its FINAL, which reports the failure instead of adapting once more.
func TestProvider_Generate_ResultCondition_SecondFailureDoesNotInsertAgain(t *testing.T) {
	results := append(mainVerifyResults(), writeTokenResult, verifyResult("d2d2", false))

	final, err := generateVulnTurn(t, "vuln-fix-main-verify", results...)
	if err != nil {
		t.Fatalf("Generate() after the second failure error = %v", err)
	}
	if got := finalText(t, final); !strings.Contains(got, `"passed":false`) || !strings.Contains(got, `"patchDigest":"d2d2"`) {
		t.Fatalf("FINAL text = %s, want the failed verification of d2d2 reported", got)
	}
}

// TestProvider_Generate_ResultCondition_PassingVerificationSkipsTheInsertedStep keeps the
// script linear when the condition does not match: a verification that passed is followed
// by the script's own next Turn, which verifies the same digest again.
func TestProvider_Generate_ResultCondition_PassingVerificationSkipsTheInsertedStep(t *testing.T) {
	results := []string{readResult, readResult, readResult, writeGoModResult, verifyResult("d1d1", true)}

	next, err := generateVulnTurn(t, "vuln-fix-main-verify", results...)
	if err != nil {
		t.Fatalf("Generate() after a passing verification error = %v", err)
	}
	assertToolCall(t, next, "verify_patch", `{"mock":"pass","patchDigest":"d1d1"}`)
}

// TestProvider_Generate_RejectedCondition_MatchesByCode plays the scope-denied script: a
// REJECTED message with code SCOPE_DENIED inserts the write inside the allowed directory,
// whose result stands in for the refused call, and the FINAL names its patch.
func TestProvider_Generate_RejectedCondition_MatchesByCode(t *testing.T) {
	const script = "vuln-fix-scope-denied"

	inserted, err := generateVulnTurn(t, script, scopeDeniedMessage)
	if err != nil {
		t.Fatalf("Generate() after SCOPE_DENIED error = %v", err)
	}
	if inserted.Decision.Kind != registry.DecisionToolCall || *inserted.Decision.ToolName != "write_file" ||
		!strings.Contains(string(inserted.Decision.Arguments), `"path":"go.mod"`) {
		t.Fatalf("inserted Decision = %+v, want write_file of go.mod", inserted.Decision)
	}

	final, err := generateVulnTurn(t, script, scopeDeniedMessage, writeGoModResult)
	if err != nil {
		t.Fatalf("final Generate() error = %v", err)
	}
	if got := finalText(t, final); !strings.Contains(got, `"patchDigest":"d1d1"`) || !strings.Contains(got, `"status":"SCOPE_RESPECTED"`) {
		t.Fatalf("FINAL text = %s, want the allowed write's patch digest", got)
	}
}

// TestProvider_Generate_RejectedCondition_OtherCodeFailsExplicitly refuses a REJECTED
// message whose code the script has no condition for: it is read as an ordinary result, so
// the next placeholder fails instead of the script guessing a branch.
func TestProvider_Generate_RejectedCondition_OtherCodeFailsExplicitly(t *testing.T) {
	_, err := generateVulnTurn(t, "vuln-fix-scope-denied", goalDeniedMessage)

	if err == nil || !strings.Contains(err.Error(), "placeholder {{tool:0:/patchDigest}}") {
		t.Fatalf("Generate() error = %v, want GOAL_DENIED read as an ordinary result", err)
	}
}

// TestProvider_Generate_RejectedCondition_SameCodeTwiceFailsExplicitly bounds a coded
// condition like a status condition: the same code arriving again fails the Turn.
func TestProvider_Generate_RejectedCondition_SameCodeTwiceFailsExplicitly(t *testing.T) {
	_, err := generateVulnTurn(t, "vuln-fix-scope-denied", scopeDeniedMessage, scopeDeniedMessage)

	if err == nil || !strings.Contains(err.Error(), "tool status REJECTED/SCOPE_DENIED arrived 2 times") {
		t.Fatalf("Generate() error = %v, want the second SCOPE_DENIED refused", err)
	}
}

// TestProvider_Generate_StaleEvidenceTrajectory_OldDigestRejectedThenVerifiesLatest plays
// the stale-evidence script end to end: the fourth Turn names the first patch's digest, the
// STALE_PATCH refusal inserts a verification of the latest digest, and the FINAL binds it.
func TestProvider_Generate_StaleEvidenceTrajectory_OldDigestRejectedThenVerifiesLatest(t *testing.T) {
	const script = "vuln-fix-stale-evidence"
	results := []string{writeGoModResult, verifyResult("d1d1", true), writeTokenResult}

	stale, err := generateVulnTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() turn 3 error = %v", err)
	}
	assertToolCall(t, stale, "verify_patch", `{"patchDigest":"d1d1"}`)

	inserted, err := generateVulnTurn(t, script, append(results, stalePatchMessage)...)
	if err != nil {
		t.Fatalf("Generate() after STALE_PATCH error = %v", err)
	}
	assertToolCall(t, inserted, "verify_patch", `{"patchDigest":"d2d2"}`)

	final, err := generateVulnTurn(t, script, append(results, stalePatchMessage, verifyResult("d2d2", true))...)
	if err != nil {
		t.Fatalf("final Generate() error = %v", err)
	}
	if got := finalText(t, final); !strings.Contains(got, `"patchDigest":"d2d2"`) || !strings.Contains(got, `"passed":true`) {
		t.Fatalf("FINAL text = %s, want the latest digest verified", got)
	}
}

// TestProvider_Generate_VulnTrajectories_SameInputYieldsSameSequence proves determinism:
// every Turn of each script, requested twice with the same request, returns the same
// Decision, conditions included.
func TestProvider_Generate_VulnTrajectories_SameInputYieldsSameSequence(t *testing.T) {
	cases := map[string][]string{
		"vuln-fix-main-verify":    append(mainVerifyResults(), writeTokenResult, verifyResult("d2d2", true)),
		"vuln-fix-scope-denied":   {scopeDeniedMessage, writeGoModResult},
		"vuln-fix-stale-evidence": {writeGoModResult, verifyResult("d1d1", true), writeTokenResult, stalePatchMessage, verifyResult("d2d2", true)},
	}
	for script, results := range cases {
		t.Run(script, func(t *testing.T) {
			for n := 0; n <= len(results); n++ {
				first, err := generateVulnTurn(t, script, results[:n]...)
				if err != nil {
					t.Fatalf("turn %d Generate() error = %v", n, err)
				}
				second, err := generateVulnTurn(t, script, results[:n]...)
				if err != nil {
					t.Fatalf("turn %d second Generate() error = %v", n, err)
				}
				if first.Decision.Kind != second.Decision.Kind || string(first.Decision.Arguments) != string(second.Decision.Arguments) ||
					string(first.Decision.Output) != string(second.Decision.Output) {
					t.Fatalf("turn %d differs between two requests: %+v vs %+v", n, first.Decision, second.Decision)
				}
			}
		})
	}
}

func TestParseTrajectory_MalformedResultCondition_Rejected(t *testing.T) {
	const turns = `"turns":[{"toolCall":{"toolName":"t","arguments":{}}},{"final":{"output":1}}]`
	cases := map[string]string{
		"no when":             `{` + turns + `,"onToolResult":[{"toolCall":{"toolName":"u","arguments":{}}}]}`,
		"negative tool index": `{` + turns + `,"onToolResult":[{"when":{"tool":-1,"pointer":"/passed","equals":false},"toolCall":{"toolName":"u","arguments":{}}}]}`,
		"tool index is final": `{` + turns + `,"onToolResult":[{"when":{"tool":1,"pointer":"/passed","equals":false},"toolCall":{"toolName":"u","arguments":{}}}]}`,
		"bad pointer":         `{` + turns + `,"onToolResult":[{"when":{"tool":0,"pointer":"passed","equals":false},"toolCall":{"toolName":"u","arguments":{}}}]}`,
		"no equals":           `{` + turns + `,"onToolResult":[{"when":{"tool":0,"pointer":"/passed"},"toolCall":{"toolName":"u","arguments":{}}}]}`,
		"no decision":         `{` + turns + `,"onToolResult":[{"when":{"tool":0,"pointer":"/passed","equals":false}}]}`,
		"two for one result": `{` + turns + `,"onToolResult":[` +
			`{"when":{"tool":0,"pointer":"/passed","equals":false},"toolCall":{"toolName":"u","arguments":{}}},` +
			`{"when":{"tool":0,"pointer":"/scanClean","equals":false},"toolCall":{"toolName":"v","arguments":{}}}]}`,
		"code outside REJECTED": `{` + turns + `,"onToolStatus":[{"status":"RESULT_UNCERTAIN","code":"X","toolCall":{"toolName":"u","arguments":{}}}]}`,
		"duplicate code": `{` + turns + `,"onToolStatus":[` +
			`{"status":"REJECTED","code":"SCOPE_DENIED","toolCall":{"toolName":"u","arguments":{}}},` +
			`{"status":"REJECTED","code":"SCOPE_DENIED","toolCall":{"toolName":"v","arguments":{}}}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseTrajectory([]byte(raw)); err == nil {
				t.Fatal("parseTrajectory() error = nil, want rejection")
			}
		})
	}
}
