package domain

import "time"

// Fact types the goal-based completion check reads. The Tools that produce them declare the
// same strings in their registrations; the check names them here because it must know which
// committed facts to look for without depending on any Tool package.
const (
	// FactTypePatchVerified is recorded when a verification of a workspace patch, named by
	// its digest, finished: tests, vulnerability scan and dependency check in one verdict.
	FactTypePatchVerified = "patch_verified"
	// FactTypePatchReviewed is recorded when a review model reviewed a workspace patch,
	// named by its digest.
	FactTypePatchReviewed = "patch_reviewed"
	// FactTypeReleaseConfirmed is recorded when a release of a workspace patch, named by
	// its digest, was confirmed deployed; the verdict is the target's health check.
	FactTypeReleaseConfirmed = "release_confirmed"
)

// CompletionOutcome is the task result the completion check decides for an Agent Run with a
// frozen goal. Three outcomes are a successful Agent NodeRun; INCOMPLETE and RELEASE_UNKNOWN
// fail it, so a completed Run never hides a task that is not done.
type CompletionOutcome string

const (
	// CompletionReadyForApproval: a PREPARE_ONLY task has its verified patch and change
	// request; a person approves and schedules the release from here.
	CompletionReadyForApproval CompletionOutcome = "READY_FOR_APPROVAL"
	// CompletionDeployed: a FIX_AND_DEPLOY task was approved, deployed and the target is
	// healthy on the patched version.
	CompletionDeployed CompletionOutcome = "DEPLOYED"
	// CompletionDeployedCheckFailed: the new version is live but its health check did not
	// pass; the rollback plan applies.
	CompletionDeployedCheckFailed CompletionOutcome = "DEPLOYED_CHECK_FAILED"
	// CompletionReleaseUnknown: a person stopped the wait before the release outcome was
	// known; the target stays occupied until reconciliation settles it.
	CompletionReleaseUnknown CompletionOutcome = "RELEASE_UNKNOWN"
	// CompletionIncomplete: at least one required fact is missing; Missing lists them.
	CompletionIncomplete CompletionOutcome = "INCOMPLETE"
)

func (o CompletionOutcome) IsValid() bool {
	switch o {
	case CompletionReadyForApproval, CompletionDeployed, CompletionDeployedCheckFailed,
		CompletionReleaseUnknown, CompletionIncomplete:
		return true
	default:
		return false
	}
}

// IsSuccess reports whether the outcome succeeds the Agent NodeRun.
func (o CompletionOutcome) IsSuccess() bool {
	return o == CompletionReadyForApproval || o == CompletionDeployed || o == CompletionDeployedCheckFailed
}

// CompletionCheck is one item the completion check went through. Name is a stable
// identifier of the check; Refs name the facts, records or patches the check rested on, so
// a reader can open the evidence behind a passed check or see what a failed one found.
type CompletionCheck struct {
	Name   string   `json:"name"`
	Passed bool     `json:"passed"`
	Refs   []string `json:"refs"`
}

// CompletionMissing is one fact an INCOMPLETE task still lacks: a stable identifier and a
// sentence a person can act on.
type CompletionMissing struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

// CompletionEvidence names one record a completion verdict rests on. Kind is one of the
// CompletionEvidence* constants; ID is the record's identifier in its own table.
type CompletionEvidence struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Evidence kinds a completion verdict can cite.
const (
	CompletionEvidenceWorkspacePatch = "WORKSPACE_PATCH"
	CompletionEvidenceFact           = "FACT"
	CompletionEvidenceChangeRequest  = "CHANGE_REQUEST"
	CompletionEvidenceArtifact       = "ARTIFACT"
	CompletionEvidenceApproval       = "APPROVAL"
)

// AgentCompletion is the persisted verdict of the completion check for one Agent Run with
// a frozen goal. It is written once, in the transaction that terminated the Agent Run, and
// never updated. An Agent Run without a goal has none.
type AgentCompletion struct {
	AgentRunID  string              `json:"agentRunId"`
	Goal        AgentGoal           `json:"goal"`
	Outcome     CompletionOutcome   `json:"outcome"`
	Checks      []CompletionCheck   `json:"checks"`
	Missing     []CompletionMissing `json:"missing"`
	EvaluatedAt time.Time           `json:"evaluatedAt"`
}
