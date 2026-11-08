package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// Execution error codes the completion check fails an Agent NodeRun with. They are bounded
// ExecutionError.Code strings, not database enums.
const (
	// CodeTaskIncomplete fails the Agent NodeRun when the Agent declared completion but the
	// facts the goal requires are not all recorded.
	CodeTaskIncomplete = "TASK_INCOMPLETE"
	// CodeReleaseUnknown fails the Agent NodeRun when a person stopped the wait before the
	// release outcome was known.
	CodeReleaseUnknown = "RELEASE_UNKNOWN"
)

// Check names, stable identifiers of the items the completion check goes through.
const (
	CheckWorkspacePatch      = "workspace_patch_recorded"
	CheckPatchVerified       = "latest_patch_verified"
	CheckChangeRequest       = "change_request_binds_latest_patch"
	CheckPatchReviewed       = "latest_patch_reviewed"
	CheckApproval            = "release_approved"
	CheckReleaseConfirmed    = "release_confirmed_for_latest_patch"
	CheckReleaseVersionBound = "deployed_version_bound_to_latest_patch"
	CheckReleaseHealthCheck  = "deployed_target_healthy"
	CheckFinalResponse       = "agent_declared_completion"
)

// Missing codes, stable identifiers of the facts an INCOMPLETE verdict still lacks.
const (
	MissingWorkspacePatch          = "WORKSPACE_PATCH_MISSING"
	MissingPatchVerification       = "PATCH_VERIFICATION_MISSING"
	MissingPatchVerificationPassed = "PATCH_VERIFICATION_FAILED"
	MissingChangeRequest           = "CHANGE_REQUEST_MISSING"
	MissingPatchReview             = "PATCH_REVIEW_MISSING"
	MissingApproval                = "APPROVAL_MISSING"
	MissingReleaseConfirmation     = "RELEASE_CONFIRMATION_MISSING"
	MissingReleaseHealthy          = "RELEASE_HEALTH_CHECK_FAILED"
	MissingFinalResponse           = "FINAL_RESPONSE_MISSING"
)

// CompletionPatch is the newest recorded workspace patch of the Agent Run: the current
// workspace, and the subject every other fact must be about.
type CompletionPatch struct {
	ID     string
	Seq    int
	Digest string
}

// CompletionFact is one committed execution fact as the check reads it: which subject it is
// about, its verdict and its bindings. The check never reinterprets Tool results; it only
// compares these recorded values.
type CompletionFact struct {
	ID      string
	Subject string
	Verdict *bool
	Binding map[string]string
}

// CompletionChangeRequest is one recorded change request: the patch digest it describes,
// the verification fact it rests on and the Artifact holding its content.
type CompletionChangeRequest struct {
	ID                 string
	PatchDigest        string
	VerificationFactID string
	ContentArtifactID  string
}

// CompletionApproval is one approval the Agent Run requested, with its settled status.
type CompletionApproval struct {
	ID     string
	Status domain.ApprovalStatus
}

// CompletionInput is everything the completion check reads about one Agent Run, gathered
// by the caller in the terminating transaction.
type CompletionInput struct {
	Goal domain.AgentGoal
	// LatestPatch is nil when the workspace was never written.
	LatestPatch *CompletionPatch
	// Verifications are every patch_verified fact of the Agent Run, in recorded order.
	Verifications []CompletionFact
	// ChangeRequests are every change request of the Agent Run, in recorded order.
	ChangeRequests []CompletionChangeRequest
	// ReviewRequired is whether the Agent could call a review Tool; when it could, a review
	// of the latest patch is part of the change material. Reviews are the patch_reviewed
	// facts; their verdict is never a completion condition.
	ReviewRequired bool
	Reviews        []CompletionFact
	// Approvals are every approval the Agent Run requested.
	Approvals []CompletionApproval
	// Releases are every release_confirmed fact of the Agent Run; the verdict is the
	// target's health check and the binding carries the deployed version.
	Releases []CompletionFact
	// Termination is how the Agent Run ended. FINAL_RESPONSE means the Agent declared
	// completion; HUMAN_STOPPED means a person stopped a release wait; anything else ended
	// the Agent before it declared completion.
	Termination domain.AgentTermination
	// FinalOutput is the Agent's validated FINAL output when it declared completion, nil
	// otherwise. A DELIVER_CLIP check reads its declaration from it; the declaration only
	// selects which facts are checked.
	FinalOutput json.RawMessage
	// The facts a DELIVER_CLIP check reads, each in recorded order: ClipRenders are the
	// clip_rendered facts, MediaChecks the media_checked facts and ContentReviews the
	// content_reviewed facts of the Agent Run. ContentReviewRequired is whether the Agent
	// could call a Tool that produces content_reviewed, in which case a passed review of
	// the delivered clip's timeline is part of delivery.
	ClipRenders           []CompletionFact
	MediaChecks           []CompletionFact
	ContentReviews        []CompletionFact
	ContentReviewRequired bool
}

// CompletionResult is the verdict: the outcome, the checks it went through, what an
// INCOMPLETE task still lacks, and the records the verdict rests on.
type CompletionResult struct {
	Outcome  domain.CompletionOutcome
	Checks   []domain.CompletionCheck
	Missing  []domain.CompletionMissing
	Evidence []domain.CompletionEvidence
}

// EvaluateCompletion decides the task result of an Agent Run against its frozen goal. It is
// a pure function over committed facts: the Agent's own report and any review verdict are
// never conditions.
//
// The check has three stages. Change preparation: the workspace holds a patch and a
// passing verification is about exactly that patch (the verification carries the tests,
// the vulnerability scan and the dependency check in one verdict). Change material: a
// change request names that patch and that passing verification, and, when the Agent could
// have the patch reviewed, a review of that patch exists. Release confirmation, for
// FIX_AND_DEPLOY only: a release was approved, a release confirmation is about that patch,
// and its health check passed.
//
// A PREPARE_ONLY task with the first two stages complete is READY_FOR_APPROVAL; a
// FIX_AND_DEPLOY task with all three is DEPLOYED, or DEPLOYED_CHECK_FAILED when only the
// health check failed. A human stop is RELEASE_UNKNOWN regardless of the facts. Every other
// combination is INCOMPLETE and Missing names each lacking fact, including the fact that
// the Agent never declared completion when it ended for another reason.
//
// A DELIVER_CLIP task has its own check, evaluateClipDelivery, which reads the Agent's
// declaration and the media facts instead of the repair material.
func EvaluateCompletion(in CompletionInput) CompletionResult {
	if in.Goal == domain.AgentGoalDeliverClip {
		return evaluateClipDelivery(in)
	}
	e := &completionEvaluator{allPassed: true}

	if in.Termination != domain.TerminationFinalResponse && in.Termination != domain.TerminationHumanStopped {
		e.fail(CheckFinalResponse, MissingFinalResponse,
			fmt.Sprintf("The agent did not declare completion; it ended with %s.", in.Termination), nil)
	} else {
		e.pass(CheckFinalResponse, nil)
	}

	var latestDigest string
	if in.LatestPatch == nil {
		e.fail(CheckWorkspacePatch, MissingWorkspacePatch, "The workspace holds no recorded patch.", nil)
	} else {
		latestDigest = in.LatestPatch.Digest
		e.pass(CheckWorkspacePatch, []string{in.LatestPatch.ID})
		e.cite(domain.CompletionEvidenceWorkspacePatch, in.LatestPatch.ID)
	}

	passing := map[string]bool{}
	var passingRefs []string
	var aboutLatest int
	for _, fact := range in.Verifications {
		if latestDigest == "" || fact.Subject != latestDigest {
			continue
		}
		aboutLatest++
		if fact.Verdict != nil && *fact.Verdict {
			passing[fact.ID] = true
			passingRefs = append(passingRefs, fact.ID)
		}
	}
	switch {
	case len(passingRefs) > 0:
		e.pass(CheckPatchVerified, passingRefs)
		for _, id := range passingRefs {
			e.cite(domain.CompletionEvidenceFact, id)
		}
	case aboutLatest > 0:
		e.fail(CheckPatchVerified, MissingPatchVerificationPassed,
			"The latest patch was verified but the verification did not pass.", nil)
	default:
		e.fail(CheckPatchVerified, MissingPatchVerification,
			"No verification is recorded for the latest patch.", nil)
	}

	var requestRefs []string
	for _, request := range in.ChangeRequests {
		if latestDigest == "" || request.PatchDigest != latestDigest || !passing[request.VerificationFactID] {
			continue
		}
		requestRefs = append(requestRefs, request.ID)
		e.cite(domain.CompletionEvidenceChangeRequest, request.ID)
		if request.ContentArtifactID != "" {
			e.cite(domain.CompletionEvidenceArtifact, request.ContentArtifactID)
		}
	}
	if len(requestRefs) > 0 {
		e.pass(CheckChangeRequest, requestRefs)
	} else {
		e.fail(CheckChangeRequest, MissingChangeRequest,
			"No change request names the latest patch together with its passing verification.", nil)
	}

	if in.ReviewRequired {
		var reviewRefs []string
		for _, fact := range in.Reviews {
			if latestDigest != "" && fact.Subject == latestDigest {
				reviewRefs = append(reviewRefs, fact.ID)
				e.cite(domain.CompletionEvidenceFact, fact.ID)
			}
		}
		if len(reviewRefs) > 0 {
			e.pass(CheckPatchReviewed, reviewRefs)
		} else {
			e.fail(CheckPatchReviewed, MissingPatchReview, "No review is recorded for the latest patch.", nil)
		}
	}

	prepared := e.allPassed

	deployed, healthy := false, false
	if in.Goal == domain.AgentGoalFixAndDeploy {
		var approvedRefs []string
		for _, approval := range in.Approvals {
			if approval.Status == domain.ApprovalApproved {
				approvedRefs = append(approvedRefs, approval.ID)
				e.cite(domain.CompletionEvidenceApproval, approval.ID)
			}
		}
		if len(approvedRefs) > 0 {
			e.pass(CheckApproval, approvedRefs)
		} else {
			e.fail(CheckApproval, MissingApproval, "No release request was approved.", nil)
		}

		var release *CompletionFact
		for i := range in.Releases {
			if latestDigest != "" && in.Releases[i].Subject == latestDigest {
				release = &in.Releases[i]
			}
		}
		if release == nil {
			e.fail(CheckReleaseConfirmed, MissingReleaseConfirmation,
				"No release confirmation is recorded for the latest patch.", nil)
		} else {
			deployed = true
			e.pass(CheckReleaseConfirmed, []string{release.ID})
			e.cite(domain.CompletionEvidenceFact, release.ID)
			// The confirmation is about the latest patch by construction; the version it
			// binds is recorded so a reader sees which deployed version that is.
			e.pass(CheckReleaseVersionBound, []string{release.ID, "version:" + release.Binding["version"]})
			if release.Verdict != nil && *release.Verdict {
				healthy = true
				e.pass(CheckReleaseHealthCheck, []string{release.ID})
			} else {
				e.fail(CheckReleaseHealthCheck, MissingReleaseHealthy,
					"The deployed target did not pass its health check.", []string{release.ID})
			}
		}
	}

	result := CompletionResult{Checks: e.checks, Missing: e.missing, Evidence: e.evidence}
	switch {
	case in.Termination == domain.TerminationHumanStopped:
		result.Outcome = domain.CompletionReleaseUnknown
		result.Missing = []domain.CompletionMissing{}
	case in.Termination != domain.TerminationFinalResponse:
		result.Outcome = domain.CompletionIncomplete
	case in.Goal == domain.AgentGoalPrepareOnly && prepared:
		result.Outcome = domain.CompletionReadyForApproval
	case in.Goal == domain.AgentGoalFixAndDeploy && e.allPassed:
		result.Outcome = domain.CompletionDeployed
	case in.Goal == domain.AgentGoalFixAndDeploy && deployed && !healthy && e.onlyFailed(CheckReleaseHealthCheck):
		result.Outcome = domain.CompletionDeployedCheckFailed
		result.Missing = []domain.CompletionMissing{}
	default:
		result.Outcome = domain.CompletionIncomplete
	}
	if result.Missing == nil {
		result.Missing = []domain.CompletionMissing{}
	}
	if result.Evidence == nil {
		result.Evidence = []domain.CompletionEvidence{}
	}
	return result
}

// completionEvaluator accumulates checks, missing items and evidence in evaluation order.
type completionEvaluator struct {
	checks    []domain.CompletionCheck
	missing   []domain.CompletionMissing
	evidence  []domain.CompletionEvidence
	cited     map[domain.CompletionEvidence]bool
	allPassed bool
	failed    map[string]bool
}

func (e *completionEvaluator) pass(name string, refs []string) {
	if refs == nil {
		refs = []string{}
	}
	e.checks = append(e.checks, domain.CompletionCheck{Name: name, Passed: true, Refs: refs})
}

func (e *completionEvaluator) fail(name, code, description string, refs []string) {
	if refs == nil {
		refs = []string{}
	}
	e.checks = append(e.checks, domain.CompletionCheck{Name: name, Passed: false, Refs: refs})
	e.missing = append(e.missing, domain.CompletionMissing{Code: code, Description: description})
	e.allPassed = false
	if e.failed == nil {
		e.failed = map[string]bool{}
	}
	e.failed[name] = true
}

// onlyFailed reports whether name is the single failed check so far.
func (e *completionEvaluator) onlyFailed(name string) bool {
	return len(e.failed) == 1 && e.failed[name]
}

func (e *completionEvaluator) cite(kind, id string) {
	item := domain.CompletionEvidence{Kind: kind, ID: id}
	if e.cited == nil {
		e.cited = map[domain.CompletionEvidence]bool{}
	}
	if e.cited[item] {
		return
	}
	e.cited[item] = true
	e.evidence = append(e.evidence, item)
}

// CompletionNodeOutput renders the value the Agent NodeRun publishes on its `text` port
// when the completion check succeeds: the outcome, checks, missing items and evidence of the
// verdict, with the Agent's validated FINAL output carried unchanged as agentReport. The
// object is encoded canonically, so immediate advancement and the Reconciler publish
// byte-identical port values.
func CompletionNodeOutput(result CompletionResult, agentReport json.RawMessage) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(agentReport))
	decoder.UseNumber()
	var report any
	if err := decoder.Decode(&report); err != nil {
		return "", fmt.Errorf("completion node output: agent report is not valid JSON: %w", err)
	}
	encoded, err := json.Marshal(map[string]any{
		"outcome":  result.Outcome,
		"checks":   result.Checks,
		"missing":  result.Missing,
		"evidence": result.Evidence,
	})
	if err != nil {
		return "", fmt.Errorf("completion node output: encode verdict: %w", err)
	}
	decoder = json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var verdict map[string]any
	if err := decoder.Decode(&verdict); err != nil {
		return "", fmt.Errorf("completion node output: decode verdict: %w", err)
	}
	verdict["agentReport"] = report
	canonical, err := encodeCanonical(verdict)
	if err != nil {
		return "", fmt.Errorf("completion node output: encode canonical JSON: %w", err)
	}
	return string(canonical), nil
}
