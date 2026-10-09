package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

func TestRunSnapshotRunDTO_TokenUsage_RendersTotalBesideRunFields(t *testing.T) {
	dto := runSnapshotRunDTO{
		Run:        domain.Run{ID: "run_1", Status: domain.RunCompleted},
		TokenUsage: &domain.TokenUsage{InputTokens: 12, OutputTokens: 3, TotalTokens: 15},
	}
	body, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := string(decoded["id"]); got != `"run_1"` {
		t.Errorf("run.id = %s, want the embedded Run's field at the top level: %s", got, body)
	}
	if got := string(decoded["tokenUsage"]); got != `{"inputTokens":12,"outputTokens":3,"totalTokens":15}` {
		t.Errorf("run.tokenUsage = %s: %s", got, body)
	}
}

func TestRunSnapshotRunDTO_NoUsage_RendersNull(t *testing.T) {
	body, err := json.Marshal(runSnapshotRunDTO{Run: domain.Run{ID: "run_1"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"tokenUsage":null`) {
		t.Errorf("run %s lacks a present, null tokenUsage", body)
	}
}

func TestAgentTraceTurnDTO_Usage_ProjectsReportedAndNull(t *testing.T) {
	reported := toAgentTraceTurnDTO(service.AgentTraceTurn{Turn: domain.AgentTurn{
		ID: "turn_1", TurnNo: 1,
		TokenUsage: &domain.TokenUsage{InputTokens: 7, OutputTokens: 2, TotalTokens: 9},
	}})
	body, err := json.Marshal(reported)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"usage":{"inputTokens":7,"outputTokens":2,"totalTokens":9}`) {
		t.Errorf("turn %s lacks its usage", body)
	}

	unreported, err := json.Marshal(toAgentTraceTurnDTO(service.AgentTraceTurn{Turn: domain.AgentTurn{ID: "turn_2", TurnNo: 2}}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(unreported), `"usage":null`) {
		t.Errorf("turn %s lacks a present, null usage", unreported)
	}
}
