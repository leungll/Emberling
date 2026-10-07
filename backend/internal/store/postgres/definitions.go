package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/leungll/Emberling/backend/internal/domain"
	"github.com/leungll/Emberling/backend/internal/store"
)

type definitionRepository struct {
	conn pgx.Tx
}

const definitionColumns = `workflow_id, version, name, description, nodes, edges,
	run_input_schema, validation, created_at`

// Save creates the Workflow row if needed and inserts the version. There is no update
// path: a stored version is immutable, so re-saving the same version is a conflict
// rather than a rewrite.
func (r *definitionRepository) Save(ctx context.Context, def domain.Definition) error {
	nodes, err := json.Marshal(def.Nodes)
	if err != nil {
		return fmt.Errorf("store/postgres definitions.Save: marshal nodes: workflow=%s version=%d: %w",
			def.WorkflowID, def.Version, err)
	}
	edges, err := json.Marshal(def.Edges)
	if err != nil {
		return fmt.Errorf("store/postgres definitions.Save: marshal edges: workflow=%s version=%d: %w",
			def.WorkflowID, def.Version, err)
	}
	validation, err := json.Marshal(def.Validation)
	if err != nil {
		return fmt.Errorf("store/postgres definitions.Save: marshal validation: workflow=%s version=%d: %w",
			def.WorkflowID, def.Version, err)
	}

	// A Workflow's latest_version must advance by exactly 1 per new version (invariant
	// #8): a brand new Workflow accepts only version 1, and an existing one accepts only
	// latest_version+1. SELECT ... FOR UPDATE serialises concurrent Saves only once the
	// workflows row already exists: it locks nothing when the row is absent, so two
	// callers racing to create the *same new* Workflow both take the "not found" branch
	// below and race on the workflows INSERT instead. That INSERT's UNIQUE (workflow_id)
	// violation is the actual arbiter for that case and is mapped to ErrVersionConflict
	// the same way the workflow_definitions INSERT already is (only one caller
	// creates the Workflow, the other gets VERSION_CONFLICT).
	var currentVersion int
	err = r.conn.QueryRow(ctx,
		`SELECT latest_version FROM workflows WHERE workflow_id = $1 FOR UPDATE`, def.WorkflowID,
	).Scan(&currentVersion)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if def.Version != 1 {
			return fmt.Errorf("store/postgres definitions.Save: workflow=%s version=%d: new workflow must start at version 1: %w",
				def.WorkflowID, def.Version, domain.ErrVersionConflict)
		}
		const insertWorkflow = `
			INSERT INTO workflows (workflow_id, name, latest_version, created_at, updated_at)
			VALUES ($1, $2, 1, $3, $3)`
		if _, err := r.conn.Exec(ctx, insertWorkflow, def.WorkflowID, def.Name, def.CreatedAt); err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("store/postgres definitions.Save: workflow=%s version=%d: %w",
					def.WorkflowID, def.Version, domain.ErrVersionConflict)
			}
			return mapError("definitions.Save insert workflow", err, def.WorkflowID)
		}
	case err != nil:
		return mapError("definitions.Save lock workflow", err, def.WorkflowID)
	default:
		if def.Version != currentVersion+1 {
			return fmt.Errorf("store/postgres definitions.Save: workflow=%s version=%d: latest is %d: %w",
				def.WorkflowID, def.Version, currentVersion, domain.ErrVersionConflict)
		}
		const advanceWorkflow = `
			UPDATE workflows SET name = $2, latest_version = $3, updated_at = $4 WHERE workflow_id = $1`
		if _, err := r.conn.Exec(ctx, advanceWorkflow,
			def.WorkflowID, def.Name, def.Version, def.CreatedAt); err != nil {
			return mapError("definitions.Save advance workflow", err, def.WorkflowID)
		}
	}

	const insertVersion = `
		INSERT INTO workflow_definitions (` + definitionColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	if _, err := r.conn.Exec(ctx, insertVersion,
		def.WorkflowID, def.Version, def.Name, def.Description,
		nodes, edges, def.RunInputSchema, validation, def.CreatedAt,
	); err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("store/postgres definitions.Save: workflow=%s version=%d: %w",
				def.WorkflowID, def.Version, domain.ErrVersionConflict)
		}
		return mapError("definitions.Save insert version", err, def.WorkflowID, def.Version)
	}
	return nil
}

func (r *definitionRepository) GetVersion(ctx context.Context, workflowID string, version int) (domain.Definition, error) {
	const query = `SELECT ` + definitionColumns + `
		  FROM workflow_definitions
		 WHERE workflow_id = $1 AND version = $2`

	rows, err := r.conn.Query(ctx, query, workflowID, version)
	if err != nil {
		return domain.Definition{}, mapError("definitions.GetVersion", err, workflowID, version)
	}
	defs, err := scanDefinitions(rows)
	if err != nil {
		return domain.Definition{}, mapError("definitions.GetVersion", err, workflowID, version)
	}
	if len(defs) == 0 {
		return domain.Definition{}, mapError("definitions.GetVersion", pgx.ErrNoRows, workflowID, version)
	}
	return defs[0], nil
}

func (r *definitionRepository) ListVersions(ctx context.Context, workflowID string) ([]domain.Definition, error) {
	const query = `SELECT ` + definitionColumns + `
		  FROM workflow_definitions
		 WHERE workflow_id = $1
		 ORDER BY version ASC`

	rows, err := r.conn.Query(ctx, query, workflowID)
	if err != nil {
		return nil, mapError("definitions.ListVersions", err, workflowID)
	}
	defs, err := scanDefinitions(rows)
	if err != nil {
		return nil, mapError("definitions.ListVersions", err, workflowID)
	}
	return defs, nil
}

func (r *definitionRepository) GetWorkflow(ctx context.Context, workflowID string) (domain.Workflow, error) {
	const query = `SELECT workflow_id, name, latest_version, created_at, updated_at
		  FROM workflows WHERE workflow_id = $1`

	var wf domain.Workflow
	err := r.conn.QueryRow(ctx, query, workflowID).Scan(
		&wf.WorkflowID, &wf.Name, &wf.LatestVersion, &wf.CreatedAt, &wf.UpdatedAt)
	if err != nil {
		return domain.Workflow{}, mapError("definitions.GetWorkflow", err, workflowID)
	}
	return wf, nil
}

// ListWorkflows returns every Workflow, most recently updated first, together with its
// latest version's Description (the Definitions-list endpoint returns name,
// description, latestVersion, updatedAt, lastRun). Description
// lives on workflow_definitions per version, so this joins the latest row rather than
// reading a workflows column.
func (r *definitionRepository) ListWorkflows(ctx context.Context) ([]store.WorkflowSummary, error) {
	const query = `SELECT w.workflow_id, w.name, w.latest_version, w.created_at, w.updated_at, wd.description
		  FROM workflows w
		  JOIN workflow_definitions wd
		    ON wd.workflow_id = w.workflow_id AND wd.version = w.latest_version
		 ORDER BY w.updated_at DESC`

	rows, err := r.conn.Query(ctx, query)
	if err != nil {
		return nil, mapError("definitions.ListWorkflows", err)
	}
	defer rows.Close()

	out := []store.WorkflowSummary{}
	for rows.Next() {
		var summary store.WorkflowSummary
		wf := &summary.Workflow
		if err := rows.Scan(&wf.WorkflowID, &wf.Name, &wf.LatestVersion, &wf.CreatedAt, &wf.UpdatedAt, &summary.Description); err != nil {
			return nil, mapError("definitions.ListWorkflows", err)
		}
		out = append(out, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError("definitions.ListWorkflows", err)
	}
	return out, nil
}

func scanDefinitions(rows pgx.Rows) ([]domain.Definition, error) {
	defer rows.Close()

	out := []domain.Definition{}
	for rows.Next() {
		var (
			def        domain.Definition
			nodes      []byte
			edges      []byte
			validation []byte
		)
		if err := rows.Scan(
			&def.WorkflowID, &def.Version, &def.Name, &def.Description,
			&nodes, &edges, &def.RunInputSchema, &validation, &def.CreatedAt,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(nodes, &def.Nodes); err != nil {
			return nil, fmt.Errorf("unmarshal nodes: workflow=%s version=%d: %w", def.WorkflowID, def.Version, err)
		}
		if err := json.Unmarshal(edges, &def.Edges); err != nil {
			return nil, fmt.Errorf("unmarshal edges: workflow=%s version=%d: %w", def.WorkflowID, def.Version, err)
		}
		if err := json.Unmarshal(validation, &def.Validation); err != nil {
			return nil, fmt.Errorf("unmarshal validation: workflow=%s version=%d: %w", def.WorkflowID, def.Version, err)
		}
		out = append(out, def)
	}
	return out, rows.Err()
}
