package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/store"
)

// FinishedCalls reads the finished Node Attempts, Tool Attempts and Agent Turns of the
// given Runs with the Cost each recorded. Every row reaches its Run through the NodeRun
// that owns it: a Tool Attempt through its Action, Turn and Agent Run. Terminal statuses
// are matched literally so a call still in flight, whose charge is not yet known, is not
// counted as finished.
func (r *runListRepository) FinishedCalls(ctx context.Context, runIDs []string) (map[string][]store.FinishedCall, error) {
	calls := make(map[string][]store.FinishedCall, len(runIDs))
	if len(runIDs) == 0 {
		return calls, nil
	}
	query := `
		SELECT nr.run_id, 'NODE_ATTEMPT', '', na.dispatched_at IS NOT NULL, COALESCE(na.uncertain, FALSE),
		       na.cost_amount::text, na.cost_currency, na.cost_source
		  FROM node_attempts na
		  JOIN node_runs nr ON nr.id = na.node_run_id
		 WHERE nr.run_id = ANY($1) AND na.status IN ('SUCCEEDED', 'FAILED')
		UNION ALL
		SELECT nr.run_id, 'TOOL_ATTEMPT', ta.tool_name, ta.dispatched_at IS NOT NULL, COALESCE(ta.uncertain, FALSE),
		       ta.cost_amount::text, ta.cost_currency, ta.cost_source
		  FROM tool_attempts ta
		  JOIN agent_actions aa ON aa.id = ta.action_id
		  JOIN agent_turns tu ON tu.id = aa.turn_id
		  JOIN agent_runs ar ON ar.id = tu.agent_run_id
		  JOIN node_runs nr ON nr.id = ar.node_run_id
		 WHERE nr.run_id = ANY($1) AND ta.status IN ('SUCCEEDED', 'FAILED')
		UNION ALL
		SELECT nr.run_id, 'AGENT_TURN', '', FALSE, FALSE,
		       tu.cost_amount::text, tu.cost_currency, tu.cost_source
		  FROM agent_turns tu
		  JOIN agent_runs ar ON ar.id = tu.agent_run_id
		  JOIN node_runs nr ON nr.id = ar.node_run_id
		 WHERE nr.run_id = ANY($1) AND tu.status IN ('COMPLETED', 'FAILED')`

	rows, err := r.conn.Query(ctx, query, runIDs)
	if err != nil {
		return nil, mapError("runList.FinishedCalls", err)
	}
	type callRow struct {
		runID string
		call  store.FinishedCall
	}
	scanned, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (callRow, error) {
		var (
			c                        callRow
			kind                     string
			amount, currency, source *string
		)
		if err := row.Scan(&c.runID, &kind, &c.call.ToolName, &c.call.Dispatched, &c.call.Uncertain,
			&amount, &currency, &source); err != nil {
			return c, err
		}
		c.call.Kind = store.FinishedCallKind(kind)
		cost, err := scanCost(amount, currency, source)
		if err != nil {
			return c, err
		}
		c.call.Cost = cost
		return c, nil
	})
	if err != nil {
		return nil, mapError("runList.FinishedCalls", err)
	}
	for _, c := range scanned {
		calls[c.runID] = append(calls[c.runID], c.call)
	}
	return calls, nil
}
