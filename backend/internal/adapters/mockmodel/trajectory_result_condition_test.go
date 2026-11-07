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
	writeImportResult  = `{"path":"internal/auth/token.go","bytesWritten":2900,"patchDigest":"d1b1","touchedPaths":["go.mod","internal/auth/token.go"]}`
	writeTokenResult   = `{"path":"internal/auth/token.go","bytesWritten":3041,"patchDigest":"d2d2","touchedPaths":["go.mod","internal/auth/token.go"]}`
	scopeDeniedMessage = `{"status":"REJECTED","code":"SCOPE_DENIED","message":"tests/auth_test.go is outside the allowed directories."}`
	stalePatchMessage  = `{"status":"REJECTED","code":"STALE_PATCH","message":"patchDigest is not the workspace's current patch."}`
	goalDeniedMessage  = `{"status":"REJECTED","code":"GOAL_DENIED","message":"the goal does not include deployment."}`
	// changeRequestResult is what create_change_request returns for the adapted patch.
	changeRequestResult = `{"patchDigest":"d2d2","verificationFactId":"fact_v2","contentArtifactId":"artifact_cr","contentDigest":"c0c0","basisCount":2,"changeRequestId":"cr_1"}`
)

// assertChangeRequestCall checks a Decision is the create_change_request call of the
// vulnerability-fix scripts: it names digest and cites the rules and the advisory read
// earlier, the advisory under the path the task's identifier resolves to.
func assertChangeRequestCall(t *testing.T, response registry.ModelResponse, digest string) {
	t.Helper()
	if response.Decision.Kind != registry.DecisionToolCall || *response.Decision.ToolName != "create_change_request" {
		t.Fatalf("Decision = %+v, want a create_change_request call", response.Decision)
	}
	args := string(response.Decision.Arguments)
	for _, want := range []string{`"patchDigest":"` + digest + `"`, `"manifestRef":"RULES.md"`, `"manifestRef":"advisories/GO-2025-3553.md"`, `"summary":"`, `"impact":"`, `"rollbackPlan":"`} {
		if !strings.Contains(args, want) {
			t.Errorf("create_change_request arguments %s lack %s", args, want)
		}
	}
}

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

// mainVerifyResults are the first six results of the main verify script: three reads,
// the go.mod write, the import-only write and the verification of that unadapted upgrade,
// which failed.
func mainVerifyResults() []string {
	return []string{readResult, readResult, readResult, writeGoModResult, writeImportResult, verifyResult("d1b1", false)}
}

// TestProvider_Generate_ResultCondition_FailedVerificationInsertsAdaptationOnce plays the
// main verify script: the sixth result reports passed=false, the condition inserts the
// adaptation write, the next scripted Turn verifies the digest of that inserted write, the
// change request names that digest and the FINAL names the passing verification.
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
	// own seventh Turn, which verifies the latest patch digest.
	results = append(results, writeTokenResult)
	verify, err := generateVulnTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() after the inserted write error = %v", err)
	}
	assertToolCall(t, verify, "verify_patch", `{"patchDigest":"d2d2"}`)

	results = append(results, verifyResult("d2d2", true))
	changeRequest, err := generateVulnTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() after the passing verification error = %v", err)
	}
	assertChangeRequestCall(t, changeRequest, "d2d2")

	results = append(results, changeRequestResult)
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
// to its own next Turn, the change request, instead of adapting once more. The Runtime,
// not the script, refuses a change request about an unverified patch.
func TestProvider_Generate_ResultCondition_SecondFailureDoesNotInsertAgain(t *testing.T) {
	results := append(mainVerifyResults(), writeTokenResult, verifyResult("d2d2", false))

	next, err := generateVulnTurn(t, "vuln-fix-main-verify", results...)
	if err != nil {
		t.Fatalf("Generate() after the second failure error = %v", err)
	}
	assertChangeRequestCall(t, next, "d2d2")
}

// TestProvider_Generate_ResultCondition_PassingVerificationSkipsTheInsertedStep keeps the
// script linear when the condition does not match: a verification that passed is followed
// by the script's own next Turn, which verifies the same digest again.
func TestProvider_Generate_ResultCondition_PassingVerificationSkipsTheInsertedStep(t *testing.T) {
	results := []string{readResult, readResult, readResult, writeGoModResult, writeImportResult, verifyResult("d1b1", true)}

	next, err := generateVulnTurn(t, "vuln-fix-main-verify", results...)
	if err != nil {
		t.Fatalf("Generate() after a passing verification error = %v", err)
	}
	assertToolCall(t, next, "verify_patch", `{"patchDigest":"d1b1"}`)
}

// TestProvider_Generate_RejectedCondition_MatchesByCode plays the scope-denied script: a
// REJECTED message with code SCOPE_DENIED inserts the write inside the allowed directory,
// whose result stands in for the refused call; the script then reads the rules and the
// advisory, adapts the code, verifies, records the change request and the FINAL names the
// verified patch.
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

	results := []string{scopeDeniedMessage, writeGoModResult}
	next, err := generateVulnTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() after the inserted write error = %v", err)
	}
	assertToolCall(t, next, "read_file", `{"path":"RULES.md"}`)

	results = append(results, readResult, readResult, writeTokenResult)
	verify, err := generateVulnTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() after the adaptation error = %v", err)
	}
	assertToolCall(t, verify, "verify_patch", `{"patchDigest":"d2d2"}`)

	results = append(results, verifyResult("d2d2", true))
	changeRequest, err := generateVulnTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() after the verification error = %v", err)
	}
	assertChangeRequestCall(t, changeRequest, "d2d2")

	final, err := generateVulnTurn(t, script, append(results, changeRequestResult)...)
	if err != nil {
		t.Fatalf("final Generate() error = %v", err)
	}
	if got := finalText(t, final); !strings.Contains(got, `"patchDigest":"d2d2"`) || !strings.Contains(got, `"status":"SCOPE_RESPECTED"`) || !strings.Contains(got, `"passed":true`) {
		t.Fatalf("FINAL text = %s, want the verified patch inside the allowed directories", got)
	}
}

// TestProvider_Generate_RejectedCondition_OtherCodeIsAnOrdinaryResult reads a REJECTED
// message whose code the script has no condition for as an ordinary result: the script's
// own next Turn is answered, with the refusal counted as that Turn's result, not the
// condition's inserted write; the script never guesses a branch for a code it does not
// name.
func TestProvider_Generate_RejectedCondition_OtherCodeIsAnOrdinaryResult(t *testing.T) {
	next, err := generateVulnTurn(t, "vuln-fix-scope-denied", goalDeniedMessage)
	if err != nil {
		t.Fatalf("Generate() after GOAL_DENIED error = %v", err)
	}
	assertToolCall(t, next, "read_file", `{"path":"RULES.md"}`)
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
// the stale-evidence script end to end: after the reads, the sixth Turn names the first
// patch's digest, the STALE_PATCH refusal inserts a verification of the latest digest, the
// change request names that digest and the FINAL binds it.
func TestProvider_Generate_StaleEvidenceTrajectory_OldDigestRejectedThenVerifiesLatest(t *testing.T) {
	const script = "vuln-fix-stale-evidence"
	results := []string{readResult, readResult, writeGoModResult, verifyResult("d1d1", true), writeTokenResult}

	stale, err := generateVulnTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() turn 5 error = %v", err)
	}
	assertToolCall(t, stale, "verify_patch", `{"patchDigest":"d1d1"}`)

	inserted, err := generateVulnTurn(t, script, append(results, stalePatchMessage)...)
	if err != nil {
		t.Fatalf("Generate() after STALE_PATCH error = %v", err)
	}
	assertToolCall(t, inserted, "verify_patch", `{"patchDigest":"d2d2"}`)

	changeRequest, err := generateVulnTurn(t, script, append(results, stalePatchMessage, verifyResult("d2d2", true))...)
	if err != nil {
		t.Fatalf("Generate() after the latest verification error = %v", err)
	}
	assertChangeRequestCall(t, changeRequest, "d2d2")

	final, err := generateVulnTurn(t, script, append(results, stalePatchMessage, verifyResult("d2d2", true), changeRequestResult)...)
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
		"vuln-fix-main-verify":    append(mainVerifyResults(), writeTokenResult, verifyResult("d2d2", true), changeRequestResult),
		"vuln-fix-scope-denied":   {scopeDeniedMessage, writeGoModResult, readResult, readResult, writeTokenResult, verifyResult("d2d2", true), changeRequestResult},
		"vuln-fix-stale-evidence": {readResult, readResult, writeGoModResult, verifyResult("d1d1", true), writeTokenResult, stalePatchMessage, verifyResult("d2d2", true), changeRequestResult},
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

// TestProvider_Generate_EmbeddedPlaceholder_ReadsTheAdvisoryNamedByTheTask plays the second
// Turn of the main verify script: the read path embeds the task's advisory identifier, so
// the Agent reads the real advisory file rather than a literal placeholder path.
func TestProvider_Generate_EmbeddedPlaceholder_ReadsTheAdvisoryNamedByTheTask(t *testing.T) {
	response, err := generateVulnTurn(t, "vuln-fix-main-verify", readResult)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	assertToolCall(t, response, "read_file", `{"path":"advisories/GO-2025-3553.md"}`)
}

// TestPlaceholderResolver_EmbeddedPlaceholders covers the string forms a script may use:
// a whole-string placeholder keeps its value's type, an embedded one is text, a non-string
// value is embedded as compact JSON, and an unresolvable embedded placeholder fails the
// value explicitly.
func TestPlaceholderResolver_EmbeddedPlaceholders(t *testing.T) {
	resolver := placeholderResolver{
		request:     scriptedVulnRequest(t, "vuln-fix-main-verify", writeGoModResult),
		toolResults: []json.RawMessage{json.RawMessage(writeGoModResult)},
	}
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{
		{name: "whole string keeps the array type", raw: `{"paths":"{{tool:0:/touchedPaths}}"}`, want: `{"paths":["go.mod"]}`},
		{name: "embedded string value", raw: `{"path":"advisories/{{task:/advisory}}.md"}`, want: `{"path":"advisories/GO-2025-3553.md"}`},
		{name: "two embedded placeholders", raw: `"{{task:/service}}@{{tool:0:/patchDigest}}"`, want: `"token-service@d1d1"`},
		{name: "embedded non-string is compact JSON", raw: `"wrote {{tool:0:/bytesWritten}} bytes to {{tool:0:/touchedPaths}}"`, want: `"wrote 80 bytes to [\"go.mod\"]"`},
		{name: "embedded placeholder misses its member", raw: `"advisories/{{task:/missing}}.md"`, wantErr: "placeholder {{task:/missing}}"},
		{name: "embedded placeholder names a missing tool result", raw: `"{{tool:3:/patchDigest}} later"`, wantErr: "request carries no tool result 3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolver.resolve(json.RawMessage(tc.raw))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("resolve(%s) error = %v, want it to contain %q", tc.raw, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve(%s) error = %v", tc.raw, err)
			}
			if string(got) != tc.want {
				t.Fatalf("resolve(%s) = %s, want %s", tc.raw, got, tc.want)
			}
		})
	}
}
