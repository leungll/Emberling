package mockmodel

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/registry"
)

// Tool messages of the release scripts. A deploy_change result is what the Runtime
// assembles from production's answers; a refusal is the REJECTED message it hands back.
const (
	healthyReleaseResult   = `{"operationKey":"op_act_1","version":"v2","patchDigest":"d2d2","healthy":true}`
	unhealthyReleaseResult = `{"operationKey":"op_act_1","version":"v2","patchDigest":"d2d2","healthy":false}`
	refusedMessage         = `{"status":"REJECTED","code":"RELEASE_REFUSED","message":"Production refused the release of op_act_1 (OUTSIDE_RELEASE_WINDOW); nothing was deployed under it."}`
	targetOccupiedMessage  = `{"status":"REJECTED","code":"TARGET_OCCUPIED","message":"token-service/staging is occupied by another release."}`
	notDeployedMessage     = `{"status":"REJECTED","code":"RELEASE_NOT_DEPLOYED","message":"Production voided op_act_2; nothing was deployed."}`
	approvalRejectedMsg    = `{"status":"REJECTED","code":"APPROVAL_REJECTED","message":"A person rejected the deploy_change call: require an expiry on every token."}`
	// writeHardenedResult is the write the resubmit script makes after the rejection.
	writeHardenedResult = `{"path":"internal/auth/token.go","bytesWritten":3050,"patchDigest":"d3d3","touchedPaths":["go.mod","internal/auth/token.go"]}`
	secondChangeRequest = `{"patchDigest":"d3d3","verificationFactId":"fact_v3","contentArtifactId":"artifact_cr2","contentDigest":"c1c1","basisCount":3,"changeRequestId":"cr_2"}`
	resubmittedRelease  = `{"operationKey":"op_act2_1","version":"v2","patchDigest":"d3d3","healthy":true}`
)

// releaseResults are the main release script's results up to its change request: the main
// verify results, the inserted adaptation, the passing verification and the change request.
func releaseResults() []string {
	return append(mainVerifyResults(), writeTokenResult, verifyResult("d2d2", true), changeRequestResult)
}

// finalReport decodes the JSON document a FINAL text carries.
func finalReport(t *testing.T, response registry.ModelResponse) map[string]any {
	t.Helper()
	var report map[string]any
	if err := json.Unmarshal([]byte(finalText(t, response)), &report); err != nil {
		t.Fatalf("FINAL text is not a JSON document: %v", err)
	}
	return report
}

// TestProvider_Generate_ReleaseTrajectory_AsksForReleaseOfTheChangeRequest plays the main
// release script to its release request and its healthy end: the ninth Turn releases the
// change request it recorded, and a healthy release ends with a RELEASED report naming the
// operation, the version and the patch.
func TestProvider_Generate_ReleaseTrajectory_AsksForReleaseOfTheChangeRequest(t *testing.T) {
	release, err := generateVulnTurn(t, "vuln-fix-release", releaseResults()...)
	if err != nil {
		t.Fatalf("Generate() after the change request error = %v", err)
	}
	assertToolCall(t, release, "deploy_change", `{"changeRequestId":"cr_1"}`)

	final, err := generateVulnTurn(t, "vuln-fix-release", append(releaseResults(), healthyReleaseResult)...)
	if err != nil {
		t.Fatalf("final Generate() error = %v", err)
	}
	report := finalReport(t, final)
	if report["status"] != "RELEASED" || report["operationKey"] != "op_act_1" || report["version"] != "v2" ||
		report["healthy"] != true || report["patchDigest"] != "d2d2" || report["changeRequestId"] != "cr_1" {
		t.Fatalf("FINAL report = %v, want RELEASED op_act_1 as v2, healthy, for d2d2 and cr_1", report)
	}
}

// TestProvider_Generate_ReleaseTrajectory_UnhealthyTargetReportedAsIs proves the script
// reports a deployed but unhealthy target as it is and leaves the rollback to a person.
func TestProvider_Generate_ReleaseTrajectory_UnhealthyTargetReportedAsIs(t *testing.T) {
	final, err := generateVulnTurn(t, "vuln-fix-release", append(releaseResults(), unhealthyReleaseResult)...)
	if err != nil {
		t.Fatalf("Generate() after an unhealthy release error = %v", err)
	}
	report := finalReport(t, final)
	if report["status"] != "DEPLOYED_CHECK_FAILED" || report["healthy"] != false || report["operationKey"] != "op_act_1" ||
		!strings.Contains(report["nextStep"].(string), "rollback plan") {
		t.Fatalf("FINAL report = %v, want DEPLOYED_CHECK_FAILED, unhealthy, with the rollback left to a person", report)
	}
}

// TestProvider_Generate_ReleaseTrajectory_EachRefusalEndsWithItsReport proves every definite
// refusal of the release request ends the script with a report that names the refusal's
// code and message, the patch and the change request, instead of a second release request.
func TestProvider_Generate_ReleaseTrajectory_EachRefusalEndsWithItsReport(t *testing.T) {
	cases := map[string]struct{ message, status, code string }{
		"refused":         {refusedMessage, "RELEASE_REFUSED", "RELEASE_REFUSED"},
		"target occupied": {targetOccupiedMessage, "TARGET_OCCUPIED", "TARGET_OCCUPIED"},
		"not deployed":    {notDeployedMessage, "RELEASE_NOT_DEPLOYED", "RELEASE_NOT_DEPLOYED"},
		"goal denied":     {goalDeniedMessage, "PREPARED_ONLY", "GOAL_DENIED"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			final, err := generateVulnTurn(t, "vuln-fix-release", append(releaseResults(), tc.message)...)
			if err != nil {
				t.Fatalf("Generate() after %s error = %v", tc.code, err)
			}
			report := finalReport(t, final)
			var refusal map[string]any
			_ = json.Unmarshal([]byte(tc.message), &refusal)
			if report["status"] != tc.status || report["code"] != tc.code || report["reason"] != refusal["message"] ||
				report["patchDigest"] != "d2d2" || report["changeRequestId"] != "cr_1" {
				t.Fatalf("FINAL report = %v, want %s with code %s, the refusal message, d2d2 and cr_1", report, tc.status, tc.code)
			}
		})
	}
}

// TestProvider_Generate_RejectedResubmitTrajectory_AdaptsVerifiesAndAsksAgain plays the
// resubmit script: a rejected release request inserts the change the person asked for,
// whose result stands in for the refused call; the script verifies that new patch, records
// a second change request about it, asks to release that one, and its report names both
// change requests and the second release.
func TestProvider_Generate_RejectedResubmitTrajectory_AdaptsVerifiesAndAsksAgain(t *testing.T) {
	const script = "vuln-fix-rejected-resubmit"
	results := releaseResults()
	first, err := generateVulnTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() turn 8 error = %v", err)
	}
	assertToolCall(t, first, "deploy_change", `{"changeRequestId":"cr_1"}`)

	results = append(results, approvalRejectedMsg)
	hardened, err := generateVulnTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() after the rejection error = %v", err)
	}
	if hardened.Decision.Kind != registry.DecisionToolCall || *hardened.Decision.ToolName != "write_file" ||
		!strings.Contains(string(hardened.Decision.Arguments), `"path":"internal/auth/token.go"`) ||
		!strings.Contains(string(hardened.Decision.Arguments), "WithExpirationRequired") {
		t.Fatalf("Decision after the rejection = %+v, want the token.go write that requires an expiry", hardened.Decision)
	}

	results = append(results, writeHardenedResult)
	verify, err := generateVulnTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() after the hardening error = %v", err)
	}
	assertToolCall(t, verify, "verify_patch", `{"patchDigest":"d3d3"}`)

	results = append(results, verifyResult("d3d3", true))
	changeRequest, err := generateVulnTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() after the third verification error = %v", err)
	}
	assertChangeRequestCall(t, changeRequest, "d3d3")

	results = append(results, secondChangeRequest)
	second, err := generateVulnTurn(t, script, results...)
	if err != nil {
		t.Fatalf("Generate() after the second change request error = %v", err)
	}
	assertToolCall(t, second, "deploy_change", `{"changeRequestId":"cr_2"}`)

	final, err := generateVulnTurn(t, script, append(results, resubmittedRelease)...)
	if err != nil {
		t.Fatalf("final Generate() error = %v", err)
	}
	report := finalReport(t, final)
	if report["status"] != "RELEASED" || report["patchDigest"] != "d3d3" || report["changeRequestId"] != "cr_2" ||
		report["rejectedChangeRequestId"] != "cr_1" || report["operationKey"] != "op_act2_1" || report["healthy"] != true {
		t.Fatalf("FINAL report = %v, want RELEASED d3d3 under cr_2 after cr_1 was rejected", report)
	}
}

// TestProvider_Generate_ReleaseTrajectories_SameInputYieldsSameSequence proves determinism
// of the release scripts, every branch included: each Turn requested twice with the same
// request returns the same Decision.
func TestProvider_Generate_ReleaseTrajectories_SameInputYieldsSameSequence(t *testing.T) {
	cases := map[string][]string{
		"vuln-fix-release healthy":   append(releaseResults(), healthyReleaseResult),
		"vuln-fix-release unhealthy": append(releaseResults(), unhealthyReleaseResult),
		"vuln-fix-release refused":   append(releaseResults(), refusedMessage),
		"vuln-fix-release occupied":  append(releaseResults(), targetOccupiedMessage),
		"vuln-fix-release voided":    append(releaseResults(), notDeployedMessage),
		"vuln-fix-release goal":      append(releaseResults(), goalDeniedMessage),
		"vuln-fix-rejected-resubmit": append(releaseResults(), approvalRejectedMsg, writeHardenedResult, verifyResult("d3d3", true), secondChangeRequest, resubmittedRelease),
	}
	for name, results := range cases {
		script := strings.Fields(name)[0]
		t.Run(name, func(t *testing.T) {
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
