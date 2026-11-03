package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

func TestAgentTraceResponse_NoCompletion_RendersNull(t *testing.T) {
	body, err := json.Marshal(toAgentTraceResponse(service.AgentTrace{}, defaultTraceMaxFieldBytes))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"completion":null`) {
		t.Errorf("response %s lacks a null completion", body)
	}
}

func TestAgentTraceResponse_Completion_RendersOutcomeChecksAndMissing(t *testing.T) {
	zone := time.FixedZone("UTC+8", 8*3600)
	trace := service.AgentTrace{Completion: &domain.AgentCompletion{
		AgentRunID: "ar_1", Goal: domain.AgentGoalPrepareOnly, Outcome: domain.CompletionIncomplete,
		Checks: []domain.CompletionCheck{
			{Name: "workspace_patch_recorded", Passed: true, Refs: []string{"wpatch_1"}},
			{Name: "latest_patch_verified", Passed: false},
		},
		Missing:     []domain.CompletionMissing{{Code: "PATCH_VERIFICATION_MISSING", Description: "No verification is recorded for the latest patch."}},
		EvaluatedAt: time.Date(2026, 11, 2, 20, 0, 0, 0, zone),
	}}
	body, err := json.Marshal(toAgentTraceResponse(trace, defaultTraceMaxFieldBytes))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `"completion":{"outcome":"INCOMPLETE","checks":[{"name":"workspace_patch_recorded","passed":true,"refs":["wpatch_1"]},` +
		`{"name":"latest_patch_verified","passed":false,"refs":[]}],` +
		`"missing":[{"code":"PATCH_VERIFICATION_MISSING","description":"No verification is recorded for the latest patch."}],` +
		`"evaluatedAt":"2026-11-02T12:00:00Z"}`
	if !strings.Contains(string(body), want) {
		t.Errorf("response %s\nlacks %s", body, want)
	}
	if strings.Contains(string(body), `"goal":"PREPARE_ONLY","outcome"`) {
		t.Errorf("the completion view repeats the goal the agent run already carries: %s", body)
	}
}
