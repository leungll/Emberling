package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/service"
)

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(body)
}

// TestNodeAttemptDTO_Cost_ProjectsRecordedChargeAndNull: a recorded Cost reaches the
// Attempt as {amount, currency, source} with the amount as text; an Attempt without one
// carries a present, null cost, never a zero.
func TestNodeAttemptDTO_Cost_ProjectsRecordedChargeAndNull(t *testing.T) {
	recorded := mustMarshal(t, toNodeAttemptDTO(domain.NodeAttempt{
		ID: "attempt_1", Status: domain.NodeAttemptSucceeded,
		Cost: &domain.Cost{Amount: "0.04", Currency: domain.CurrencyUSD, Source: "image-model-v1"},
	}, nil))
	if !strings.Contains(recorded, `"cost":{"amount":"0.04","currency":"USD","source":"image-model-v1"}`) {
		t.Errorf("attempt %s lacks its recorded cost", recorded)
	}
	unknown := mustMarshal(t, toNodeAttemptDTO(domain.NodeAttempt{ID: "attempt_2", Status: domain.NodeAttemptFailed}, nil))
	if !strings.Contains(unknown, `"cost":null`) {
		t.Errorf("attempt %s lacks a present, null cost", unknown)
	}
}

// TestAgentTraceTurnDTO_Cost_ProjectsRecordedChargeAndNull covers the Turn and its Tool
// Attempts: each carries its own recorded charge or null, and a Tool Attempt's charge is
// never folded into the Turn's.
func TestAgentTraceTurnDTO_Cost_ProjectsRecordedChargeAndNull(t *testing.T) {
	turn := toAgentTraceTurnDTO(service.AgentTraceTurn{
		Turn: domain.AgentTurn{
			ID: "turn_1", TurnNo: 1, Status: domain.AgentTurnCompleted,
			Cost: &domain.Cost{Amount: "0", Currency: domain.CurrencyUSD, Source: "text-model-v1"},
		},
		ToolAttempts: []domain.ToolAttempt{
			{ID: "ta_1", ToolName: "generate_image", AttemptNo: 1, Status: domain.ToolAttemptSucceeded,
				Cost: &domain.Cost{Amount: "0.004", Currency: domain.CurrencyUSD, Source: "image-model-v1"}},
			{ID: "ta_2", ToolName: "generate_image", AttemptNo: 2, Status: domain.ToolAttemptFailed},
		},
	})
	body := mustMarshal(t, turn)
	if !strings.Contains(body, `"cost":{"amount":"0","currency":"USD","source":"text-model-v1"}`) {
		t.Errorf("turn %s lacks its known zero cost", body)
	}
	if !strings.Contains(body, `"cost":{"amount":"0.004","currency":"USD","source":"image-model-v1"}`) {
		t.Errorf("turn %s lacks the first Tool Attempt's cost", body)
	}
	if strings.Count(body, `"cost":null`) != 1 {
		t.Errorf("turn %s: want exactly one null cost, the failed Tool Attempt's", body)
	}

	unknown := mustMarshal(t, toAgentTraceTurnDTO(service.AgentTraceTurn{Turn: domain.AgentTurn{ID: "turn_2", TurnNo: 2}}))
	if !strings.Contains(unknown, `"cost":null`) {
		t.Errorf("turn %s lacks a present, null cost", unknown)
	}
}

// TestRunListItemDTO_Cost_RendersTotalsAndUnknownCount: the Run list item's cost always
// carries a totals array and an unknownCalls count; with nothing known the array is empty
// (not null) and the count is zero.
func TestRunListItemDTO_Cost_RendersTotalsAndUnknownCount(t *testing.T) {
	summed := mustMarshal(t, toRunListItemDTO(service.RunListItem{
		ID: "run_1", Status: domain.RunCompleted,
		Cost: service.RunCost{Totals: []service.CostTotal{{Amount: "0.084032", Currency: domain.CurrencyUSD}}, UnknownCalls: 2},
	}))
	if !strings.Contains(summed, `"cost":{"totals":[{"amount":"0.084032","currency":"USD"}],"unknownCalls":2}`) {
		t.Errorf("item %s lacks its cost summary", summed)
	}
	empty := mustMarshal(t, toRunListItemDTO(service.RunListItem{ID: "run_2", Status: domain.RunRunning}))
	if !strings.Contains(empty, `"cost":{"totals":[],"unknownCalls":0}`) {
		t.Errorf("item %s: want an empty totals array and a zero unknown count", empty)
	}
}
