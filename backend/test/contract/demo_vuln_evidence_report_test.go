//go:build integration

package contract

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The vulnerability demo's evidence report is rendered offline from the data the demo
// collected after each variant. These tests render it from fixed data, so its sections,
// its verdicts and the absence of any secret are proven without a Compose stack.

const (
	evidencePatchOld = "1111111111111111111111111111111111111111111111111111111111111111"
	evidencePatchNew = "2222222222222222222222222222222222222222222222222222222222222222"
	// plantedSecret stands for every value the report must never print: it is planted in
	// the callback binding of a Tool Attempt and in a production record line.
	plantedSecret = "cbt-planted-secret-value-0123"
)

// evidenceVariant is one collected variant in the format the report's collect step writes.
func evidenceVariant(order int, name string, verdict string, runs ...map[string]any) map[string]any {
	if runs == nil {
		runs = []map[string]any{}
	}
	return map[string]any{
		"variant": name, "order": order, "agent": "scripted",
		"runIds":   runIDsOf(runs),
		"verdicts": map[string]any{"story": verdict, "invariants": "PASS", "ledger": "PASS"},
		"checks": map[string]any{
			"story":      []map[string]any{{"name": "run_checked", "passed": verdict == "PASS"}},
			"invariants": []map[string]any{{"name": "event_seq_contiguous", "passed": true}},
			"ledger":     []map[string]any{{"name": "release_operations_match_record", "passed": true}},
		},
		"runs": runs,
	}
}

func runIDsOf(runs []map[string]any) []string {
	ids := make([]string, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run["runId"].(string))
	}
	return ids
}

// evidenceRun is one collected Run with a single Agent Trace.
func evidenceRun(runID, status string, trace map[string]any, events []map[string]any) map[string]any {
	if events == nil {
		events = []map[string]any{}
	}
	return map[string]any{"runId": runID, "status": status, "traces": []map[string]any{trace}, "events": events}
}

// evidenceTrace builds an Agent Trace: two recorded patches of which only the newer passed
// verification, a change request for it, the given completion and release Turn.
func evidenceTrace(runID string, completion map[string]any, releaseTurn map[string]any) map[string]any {
	turns := []map[string]any{{
		"turnNo":   1,
		"decision": map[string]any{"kind": "TOOL_CALL", "tool": "verify_patch"},
		"action":   map[string]any{"id": "act_verify_" + runID, "status": "SUCCEEDED", "authorization": "ALLOW"},
		"toolAttempts": []map[string]any{{
			"id": "ta_verify_" + runID, "toolName": "verify_patch", "attemptNo": 1, "status": "SUCCEEDED",
			"callbackBinding": map[string]any{
				"providerId": "sandboxrunner", "externalTaskId": "ver_" + runID, "callbackToken": plantedSecret,
			},
		}},
	}}
	if releaseTurn != nil {
		turns = append(turns, releaseTurn)
	}
	return map[string]any{
		"agentRun": map[string]any{"termination": "FINAL_RESPONSE", "completion": completion, "error": nil},
		"turns":    turns,
		"workspace": map[string]any{"patches": []map[string]any{
			{"seq": 1, "patchDigest": evidencePatchOld}, {"seq": 2, "patchDigest": evidencePatchNew},
		}},
		"facts": map[string]any{"items": []map[string]any{
			{"id": "fact_old", "factType": "patch_verified", "subject": evidencePatchOld, "verdict": false},
			{"id": "fact_new", "factType": "patch_verified", "subject": evidencePatchNew, "verdict": true},
		}, "truncated": false},
		"changeRequests": map[string]any{"items": []map[string]any{
			{"id": "cr_" + runID, "patchDigest": evidencePatchNew, "verificationFactId": "fact_new"},
		}, "truncated": false},
	}
}

func completed(outcome string, missing ...map[string]any) map[string]any {
	if missing == nil {
		missing = []map[string]any{}
	}
	return map[string]any{
		"outcome": outcome,
		"checks": []map[string]any{
			{"name": "latest_patch_verified", "passed": true, "refs": []string{}},
			{"name": "agent_declared_completion", "passed": true, "refs": []string{}},
		},
		"missing": missing,
	}
}

// releaseTurn is a deploy_change Turn whose one operation has the given status and outcome
// and whose occupation was freed for reason, with an optional unchecked version.
func releaseTurn(actionID, attemptStatus, outcome, reason, unchecked string, stopped bool) map[string]any {
	key := "op_" + actionID + "_1"
	occupation := map[string]any{
		"id": "occupation_" + actionID, "targetKey": "token-service/staging", "operationKey": key,
		"status": "RELEASED", "releaseReason": reason, "deployedUncheckedVersion": nil,
	}
	if unchecked != "" {
		occupation["deployedUncheckedVersion"] = unchecked
	}
	var humanWait any
	if stopped {
		humanWait = map[string]any{"id": "hwait_" + actionID, "status": "STOPPED", "closedBy": "operator", "outcome": nil}
	}
	return map[string]any{
		"turnNo":   2,
		"decision": map[string]any{"kind": "TOOL_CALL", "tool": "deploy_change"},
		"action": map[string]any{
			"id": actionID, "status": map[bool]string{true: "SUCCEEDED", false: "FAILED"}[attemptStatus == "SUCCEEDED"],
			"authorization": "ASK",
			"approval":      map[string]any{"id": "approval_" + actionID, "status": "APPROVED", "decidedBy": "reviewer"},
			"release": map[string]any{
				"operations": []map[string]any{{"operationKey": key, "attemptNo": 1, "status": attemptStatus, "outcome": outcome}},
				"occupation": occupation, "humanWait": humanWait,
			},
		},
		"toolAttempts": []map[string]any{{
			"id": "ta_" + actionID, "toolName": "deploy_change", "attemptNo": 1, "status": attemptStatus,
			"callbackBinding": map[string]any{"providerId": "release", "externalTaskId": key},
		}},
	}
}

func deployedLines(actionID, version string) []map[string]any {
	key := "op_" + actionID + "_1"
	return []map[string]any{
		{"seq": 1, "event": "arrived", "kind": "release", "operationId": key, "authorization": "Bearer " + plantedSecret},
		{"seq": 2, "event": "responded", "kind": "release", "operationId": key, "status": 202},
		{"seq": 3, "event": "deployed", "kind": "release", "operationId": key, "patchDigest": evidencePatchNew, "version": version},
	}
}

// evidenceData is a consistent demo: the main release deployed once, a release a person
// stopped waiting for that production deployed afterwards, and an Agent that declared
// completion without the required facts.
func evidenceData() (variants []map[string]any, production, runner []map[string]any) {
	mainRun := evidenceRun("run_main", "COMPLETED",
		evidenceTrace("run_main", completed("DEPLOYED"), releaseTurn("act_rel_main", "SUCCEEDED", "DEPLOYED", "DEPLOYED", "", false)),
		[]map[string]any{{"seq": 1, "type": "AGENT_ACTION_COMPLETED", "payload": map[string]any{
			"completionSource": "CALLBACK", "toolAttemptId": "ta_verify_run_main",
		}}})
	stopRun := evidenceRun("run_stop", "FAILED",
		evidenceTrace("run_stop", completed("RELEASE_UNKNOWN"), releaseTurn("act_rel_stop", "FAILED", "UNKNOWN", "DEPLOYED", "v3", true)), nil)
	incompleteRun := evidenceRun("run_incomplete", "FAILED", evidenceTrace("run_incomplete", completed("INCOMPLETE",
		map[string]any{"code": "PATCH_VERIFICATION_MISSING", "description": "The latest patch has no passing verification."},
		map[string]any{"code": "CHANGE_REQUEST_MISSING", "description": "No change request binds the latest patch."},
	), nil), nil)
	variants = []map[string]any{
		evidenceVariant(0, "vuln-main", "PASS", mainRun),
		evidenceVariant(1, "vuln-stop-then-deployed", "PASS", stopRun),
		evidenceVariant(2, "vuln-incomplete-final", "PASS", incompleteRun),
	}
	production = append(deployedLines("act_rel_main", "v2"), deployedLines("act_rel_stop", "v3")...)
	for i := range production {
		production[i]["seq"] = i + 1
	}
	for _, runID := range []string{"run_main", "run_stop", "run_incomplete"} {
		runner = append(runner,
			map[string]any{"event": "arrived", "kind": "verification", "verificationId": "ver_" + runID},
			map[string]any{"event": "started", "kind": "verification", "verificationId": "ver_" + runID},
			map[string]any{"event": "completed", "kind": "verification", "verificationId": "ver_" + runID})
	}
	// The runner delivered the main Run's verification twice; the Backend answered the
	// second delivery as a duplicate.
	runner = append(runner,
		map[string]any{"event": "callback", "testId": "ver_run_main", "httpStatus": 200},
		map[string]any{"event": "callback", "testId": "ver_run_main", "httpStatus": 200})
	return variants, production, runner
}

// everyVariant adds, without Runs, each variant an aspect rests on that the data lacks, so
// every aspect can reach a conclusion other than NOT VERIFIED.
func everyVariant(variants []map[string]any) []map[string]any {
	have := map[string]bool{}
	for _, v := range variants {
		have[v["variant"].(string)] = true
	}
	for _, name := range []string{
		"vuln-verify", "vuln-scope-denied", "vuln-stale-evidence", "vuln-restart-during-verify", "vuln-prepare-only",
		"vuln-restart-during-approval", "vuln-duplicate-approval", "vuln-reject-resubmit", "vuln-release-crash-deployed",
		"vuln-release-crash-voided", "vuln-void-exhausted", "vuln-target-occupied", "vuln-release-refused", "vuln-unhealthy",
		"vuln-release-unreachable-stop",
	} {
		if !have[name] {
			variants = append(variants, evidenceVariant(len(variants), name, "PASS"))
		}
	}
	return variants
}

// renderEvidence writes the collected data to a directory, renders the report from it and
// returns the report and the render's exit status.
func renderEvidence(t *testing.T, variants, production, runner []map[string]any) (string, int) {
	t.Helper()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is required to run the demo script: %v", tool, err)
		}
	}
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	if err := os.MkdirAll(filepath.Join(data, "variants"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONFile := func(path string, body any) {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range variants {
		writeJSONFile(filepath.Join(data, "variants", v["variant"].(string)+".json"), v)
	}
	writeJSONFile(filepath.Join(data, "production-record.json"), production)
	writeJSONFile(filepath.Join(data, "runner-record.json"), runner)

	out := filepath.Join(dir, "report.md")
	cmd := exec.Command("bash", "../../../scripts/demo/vuln-evidence-report.sh", "render", data, out)
	cmd.Env = append(os.Environ(), "EMBERLING_MODEL_PROVIDER_API_KEY=unused", "EMBERLING_CALLBACK_SIGNING_SECRET=unused")
	combined, err := cmd.CombinedOutput()
	status := 0
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		status = exitErr.ExitCode()
	default:
		t.Fatalf("run vuln-evidence-report.sh: %v\n%s", err, combined)
	}
	report, readErr := os.ReadFile(out)
	if readErr != nil {
		t.Fatalf("read the report: %v (render exit %d)\n%s", readErr, status, combined)
	}
	return string(report), status
}

// section returns the report text from the heading to the next heading of the same level.
func section(report, heading string) string {
	start := strings.Index(report, "\n"+heading+"\n")
	if start < 0 {
		return ""
	}
	rest := report[start+len(heading)+2:]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		return rest[:end]
	}
	return rest
}

func TestDemoVulnEvidenceReport_ConsistentDemo_AllAspectsAndUncheckedVersions(t *testing.T) {
	variants, production, runner := evidenceData()
	report, status := renderEvidence(t, everyVariant(variants), production, runner)
	if status != 0 {
		t.Fatalf("render exit %d, want 0\n%s", status, report)
	}
	for _, heading := range []string{
		"## Authorization", "## Evidence consistency", "## Completion", "## Release reconciliation",
		"## Execution recovery", "## Versions confirmed live only after a stop, never checked", "## Not covered",
	} {
		body := section(report, heading)
		if body == "" {
			t.Errorf("report lacks the section %q\n%s", heading, report)
			continue
		}
		if heading != "## Versions confirmed live only after a stop, never checked" && heading != "## Not covered" &&
			!strings.Contains(body, "Conclusion: PASS") {
			t.Errorf("section %q is not PASS for a consistent demo\n%s", heading, body)
		}
	}

	unchecked := section(report, "## Versions confirmed live only after a stop, never checked")
	for _, want := range []string{"run_stop", "vuln-stop-then-deployed", "v3", "op_act_rel_stop_1", "token-service/staging"} {
		if !strings.Contains(unchecked, want) {
			t.Errorf("unchecked versions section lacks %q\n%s", want, unchecked)
		}
	}
	if strings.Contains(unchecked, "run_main") {
		t.Errorf("unchecked versions section lists the checked main release\n%s", unchecked)
	}

	completion := section(report, "## Completion")
	for _, want := range []string{
		"run_main", "DEPLOYED", "run_stop", "RELEASE_UNKNOWN", "run_incomplete", "INCOMPLETE",
		"PATCH_VERIFICATION_MISSING", "CHANGE_REQUEST_MISSING", "latest_patch_verified",
	} {
		if !strings.Contains(completion, want) {
			t.Errorf("completion section lacks %q\n%s", want, completion)
		}
	}
	recovery := section(report, "## Execution recovery")
	for _, want := range []string{"TestAPI_Stream_Reconnect_WithLastEventID_ResumesWithoutDuplicates", "duplicate"} {
		if !strings.Contains(recovery, want) {
			t.Errorf("execution recovery section lacks %q\n%s", want, recovery)
		}
	}
	assertNoSecrets(t, report)
}

func assertNoSecrets(t *testing.T, report string) {
	t.Helper()
	if strings.Contains(report, plantedSecret) {
		t.Errorf("report contains the planted secret value\n%s", report)
	}
	if long := regexp.MustCompile(`[0-9A-Fa-f]{32,}`).FindString(report); long != "" {
		t.Errorf("report contains the token-like string %q", long)
	}
	lower := strings.ToLower(report)
	for _, word := range []string{"bearer", "callbacktoken", "callback token", "signing secret", "api key", "operator-token", "x-emberling"} {
		if strings.Contains(lower, word) {
			t.Errorf("report contains %q", word)
		}
	}
}

// TestDemoVulnEvidenceReport_SecondDeploymentUnderOneApproval_Fails is a production record
// that deployed one approved Action twice: the release reconciliation fails and so does the
// render.
func TestDemoVulnEvidenceReport_SecondDeploymentUnderOneApproval_Fails(t *testing.T) {
	variants, production, runner := evidenceData()
	production = append(production, map[string]any{
		"seq": len(production) + 1, "event": "deployed", "kind": "release", "operationId": "op_act_rel_main_2",
		"patchDigest": evidencePatchNew, "version": "v4",
	})
	report, status := renderEvidence(t, everyVariant(variants), production, runner)
	if status != 1 {
		t.Fatalf("render exit %d, want 1\n%s", status, report)
	}
	if !strings.Contains(section(report, "## Release reconciliation"), "Conclusion: FAIL") {
		t.Errorf("release reconciliation is not FAIL\n%s", report)
	}
	assertNoSecrets(t, report)
}

// TestDemoVulnEvidenceReport_DeployedPatchNotVerified_Fails is a deployment of a patch that
// is not the Run's latest verified patch.
func TestDemoVulnEvidenceReport_DeployedPatchNotVerified_Fails(t *testing.T) {
	variants, production, runner := evidenceData()
	production[2]["patchDigest"] = evidencePatchOld
	report, status := renderEvidence(t, everyVariant(variants), production, runner)
	if status != 1 || !strings.Contains(section(report, "## Evidence consistency"), "Conclusion: FAIL") {
		t.Fatalf("render exit %d, want 1 with evidence consistency FAIL\n%s", status, report)
	}
}

// TestDemoVulnEvidenceReport_FailedVariant_FailsItsAspects proves a variant whose own
// checks failed makes the aspects it supports fail.
func TestDemoVulnEvidenceReport_FailedVariant_FailsItsAspects(t *testing.T) {
	variants, production, runner := evidenceData()
	variants[2]["verdicts"].(map[string]any)["story"] = "FAIL"
	report, status := renderEvidence(t, everyVariant(variants), production, runner)
	if status != 1 || !strings.Contains(section(report, "## Completion"), "Conclusion: FAIL") {
		t.Fatalf("render exit %d, want 1 with completion FAIL\n%s", status, report)
	}
}

// TestDemoVulnEvidenceReport_VariantNotRun_AspectNotVerified proves an aspect whose source
// variants did not run is reported as not verified rather than passed.
func TestDemoVulnEvidenceReport_VariantNotRun_AspectNotVerified(t *testing.T) {
	variants, production, runner := evidenceData()
	report, _ := renderEvidence(t, variants, production, runner)
	authorization := section(report, "## Authorization")
	if !strings.Contains(authorization, "Conclusion: NOT VERIFIED") || !strings.Contains(authorization, "vuln-scope-denied") {
		t.Fatalf("authorization is not NOT VERIFIED naming the missing variant\n%s", authorization)
	}
}
