package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

func TestAgentTraceResponse_NoFactsAndNoLimit_RendersEmptyLedgerAndNullLimit(t *testing.T) {
	body, err := json.Marshal(toAgentTraceResponse(service.AgentTrace{}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{
		`"facts":{"items":[],"truncated":false}`,
		`"generationBudget":{"maxGenerationCalls":null,"generationCallsUsed":0}`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("response %s lacks %s", body, want)
		}
	}
}

func TestAgentTraceResponse_FactWithoutBindings_RendersEmptyObjectInUTC(t *testing.T) {
	verdict := true
	basis := "fact_gen"
	limit := 3
	zone := time.FixedZone("UTC+8", 8*3600)
	trace := service.AgentTrace{
		Facts: service.AgentTraceFacts{Truncated: true, Facts: []domain.ExecutionFact{{
			ID: "fact_review", FactType: "asset_reviewed", SubjectRef: "asset_1",
			Verdict: &verdict, BasisFactID: &basis, ToolAttemptID: "ta_2",
			CreatedAt: time.Date(2026, 10, 9, 20, 0, 0, 0, zone),
		}}},
		Budget: service.AgentGenerationBudget{MaxGenerationCalls: &limit, Used: 1},
	}

	body, err := json.Marshal(toAgentTraceResponse(trace))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `"facts":{"items":[{"id":"fact_review","factType":"asset_reviewed","subject":"asset_1",` +
		`"bindings":{},"verdict":true,"basisFactId":"fact_gen","toolAttemptId":"ta_2",` +
		`"createdAt":"2026-10-09T12:00:00Z"}],"truncated":true},` +
		`"generationBudget":{"maxGenerationCalls":3,"generationCallsUsed":1}`
	if !strings.Contains(string(body), want) {
		t.Errorf("response %s\nlacks %s", body, want)
	}
}
