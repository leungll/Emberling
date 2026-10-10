package service

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/registry"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/tools/generateimage"
	"github.com/leungll/Emberling/backend/internal/tools/lookup"
)

// traceLedgerFakeTx serves only the fact ledger and the generation count; any other
// repository use panics on the nil embedded interface, proving the Trace reads nothing
// else for them and writes nothing at all.
type traceLedgerFakeTx struct {
	store.Tx
	facts    *traceLedgerFactRepo
	attempts *traceLedgerAttemptRepo
}

func (t traceLedgerFakeTx) ExecutionFacts() store.ExecutionFactRepository { return t.facts }
func (t traceLedgerFakeTx) ToolAttempts() store.ToolAttemptRepository     { return t.attempts }

type traceLedgerFactRepo struct {
	store.ExecutionFactRepository
	stored []domain.ExecutionFact
	limits []int
}

func (r *traceLedgerFactRepo) ListByAgentRun(_ context.Context, _ string, limit int) ([]domain.ExecutionFact, error) {
	r.limits = append(r.limits, limit)
	rows := r.stored
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return slices.Clone(rows), nil
}

type traceLedgerAttemptRepo struct {
	store.ToolAttemptRepository
	used    int
	queried [][]string
}

func (r *traceLedgerAttemptRepo) CountByAgentRunForTools(_ context.Context, _ string, toolNames []string) (int, error) {
	r.queried = append(r.queried, slices.Clone(toolNames))
	return r.used, nil
}

func traceLedgerFacts(n int) []domain.ExecutionFact {
	base := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	facts := make([]domain.ExecutionFact, n)
	for i := range facts {
		facts[i] = domain.ExecutionFact{
			ID:        fmt.Sprintf("fact_%04d", i),
			FactType:  "image_generated",
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}
	}
	return facts
}

func TestAgentTraceFacts_MoreFactsThanShown_ShowsOldestAndReportsTruncated(t *testing.T) {
	repo := &traceLedgerFactRepo{stored: traceLedgerFacts(maxTraceFacts + 5)}

	ledger, err := agentTraceFacts(context.Background(), traceLedgerFakeTx{facts: repo}, "ar_1")
	if err != nil {
		t.Fatalf("agentTraceFacts: %v", err)
	}
	if !ledger.Truncated {
		t.Error("Truncated = false for an Agent Run holding more facts than the ledger shows")
	}
	if len(ledger.Facts) != maxTraceFacts {
		t.Fatalf("ledger shows %d facts, want %d", len(ledger.Facts), maxTraceFacts)
	}
	if ledger.Facts[0].ID != "fact_0000" || ledger.Facts[maxTraceFacts-1].ID != fmt.Sprintf("fact_%04d", maxTraceFacts-1) {
		t.Errorf("ledger = %s..%s, want the oldest %d facts", ledger.Facts[0].ID, ledger.Facts[len(ledger.Facts)-1].ID, maxTraceFacts)
	}
	if len(repo.stored) != maxTraceFacts+5 {
		t.Errorf("stored facts = %d after the read, want them untouched", len(repo.stored))
	}
	if !slices.Equal(repo.limits, []int{maxTraceFacts + 1}) {
		t.Errorf("read limits = %v, want one bounded read of %d", repo.limits, maxTraceFacts+1)
	}
}

func TestAgentTraceFacts_ExactlyAtBound_IsNotTruncated(t *testing.T) {
	repo := &traceLedgerFactRepo{stored: traceLedgerFacts(maxTraceFacts)}

	ledger, err := agentTraceFacts(context.Background(), traceLedgerFakeTx{facts: repo}, "ar_1")
	if err != nil {
		t.Fatalf("agentTraceFacts: %v", err)
	}
	if ledger.Truncated || len(ledger.Facts) != maxTraceFacts {
		t.Errorf("ledger = %d facts truncated=%v, want all %d and not truncated", len(ledger.Facts), ledger.Truncated, maxTraceFacts)
	}
}

func traceLedgerTools(t *testing.T, regs ...registry.ToolRegistration) *registry.ToolRegistry {
	t.Helper()
	tools := registry.NewToolRegistry()
	for _, reg := range regs {
		if err := tools.Register(reg); err != nil {
			t.Fatalf("register tool %s: %v", reg.Metadata.Name, err)
		}
	}
	return tools
}

func TestQueryService_AgentGenerationBudget_CountsOnlyToolsRegisteredAsCounting(t *testing.T) {
	tools := traceLedgerTools(t, generateimage.Registration("http://mock-provider.test", nil, nil), lookup.Registration())
	svc := NewQueryService(Deps{Tools: tools})
	attempts := &traceLedgerAttemptRepo{used: 3}
	limit := 12

	budget, err := svc.agentGenerationBudget(context.Background(), traceLedgerFakeTx{attempts: attempts},
		domain.AgentRun{ID: "ar_1", MaxGenerationCalls: &limit})
	if err != nil {
		t.Fatalf("agentGenerationBudget: %v", err)
	}
	if budget.MaxGenerationCalls == nil || *budget.MaxGenerationCalls != 12 || budget.Used != 3 {
		t.Errorf("budget = %v/%d, want 12/3", budget.MaxGenerationCalls, budget.Used)
	}
	if len(attempts.queried) != 1 || !slices.Equal(attempts.queried[0], []string{generateimage.ToolName}) {
		t.Errorf("counted Tools = %v, want only %s", attempts.queried, generateimage.ToolName)
	}
}

func TestQueryService_AgentGenerationBudget_NoLimit_StillCountsCalls(t *testing.T) {
	tools := traceLedgerTools(t, generateimage.Registration("http://mock-provider.test", nil, nil))
	svc := NewQueryService(Deps{Tools: tools})
	attempts := &traceLedgerAttemptRepo{used: 2}

	budget, err := svc.agentGenerationBudget(context.Background(), traceLedgerFakeTx{attempts: attempts},
		domain.AgentRun{ID: "ar_1"})
	if err != nil {
		t.Fatalf("agentGenerationBudget: %v", err)
	}
	if budget.MaxGenerationCalls != nil || budget.Used != 2 {
		t.Errorf("budget = %v/%d, want no limit and 2 calls used", budget.MaxGenerationCalls, budget.Used)
	}
}

func TestQueryService_AgentGenerationBudget_NoCountingTool_ReportsZeroWithoutReading(t *testing.T) {
	svc := NewQueryService(Deps{Tools: traceLedgerTools(t, lookup.Registration())})
	attempts := &traceLedgerAttemptRepo{used: 99}
	limit := 4

	budget, err := svc.agentGenerationBudget(context.Background(), traceLedgerFakeTx{attempts: attempts},
		domain.AgentRun{ID: "ar_1", MaxGenerationCalls: &limit})
	if err != nil {
		t.Fatalf("agentGenerationBudget: %v", err)
	}
	if budget.Used != 0 || budget.MaxGenerationCalls == nil || *budget.MaxGenerationCalls != 4 {
		t.Errorf("budget = %v/%d, want 4/0", budget.MaxGenerationCalls, budget.Used)
	}
	if len(attempts.queried) != 0 {
		t.Errorf("counted Tool Attempts %v with no counting Tool registered, want no read", attempts.queried)
	}
}
