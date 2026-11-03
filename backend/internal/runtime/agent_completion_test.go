package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// Fixture identifiers. The digests are the shape a real patch digest has, so a test that
// compares them against a check's refs reads like the persisted rows do.
const (
	completionLatestDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	completionOldDigest    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func completionLatestPatch() *CompletionPatch {
	return &CompletionPatch{ID: "wpatch_2", Seq: 2, Digest: completionLatestDigest}
}

func completionVerification(id, subject string, passed bool) CompletionFact {
	return CompletionFact{ID: id, Subject: subject, Verdict: &passed, Binding: map[string]string{"baseCommit": "c0ffee", "resultDigest": "sha256:" + id}}
}

func completionReview(id, subject string) CompletionFact {
	blocking := false
	return CompletionFact{ID: id, Subject: subject, Verdict: &blocking}
}

func completionRelease(id, subject string, healthy bool) CompletionFact {
	return CompletionFact{ID: id, Subject: subject, Verdict: &healthy, Binding: map[string]string{"operationKey": "op_1", "version": "v7"}}
}

func completionRequest(id, digest, factID string) CompletionChangeRequest {
	return CompletionChangeRequest{ID: id, PatchDigest: digest, VerificationFactID: factID, ContentArtifactID: "artifact_" + id}
}

// completionPrepared is the input of a PREPARE_ONLY task whose patch is verified and whose
// change request names that patch and that verification: every preparation and material
// check passes.
func completionPrepared(goal domain.AgentGoal) CompletionInput {
	return CompletionInput{
		Goal:           goal,
		LatestPatch:    completionLatestPatch(),
		Verifications:  []CompletionFact{completionVerification("fact_old", completionOldDigest, false), completionVerification("fact_ok", completionLatestDigest, true)},
		ChangeRequests: []CompletionChangeRequest{completionRequest("cr_1", completionLatestDigest, "fact_ok")},
		Termination:    domain.TerminationFinalResponse,
	}
}

// completionDeployed is completionPrepared for FIX_AND_DEPLOY, approved and confirmed
// released with the given health verdict.
func completionDeployed(healthy bool) CompletionInput {
	in := completionPrepared(domain.AgentGoalFixAndDeploy)
	in.Approvals = []CompletionApproval{{ID: "approval_1", Status: domain.ApprovalApproved}}
	in.Releases = []CompletionFact{completionRelease("fact_release", completionLatestDigest, healthy)}
	return in
}

func missingCodes(result CompletionResult) []string {
	codes := make([]string, len(result.Missing))
	for i, item := range result.Missing {
		codes[i] = item.Code
	}
	return codes
}

func containsAll(got, want []string) bool {
	seen := make(map[string]bool, len(got))
	for _, g := range got {
		seen[g] = true
	}
	for _, w := range want {
		if !seen[w] {
			return false
		}
	}
	return true
}

// TestEvaluateCompletion_GoalAndFactCombinations_YieldFiveOutcomes is the table over goal
// and fact combinations: every one of the five outcomes, and for INCOMPLETE the item the
// verdict names as missing. The function is pure, so each row is one call.
func TestEvaluateCompletion_GoalAndFactCombinations_YieldFiveOutcomes(t *testing.T) {
	cases := []struct {
		name        string
		input       func() CompletionInput
		wantOutcome domain.CompletionOutcome
		wantMissing []string
		wantAbsent  []string
	}{
		{
			name:        "PREPARE_ONLY with verified patch and change request is ready for approval",
			input:       func() CompletionInput { return completionPrepared(domain.AgentGoalPrepareOnly) },
			wantOutcome: domain.CompletionReadyForApproval,
		},
		{
			name: "no patch at all is incomplete and names the workspace patch",
			input: func() CompletionInput {
				return CompletionInput{Goal: domain.AgentGoalPrepareOnly, Termination: domain.TerminationFinalResponse}
			},
			wantOutcome: domain.CompletionIncomplete,
			wantMissing: []string{MissingWorkspacePatch, MissingPatchVerification, MissingChangeRequest},
		},
		{
			name: "verification of an earlier patch only is incomplete",
			input: func() CompletionInput {
				in := completionPrepared(domain.AgentGoalPrepareOnly)
				in.Verifications = []CompletionFact{completionVerification("fact_old", completionOldDigest, true)}
				return in
			},
			wantOutcome: domain.CompletionIncomplete,
			wantMissing: []string{MissingPatchVerification},
		},
		{
			name: "latest verification failed is incomplete and says so",
			input: func() CompletionInput {
				in := completionPrepared(domain.AgentGoalPrepareOnly)
				in.Verifications = []CompletionFact{completionVerification("fact_failed", completionLatestDigest, false)}
				return in
			},
			wantOutcome: domain.CompletionIncomplete,
			wantMissing: []string{MissingPatchVerificationPassed},
			wantAbsent:  []string{MissingPatchVerification},
		},
		{
			name: "change request about an earlier patch is incomplete",
			input: func() CompletionInput {
				in := completionPrepared(domain.AgentGoalPrepareOnly)
				in.ChangeRequests = []CompletionChangeRequest{completionRequest("cr_old", completionOldDigest, "fact_old")}
				return in
			},
			wantOutcome: domain.CompletionIncomplete,
			wantMissing: []string{MissingChangeRequest},
		},
		{
			name: "change request citing a superseded verification fact is incomplete",
			input: func() CompletionInput {
				in := completionPrepared(domain.AgentGoalPrepareOnly)
				in.ChangeRequests = []CompletionChangeRequest{completionRequest("cr_stale", completionLatestDigest, "fact_old")}
				return in
			},
			wantOutcome: domain.CompletionIncomplete,
			wantMissing: []string{MissingChangeRequest},
		},
		{
			name: "review enabled without a review of the latest patch is incomplete",
			input: func() CompletionInput {
				in := completionPrepared(domain.AgentGoalPrepareOnly)
				in.ReviewRequired = true
				in.Reviews = []CompletionFact{completionReview("fact_review_old", completionOldDigest)}
				return in
			},
			wantOutcome: domain.CompletionIncomplete,
			wantMissing: []string{MissingPatchReview},
		},
		{
			name: "review enabled with a review of the latest patch is ready for approval",
			input: func() CompletionInput {
				in := completionPrepared(domain.AgentGoalPrepareOnly)
				in.ReviewRequired = true
				in.Reviews = []CompletionFact{completionReview("fact_review", completionLatestDigest)}
				return in
			},
			wantOutcome: domain.CompletionReadyForApproval,
		},
		{
			name: "review disabled ignores the absence of a review",
			input: func() CompletionInput {
				in := completionPrepared(domain.AgentGoalPrepareOnly)
				in.ReviewRequired = false
				return in
			},
			wantOutcome: domain.CompletionReadyForApproval,
			wantAbsent:  []string{MissingPatchReview},
		},
		{
			name:        "FIX_AND_DEPLOY declared complete before any approval is incomplete",
			input:       func() CompletionInput { return completionPrepared(domain.AgentGoalFixAndDeploy) },
			wantOutcome: domain.CompletionIncomplete,
			wantMissing: []string{MissingApproval, MissingReleaseConfirmation},
		},
		{
			name: "FIX_AND_DEPLOY approved but not yet confirmed released is incomplete",
			input: func() CompletionInput {
				in := completionPrepared(domain.AgentGoalFixAndDeploy)
				in.Approvals = []CompletionApproval{{ID: "approval_1", Status: domain.ApprovalApproved}}
				return in
			},
			wantOutcome: domain.CompletionIncomplete,
			wantMissing: []string{MissingReleaseConfirmation},
			wantAbsent:  []string{MissingApproval},
		},
		{
			name: "FIX_AND_DEPLOY with only a rejected approval is incomplete",
			input: func() CompletionInput {
				in := completionPrepared(domain.AgentGoalFixAndDeploy)
				in.Approvals = []CompletionApproval{{ID: "approval_1", Status: domain.ApprovalRejected}}
				return in
			},
			wantOutcome: domain.CompletionIncomplete,
			wantMissing: []string{MissingApproval},
		},
		{
			name: "release confirmed for an earlier patch is incomplete",
			input: func() CompletionInput {
				in := completionDeployed(true)
				in.Releases = []CompletionFact{completionRelease("fact_release_old", completionOldDigest, true)}
				return in
			},
			wantOutcome: domain.CompletionIncomplete,
			wantMissing: []string{MissingReleaseConfirmation},
		},
		{
			name:        "FIX_AND_DEPLOY approved, deployed and healthy is deployed",
			input:       func() CompletionInput { return completionDeployed(true) },
			wantOutcome: domain.CompletionDeployed,
		},
		{
			name:        "deployed but the health check failed is deployed with a failed check",
			input:       func() CompletionInput { return completionDeployed(false) },
			wantOutcome: domain.CompletionDeployedCheckFailed,
		},
		{
			name: "deployed unhealthy with a missing change request is still incomplete",
			input: func() CompletionInput {
				in := completionDeployed(false)
				in.ChangeRequests = nil
				return in
			},
			wantOutcome: domain.CompletionIncomplete,
			wantMissing: []string{MissingChangeRequest, MissingReleaseHealthy},
		},
		{
			name: "a human stop is release unknown whatever the facts say",
			input: func() CompletionInput {
				in := completionPrepared(domain.AgentGoalFixAndDeploy)
				in.Approvals = []CompletionApproval{{ID: "approval_1", Status: domain.ApprovalApproved}}
				in.Termination = domain.TerminationHumanStopped
				return in
			},
			wantOutcome: domain.CompletionReleaseUnknown,
		},
		{
			name: "a timeout with every fact in place is incomplete because the Agent never declared completion",
			input: func() CompletionInput {
				in := completionPrepared(domain.AgentGoalPrepareOnly)
				in.Termination = domain.TerminationTimeout
				return in
			},
			wantOutcome: domain.CompletionIncomplete,
			wantMissing: []string{MissingFinalResponse},
		},
		{
			name: "a tool error termination is incomplete and lists the facts it lacks",
			input: func() CompletionInput {
				return CompletionInput{Goal: domain.AgentGoalPrepareOnly, LatestPatch: completionLatestPatch(), Termination: domain.TerminationToolError}
			},
			wantOutcome: domain.CompletionIncomplete,
			wantMissing: []string{MissingFinalResponse, MissingPatchVerification, MissingChangeRequest},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := EvaluateCompletion(tc.input())
			if result.Outcome != tc.wantOutcome {
				t.Fatalf("outcome = %s, want %s; checks=%+v missing=%+v", result.Outcome, tc.wantOutcome, result.Checks, result.Missing)
			}
			codes := missingCodes(result)
			if result.Outcome == domain.CompletionIncomplete && len(codes) == 0 {
				t.Fatalf("INCOMPLETE verdict names nothing as missing; checks=%+v", result.Checks)
			}
			if result.Outcome != domain.CompletionIncomplete && len(codes) != 0 {
				t.Errorf("%s verdict names missing items %v, want none", result.Outcome, codes)
			}
			if !containsAll(codes, tc.wantMissing) {
				t.Errorf("missing = %v, want it to include %v", codes, tc.wantMissing)
			}
			for _, absent := range tc.wantAbsent {
				if containsAll(codes, []string{absent}) {
					t.Errorf("missing = %v names %s, want it absent", codes, absent)
				}
			}
			for _, item := range result.Missing {
				if item.Description == "" {
					t.Errorf("missing item %s has no description", item.Code)
				}
			}
			if len(result.Checks) == 0 {
				t.Errorf("verdict recorded no checks")
			}
		})
	}
}

// TestEvaluateCompletion_PreparedTask_ChecksCiteTheRecordsTheyRestOn proves a passed check
// names the fact or record it found, and the evidence list names every record the verdict
// rests on exactly once: this is what Trace and the node output hand to a reader.
func TestEvaluateCompletion_PreparedTask_ChecksCiteTheRecordsTheyRestOn(t *testing.T) {
	result := EvaluateCompletion(completionDeployed(true))

	byName := make(map[string]domain.CompletionCheck, len(result.Checks))
	for _, check := range result.Checks {
		if _, dup := byName[check.Name]; dup {
			t.Errorf("check %s is recorded twice", check.Name)
		}
		byName[check.Name] = check
		if !check.Passed {
			t.Errorf("check %+v failed in a fully deployed task", check)
		}
	}
	wantRefs := map[string]string{
		CheckWorkspacePatch:      "wpatch_2",
		CheckPatchVerified:       "fact_ok",
		CheckChangeRequest:       "cr_1",
		CheckApproval:            "approval_1",
		CheckReleaseConfirmed:    "fact_release",
		CheckReleaseHealthCheck:  "fact_release",
		CheckReleaseVersionBound: "fact_release",
	}
	for name, ref := range wantRefs {
		check, ok := byName[name]
		if !ok {
			t.Errorf("check %s is missing from %+v", name, result.Checks)
			continue
		}
		if !containsAll(check.Refs, []string{ref}) {
			t.Errorf("check %s refs = %v, want %s among them", name, check.Refs, ref)
		}
	}
	if _, ok := byName[CheckPatchReviewed]; ok {
		t.Errorf("a task without review enabled recorded a review check")
	}

	seen := make(map[domain.CompletionEvidence]bool)
	for _, item := range result.Evidence {
		if seen[item] {
			t.Errorf("evidence %+v listed twice", item)
		}
		seen[item] = true
	}
	for _, want := range []domain.CompletionEvidence{
		{Kind: domain.CompletionEvidenceWorkspacePatch, ID: "wpatch_2"},
		{Kind: domain.CompletionEvidenceFact, ID: "fact_ok"},
		{Kind: domain.CompletionEvidenceChangeRequest, ID: "cr_1"},
		{Kind: domain.CompletionEvidenceArtifact, ID: "artifact_cr_1"},
		{Kind: domain.CompletionEvidenceApproval, ID: "approval_1"},
		{Kind: domain.CompletionEvidenceFact, ID: "fact_release"},
	} {
		if !seen[want] {
			t.Errorf("evidence lacks %+v: %+v", want, result.Evidence)
		}
	}
	if seen[domain.CompletionEvidence{Kind: domain.CompletionEvidenceFact, ID: "fact_old"}] {
		t.Errorf("evidence cites the superseded verification fact_old")
	}
}

// TestCompletionNodeOutput_Encodes_OutcomeChecksMissingEvidenceAndReport covers the node
// output the Agent NodeRun publishes on a successful outcome: the five documented members,
// canonically encoded so immediate advancement and the Reconciler publish identical bytes,
// with the Agent's own FINAL output carried unchanged as agentReport.
func TestCompletionNodeOutput_Encodes_OutcomeChecksMissingEvidenceAndReport(t *testing.T) {
	result := EvaluateCompletion(completionPrepared(domain.AgentGoalPrepareOnly))
	report := json.RawMessage(`{"status":"PATCH_VERIFIED","z":1,"a":[1,2]}`)

	text, err := CompletionNodeOutput(result, report)
	if err != nil {
		t.Fatalf("CompletionNodeOutput: %v", err)
	}
	var decoded struct {
		Outcome     string                      `json:"outcome"`
		Checks      []domain.CompletionCheck    `json:"checks"`
		Missing     []domain.CompletionMissing  `json:"missing"`
		Evidence    []domain.CompletionEvidence `json:"evidence"`
		AgentReport json.RawMessage             `json:"agentReport"`
	}
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("node output %q is not JSON: %v", text, err)
	}
	if decoded.Outcome != string(domain.CompletionReadyForApproval) || len(decoded.Checks) != len(result.Checks) ||
		len(decoded.Evidence) != len(result.Evidence) || decoded.Missing == nil {
		t.Errorf("node output = %s, want outcome, checks, an empty missing array and evidence", text)
	}
	if !strings.Contains(string(decoded.AgentReport), `"status":"PATCH_VERIFIED"`) {
		t.Errorf("agentReport = %s, want the Agent's FINAL output", decoded.AgentReport)
	}
	// Canonical form: keys sorted at every level, including inside the report.
	if !strings.Contains(text, `"agentReport":{"a":[1,2],"status":"PATCH_VERIFIED","z":1}`) {
		t.Errorf("node output is not canonically encoded: %s", text)
	}
	if strings.Index(text, `"checks"`) > strings.Index(text, `"evidence"`) {
		t.Errorf("node output keys are not sorted: %s", text)
	}
}
