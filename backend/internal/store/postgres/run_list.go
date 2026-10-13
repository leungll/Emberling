package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

// runListRepository reads the Run list projection. Every statement is a plain SELECT,
// so it is safe inside a read-only transaction.
type runListRepository struct {
	conn pgx.Tx
}

// runListArgs accumulates positional parameters while a statement is assembled from
// fixed SQL fragments. Only placeholders are generated; values never enter the SQL text.
type runListArgs struct {
	values []any
}

func (a *runListArgs) add(v any) string {
	a.values = append(a.values, v)
	return "$" + strconv.Itoa(len(a.values))
}

// filterSQL renders filter as a conjunction over the runs alias r. Each predicate maps to
// one of the run list indexes: workflow_id and status lead a (…, started_at, id) index,
// and the ID prefix is a byte-wise range over the text_pattern_ops index on id.
func (a *runListArgs) filterSQL(filter store.RunListFilter) string {
	clauses := []string{"TRUE"}
	if filter.WorkflowID != "" {
		clauses = append(clauses, "r.workflow_id = "+a.add(filter.WorkflowID))
	}
	if filter.Status != "" {
		clauses = append(clauses, "r.status = "+a.add(string(filter.Status)))
	}
	if filter.StartedFrom != nil {
		clauses = append(clauses, "r.started_at >= "+a.add(*filter.StartedFrom))
	}
	if filter.IDPrefix != "" {
		clauses = append(clauses, a.idPrefixSQL(filter.IDPrefix))
	}
	return strings.Join(clauses, " AND ")
}

// idPrefixSQL matches IDs starting with prefix. For an ASCII prefix the match is the
// half-open byte range [prefix, successor), which the pattern index serves with bound
// parameters; any other prefix falls back to an escaped LIKE, which is still exact.
func (a *runListArgs) idPrefixSQL(prefix string) string {
	if upper, ok := asciiPrefixSuccessor(prefix); ok {
		return "r.id ~>=~ " + a.add(prefix) + " AND r.id ~<~ " + a.add(upper)
	}
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(prefix)
	return "r.id LIKE " + a.add(escaped+"%")
}

// asciiPrefixSuccessor returns the smallest string greater than every string that starts
// with prefix, when prefix is printable ASCII.
func asciiPrefixSuccessor(prefix string) (string, bool) {
	for i := 0; i < len(prefix); i++ {
		if prefix[i] < 0x20 || prefix[i] >= 0x7e {
			return "", false
		}
	}
	last := len(prefix) - 1
	return prefix[:last] + string(prefix[last]+1), true
}

// positionSQL renders the strict keyset predicate beyond pos in direction.
func (a *runListArgs) positionSQL(pos store.RunPosition, direction store.RunListDirection) string {
	op := "<"
	if direction == store.RunsNewer {
		op = ">"
	}
	return fmt.Sprintf("(r.started_at, r.id) %s (%s, %s)", op, a.add(pos.StartedAt), a.add(pos.ID))
}

func (r *runListRepository) ListSummaries(
	ctx context.Context, filter store.RunListFilter, after *store.RunPosition, direction store.RunListDirection, limit int,
) ([]store.RunSummary, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store/postgres runList.ListSummaries: limit must be positive, got %d", limit)
	}
	args := &runListArgs{}
	where := args.filterSQL(filter)
	if after != nil {
		where += " AND " + args.positionSQL(*after, direction)
	}
	order := "r.started_at DESC, r.id DESC"
	if direction == store.RunsNewer {
		order = "r.started_at ASC, r.id ASC"
	}
	query := `
		SELECT r.id, r.workflow_id, w.name, r.definition_version, w.latest_version,
		       r.status, r.started_at, r.completed_at, COALESCE(r.error->>'code', '')
		  FROM runs r
		  JOIN workflows w ON w.workflow_id = r.workflow_id
		 WHERE ` + where + `
		 ORDER BY ` + order + `
		 LIMIT ` + args.add(limit)

	rows, err := r.conn.Query(ctx, query, args.values...)
	if err != nil {
		return nil, mapError("runList.ListSummaries", err)
	}
	summaries, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.RunSummary, error) {
		var (
			s      store.RunSummary
			status string
		)
		err := row.Scan(&s.ID, &s.WorkflowID, &s.WorkflowName, &s.DefinitionVersion, &s.LatestVersion,
			&status, &s.StartedAt, &s.CompletedAt, &s.ErrorCode)
		s.Status = domain.RunStatus(status)
		return s, err
	})
	if err != nil {
		return nil, mapError("runList.ListSummaries", err)
	}
	return summaries, nil
}

func (r *runListRepository) ExistsBeyond(
	ctx context.Context, filter store.RunListFilter, pos store.RunPosition, direction store.RunListDirection,
) (bool, error) {
	args := &runListArgs{}
	where := args.filterSQL(filter) + " AND " + args.positionSQL(pos, direction)
	query := `SELECT EXISTS (SELECT 1 FROM runs r WHERE ` + where + `)`

	var exists bool
	if err := r.conn.QueryRow(ctx, query, args.values...).Scan(&exists); err != nil {
		return false, mapError("runList.ExistsBeyond", err)
	}
	return exists, nil
}

func (r *runListRepository) CountByStatus(ctx context.Context, filter store.RunListFilter) (map[domain.RunStatus]int, error) {
	args := &runListArgs{}
	query := `SELECT r.status, COUNT(*) FROM runs r WHERE ` + args.filterSQL(filter) + ` GROUP BY r.status`

	rows, err := r.conn.Query(ctx, query, args.values...)
	if err != nil {
		return nil, mapError("runList.CountByStatus", err)
	}
	type statusCount struct {
		status string
		n      int
	}
	rowsOut, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (statusCount, error) {
		var c statusCount
		err := row.Scan(&c.status, &c.n)
		return c, err
	})
	if err != nil {
		return nil, mapError("runList.CountByStatus", err)
	}
	counts := make(map[domain.RunStatus]int, len(rowsOut))
	for _, c := range rowsOut {
		counts[domain.RunStatus(c.status)] = c.n
	}
	return counts, nil
}

// nodeNameSQL reads a node's name from the Definition version the Run is bound to, so a
// later rename in a newer version never relabels an older Run's step.
const nodeNameSQL = `
	COALESCE((SELECT elem->>'name'
	            FROM jsonb_array_elements(d.nodes) AS elem
	           WHERE elem->>'id' = nr.node_id
	           LIMIT 1), '')`

func (r *runListRepository) FailedSteps(ctx context.Context, runIDs []string) (map[string]store.RunStep, error) {
	steps := make(map[string]store.RunStep, len(runIDs))
	if len(runIDs) == 0 {
		return steps, nil
	}
	query := `
		SELECT DISTINCT ON (e.run_id) e.run_id, nr.node_id, ` + nodeNameSQL + `
		  FROM events e
		  JOIN node_runs nr ON nr.id = e.node_run_id
		  JOIN runs r ON r.id = e.run_id
		  JOIN workflow_definitions d ON d.workflow_id = r.workflow_id AND d.version = r.definition_version
		 WHERE e.run_id = ANY($1) AND e.type = $2
		 ORDER BY e.run_id, e.seq DESC`

	rows, err := r.conn.Query(ctx, query, runIDs, string(domain.EventNodeFailed))
	if err != nil {
		return nil, mapError("runList.FailedSteps", err)
	}
	type failedRow struct {
		runID string
		step  store.RunStep
	}
	failed, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (failedRow, error) {
		var f failedRow
		err := row.Scan(&f.runID, &f.step.NodeID, &f.step.NodeName)
		return f, err
	})
	if err != nil {
		return nil, mapError("runList.FailedSteps", err)
	}
	for _, f := range failed {
		steps[f.runID] = f.step
	}
	return steps, nil
}

func (r *runListRepository) WaitingSteps(ctx context.Context, runIDs []string) (map[string][]store.RunStep, error) {
	steps := make(map[string][]store.RunStep, len(runIDs))
	if len(runIDs) == 0 {
		return steps, nil
	}
	query := `
		SELECT nr.run_id, nr.node_id, ` + nodeNameSQL + `, nr.waiting_at
		  FROM node_runs nr
		  JOIN runs r ON r.id = nr.run_id
		  JOIN workflow_definitions d ON d.workflow_id = r.workflow_id AND d.version = r.definition_version
		 WHERE nr.run_id = ANY($1) AND nr.status = $2
		 ORDER BY nr.run_id, nr.waiting_at, nr.node_id`

	rows, err := r.conn.Query(ctx, query, runIDs, string(domain.NodeRunWaitingCallback))
	if err != nil {
		return nil, mapError("runList.WaitingSteps", err)
	}
	type waitingRow struct {
		runID string
		step  store.RunStep
	}
	waiting, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (waitingRow, error) {
		var w waitingRow
		err := row.Scan(&w.runID, &w.step.NodeID, &w.step.NodeName, &w.step.WaitingAt)
		return w, err
	})
	if err != nil {
		return nil, mapError("runList.WaitingSteps", err)
	}
	for _, w := range waiting {
		steps[w.runID] = append(steps[w.runID], w.step)
	}
	return steps, nil
}

var _ store.RunListRepository = (*runListRepository)(nil)
