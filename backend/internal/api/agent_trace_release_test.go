package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

func releaseTraceRound() service.AgentTraceTurn {
	zone := time.FixedZone("UTC+8", 8*3600)
	at := func(minute int) time.Time { return time.Date(2026, 11, 2, 20, minute, 0, 0, zone) }
	voided, deadline, unknown, voidedAt := at(5), at(1), at(1), at(4)
	released, reason := at(9), domain.ReleaseReasonDeployed
	closed, outcome := at(9), "DEPLOYED"
	key1, key2 := "op_action_1_1", "op_action_1_2"
	unchecked := "1.4.2"
	return service.AgentTraceTurn{
		Turn:     domain.AgentTurn{ID: "turn_1", TurnNo: 3},
		Action:   &domain.AgentAction{ID: "action_1", Type: domain.AgentActionToolCall, Status: domain.AgentActionSucceeded},
		Decision: &domain.AgentDecision{Kind: domain.DecisionToolCall},
		ToolAttempts: []domain.ToolAttempt{
			{ID: "ta_1", AttemptNo: 1, ToolName: "deploy_change", Status: domain.ToolAttemptFailed, OperationKey: &key1,
				Error: &domain.ExecutionError{Code: domain.ReleaseCodeVoided, Message: "voided"}, StartedAt: at(0),
				ConfirmDeadline: &deadline, OutcomeUnknownSince: &unknown, VoidRequestedAt: &voidedAt, CompletedAt: &voided},
			{ID: "ta_2", AttemptNo: 2, ToolName: "deploy_change", Status: domain.ToolAttemptSucceeded, OperationKey: &key2, StartedAt: at(5), ConfirmDeadline: &released},
		},
		Occupation: &domain.ReleaseOccupation{
			ID: "occupation_1", TargetKey: "orders-api/staging", OperationKey: key2, Status: domain.ReleaseOccupationReleased,
			AcquiredAt: at(0), ReleasedAt: &released, ReleaseReason: &reason, QueryCount: 2, DeployedUncheckedVersion: &unchecked,
		},
		HumanWait: &domain.HumanWait{ID: "hw_1", ActionID: "action_1", RunID: "run_1", Status: domain.HumanWaitResolved,
			OpenedAt: at(1), Deadline: at(31), ClosedAt: &closed, Outcome: &outcome},
	}
}

func TestAgentTraceTurnDTO_ReleaseAction_RendersOperationsOccupationAndHumanWaitInUTC(t *testing.T) {
	body, err := json.Marshal(toAgentTraceTurnDTO(releaseTraceRound()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `"release":{"operations":[` +
		`{"operationKey":"op_action_1_1","toolAttemptId":"ta_1","attemptNo":1,"status":"FAILED","outcome":"VOIDED",` +
		`"confirmDeadline":"2026-11-02T12:01:00Z","outcomeUnknownSince":"2026-11-02T12:01:00Z","voidRequestedAt":"2026-11-02T12:04:00Z"},` +
		`{"operationKey":"op_action_1_2","toolAttemptId":"ta_2","attemptNo":2,"status":"SUCCEEDED","outcome":"DEPLOYED",` +
		`"confirmDeadline":"2026-11-02T12:09:00Z","outcomeUnknownSince":null,"voidRequestedAt":null}],` +
		`"occupation":{"id":"occupation_1","targetKey":"orders-api/staging","operationKey":"op_action_1_2","status":"RELEASED",` +
		`"acquiredAt":"2026-11-02T12:00:00Z","releasedAt":"2026-11-02T12:09:00Z","releaseReason":"DEPLOYED",` +
		`"nextQueryAt":null,"queryCount":2,"deployedUncheckedVersion":"1.4.2"},` +
		`"humanWait":{"id":"hw_1","status":"RESOLVED","openedAt":"2026-11-02T12:01:00Z","deadline":"2026-11-02T12:31:00Z",` +
		`"closedAt":"2026-11-02T12:09:00Z","closedBy":null,"outcome":"DEPLOYED"}}`
	if !strings.Contains(string(body), want) {
		t.Errorf("response %s\nlacks %s", body, want)
	}
}

func TestAgentTraceTurnDTO_ActionWithoutReleaseFacts_RendersReleaseNull(t *testing.T) {
	round := service.AgentTraceTurn{
		Turn:         domain.AgentTurn{ID: "turn_1", TurnNo: 1},
		Action:       &domain.AgentAction{ID: "action_1", Type: domain.AgentActionToolCall, Status: domain.AgentActionSucceeded},
		Decision:     &domain.AgentDecision{Kind: domain.DecisionToolCall},
		ToolAttempts: []domain.ToolAttempt{{ID: "ta_1", AttemptNo: 1, ToolName: "read_file", Status: domain.ToolAttemptSucceeded}},
	}
	body, err := json.Marshal(toAgentTraceTurnDTO(round))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"release":null`) {
		t.Errorf("response %s lacks a null release view", body)
	}
}

func TestReleaseOutcomeOf_OpenAttempts_PendingUntilOutcomeUnknown(t *testing.T) {
	now := time.Now()
	uncertain := true
	cases := map[string]struct {
		attempt domain.ToolAttempt
		want    releaseOperationOutcome
	}{
		"started":            {domain.ToolAttempt{Status: domain.ToolAttemptStarted}, releaseOutcomePending},
		"dispatched":         {domain.ToolAttempt{Status: domain.ToolAttemptDispatched}, releaseOutcomePending},
		"dispatched unknown": {domain.ToolAttempt{Status: domain.ToolAttemptDispatched, OutcomeUnknownSince: &now}, releaseOutcomeUnknown},
		"refused":            {domain.ToolAttempt{Status: domain.ToolAttemptFailed, Error: &domain.ExecutionError{Code: "CHANGE_FREEZE"}}, releaseOutcomeRefused},
		"failed uncertain":   {domain.ToolAttempt{Status: domain.ToolAttemptFailed, Uncertain: &uncertain, Error: &domain.ExecutionError{Code: "RELEASE_UNKNOWN"}}, releaseOutcomeUnknown},
		"voided":             {domain.ToolAttempt{Status: domain.ToolAttemptFailed, Error: &domain.ExecutionError{Code: domain.ReleaseCodeVoided}}, releaseOutcomeVoided},
		"deployed":           {domain.ToolAttempt{Status: domain.ToolAttemptSucceeded}, releaseOutcomeDeployed},
	}
	for name, tc := range cases {
		if got := releaseOutcomeOf(tc.attempt); got != tc.want {
			t.Errorf("%s: outcome = %s, want %s", name, got, tc.want)
		}
	}
}
