package runtime

import (
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
)

func TestDecideAuthorization_RiskByGoal_EveryCombination(t *testing.T) {
	goal := func(g domain.AgentGoal) *domain.AgentGoal { return &g }
	low := &domain.ToolAuthorization{Risk: domain.ToolRiskLow}
	production := &domain.ToolAuthorization{Risk: domain.ToolRiskProduction, Reversible: false}

	cases := []struct {
		name          string
		authorization *domain.ToolAuthorization
		goal          *domain.AgentGoal
		want          domain.AuthorizationDecision
		wantReason    bool
	}{
		{"undeclared, no goal", nil, nil, domain.AuthorizationAllow, false},
		{"undeclared, prepare only", nil, goal(domain.AgentGoalPrepareOnly), domain.AuthorizationAllow, false},
		{"undeclared, fix and deploy", nil, goal(domain.AgentGoalFixAndDeploy), domain.AuthorizationAllow, false},
		{"low, no goal", low, nil, domain.AuthorizationAllow, false},
		{"low, prepare only", low, goal(domain.AgentGoalPrepareOnly), domain.AuthorizationAllow, false},
		{"low, fix and deploy", low, goal(domain.AgentGoalFixAndDeploy), domain.AuthorizationAllow, false},
		{"production, no goal", production, nil, domain.AuthorizationDeny, true},
		{"production, prepare only", production, goal(domain.AgentGoalPrepareOnly), domain.AuthorizationDeny, true},
		{"production, fix and deploy", production, goal(domain.AgentGoalFixAndDeploy), domain.AuthorizationAsk, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := DecideAuthorization(tc.authorization, tc.goal)
			if got != tc.want {
				t.Fatalf("DecideAuthorization = %s, want %s", got, tc.want)
			}
			if (reason != "") != tc.wantReason {
				t.Fatalf("reason = %q, want present=%v", reason, tc.wantReason)
			}
		})
	}
}
