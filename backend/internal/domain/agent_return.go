package domain

// AgentReturnCode is one of the definite refusals the Runtime hands back to the Agent
// instead of ending the Agent Run as a Tool error. The set is closed: a refusal the Agent
// can act on in its next Turn is named here, and every other failure keeps the existing
// terminal behaviour. Each code maps to the failure source its AGENT_ACTION_FAILED Event
// records, so Trace can tell an authorization refusal from a human decision or a
// Provider's definite answer without reading the message.
type AgentReturnCode string

const (
	// Authorization refusals: the Runtime itself refused the call on the Run's facts.
	ReturnScopeDenied         AgentReturnCode = "SCOPE_DENIED"
	ReturnStalePatch          AgentReturnCode = "STALE_PATCH"
	ReturnVerificationMissing AgentReturnCode = "VERIFICATION_MISSING"
	ReturnGoalDenied          AgentReturnCode = "GOAL_DENIED"
	ReturnTargetOccupied      AgentReturnCode = "TARGET_OCCUPIED"
	// Human decisions: an authenticated person rejected the request or let it expire.
	ReturnApprovalRejected AgentReturnCode = "APPROVAL_REJECTED"
	ReturnApprovalExpired  AgentReturnCode = "APPROVAL_EXPIRED"
	// Provider refusals: the external system gave a definite negative answer.
	ReturnReleaseRefused     AgentReturnCode = "RELEASE_REFUSED"
	ReturnReleaseNotDeployed AgentReturnCode = "RELEASE_NOT_DEPLOYED"
)

// agentReturnSources is the closed mapping from return code to failure source. A code
// absent from it is not returnable to the Agent.
var agentReturnSources = map[AgentReturnCode]FailureSource{
	ReturnScopeDenied:         FailureAuthorization,
	ReturnStalePatch:          FailureAuthorization,
	ReturnVerificationMissing: FailureAuthorization,
	ReturnGoalDenied:          FailureAuthorization,
	ReturnTargetOccupied:      FailureAuthorization,
	ReturnApprovalRejected:    FailureHuman,
	ReturnApprovalExpired:     FailureHuman,
	ReturnReleaseRefused:      FailureProviderRefusal,
	ReturnReleaseNotDeployed:  FailureProviderRefusal,
}

// IsValid reports whether the code belongs to the closed returnable set.
func (c AgentReturnCode) IsValid() bool {
	_, ok := agentReturnSources[c]
	return ok
}

// FailureSource is the failure source an Action returned with this code fails with. The
// second result is false for a code outside the returnable set.
func (c AgentReturnCode) FailureSource() (FailureSource, bool) {
	source, ok := agentReturnSources[c]
	return source, ok
}
