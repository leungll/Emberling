//go:build integration

// Call cost persistence: the three cost columns of node_attempts, tool_attempts and
// agent_turns are written once, in the same conditional update that commits the call's
// result, read back in canonical decimal form, and are all NULL or all set. A call that
// commits no Cost, a failed call and an uncertain call leave NULL, which the read side
// reports as "unknown" and never as zero.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
	"github.com/leungll/Emberling/backend/internal/store/postgres"
	"github.com/leungll/Emberling/backend/test/testdb"
)

func readToolAttempt(ctx context.Context, t *testing.T, uow store.UnitOfWork, attemptID string) domain.ToolAttempt {
	t.Helper()
	var attempt domain.ToolAttempt
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		attempt, err = tx.ToolAttempts().Get(ctx, attemptID)
		return err
	}); err != nil {
		t.Fatalf("get tool attempt %s: %v", attemptID, err)
	}
	return attempt
}

func usdCost(amount, source string) *domain.Cost {
	return &domain.Cost{Amount: amount, Currency: domain.CurrencyUSD, Source: source}
}

// TestCallCostColumns_PartialRow_RejectedByCheck: a row with only some of the three cost
// columns set violates the all-or-none constraint on each table, so no half-recorded
// charge can exist.
func TestCallCostColumns_PartialRow_RejectedByCheck(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)

	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_cost", "turn_cost", "decision_cost", "action_cost")
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.NodeAttempts().Create(ctx, newNodeAttempt("attempt_cost", f.nodeRunID, 1)); err != nil {
			return err
		}
		return tx.ToolAttempts().Create(ctx, newToolAttempt("tool_attempt_cost", "action_cost", 1))
	}); err != nil {
		t.Fatalf("seed attempts: %v", err)
	}

	cases := map[string]string{
		"node_attempts amount only":   `UPDATE node_attempts SET cost_amount = 0.04 WHERE id = 'attempt_cost'`,
		"node_attempts no source":     `UPDATE node_attempts SET cost_amount = 0.04, cost_currency = 'USD' WHERE id = 'attempt_cost'`,
		"tool_attempts currency only": `UPDATE tool_attempts SET cost_currency = 'USD' WHERE id = 'tool_attempt_cost'`,
		"tool_attempts no amount":     `UPDATE tool_attempts SET cost_currency = 'USD', cost_source = 'm' WHERE id = 'tool_attempt_cost'`,
		"agent_turns source only":     `UPDATE agent_turns SET cost_source = 'm' WHERE id = 'turn_cost'`,
		"agent_turns no currency":     `UPDATE agent_turns SET cost_amount = 0, cost_source = 'm' WHERE id = 'turn_cost'`,
	}
	for name, statement := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := pool.Exec(ctx, statement)
			if err == nil || !isCheckViolation(err) {
				t.Fatalf("partial cost row: want a CHECK violation, got %v", err)
			}
		})
	}
	for _, table := range []string{"node_attempts", "tool_attempts", "agent_turns"} {
		assertColumnsExist(ctx, t, pool, table, "cost_amount", "cost_currency", "cost_source")
	}
}

func isCheckViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23514"
}

func assertColumnsExist(ctx context.Context, t *testing.T, pool *pgxpool.Pool, table string, columns ...string) {
	t.Helper()
	for _, column := range columns {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM information_schema.columns WHERE table_name = $1 AND column_name = $2)`, table, column).Scan(&exists); err != nil {
			t.Fatalf("read columns of %s: %v", table, err)
		}
		if !exists {
			t.Fatalf("%s.%s: want created by the migration, got missing", table, column)
		}
	}
}

// TestNodeAttempts_MarkSucceeded_Cost_WrittenOnceAndReadBackCanonical: the Cost commits
// with the result and reads back in canonical decimal form, a reported zero reads back as
// the known "0", and a completion without a Cost leaves every cost column NULL.
func TestNodeAttempts_MarkSucceeded_Cost_WrittenOnceAndReadBackCanonical(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)
	f := seedRun(ctx, t, uow)

	cases := map[string]struct {
		cost *domain.Cost
		want *domain.Cost
	}{
		"reported charge": {cost: usdCost("0.04", "image-model-v1"), want: usdCost("0.04", "image-model-v1")},
		"micro amount":    {cost: usdCost("0.000032", "image-model-v1"), want: usdCost("0.000032", "image-model-v1")},
		"known zero":      {cost: usdCost("0", "image-model-v1"), want: usdCost("0", "image-model-v1")},
		"none reported":   {cost: nil, want: nil},
	}
	attemptNo := 0
	for name, tc := range cases {
		attemptNo++
		attemptID := "attempt_cost_" + name
		t.Run(name, func(t *testing.T) {
			if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
				if err := tx.NodeAttempts().Create(ctx, newNodeAttempt(attemptID, f.nodeRunID, attemptNo)); err != nil {
					return err
				}
				return tx.NodeAttempts().MarkSucceeded(ctx, attemptID, domain.NodeAttemptStarted, fixtureTime, json.RawMessage(`{"ok":true}`), tc.cost)
			}); err != nil {
				t.Fatalf("MarkSucceeded: %v", err)
			}
			got := getNodeAttempt(ctx, t, uow, attemptID)
			if diff := cmp.Diff(tc.want, got.Cost); diff != "" {
				t.Fatalf("persisted cost (-want +got):\n%s", diff)
			}
			if got.Status != domain.NodeAttemptSucceeded {
				t.Fatalf("status = %s, want SUCCEEDED", got.Status)
			}
		})
	}

	// A stale second completion carrying another charge changes nothing: the Cost is
	// written once, by the transaction that won the result.
	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeAttempts().MarkSucceeded(ctx, "attempt_cost_reported charge", domain.NodeAttemptStarted, fixtureTime, json.RawMessage(`{"late":true}`), usdCost("9", "other"))
	})
	if !errors.Is(err, domain.ErrStaleClaim) {
		t.Fatalf("second MarkSucceeded: want domain.ErrStaleClaim, got %v", err)
	}
	if got := getNodeAttempt(ctx, t, uow, "attempt_cost_reported charge"); got.Cost == nil || got.Cost.Amount != "0.04" {
		t.Fatalf("cost after stale completion = %+v, want the first charge 0.04 untouched", got.Cost)
	}
}

// TestNodeAttempts_MarkSucceeded_InvalidCost_RefusedBeforeSQL: a Cost that fails
// validation never reaches the row; the Attempt stays STARTED without a result.
func TestNodeAttempts_MarkSucceeded_InvalidCost_RefusedBeforeSQL(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)
	f := seedRun(ctx, t, uow)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeAttempts().Create(ctx, newNodeAttempt("attempt_bad_cost", f.nodeRunID, 1))
	}); err != nil {
		t.Fatalf("seed attempt: %v", err)
	}

	err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.NodeAttempts().MarkSucceeded(ctx, "attempt_bad_cost", domain.NodeAttemptStarted, fixtureTime,
			json.RawMessage(`{"ok":true}`), &domain.Cost{Amount: "1e-3", Currency: domain.CurrencyUSD, Source: "m"})
	})
	if !errors.Is(err, domain.ErrInvalidCost) {
		t.Fatalf("MarkSucceeded with an invalid amount: want domain.ErrInvalidCost, got %v", err)
	}
	if got := getNodeAttempt(ctx, t, uow, "attempt_bad_cost"); got.Status != domain.NodeAttemptStarted || got.Cost != nil {
		t.Fatalf("attempt after refused completion = %s cost %+v, want STARTED with no cost", got.Status, got.Cost)
	}
}

// TestNodeAttempts_UncertainFailure_CostStaysNull: an Attempt that failed with an
// uncertain outcome records no Cost, since whether and how much the Provider charged is
// not known; nothing fills in a zero.
func TestNodeAttempts_UncertainFailure_CostStaysNull(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)
	f := seedRun(ctx, t, uow)
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.NodeAttempts().Create(ctx, newNodeAttempt("attempt_uncertain", f.nodeRunID, 1)); err != nil {
			return err
		}
		return tx.NodeAttempts().MarkFailed(ctx, "attempt_uncertain", domain.NodeAttemptStarted, fixtureTime,
			domain.ExecutionError{Code: "DISPATCH_UNCERTAIN", Message: "connection reset"}, true)
	}); err != nil {
		t.Fatalf("seed uncertain failure: %v", err)
	}
	got := getNodeAttempt(ctx, t, uow, "attempt_uncertain")
	if got.Uncertain == nil || !*got.Uncertain {
		t.Fatalf("uncertain = %v, want true", got.Uncertain)
	}
	if got.Cost != nil {
		t.Fatalf("cost = %+v, want nil on an uncertain failure", *got.Cost)
	}
}

// TestToolAttempts_MarkSucceeded_Cost_WrittenWithResult covers the Tool Attempt mapping:
// the charge commits with the result, reads back canonical, and a result without one
// leaves NULL.
func TestToolAttempts_MarkSucceeded_Cost_WrittenWithResult(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)
	f := seedRun(ctx, t, uow)
	seedDecidedTurn(ctx, t, uow, f, "ar_1", "turn_1", "decision_1", "action_1")

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := tx.ToolAttempts().Create(ctx, newToolAttempt("tool_attempt_1", "action_1", 1)); err != nil {
			return err
		}
		if err := tx.ToolAttempts().MarkSucceeded(ctx, "tool_attempt_1", domain.ToolAttemptStarted, fixtureTime,
			json.RawMessage(`{"assetRef":"artifact_1"}`), usdCost("0.045", "image-model-v1")); err != nil {
			return err
		}
		if err := tx.ToolAttempts().Create(ctx, newToolAttempt("tool_attempt_2", "action_1", 2)); err != nil {
			return err
		}
		return tx.ToolAttempts().MarkSucceeded(ctx, "tool_attempt_2", domain.ToolAttemptStarted, fixtureTime,
			json.RawMessage(`{"value":"k1"}`), nil)
	}); err != nil {
		t.Fatalf("seed tool attempts: %v", err)
	}

	withCost := readToolAttempt(ctx, t, uow, "tool_attempt_1")
	if diff := cmp.Diff(usdCost("0.045", "image-model-v1"), withCost.Cost); diff != "" {
		t.Fatalf("tool attempt cost (-want +got):\n%s", diff)
	}
	if without := readToolAttempt(ctx, t, uow, "tool_attempt_2"); without.Cost != nil {
		t.Fatalf("tool attempt without a reported charge: cost = %+v, want nil", *without.Cost)
	}

	var listed []domain.ToolAttempt
	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		listed, err = tx.ToolAttempts().ListByActionID(ctx, "action_1")
		return err
	}); err != nil {
		t.Fatalf("list tool attempts: %v", err)
	}
	if len(listed) != 2 || listed[0].Cost == nil || listed[0].Cost.Amount != "0.045" || listed[1].Cost != nil {
		t.Fatalf("listed tool attempts carry costs %+v / %+v, want 0.045 and nil", listed[0].Cost, listed[1].Cost)
	}
}

// TestAgentTurns_MarkCompleted_Cost_WrittenWithResponse covers the Turn mapping: the
// charge commits with the response and reads back canonical; a Turn whose Provider
// reported none, and a FAILED Turn, carry NULL.
func TestAgentTurns_MarkCompleted_Cost_WrittenWithResponse(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t)
	uow := postgres.NewUnitOfWork(pool)
	f := seedRun(ctx, t, uow)
	createAgentRun(ctx, t, uow, newAgentRun("ar_1", f.nodeRunID))

	if err := uow.WithinTx(ctx, func(ctx context.Context, tx store.Tx) error {
		for turnNo, turnID := range []string{"turn_1", "turn_2", "turn_3"} {
			if err := tx.AgentTurns().Create(ctx, newAgentTurn(turnID, "ar_1", turnNo+1)); err != nil {
				return err
			}
			if _, err := tx.AgentTurns().ClaimReady(ctx, turnID, fixtureTime); err != nil {
				return err
			}
		}
		if err := tx.AgentTurns().MarkCompleted(ctx, "turn_1", fixtureTime, json.RawMessage(`{"kind":"TOOL_CALL"}`),
			&domain.TokenUsage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}, usdCost("0.000032", "decision-model-v1")); err != nil {
			return err
		}
		if err := tx.AgentTurns().MarkCompleted(ctx, "turn_2", fixtureTime, json.RawMessage(`{"kind":"FINAL"}`), nil, nil); err != nil {
			return err
		}
		return tx.AgentTurns().MarkFailed(ctx, "turn_3", fixtureTime, domain.ExecutionError{Code: "MODEL_ERROR", Message: "refused"})
	}); err != nil {
		t.Fatalf("seed turns: %v", err)
	}

	if got := getAgentTurn(ctx, t, uow, "turn_1"); got.Cost == nil || *got.Cost != *usdCost("0.000032", "decision-model-v1") {
		t.Fatalf("turn_1 cost = %+v, want 0.000032 USD from decision-model-v1", got.Cost)
	}
	if got := getAgentTurn(ctx, t, uow, "turn_2"); got.Cost != nil {
		t.Fatalf("turn_2 cost = %+v, want nil when the Provider reported none", *got.Cost)
	}
	if got := getAgentTurn(ctx, t, uow, "turn_3"); got.Cost != nil || got.Status != domain.AgentTurnFailed {
		t.Fatalf("turn_3 = %s cost %+v, want FAILED with no cost", got.Status, got.Cost)
	}
	turns := listTurnsOfAgentRun(ctx, t, uow, "ar_1")
	if len(turns) != 3 || turns[0].Cost == nil || turns[1].Cost != nil || turns[2].Cost != nil {
		t.Fatalf("listed turns carry costs %+v, want only the first", turns)
	}
}
