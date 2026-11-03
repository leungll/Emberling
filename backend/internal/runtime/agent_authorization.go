package runtime

import (
	"fmt"

	"github.com/leungll/Emberling/backend/internal/domain"
)

// DecideAuthorization is the per-Action authorization decision: a pure function of the
// Tool's declared authorization and the goal the Agent Run was frozen to. A Tool that
// declares no authorization, or declares LOW risk, is allowed without a person. A
// production-risk Tool needs the FIX_AND_DEPLOY goal to be asked about at all: under
// PREPARE_ONLY, or when the Agent Run froze no goal, the call is refused with a reason the
// Agent can read. The decision never reads the Tool Name or the call's arguments.
func DecideAuthorization(authorization *domain.ToolAuthorization, goal *domain.AgentGoal) (domain.AuthorizationDecision, string) {
	if authorization == nil || authorization.Risk != domain.ToolRiskProduction {
		return domain.AuthorizationAllow, ""
	}
	switch {
	case goal == nil:
		return domain.AuthorizationDeny, "the tool changes a production system, but the agent run has no goal that allows deploying"
	case *goal == domain.AgentGoalFixAndDeploy:
		return domain.AuthorizationAsk, ""
	default:
		return domain.AuthorizationDeny, fmt.Sprintf("the tool changes a production system, which the %s goal does not allow", *goal)
	}
}
