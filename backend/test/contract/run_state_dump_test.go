//go:build integration

package contract

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// runStateDump renders the persisted execution facts of one Run -- the Run, every NodeRun
// with its Attempts, every Agent Turn with its Action and Tool Attempts, the Callback
// Bindings and Pending Callbacks those Attempts own, and the tail of the Event log -- so a
// Run that never reaches a terminal status shows which recoverable work it was left on.
// It reads statuses, identifiers and timestamps only: no payloads, token hashes or other
// credential-adjacent columns.
func runStateDump(t *testing.T, e *testEnv, runID string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var b strings.Builder
	section := func(title, query string, args ...any) {
		fmt.Fprintf(&b, "\n-- %s\n", title)
		rows, err := e.pool.Query(ctx, query, args...)
		if err != nil {
			fmt.Fprintf(&b, "   query failed: %v\n", err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			values, err := rows.Values()
			if err != nil {
				fmt.Fprintf(&b, "   scan failed: %v\n", err)
				return
			}
			fields := rows.FieldDescriptions()
			parts := make([]string, len(values))
			for i, v := range values {
				if ts, ok := v.(time.Time); ok {
					v = ts.UTC().Format("15:04:05.000000")
				}
				parts[i] = fmt.Sprintf("%s=%v", fields[i].Name, v)
			}
			fmt.Fprintf(&b, "   %s\n", strings.Join(parts, " "))
		}
		if err := rows.Err(); err != nil {
			fmt.Fprintf(&b, "   rows failed: %v\n", err)
		}
	}

	fmt.Fprintf(&b, "persisted state of run %s (dumped at %s)", runID, time.Now().UTC().Format("15:04:05.000000"))
	section("run", `SELECT status, last_seq, started_at, completed_at, updated_at FROM runs WHERE id = $1`, runID)
	section("node runs", `
		SELECT nr.node_id, nr.status, nr.attempt_count, nr.next_attempt_at, nr.ready_at, nr.started_at, nr.waiting_at, nr.completed_at
		FROM node_runs nr WHERE nr.run_id = $1 ORDER BY nr.ready_at, nr.node_id`, runID)
	section("node attempts", `
		SELECT nr.node_id, na.attempt_no, na.status, na.started_at, na.dispatched_at, na.completed_at, na.deadline_at
		FROM node_attempts na JOIN node_runs nr ON nr.id = na.node_run_id
		WHERE nr.run_id = $1 ORDER BY nr.node_id, na.attempt_no`, runID)
	section("agent runs", `
		SELECT ar.id, ar.current_turn_no, ar.max_turns, ar.deadline, ar.terminated_at, ar.termination
		FROM agent_runs ar JOIN node_runs nr ON nr.id = ar.node_run_id WHERE nr.run_id = $1`, runID)
	section("agent turns / actions", `
		SELECT t.turn_no, t.id AS turn_id, t.status AS turn_status, t.started_at AS turn_started, t.completed_at AS turn_completed,
		       d.kind, d.tool_name, a.status AS action_status, a.started_at AS action_started, a.waiting_at, a.completed_at AS action_completed
		FROM agent_turns t
		JOIN agent_runs ar ON ar.id = t.agent_run_id
		JOIN node_runs nr ON nr.id = ar.node_run_id
		LEFT JOIN agent_decisions d ON d.turn_id = t.id
		LEFT JOIN agent_actions a ON a.turn_id = t.id
		WHERE nr.run_id = $1 ORDER BY t.turn_no`, runID)
	section("tool attempts", `
		SELECT t.turn_no, ta.attempt_no, ta.tool_name, ta.status, ta.started_at, ta.dispatched_at, ta.completed_at,
		       cb.external_task_id, pc.received_at AS pending_received, pc.consumed_at AS pending_consumed
		FROM tool_attempts ta
		JOIN agent_actions a ON a.id = ta.action_id
		JOIN agent_turns t ON t.id = a.turn_id
		JOIN agent_runs ar ON ar.id = t.agent_run_id
		JOIN node_runs nr ON nr.id = ar.node_run_id
		LEFT JOIN callback_bindings cb ON cb.target_type = 'TOOL_ATTEMPT' AND cb.target_id = ta.id
		LEFT JOIN pending_callbacks pc ON pc.external_task_id = cb.external_task_id
		WHERE nr.run_id = $1 ORDER BY t.turn_no, ta.attempt_no`, runID)
	section("unbound pending callbacks", `
		SELECT pc.external_task_id, pc.received_at, pc.consumed_at, pc.duplicate_count
		FROM pending_callbacks pc
		WHERE NOT EXISTS (SELECT 1 FROM callback_bindings cb WHERE cb.external_task_id = pc.external_task_id)`)
	section("last 20 events", `
		SELECT seq, type, occurred_at FROM (
			SELECT seq, type, occurred_at FROM events WHERE run_id = $1 ORDER BY seq DESC LIMIT 20
		) tail ORDER BY seq`, runID)
	return b.String()
}
